package test_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/test"
	"github.com/mobqa/backend/internal/worker"
)

const (
	projID = "project-1"
	testID = "test-1"
)

type fakeAccess struct {
	roles map[string]project.Role
}

func (a fakeAccess) Require(_ context.Context, _, userID string, need project.Role) (project.Role, error) {
	role, ok := a.roles[userID]
	if !ok {
		return "", fault.ErrNotFound
	}
	if !role.Includes(need) {
		return "", fault.ErrForbidden
	}
	return role, nil
}

func access() fakeAccess {
	return fakeAccess{roles: map[string]project.Role{"viewer": project.RoleViewer, "engineer": project.RoleEngineer}}
}

type fakeStore struct {
	stored   test.Test
	versions map[int]test.Version
	created  []test.NewTest
	changes  []test.Change
	queries  []test.Query
	conflict bool

	generations []test.Generation
	jobs        []worker.EnqueueInput
}

func signIn() test.Test {
	return test.Test{
		ID: testID, ProjectID: projID, Platform: project.PlatformAndroid, Priority: test.PriorityHigh,
		Tags: []string{"auth"}, Source: test.SourceManual, Version: 2,
		Content: test.Content{Title: "Sign in", Steps: []test.Step{{Action: test.ActionTap, Target: "Sign in"}}},
	}
}

func newStore() *fakeStore {
	return &fakeStore{stored: signIn(), versions: map[int]test.Version{
		1: {Number: 1, Content: test.Content{Title: "Log in"}},
	}}
}

func (s *fakeStore) CreateTest(_ context.Context, in test.NewTest) (test.Test, error) {
	s.created = append(s.created, in)
	return test.Test{ID: "test-2", ProjectID: in.ProjectID, Platform: in.Platform, Priority: in.Priority,
		Tags: in.Tags, Source: in.Source, Content: in.Content, Version: 1}, nil
}

func (s *fakeStore) Tests(_ context.Context, query test.Query) ([]test.Test, int, error) {
	s.queries = append(s.queries, query)
	return []test.Test{s.stored}, 41, nil
}

func (s *fakeStore) Test(_ context.Context, projectID, id string) (test.Test, error) {
	if projectID != s.stored.ProjectID || id != s.stored.ID {
		return test.Test{}, fault.ErrNotFound
	}
	return s.stored, nil
}

func (s *fakeStore) UpdateTest(_ context.Context, change test.Change) (test.Test, error) {
	s.changes = append(s.changes, change)
	if s.conflict {
		return test.Test{}, fault.ErrConflict
	}
	updated := s.stored
	if change.Content != nil {
		updated.Content, updated.Version = *change.Content, updated.Version+1
	}
	return updated, nil
}

func (s *fakeStore) DeleteTest(ctx context.Context, projectID, id string) error {
	_, err := s.Test(ctx, projectID, id)
	return err
}

func (s *fakeStore) Versions(context.Context, string, string) ([]test.Version, error) {
	return []test.Version{s.versions[1]}, nil
}

func (s *fakeStore) Version(_ context.Context, _, _ string, number int) (test.Version, error) {
	version, ok := s.versions[number]
	if !ok {
		return test.Version{}, fault.ErrNotFound
	}
	return version, nil
}

func (s *fakeStore) CreateGeneration(_ context.Context, g test.Generation, job worker.EnqueueInput,
) (test.Generation, error) {
	s.generations = append(s.generations, g)
	s.jobs = append(s.jobs, job)
	return g, nil
}

func (s *fakeStore) Generation(_ context.Context, _, id string) (test.Generation, error) {
	for _, g := range s.generations {
		if g.ID == id {
			return g, nil
		}
	}
	return test.Generation{}, fault.ErrNotFound
}

func ref(userID string) test.Ref {
	return test.Ref{UserID: userID, ProjectID: projID, TestID: testID}
}

func text(value string) *string {
	return &value
}

