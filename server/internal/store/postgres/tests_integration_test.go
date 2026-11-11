//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/store/postgres"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/test"
)

type testFixture struct {
	projectFixture
	tests     *postgres.Tests
	projectID string
	userID    string
}

func newTests(t *testing.T) testFixture {
	t.Helper()
	f := newProjects(t)
	owner := f.account(t, "ada")
	return testFixture{
		projectFixture: f, tests: postgres.NewTests(f.db), projectID: f.create(t, owner.ID, "Shop").ID,
		userID: owner.ID,
	}
}

func (f testFixture) add(t *testing.T, title string, tags ...string) test.Test {
	t.Helper()
	created, err := f.tests.CreateTest(context.Background(), test.NewTest{
		ProjectID: f.projectID, Platform: project.PlatformAndroid, Priority: test.PriorityHigh, Tags: tags,
		Source: test.SourceManual, Change: test.ChangeManual, CreatedBy: f.userID,
		Content: test.Content{Title: title, Description: "about " + title, Steps: []test.Step{
			{Action: test.ActionTap, Target: "Sign in", ExpectedResult: "The login form opens"},
			{Action: test.ActionInput, Target: "Phone"},
		}},
	})
	if err != nil {
		t.Fatalf("CreateTest: %v", err)
	}
	f.now.Advance(time.Minute)
	return created
}

func TestTestsCreateStoresFirstVersion(t *testing.T) {
	f := newTests(t)

	created := f.add(t, "Sign in", "auth", "smoke")

	if created.Version != 1 || created.StepCount != 2 || !slices.Equal(created.Tags, []string{"auth", "smoke"}) ||
		created.UpdatedBy != f.userID || !created.CreatedAt.Equal(testStart()) {
		t.Fatalf("created = %+v", created)
	}
	if created.Content.Steps[0] != (test.Step{Action: test.ActionTap, Target: "Sign in",
		ExpectedResult: "The login form opens"}) {
		t.Fatalf("first step = %+v", created.Content.Steps[0])
	}
	versions, err := f.tests.Versions(context.Background(), f.projectID, created.ID)
	if err != nil || len(versions) != 1 || versions[0].Number != 1 || versions[0].StepCount != 2 ||
		versions[0].Source != test.ChangeManual || versions[0].Content.Steps != nil {
		t.Fatalf("Versions = %+v, %v", versions, err)
	}
}

func TestTestsListFiltersAndPages(t *testing.T) {
	f := newTests(t)
	f.add(t, "Sign in", "auth")
	f.add(t, "100% discount", "checkout")
	f.add(t, "Sign out", "auth")
	ctx := context.Background()
	query := test.Query{ProjectID: f.projectID, Limit: 10}

	tests := []struct {
		name   string
		change func(*test.Query)
		titles []string
		total  int
	}{
		{name: "newest first", change: func(*test.Query) {}, titles: []string{"Sign out", "100% discount", "Sign in"},
			total: 3},
		{name: "search", change: func(q *test.Query) { q.Search = "sign" }, titles: []string{"Sign out", "Sign in"},
			total: 2},
		{name: "literal percent", change: func(q *test.Query) { q.Search = "0%" }, titles: []string{"100% discount"},
			total: 1},
		{name: "tag", change: func(q *test.Query) { q.Tag = "checkout" }, titles: []string{"100% discount"}, total: 1},
		{name: "platform", change: func(q *test.Query) { q.Platform = project.PlatformIOS }, total: 0},
		{name: "priority", change: func(q *test.Query) { q.Priority = test.PriorityLow }, total: 0},
		{name: "page", change: func(q *test.Query) { q.Limit, q.Offset = 1, 1 }, titles: []string{"100% discount"},
			total: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := query
			tt.change(&q)

			found, total, err := f.tests.Tests(ctx, q)

			if err != nil || total != tt.total {
				t.Fatalf("Tests = %d, %v, want total %d", total, err, tt.total)
			}
			titles := make([]string, len(found))
			for i, item := range found {
				titles[i] = item.Content.Title
			}
			if !slices.Equal(titles, tt.titles) {
				t.Fatalf("titles = %v, want %v", titles, tt.titles)
			}
		})
	}
}

