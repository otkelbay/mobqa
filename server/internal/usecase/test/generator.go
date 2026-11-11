package test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/chat"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/worker"
)

const (
	// generationMaxTokens fits four tests of fifteen steps with room to spare.
	generationMaxTokens = 4000
	// maxReplies lets a second reply repair a malformed first one; more rarely helps at temperature 0.
	maxReplies         = 2
	maxGeneratedTests  = 4
	failureUnavailable = "the language model is unavailable; try again later"
	failureUnusable    = "the model returned no usable tests"
	failureUnset       = "test generation is not configured"
)

var errUnusable = errors.New("unusable model reply")

type generations interface {
	Generation(ctx context.Context, projectID, id string) (Generation, error)
	// CompleteGeneration creates the tests and marks the generation succeeded in one transaction. It
	// reports ErrConflict when the generation is no longer pending, so a repeated job creates nothing twice.
	CompleteGeneration(ctx context.Context, id string, tests []NewTest) (Generation, error)
	// FailGeneration reports ErrConflict when the generation is no longer pending.
	FailGeneration(ctx context.Context, id, failure string) error
}

type projects interface {
	Project(ctx context.Context, id string) (project.Project, error)
}

type model interface {
	Chat(ctx context.Context, request chat.Request) (chat.Response, error)
}

// Generator fulfils generation jobs: it asks the model for tests and saves the valid ones.
type Generator struct {
	store    generations
	projects projects
	model    model
}

func NewGenerator(tests generations, catalog projects, llm model) *Generator {
	return &Generator{store: tests, projects: catalog, model: llm}
}

// Run handles a generation job. A model that is unavailable is retried until the last attempt; a model
// that is not configured or keeps replying with nothing usable fails the generation at once.
func (g *Generator) Run(ctx context.Context, job worker.Job) error {
	var payload generationPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return worker.Permanent(fmt.Errorf("decode generation payload: %w", err))
	}
	generation, err := g.store.Generation(ctx, payload.ProjectID, payload.GenerationID)
	if errors.Is(err, fault.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load generation: %w", err)
	}
	if generation.Status != GenerationPending {
		return nil
	}
	app, err := g.projects.Project(ctx, generation.ProjectID)
	if err != nil {
		return fmt.Errorf("load project: %w", err)
	}
	drafts, err := g.draft(ctx, app, generation)
	if err != nil {
		return g.fail(ctx, job, generation.ID, err)
	}
	if _, err := g.store.CompleteGeneration(ctx, generation.ID, drafts); err != nil &&
		!errors.Is(err, fault.ErrConflict) {
		return fmt.Errorf("complete generation: %w", err)
	}
	return nil
}

func (g *Generator) fail(ctx context.Context, job worker.Job, generationID string, cause error) error {
	failure, retry := failureUnavailable, job.Attempt < job.MaxAttempts
	switch {
	case errors.Is(cause, chat.ErrNotConfigured):
		failure, retry = failureUnset, false
	case errors.Is(cause, errUnusable):
		failure, retry = failureUnusable, false
	}
	if retry {
		return cause
	}
	if err := g.store.FailGeneration(ctx, generationID, failure); err != nil && !errors.Is(err, fault.ErrConflict) {
		return errors.Join(cause, fmt.Errorf("record generation failure: %w", err))
	}
	return worker.Permanent(cause)
}

func (g *Generator) draft(ctx context.Context, app project.Project, generation Generation) ([]NewTest, error) {
	messages := []chat.Message{
		{Role: chat.RoleSystem, Text: generationPrompt()},
		{Role: chat.RoleUser, Text: briefPrompt(app, generation)},
	}
	var cause error
	for range maxReplies {
		response, err := g.model.Chat(ctx, chat.Request{Messages: messages, MaxTokens: generationMaxTokens, JSON: true})
		if err != nil {
			return nil, fmt.Errorf("ask the model: %w", err)
		}
		drafts, err := parseDrafts(response, generation)
		if err == nil {
			return drafts, nil
		}
		cause = err
		messages = append(messages,
			chat.Message{Role: chat.RoleAssistant, Text: response.Text},
			chat.Message{Role: chat.RoleUser, Text: repairPrompt},
		)
	}
	return nil, cause
}