func TestCreateNormalizesTagsAndDefaultsPriority(t *testing.T) {
	store := newStore()

	created, err := test.NewService(store, access()).Create(context.Background(), test.CreateInput{
		UserID: "engineer", ProjectID: projID, Platform: project.PlatformIOS, Title: "Checkout",
		Tags: []string{" Smoke", "smoke", "PAYMENTS "}, Steps: []test.Step{{Action: test.ActionInput, Target: "Card"}},
	})

	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	in := store.created[0]
	if !slices.Equal(in.Tags, []string{"smoke", "payments"}) || in.Priority != test.PriorityMedium {
		t.Fatalf("stored tags %v and priority %q", in.Tags, in.Priority)
	}
	if in.Source != test.SourceManual || in.Change != test.ChangeManual || in.CreatedBy != "engineer" {
		t.Fatalf("stored provenance = %+v", in)
	}
	if created.Version != 1 || len(created.Content.Steps) != 1 {
		t.Fatalf("created = %+v", created)
	}
}

func TestCreateRejectsInvalidInput(t *testing.T) {
	valid := test.CreateInput{UserID: "engineer", ProjectID: projID, Platform: project.PlatformAndroid, Title: "Ok"}
	tests := []struct {
		name   string
		change func(*test.CreateInput)
	}{
		{name: "missing platform", change: func(in *test.CreateInput) { in.Platform = "" }},
		{name: "web platform", change: func(in *test.CreateInput) { in.Platform = "web" }},
		{name: "empty title", change: func(in *test.CreateInput) { in.Title = "" }},
		{name: "padded title", change: func(in *test.CreateInput) { in.Title = " Ok" }},
		{name: "long title", change: func(in *test.CreateInput) { in.Title = strings.Repeat("t", 201) }},
		{name: "long description", change: func(in *test.CreateInput) { in.Description = strings.Repeat("d", 5001) }},
		{name: "unknown priority", change: func(in *test.CreateInput) { in.Priority = "urgent" }},
		{name: "unknown action", change: func(in *test.CreateInput) { in.Steps = []test.Step{{Action: "jump"}} }},
		{name: "long target", change: func(in *test.CreateInput) {
			in.Steps = []test.Step{{Action: test.ActionTap, Target: strings.Repeat("x", 501)}}
		}},
		{name: "call without action", change: func(in *test.CreateInput) {
			in.Steps = []test.Step{{Action: test.ActionCall, Target: " "}}
		}},
		{name: "too many steps", change: func(in *test.CreateInput) {
			in.Steps = make([]test.Step, 201)
			for i := range in.Steps {
				in.Steps[i].Action = test.ActionWait
			}
		}},
		{name: "empty tag", change: func(in *test.CreateInput) { in.Tags = []string{" "} }},
		{name: "too many tags", change: func(in *test.CreateInput) {
			for i := range 21 {
				in.Tags = append(in.Tags, strings.Repeat("t", i+1))
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := valid
			tt.change(&in)
			store := newStore()

			_, err := test.NewService(store, access()).Create(context.Background(), in)

			if !errors.Is(err, fault.ErrInvalidInput) || len(store.created) != 0 {
				t.Fatalf("Create = %v, want ErrInvalidInput and nothing stored", err)
			}
		})
	}
}

func TestCreateNeedsEngineer(t *testing.T) {
	_, err := test.NewService(newStore(), access()).Create(context.Background(), test.CreateInput{
		UserID: "viewer", ProjectID: projID, Platform: project.PlatformAndroid, Title: "Ok",
	})

	if !errors.Is(err, fault.ErrForbidden) {
		t.Fatalf("Create = %v, want ErrForbidden", err)
	}
}

func TestListPagesAndNormalizesFilters(t *testing.T) {
	store := newStore()

	page, err := test.NewService(store, access()).List(context.Background(), test.ListInput{
		UserID: "viewer", ProjectID: projID, Search: " sign ", Tag: " Auth", Page: 3, Limit: 20,
	})

	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := test.Query{ProjectID: projID, Search: "sign", Tag: "auth", Limit: 20, Offset: 40}
	if store.queries[0] != want {
		t.Fatalf("query = %+v, want %+v", store.queries[0], want)
	}
	if page.Total != 41 || page.HasMore() {
		t.Fatalf("page = %+v, has more %v", page, page.HasMore())
	}
}

func TestGetHidesOtherProjectsTests(t *testing.T) {
	_, err := test.NewService(newStore(), access()).Get(context.Background(),
		test.Ref{UserID: "viewer", ProjectID: projID, TestID: "test-elsewhere"})

	if !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("Get = %v, want ErrNotFound", err)
	}
}

