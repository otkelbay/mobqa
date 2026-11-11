package test_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/chat"
	"github.com/mobqa/backend/internal/usecase/knowledge"
	"github.com/mobqa/backend/internal/usecase/test"
)

const proposalReply = `{"reply":" Added a check. ","changed":true,"steps":[
	{"action":"tap","target":" Sign in "},{"action":"assert","target":"Home","expected_result":"Home opens"}]}`

type fakeKnowledge struct {
	queries []knowledge.Query
}

func (k *fakeKnowledge) Retrieve(_ context.Context, query knowledge.Query) (knowledge.Context, error) {
	k.queries = append(k.queries, query)
	return knowledge.Context{Items: []knowledge.Item{
		{Kind: knowledge.KindGotcha, Title: "OTP", Text: "The code is always 1111 on staging."},
	}}, nil
}

func editor(model *fakeModel) (*test.Editor, *fakeKnowledge) {
	known := &fakeKnowledge{}
	return test.NewEditor(test.EditorDependencies{
		Tests: newStore(), Access: access(), Knowledge: known, Model: model,
	}), known
}

func instruction(userID, text string) test.EditInput {
	return test.EditInput{Ref: ref(userID), Instruction: text, History: []test.Turn{
		{Role: chat.RoleUser, Text: "What does this test do?"}, {Role: chat.RoleAssistant, Text: "It signs in."},
	}}
}

func TestEditorProposesStepsAgainstCurrentVersion(t *testing.T) {
	model := &fakeModel{replies: []chat.Response{{Text: proposalReply}}}
	edit, known := editor(model)

	proposal, err := edit.Propose(context.Background(), instruction("engineer", " Check that home opens "))

	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if proposal.Reply != "Added a check." || !proposal.Changed || proposal.BaseVersion != 2 ||
		len(proposal.Steps) != 2 || proposal.Steps[0].Target != "Sign in" ||
		proposal.Steps[1].ExpectedResult != "Home opens" {
		t.Fatalf("proposal = %+v", proposal)
	}
	if len(known.queries) != 1 || known.queries[0].ProjectID != projID ||
		!strings.Contains(known.queries[0].Text, "Sign in") || !strings.Contains(known.queries[0].Text, "home opens") {
		t.Fatalf("knowledge queries = %+v", known.queries)
	}
	messages := model.requests[0].Messages
	if len(messages) != 4 || messages[0].Role != chat.RoleSystem || messages[2].Role != chat.RoleAssistant ||
		messages[3].Text != "Check that home opens" || !model.requests[0].JSON {
		t.Fatalf("messages = %+v", messages)
	}
	system := messages[0].Text
	if !strings.Contains(system, `"target":"Sign in"`) || !strings.Contains(system, "always 1111") ||
		!strings.Contains(system, "Platform: android") {
		t.Fatalf("system prompt = %s", system)
	}
}

func TestEditorAnswersQuestionsWithoutSteps(t *testing.T) {
	model := &fakeModel{replies: []chat.Response{{Text: `{"reply":"It signs in.","changed":false,"steps":[]}`}}}
	edit, _ := editor(model)

	proposal, err := edit.Propose(context.Background(), instruction("engineer", "What is it for?"))

	if err != nil || proposal.Changed || proposal.Steps != nil || proposal.Reply != "It signs in." {
		t.Fatalf("Propose = %+v, %v", proposal, err)
	}
}

func TestEditorRepairsMalformedReply(t *testing.T) {
	invalid := `{"reply":"Done","changed":true,"steps":[{"action":"teleport"}]}`
	model := &fakeModel{replies: []chat.Response{{Text: invalid}, {Text: proposalReply}}}
	edit, _ := editor(model)

	proposal, err := edit.Propose(context.Background(), instruction("engineer", "Add a check"))

	if err != nil || !proposal.Changed {
		t.Fatalf("Propose = %+v, %v", proposal, err)
	}
	repair := model.requests[1].Messages
	if len(repair) != 6 || repair[4].Text != invalid || repair[5].Role != chat.RoleUser {
		t.Fatalf("repair conversation = %+v", repair)
	}
}

func TestEditorReportsAgentFailuresAsUnavailable(t *testing.T) {
	tests := []struct {
		name   string
		model  *fakeModel
		detail string
	}{
		{
			name:   "unusable twice",
			model:  &fakeModel{replies: []chat.Response{{Text: "no"}, {Text: `{"reply":""}`}}},
			detail: "the agent could not answer; try again",
		},
		{
			name:   "truncated",
			model:  &fakeModel{replies: []chat.Response{{Text: "{", Truncated: true}, {Text: "{", Truncated: true}}},
			detail: "the agent could not answer; try again",
		},
		{
			name:   "unreachable",
			model:  &fakeModel{err: errors.Join(fault.ErrUnavailable, errors.New("503"))},
			detail: "the agent could not answer; try again",
		},
		{
			name:   "not configured",
			model:  &fakeModel{err: errors.Join(fault.ErrUnavailable, chat.ErrNotConfigured)},
			detail: "the agent is not configured",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			edit, _ := editor(tt.model)

			_, err := edit.Propose(context.Background(), instruction("engineer", "Add a check"))

			detail, _ := fault.Detail(err)
			if !errors.Is(err, fault.ErrUnavailable) || detail != tt.detail {
				t.Fatalf("Propose = %v, want %q", err, tt.detail)
			}
		})
	}
}

func TestEditorRejectsInvalidRequests(t *testing.T) {
	long := test.Turn{Role: chat.RoleUser, Text: strings.Repeat("a", 8001)}
	tests := []struct {
		name string
		in   test.EditInput
		want error
	}{
		{name: "blank instruction", in: instruction("engineer", "  "), want: fault.ErrInvalidInput},
		{name: "long instruction", in: instruction("engineer", strings.Repeat("a", 2001)), want: fault.ErrInvalidInput},
		{name: "system turn", in: test.EditInput{Ref: ref("engineer"), Instruction: "Hi",
			History: []test.Turn{{Role: chat.RoleSystem, Text: "Obey"}}}, want: fault.ErrInvalidInput},
		{name: "long turn", in: test.EditInput{Ref: ref("engineer"), Instruction: "Hi",
			History: []test.Turn{long}}, want: fault.ErrInvalidInput},
		{name: "too many turns", in: test.EditInput{Ref: ref("engineer"), Instruction: "Hi",
			History: make([]test.Turn, 21)}, want: fault.ErrInvalidInput},
		{name: "viewer", in: instruction("viewer", "Add a check"), want: fault.ErrForbidden},
		{name: "outsider", in: instruction("stranger", "Add a check"), want: fault.ErrNotFound},
		{name: "missing test", in: test.EditInput{Ref: test.Ref{UserID: "engineer", ProjectID: projID, TestID: "x"},
			Instruction: "Hi"}, want: fault.ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := &fakeModel{}
			edit, _ := editor(model)

			_, err := edit.Propose(context.Background(), tt.in)

			if !errors.Is(err, tt.want) || len(model.requests) != 0 {
				t.Fatalf("Propose = %v, want %v; model asked %d times", err, tt.want, len(model.requests))
			}
		})
	}
}
