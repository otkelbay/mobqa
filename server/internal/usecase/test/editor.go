package test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/chat"
	"github.com/mobqa/backend/internal/usecase/knowledge"
	"github.com/mobqa/backend/internal/usecase/project"
)

const (
	maxInstructionLength = 2000
	maxTurns             = 20
	maxTurnLength        = 8000
	// editMaxTokens fits a reply and about a hundred rewritten steps; a longer rewrite comes back truncated
	// and is reported instead of being applied in part.
	editMaxTokens = 8000
	// editTimeout bounds a chat turn the user is waiting on, repair included.
	editTimeout = 90 * time.Second
	// knowledgeBudget leaves most of the prompt to the test and the conversation.
	knowledgeBudget = 4000
)

// Turn is an earlier message of an editing conversation, as the client kept it.
type Turn struct {
	Role chat.Role
	Text string
}

// Proposal is the agent's answer to one instruction. When Changed, Steps replaces the test's steps and
// is meant to be saved with BaseVersion; nothing is saved by proposing.
type Proposal struct {
	Reply       string
	Changed     bool
	Steps       []Step
	BaseVersion int
}

type tests interface {
	Test(ctx context.Context, projectID, id string) (Test, error)
}

type retriever interface {
	Retrieve(ctx context.Context, query knowledge.Query) (knowledge.Context, error)
}

type EditorDependencies struct {
	Tests     tests
	Access    access
	Knowledge retriever
	Model     model
}

// Editor proposes changes to a test's steps from a conversation, grounded in the project's knowledge.
type Editor struct {
	tests     tests
	access    access
	knowledge retriever
	model     model
}

func NewEditor(deps EditorDependencies) *Editor {
	return &Editor{tests: deps.Tests, access: deps.Access, knowledge: deps.Knowledge, model: deps.Model}
}

type EditInput struct {
	Ref         Ref
	Instruction string
	History     []Turn
}

func (in EditInput) Validate() error {
	if strings.TrimSpace(in.Instruction) == "" || utf8.RuneCountInString(in.Instruction) > maxInstructionLength {
		return fault.WithDetail(fault.ErrInvalidInput, "instruction must be 1 to 2000 characters")
	}
	if len(in.History) > maxTurns {
		return fault.WithDetail(fault.ErrInvalidInput, "history must have at most 20 turns")
	}
	for _, turn := range in.History {
		if turn.Role != chat.RoleUser && turn.Role != chat.RoleAssistant {
			return fault.WithDetail(fault.ErrInvalidInput, "history roles must be user or assistant")
		}
		if utf8.RuneCountInString(turn.Text) > maxTurnLength {
			return fault.WithDetail(fault.ErrInvalidInput, "a history turn must be at most 8000 characters")
		}
	}
	return nil
}