func TestUpdateMergesContentIntoNewVersion(t *testing.T) {
	store := newStore()

	updated, err := test.NewService(store, access()).Update(context.Background(), test.UpdateInput{
		Ref: ref("engineer"), BaseVersion: 2, Description: text("Signs in with a phone number"), Note: "clarify",
	})

	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	change := store.changes[0]
	if change.BaseVersion != 2 || change.Source != test.ChangeManual || change.Note != "clarify" {
		t.Fatalf("change = %+v", change)
	}
	if change.Content.Title != "Sign in" || len(change.Content.Steps) != 1 ||
		change.Content.Description != "Signs in with a phone number" {
		t.Fatalf("merged content = %+v", change.Content)
	}
	if updated.Version != 3 {
		t.Fatalf("version = %d, want 3", updated.Version)
	}
}

func TestUpdateMetadataDoesNotVersion(t *testing.T) {
	store := newStore()
	priority := test.PriorityLow

	_, err := test.NewService(store, access()).Update(context.Background(), test.UpdateInput{
		Ref: ref("engineer"), Priority: &priority, Tags: []string{"Regression"},
	})

	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	change := store.changes[0]
	if change.Content != nil || *change.Priority != test.PriorityLow ||
		!slices.Equal(change.Tags, []string{"regression"}) {
		t.Fatalf("change = %+v", change)
	}
}