func TestTestsUpdateContentAddsVersion(t *testing.T) {
	f := newTests(t)
	created := f.add(t, "Sign in")
	editor := f.account(t, "bob")
	content := test.Content{Title: "Sign in with phone", Steps: []test.Step{{Action: test.ActionBack}}}

	updated, err := f.tests.UpdateTest(context.Background(), test.Change{
		ProjectID: f.projectID, TestID: created.ID, UserID: editor.ID, Content: &content, BaseVersion: 1,
		Source: test.ChangeAgent, Note: "shorter",
	})

	if err != nil {
		t.Fatalf("UpdateTest: %v", err)
	}
	if updated.Version != 2 || updated.Content.Title != "Sign in with phone" || len(updated.Content.Steps) != 1 ||
		updated.UpdatedBy != editor.ID || !updated.UpdatedAt.Equal(testStart().Add(time.Minute)) {
		t.Fatalf("updated = %+v", updated)
	}
	first, err := f.tests.Version(context.Background(), f.projectID, created.ID, 1)
	if err != nil || first.Content.Title != "Sign in" || len(first.Content.Steps) != 2 {
		t.Fatalf("version 1 = %+v, %v", first, err)
	}
	second, err := f.tests.Version(context.Background(), f.projectID, created.ID, 2)
	if err != nil || second.Source != test.ChangeAgent || second.Note != "shorter" || second.CreatedBy != editor.ID {
		t.Fatalf("version 2 = %+v, %v", second, err)
	}
}

func TestTestsUpdateMetadataKeepsVersion(t *testing.T) {
	f := newTests(t)
	created := f.add(t, "Sign in", "auth")
	priority := test.PriorityCritical

	updated, err := f.tests.UpdateTest(context.Background(), test.Change{
		ProjectID: f.projectID, TestID: created.ID, UserID: f.userID, Priority: &priority,
	})

	if err != nil || updated.Version != 1 || updated.Priority != test.PriorityCritical ||
		!slices.Equal(updated.Tags, []string{"auth"}) || len(updated.Content.Steps) != 2 {
		t.Fatalf("UpdateTest = %+v, %v", updated, err)
	}
}

func TestTestsUpdateRejectsStaleBase(t *testing.T) {
	f := newTests(t)
	created := f.add(t, "Sign in")
	content := test.Content{Title: "Edited"}
	change := test.Change{
		ProjectID: f.projectID, TestID: created.ID, UserID: f.userID, Content: &content, BaseVersion: 1,
		Source: test.ChangeManual,
	}
	if _, err := f.tests.UpdateTest(context.Background(), change); err != nil {
		t.Fatalf("first UpdateTest: %v", err)
	}

	_, err := f.tests.UpdateTest(context.Background(), change)

	if !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("second UpdateTest = %v, want ErrConflict", err)
	}
}

func TestTestsAreScopedToTheirProject(t *testing.T) {
	f := newTests(t)
	created := f.add(t, "Sign in")
	other := f.create(t, f.userID, "Other").ID
	ctx := context.Background()
	priority := test.PriorityLow
	missing := uuid.Must(uuid.NewV7()).String()

	calls := map[string]error{}
	_, calls["get"] = f.tests.Test(ctx, other, created.ID)
	_, calls["get malformed id"] = f.tests.Test(ctx, f.projectID, "nope")
	_, calls["update"] = f.tests.UpdateTest(ctx, test.Change{
		ProjectID: other, TestID: created.ID, UserID: f.userID, Priority: &priority,
	})
	_, calls["versions"] = f.tests.Versions(ctx, other, created.ID)
	_, calls["version"] = f.tests.Version(ctx, other, created.ID, 1)
	_, calls["unknown version"] = f.tests.Version(ctx, f.projectID, created.ID, 2)
	calls["delete"] = f.tests.DeleteTest(ctx, other, created.ID)
	calls["delete missing"] = f.tests.DeleteTest(ctx, f.projectID, missing)

	for name, err := range calls {
		if !errors.Is(err, fault.ErrNotFound) {
			t.Errorf("%s = %v, want ErrNotFound", name, err)
		}
	}
}

func TestTestsDeleteRemovesVersions(t *testing.T) {
	f := newTests(t)
	created := f.add(t, "Sign in")
	ctx := context.Background()

	if err := f.tests.DeleteTest(ctx, f.projectID, created.ID); err != nil {
		t.Fatalf("DeleteTest: %v", err)
	}

	if _, err := f.tests.Versions(ctx, f.projectID, created.ID); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("Versions after delete = %v, want ErrNotFound", err)
	}
}
