//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/store/postgres"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/test"
	"github.com/mobqa/backend/internal/worker"
)

func (f testFixture) requestGeneration(t *testing.T) test.Generation {
	t.Helper()
	id := uuid.Must(uuid.NewV7()).String()
	created, err := f.tests.CreateGeneration(context.Background(), test.Generation{
		ID: id, ProjectID: f.projectID, Platform: project.PlatformIOS, Brief: "Sign in",
		Status: test.GenerationPending, CreatedBy: f.userID,
	}, worker.EnqueueInput{Kind: test.GenerationJobKind, Payload: []byte(`{"generation_id":"` + id + `"}`),
		MaxAttempts: 3})
	if err != nil {
		t.Fatalf("CreateGeneration: %v", err)
	}
	f.now.Advance(time.Minute)
	return created
}

func draft(f testFixture, title string) test.NewTest {
	return test.NewTest{
		ProjectID: f.projectID, Platform: project.PlatformIOS, Priority: test.PriorityMedium, Tags: []string{},
		Source: test.SourceGenerated, Change: test.ChangeGenerate, CreatedBy: f.userID,
		Content: test.Content{Title: title, Steps: []test.Step{{Action: test.ActionTap, Target: "Sign in"}}},
	}
}

func TestGenerationCreateQueuesJob(t *testing.T) {
	f := newTests(t)

	created := f.requestGeneration(t)

	if created.Status != test.GenerationPending || len(created.TestIDs) != 0 || created.FinishedAt != nil ||
		!created.CreatedAt.Equal(testStart()) {
		t.Fatalf("created = %+v", created)
	}
	job, ok, err := postgres.NewJobs(f.db).Claim(context.Background(), worker.ClaimInput{
		Kind: test.GenerationJobKind, Owner: "test", Lease: time.Minute,
	})
	if err != nil || !ok || job.MaxAttempts != 3 {
		t.Fatalf("Claim = %+v, %v, %v", job, ok, err)
	}
}

func TestGenerationCompleteCreatesTestsOnce(t *testing.T) {
	f := newTests(t)
	pending := f.requestGeneration(t)
	ctx := context.Background()
	drafts := []test.NewTest{draft(f, "Sign in"), draft(f, "Sign out")}

	completed, err := f.tests.CompleteGeneration(ctx, pending.ID, drafts)

	if err != nil || completed.Status != test.GenerationSucceeded || len(completed.TestIDs) != 2 ||
		completed.FinishedAt == nil || !completed.FinishedAt.Equal(testStart().Add(time.Minute)) {
		t.Fatalf("CompleteGeneration = %+v, %v", completed, err)
	}
	saved, err := f.tests.Test(ctx, f.projectID, completed.TestIDs[1])
	if err != nil || saved.Content.Title != "Sign out" || saved.Source != test.SourceGenerated {
		t.Fatalf("saved test = %+v, %v", saved, err)
	}
	versions, err := f.tests.Versions(ctx, f.projectID, saved.ID)
	if err != nil || versions[0].Source != test.ChangeGenerate {
		t.Fatalf("versions = %+v, %v", versions, err)
	}

	if _, err := f.tests.CompleteGeneration(ctx, pending.ID, drafts); !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("second CompleteGeneration = %v, want ErrConflict", err)
	}
	if _, total, _ := f.tests.Tests(ctx, test.Query{ProjectID: f.projectID, Limit: 10}); total != 2 {
		t.Fatalf("tests after a repeated completion = %d, want 2", total)
	}
}

func TestGenerationCompleteRollsBackOnInvalidTest(t *testing.T) {
	f := newTests(t)
	pending := f.requestGeneration(t)
	broken := draft(f, "Broken")
	broken.Priority = "urgent"

	_, err := f.tests.CompleteGeneration(context.Background(), pending.ID, []test.NewTest{draft(f, "Ok"), broken})

	if err == nil {
		t.Fatal("CompleteGeneration with an invalid test succeeded")
	}
	found, err := f.tests.Generation(context.Background(), f.projectID, pending.ID)
	if err != nil || found.Status != test.GenerationPending {
		t.Fatalf("generation after rollback = %+v, %v", found, err)
	}
	if _, total, _ := f.tests.Tests(context.Background(), test.Query{ProjectID: f.projectID, Limit: 10}); total != 0 {
		t.Fatalf("tests after rollback = %d, want 0", total)
	}
}

func TestGenerationFailOnlyWhilePending(t *testing.T) {
	f := newTests(t)
	pending := f.requestGeneration(t)
	ctx := context.Background()

	if err := f.tests.FailGeneration(ctx, pending.ID, "the model returned no usable tests"); err != nil {
		t.Fatalf("FailGeneration: %v", err)
	}

	found, err := f.tests.Generation(ctx, f.projectID, pending.ID)
	if err != nil || found.Status != test.GenerationFailed || found.Failure != "the model returned no usable tests" {
		t.Fatalf("failed generation = %+v, %v", found, err)
	}
	if err := f.tests.FailGeneration(ctx, pending.ID, "again"); !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("second FailGeneration = %v, want ErrConflict", err)
	}
	if _, err := f.tests.CompleteGeneration(ctx, pending.ID, nil); !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("CompleteGeneration after failure = %v, want ErrConflict", err)
	}
}

func TestGenerationIsScopedToItsProject(t *testing.T) {
	f := newTests(t)
	pending := f.requestGeneration(t)
	other := f.create(t, f.userID, "Other").ID

	_, err := f.tests.Generation(context.Background(), other, pending.ID)

	if !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("Generation from another project = %v, want ErrNotFound", err)
	}
}