func TestUpdateRejectsStaleOrMissingBaseVersion(t *testing.T) {
	tests := []struct {
		name     string
		base     int
		conflict bool
		want     error
	}{
		{name: "missing", base: 0, want: fault.ErrInvalidInput},
		{name: "stale", base: 1, want: fault.ErrConflict},
		{name: "raced", base: 2, conflict: true, want: fault.ErrConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newStore()
			store.conflict = tt.conflict

			_, err := test.NewService(store, access()).Update(context.Background(), test.UpdateInput{
				Ref: ref("engineer"), BaseVersion: tt.base, Title: text("Sign up"),
			})

			if !errors.Is(err, tt.want) {
				t.Fatalf("Update = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestUpdateValidatesMergedContent(t *testing.T) {
	store := newStore()

	_, err := test.NewService(store, access()).Update(context.Background(), test.UpdateInput{
		Ref: ref("engineer"), BaseVersion: 2, Steps: []test.Step{{Action: "teleport"}},
	})

	if !errors.Is(err, fault.ErrInvalidInput) || len(store.changes) != 0 {
		t.Fatalf("Update = %v, want ErrInvalidInput and nothing stored", err)
	}
}

func TestDuplicateCopiesContentWithNewTitle(t *testing.T) {
	store := newStore()
	store.stored.Content.Title = strings.Repeat("a", 200)

	duplicate, err := test.NewService(store, access()).Duplicate(context.Background(), ref("engineer"))

	if err != nil {
		t.Fatalf("Duplicate: %v", err)
	}
	in := store.created[0]
	if in.Source != test.SourceDuplicate || in.Priority != test.PriorityHigh || !slices.Equal(in.Tags, []string{"auth"}) {
		t.Fatalf("duplicate input = %+v", in)
	}
	if title := duplicate.Content.Title; len(title) != 200 || !strings.HasSuffix(title, " (copy)") {
		t.Fatalf("title = %q", title)
	}
}

func TestRestoreRecordsEarlierContentAsNewVersion(t *testing.T) {
	store := newStore()

	restored, err := test.NewService(store, access()).Restore(context.Background(), ref("engineer"), 1)

	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	change := store.changes[0]
	if change.Source != test.ChangeRestore || change.Note != "restored version 1" || change.BaseVersion != 2 {
		t.Fatalf("change = %+v", change)
	}
	if restored.Content.Title != "Log in" || restored.Version != 3 {
		t.Fatalf("restored = %+v", restored)
	}
}

func TestRestoreRejectsCurrentAndUnknownVersions(t *testing.T) {
	tests := []struct {
		name    string
		version int
		want    error
	}{
		{name: "current", version: 2, want: fault.ErrInvalidInput},
		{name: "unknown", version: 9, want: fault.ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newStore()

			_, err := test.NewService(store, access()).Restore(context.Background(), ref("engineer"), tt.version)

			if !errors.Is(err, tt.want) || len(store.changes) != 0 {
				t.Fatalf("Restore = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestViewerCannotChangeTests(t *testing.T) {
	service := test.NewService(newStore(), access())
	ctx := context.Background()
	calls := map[string]func() error{
		"update": func() error {
			_, err := service.Update(ctx, test.UpdateInput{Ref: ref("viewer"), BaseVersion: 2, Title: text("x")})
			return err
		},
		"delete":    func() error { return service.Delete(ctx, ref("viewer")) },
		"duplicate": func() error { _, err := service.Duplicate(ctx, ref("viewer")); return err },
		"restore":   func() error { _, err := service.Restore(ctx, ref("viewer"), 1); return err },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, fault.ErrForbidden) {
				t.Fatalf("%s = %v, want ErrForbidden", name, err)
			}
		})
	}
}

func TestGenerateQueuesJob(t *testing.T) {
	store := newStore()

	created, err := test.NewService(store, access()).Generate(context.Background(), test.GenerateInput{
		UserID: "engineer", ProjectID: projID, Platform: project.PlatformIOS, Brief: "  Sign in with a phone  ",
	})

	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if created.ID == "" || created.Status != test.GenerationPending || created.Brief != "Sign in with a phone" {
		t.Fatalf("generation = %+v", created)
	}
	job := store.jobs[0]
	want := `{"project_id":"project-1","generation_id":"` + created.ID + `"}`
	if job.Kind != test.GenerationJobKind || string(job.Payload) != want || job.MaxAttempts != 3 {
		t.Fatalf("job = %+v, payload %s", job, job.Payload)
	}
}

func TestGenerateRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		in   test.GenerateInput
		want error
	}{
		{name: "empty brief", in: test.GenerateInput{UserID: "engineer", Platform: "ios", Brief: " "},
			want: fault.ErrInvalidInput},
		{name: "long brief", in: test.GenerateInput{UserID: "engineer", Platform: "ios",
			Brief: strings.Repeat("b", 4001)}, want: fault.ErrInvalidInput},
		{name: "no platform", in: test.GenerateInput{UserID: "engineer", Brief: "x"}, want: fault.ErrInvalidInput},
		{name: "viewer", in: test.GenerateInput{UserID: "viewer", Platform: "ios", Brief: "x"},
			want: fault.ErrForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newStore()
			tt.in.ProjectID = projID

			_, err := test.NewService(store, access()).Generate(context.Background(), tt.in)

			if !errors.Is(err, tt.want) || len(store.jobs) != 0 {
				t.Fatalf("Generate = %v, want %v and no job", err, tt.want)
			}
		})
	}
}

func TestGenerationIsReadableByViewers(t *testing.T) {
	store := newStore()
	store.generations = []test.Generation{{ID: "gen-1", Status: test.GenerationSucceeded}}
	service := test.NewService(store, access())

	found, err := service.Generation(context.Background(),
		test.GenerationRef{UserID: "viewer", ProjectID: projID, GenerationID: "gen-1"})
	_, missing := service.Generation(context.Background(),
		test.GenerationRef{UserID: "viewer", ProjectID: projID, GenerationID: "gen-2"})

	if err != nil || found.Status != test.GenerationSucceeded {
		t.Fatalf("Generation = %+v, %v", found, err)
	}
	if !errors.Is(missing, fault.ErrNotFound) {
		t.Fatalf("missing generation = %v, want ErrNotFound", missing)
	}
}