const repairPrompt = "That reply was not a JSON object in the required shape, or none of its tests were valid. " +
	"Reply again with only the JSON object."

func actionNames() string {
	names := make([]string, 0, len(actions()))
	for _, action := range actions() {
		names = append(names, string(action))
	}
	return strings.Join(names, ", ")
}

func generationPrompt() string {
	return `You are a senior mobile QA engineer. You turn a brief into manual UI tests for one mobile app.
Reply with one JSON object and nothing else:
{"tests":[{"title":"...","description":"...","priority":"critical|high|medium|low","tags":["..."],` +
		`"steps":[{"action":"...","target":"...","description":"...","expected_result":"..."}]}]}
Rules:
- 1 to 4 tests, each with 3 to 15 atomic steps.
- action is one of: ` + actionNames() + `.
- target is the element or value the step uses, named as the user sees it on screen.
- A call step performs an API action of the project; use it only for an action the brief names, as its target.
- Every assert step has an expected_result.
- Write titles, descriptions and steps in the language of the brief.
- Cover only what the brief and the app description mention.`
}

func briefPrompt(app project.Project, generation Generation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "App: %s\nPlatform: %s\n", app.Name, generation.Platform)
	if app.PackageName != "" {
		fmt.Fprintf(&b, "Package: %s\n", app.PackageName)
	}
	if app.Description != "" {
		fmt.Fprintf(&b, "About the app: %s\n", app.Description)
	}
	fmt.Fprintf(&b, "\nBrief:\n%s", generation.Brief)
	return b.String()
}

type draftReply struct {
	Tests []draftTest `json:"tests"`
}

type draftTest struct {
	Title       string      `json:"title"`
	Description string      `json:"description"`
	Priority    string      `json:"priority"`
	Tags        []string    `json:"tags"`
	Steps       []draftStep `json:"steps"`
}

type draftStep struct {
	Action         string `json:"action"`
	Target         string `json:"target"`
	Description    string `json:"description"`
	ExpectedResult string `json:"expected_result"`
}

// parseDrafts keeps the valid tests of a reply and drops the rest; a reply with none is unusable.
func parseDrafts(response chat.Response, generation Generation) ([]NewTest, error) {
	if response.Truncated {
		return nil, fmt.Errorf("%w: the reply hit the token limit", errUnusable)
	}
	var reply draftReply
	if err := json.Unmarshal([]byte(response.Text), &reply); err != nil {
		return nil, fmt.Errorf("%w: %w", errUnusable, err)
	}
	drafts := []NewTest{}
	for _, candidate := range reply.Tests {
		content, ok := candidate.content()
		if !ok || len(drafts) == maxGeneratedTests {
			continue
		}
		priority := Priority(candidate.Priority)
		if validatePriority(priority) != nil {
			priority = PriorityMedium
		}
		tags, err := normalizeTags(candidate.Tags)
		if err != nil {
			tags = []string{}
		}
		drafts = append(drafts, NewTest{
			ProjectID: generation.ProjectID, Platform: generation.Platform, Priority: priority, Tags: tags,
			Source: SourceGenerated, Content: content, Change: ChangeGenerate, CreatedBy: generation.CreatedBy,
		})
	}
	if len(drafts) == 0 {
		return nil, fmt.Errorf("%w: no valid tests", errUnusable)
	}
	return drafts, nil
}

func (d draftTest) content() (Content, bool) {
	content := Content{
		Title: strings.TrimSpace(d.Title), Description: strings.TrimSpace(d.Description), Steps: openDrafts(d.Steps),
	}
	return content, len(content.Steps) > 0 && content.validate() == nil
}

func openDrafts(drafts []draftStep) []Step {
	steps := make([]Step, len(drafts))
	for i, step := range drafts {
		steps[i] = Step{
			Action: Action(strings.TrimSpace(step.Action)), Target: strings.TrimSpace(step.Target),
			Description: strings.TrimSpace(step.Description), ExpectedResult: strings.TrimSpace(step.ExpectedResult),
		}
	}
	return steps
}