// Propose answers one instruction about a test. A reply that is malformed or names invalid steps gets
// one repair turn; a model that still does not comply, or cannot be reached, is reported as unavailable.
func (e *Editor) Propose(ctx context.Context, in EditInput) (Proposal, error) {
	if err := in.Validate(); err != nil {
		return Proposal{}, err
	}
	if _, err := e.access.Require(ctx, in.Ref.ProjectID, in.Ref.UserID, project.RoleEngineer); err != nil {
		return Proposal{}, err
	}
	current, err := e.tests.Test(ctx, in.Ref.ProjectID, in.Ref.TestID)
	if errors.Is(err, fault.ErrNotFound) {
		return Proposal{}, fault.WithDetail(fault.ErrNotFound, "test not found")
	}
	if err != nil {
		return Proposal{}, fmt.Errorf("load test: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, editTimeout)
	defer cancel()
	known, err := e.knowledge.Retrieve(ctx, knowledge.Query{
		ProjectID: in.Ref.ProjectID, Text: current.Content.Title + "\n" + in.Instruction, Budget: knowledgeBudget,
	})
	if err != nil {
		return Proposal{}, fmt.Errorf("retrieve knowledge: %w", err)
	}
	proposal, err := e.converse(ctx, editMessages(current, known, in))
	switch {
	case errors.Is(err, chat.ErrNotConfigured):
		return Proposal{}, fault.WithDetail(fault.ErrUnavailable, "the agent is not configured")
	case errors.Is(err, errUnusable), errors.Is(err, fault.ErrUnavailable):
		return Proposal{}, fault.WithDetail(fault.ErrUnavailable, "the agent could not answer; try again")
	case err != nil:
		return Proposal{}, err
	}
	proposal.BaseVersion = current.Version
	return proposal, nil
}

func (e *Editor) converse(ctx context.Context, messages []chat.Message) (Proposal, error) {
	var cause error
	for range maxReplies {
		response, err := e.model.Chat(ctx, chat.Request{Messages: messages, MaxTokens: editMaxTokens, JSON: true})
		if err != nil {
			return Proposal{}, fmt.Errorf("ask the model: %w", err)
		}
		proposal, err := parseProposal(response)
		if err == nil {
			return proposal, nil
		}
		cause = err
		messages = append(messages,
			chat.Message{Role: chat.RoleAssistant, Text: response.Text},
			chat.Message{Role: chat.RoleUser, Text: editRepairPrompt},
		)
	}
	return Proposal{}, cause
}

const editRepairPrompt = "That reply was not a JSON object in the required shape, or its steps were not valid. " +
	"Reply again with only the JSON object."

func editPrompt() string {
	return `You are a senior mobile QA engineer editing one manual UI test together with a colleague.
Reply with one JSON object and nothing else:
{"reply":"...","changed":true|false,"steps":[{"action":"...","target":"...","description":"...",` +
		`"expected_result":"..."}]}
Rules:
- reply is a short answer to the colleague, in the language they write in.
- When the colleague asks for a change, set changed to true and return the complete new list of steps.
- When they only ask a question, set changed to false and leave steps empty.
- action is one of: ` + actionNames() + `.
- Keep steps atomic; every assert step has an expected_result.
- A call step performs an API action of the project; use it only for an action the colleague names, as its target.
- Prefer what is known about the app over guesses.`
}

func editMessages(current Test, known knowledge.Context, in EditInput) []chat.Message {
	steps, _ := json.Marshal(draftSteps(current.Content.Steps))
	var b strings.Builder
	b.WriteString(editPrompt())
	fmt.Fprintf(&b, "\n\nThe test as it is now:\nTitle: %s\nPlatform: %s\n", current.Content.Title, current.Platform)
	if current.Content.Description != "" {
		fmt.Fprintf(&b, "Description: %s\n", current.Content.Description)
	}
	fmt.Fprintf(&b, "Steps: %s\n", steps)
	if rendered := known.Render(); rendered != "" {
		b.WriteString("\n" + rendered)
	}
	messages := []chat.Message{{Role: chat.RoleSystem, Text: b.String()}}
	for _, turn := range in.History {
		messages = append(messages, chat.Message{Role: turn.Role, Text: turn.Text})
	}
	return append(messages, chat.Message{Role: chat.RoleUser, Text: strings.TrimSpace(in.Instruction)})
}

func draftSteps(steps []Step) []draftStep {
	drafts := make([]draftStep, len(steps))
	for i, step := range steps {
		drafts[i] = draftStep{
			Action: string(step.Action), Target: step.Target, Description: step.Description,
			ExpectedResult: step.ExpectedResult,
		}
	}
	return drafts
}

type proposalReply struct {
	Reply   string      `json:"reply"`
	Changed bool        `json:"changed"`
	Steps   []draftStep `json:"steps"`
}

func parseProposal(response chat.Response) (Proposal, error) {
	if response.Truncated {
		return Proposal{}, fmt.Errorf("%w: the reply hit the token limit", errUnusable)
	}
	var reply proposalReply
	if err := json.Unmarshal([]byte(response.Text), &reply); err != nil {
		return Proposal{}, fmt.Errorf("%w: %w", errUnusable, err)
	}
	proposal := Proposal{Reply: strings.TrimSpace(reply.Reply), Changed: reply.Changed}
	if proposal.Reply == "" {
		return Proposal{}, fmt.Errorf("%w: no reply", errUnusable)
	}
	if !proposal.Changed {
		return proposal, nil
	}
	proposal.Steps = openDrafts(reply.Steps)
	if err := validateSteps(proposal.Steps); err != nil {
		return Proposal{}, fmt.Errorf("%w: %w", errUnusable, err)
	}
	return proposal, nil
}
