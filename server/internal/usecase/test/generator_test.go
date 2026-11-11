package test_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/chat"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/test"
	"github.com/mobqa/backend/internal/worker"
)

const goodReply = `{"tests":[
	{"title":" Sign in ","priority":"high","tags":["Auth"],"steps":[
		{"action":"input","target":"Phone"},{"action":"tap","target":"Continue"},
		{"action":"assert","target":"Home","expected_result":"Home opens"}]},
	{"title":"Broken","steps":[{"action":"teleport"}]},
	{"title":"No steps","steps":[]},
	{"title":"Sign out","priority":"urgent","steps":[{"action":"tap","target":"Log out"}]}
]}`

type fakeGenerations struct {
	stored    test.Generation
	completed [][]test.NewTest
	failures  []string
}

func (f *fakeGenerations) Generation(_ context.Context, _, id string) (test.Generation, error) {
	if id != f.stored.ID {
		return test.Generation{}, fault.ErrNotFound
	}
	return f.stored, nil
}

func (f *fakeGenerations) CompleteGeneration(_ context.Context, _ string, tests []test.NewTest,
) (test.Generation, error) {
	f.completed = append(f.completed, tests)
	return f.stored, nil
}

func (f *fakeGenerations) FailGeneration(_ context.Context, _, failure string) error {
	f.failures = append(f.failures, failure)
	return nil
}

type fakeProjects struct{}

func (fakeProjects) Project(context.Context, string) (project.Project, error) {
	return project.Project{ID: projID, Name: "Shop", PackageName: "com.shop.app", Description: "Sells shoes"}, nil
}

// fakeModel answers each request with the next scripted reply, or fails with err when it is set.
type fakeModel struct {
	replies  []chat.Response
	err      error
	requests []chat.Request
}

func (m *fakeModel) Chat(_ context.Context, request chat.Request) (chat.Response, error) {
	m.requests = append(m.requests, request)
	if m.err != nil {
		return chat.Response{}, m.err
	}
	reply := m.replies[0]
	m.replies = m.replies[1:]
	return reply, nil
}

func pending() *fakeGenerations {
	return &fakeGenerations{stored: test.Generation{
		ID: "gen-1", ProjectID: projID, Platform: project.PlatformAndroid, Brief: "Sign in and out",
		Status: test.GenerationPending, CreatedBy: "engineer",
	}}
}

func generationJob(attempt int) worker.Job {
	return worker.Job{
		Kind: test.GenerationJobKind, Payload: []byte(`{"project_id":"project-1","generation_id":"gen-1"}`),
		Attempt: attempt, MaxAttempts: 3,
	}
}

func TestGeneratorSavesValidTests(t *testing.T) {
	store := pending()
	model := &fakeModel{replies: []chat.Response{{Text: goodReply}}}

	err := test.NewGenerator(store, fakeProjects{}, model).Run(context.Background(), generationJob(1))

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	saved := store.completed[0]
	if len(saved) != 2 || saved[0].Content.Title != "Sign in" || saved[1].Content.Title != "Sign out" {
		t.Fatalf("saved = %+v", saved)
	}
	first := saved[0]
	if first.Priority != test.PriorityHigh || !slices.Equal(first.Tags, []string{"auth"}) ||
		first.Source != test.SourceGenerated || first.Change != test.ChangeGenerate || first.CreatedBy != "engineer" ||
		first.Platform != project.PlatformAndroid || len(first.Content.Steps) != 3 {
		t.Fatalf("first draft = %+v", first)
	}
	if saved[1].Priority != test.PriorityMedium {
		t.Fatalf("unknown priority became %q, want medium", saved[1].Priority)
	}
	request := model.requests[0]
	if !request.JSON || request.MaxTokens == 0 || !strings.Contains(request.Messages[1].Text, "Sells shoes") ||
		!strings.Contains(request.Messages[1].Text, "Sign in and out") ||
		!strings.Contains(request.Messages[0].Text, "scroll_until_visible") {
		t.Fatalf("request = %+v", request)
	}
}

func TestGeneratorRepairsMalformedReply(t *testing.T) {
	store := pending()
	model := &fakeModel{replies: []chat.Response{{Text: "Here are your tests:"}, {Text: goodReply}}}

	err := test.NewGenerator(store, fakeProjects{}, model).Run(context.Background(), generationJob(1))

	if err != nil || len(store.completed) != 1 {
		t.Fatalf("Run = %v, completed %d", err, len(store.completed))
	}
	repair := model.requests[1].Messages
	if len(repair) != 4 || repair[2].Role != chat.RoleAssistant || repair[2].Text != "Here are your tests:" {
		t.Fatalf("repair conversation = %+v", repair)
	}
}

func TestGeneratorFailsWithUnusableReplies(t *testing.T) {
	tests := []struct {
		name    string
		replies []chat.Response
	}{
		{name: "not JSON", replies: []chat.Response{{Text: "no"}, {Text: "still no"}}},
		{name: "no valid tests", replies: []chat.Response{{Text: `{"tests":[]}`}, {Text: `{"tests":[{"title":""}]}`}}},
		{name: "truncated", replies: []chat.Response{{Text: goodReply, Truncated: true}, {Text: "{", Truncated: true}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := pending()

			err := test.NewGenerator(store, fakeProjects{}, &fakeModel{replies: tt.replies}).
				Run(context.Background(), generationJob(1))

			if err == nil || !slices.Equal(store.failures, []string{"the model returned no usable tests"}) {
				t.Fatalf("Run = %v, failures %v", err, store.failures)
			}
		})
	}
}

func TestGeneratorRetriesUnavailableModelUntilLastAttempt(t *testing.T) {
	unavailable := errors.Join(fault.ErrUnavailable, errors.New("503"))
	store := pending()
	generator := test.NewGenerator(store, fakeProjects{}, &fakeModel{err: unavailable})

	retried := generator.Run(context.Background(), generationJob(1))
	if !errors.Is(retried, fault.ErrUnavailable) || len(store.failures) != 0 {
		t.Fatalf("first attempt = %v, failures %v", retried, store.failures)
	}

	last := generator.Run(context.Background(), generationJob(3))

	if last == nil || !slices.Equal(store.failures, []string{"the language model is unavailable; try again later"}) {
		t.Fatalf("last attempt = %v, failures %v", last, store.failures)
	}
}

func TestGeneratorFailsAtOnceWithoutModel(t *testing.T) {
	store := pending()
	model := &fakeModel{err: errors.Join(fault.ErrUnavailable, chat.ErrNotConfigured)}

	err := test.NewGenerator(store, fakeProjects{}, model).Run(context.Background(), generationJob(1))

	if err == nil || !slices.Equal(store.failures, []string{"test generation is not configured"}) {
		t.Fatalf("Run = %v, failures %v", err, store.failures)
	}
}

func TestGeneratorSkipsFinishedOrMissingGenerations(t *testing.T) {
	finished := pending()
	finished.stored.Status = test.GenerationSucceeded
	missing := &fakeGenerations{stored: test.Generation{ID: "other"}}
	for name, store := range map[string]*fakeGenerations{"finished": finished, "missing": missing} {
		t.Run(name, func(t *testing.T) {
			model := &fakeModel{}

			err := test.NewGenerator(store, fakeProjects{}, model).Run(context.Background(), generationJob(1))

			if err != nil || len(model.requests) != 0 {
				t.Fatalf("Run = %v, model asked %d times", err, len(model.requests))
			}
		})
	}
}
