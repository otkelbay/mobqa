//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/clock/clocktest"
	"github.com/mobqa/backend/internal/dbtest"
	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/store/postgres"
	"github.com/mobqa/backend/internal/worker"
)

const (
	jobKind = "render"
	ownerA  = "worker-a"
	ownerB  = "worker-b"
	lease   = time.Minute
)

func newJobs(t *testing.T) (*postgres.Jobs, *clocktest.Manual) {
	t.Helper()
	now := clocktest.New(time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC))
	return postgres.NewJobs(dbtest.New(t, now)), now
}

func enqueue(t *testing.T, jobs *postgres.Jobs, in worker.EnqueueInput) string {
	t.Helper()
	id, err := jobs.Enqueue(context.Background(), in)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	return id
}

func renderJob() worker.EnqueueInput {
	return worker.EnqueueInput{Kind: jobKind, Payload: []byte(`{"frame": 7}`), MaxAttempts: 3}
}

func claim(t *testing.T, jobs *postgres.Jobs, owner string) (worker.Job, bool) {
	t.Helper()
	job, found, err := jobs.Claim(context.Background(), worker.ClaimInput{Kind: jobKind, Owner: owner, Lease: lease})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	return job, found
}

func requireNothingClaimable(t *testing.T, jobs *postgres.Jobs) {
	t.Helper()
	if job, found := claim(t, jobs, ownerB); found {
		t.Fatalf("claimed %+v, want nothing", job)
	}
}

func finish(t *testing.T, jobs *postgres.Jobs, in worker.FinishInput) {
	t.Helper()
	if err := jobs.Finish(context.Background(), in); err != nil {
		t.Fatalf("Finish: %v", err)
	}
}

func TestJobsClaimLeasesEnqueuedJobOnce(t *testing.T) {
	jobs, _ := newJobs(t)
	id := enqueue(t, jobs, renderJob())

	job, found := claim(t, jobs, ownerA)

	want := worker.Job{ID: id, Kind: jobKind, Payload: []byte(`{"frame": 7}`), Attempt: 1, MaxAttempts: 3}
	if !found || job.ID != want.ID || job.Kind != want.Kind || string(job.Payload) != string(want.Payload) ||
		job.Attempt != want.Attempt || job.MaxAttempts != want.MaxAttempts || job.CancelRequested {
		t.Fatalf("Claim = %+v, %v, want %+v", job, found, want)
	}
	requireNothingClaimable(t, jobs)
}

func TestJobsClaimWaitsForDelay(t *testing.T) {
	jobs, now := newJobs(t)
	in := renderJob()
	in.Delay = time.Minute
	enqueue(t, jobs, in)

	requireNothingClaimable(t, jobs)
	now.Advance(time.Minute)

	if _, found := claim(t, jobs, ownerA); !found {
		t.Fatal("delayed job not claimable after its delay")
	}
}

func TestJobsClaimTakesOldestFirstAndOnlyItsKind(t *testing.T) {
	jobs, now := newJobs(t)
	enqueue(t, jobs, worker.EnqueueInput{Kind: "other", Payload: []byte(`{}`), MaxAttempts: 1})
	first := enqueue(t, jobs, renderJob())
	now.Advance(time.Second)
	second := enqueue(t, jobs, renderJob())

	a, _ := claim(t, jobs, ownerA)
	b, _ := claim(t, jobs, ownerA)

	if a.ID != first || b.ID != second {
		t.Fatalf("claimed %s then %s, want %s then %s", a.ID, b.ID, first, second)
	}
	requireNothingClaimable(t, jobs)
}

func TestJobsConcurrentClaimsNeverShareAJob(t *testing.T) {
	const jobCount, claimers = 30, 8
	jobs, _ := newJobs(t)
	for range jobCount {
		enqueue(t, jobs, renderJob())
	}

	var mu sync.Mutex
	claimed := map[string]int{}
	var wg sync.WaitGroup
	wg.Add(claimers)
	for range claimers {
		go func() {
			defer wg.Done()
			for {
				job, found, err := jobs.Claim(context.Background(),
					worker.ClaimInput{Kind: jobKind, Owner: ownerA, Lease: lease})
				if err != nil || !found {
					return
				}
				mu.Lock()
				claimed[job.ID]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(claimed) != jobCount {
		t.Fatalf("claimed %d distinct jobs, want %d", len(claimed), jobCount)
	}
	for id, times := range claimed {
		if times != 1 {
			t.Fatalf("job %s claimed %d times", id, times)
		}
	}
}

func TestJobsLapsedLeaseMovesToNewOwner(t *testing.T) {
	jobs, now := newJobs(t)
	id := enqueue(t, jobs, renderJob())
	claim(t, jobs, ownerA)
	requireNothingClaimable(t, jobs)

	now.Advance(lease)
	job, found := claim(t, jobs, ownerB)

	if !found || job.ID != id || job.Attempt != 2 {
		t.Fatalf("Claim = %+v, %v, want job %s at attempt 2", job, found, id)
	}
	ctx := context.Background()
	if _, err := jobs.Heartbeat(ctx, worker.HeartbeatInput{JobID: id, Owner: ownerA, Lease: lease}); !errors.Is(
		err, worker.ErrLeaseLost) {
		t.Fatalf("stale Heartbeat = %v, want ErrLeaseLost", err)
	}
	err := jobs.Finish(ctx, worker.FinishInput{JobID: id, Owner: ownerA, State: worker.StateSucceeded})
	if !errors.Is(err, worker.ErrLeaseLost) {
		t.Fatalf("stale Finish = %v, want ErrLeaseLost", err)
	}
}

func TestJobsHeartbeatExtendsLease(t *testing.T) {
	jobs, now := newJobs(t)
	id := enqueue(t, jobs, renderJob())
	claim(t, jobs, ownerA)

	now.Advance(50 * time.Second)
	got, err := jobs.Heartbeat(context.Background(), worker.HeartbeatInput{JobID: id, Owner: ownerA, Lease: lease})
	if err != nil || got.CancelRequested {
		t.Fatalf("Heartbeat = %+v, %v", got, err)
	}
	now.Advance(50 * time.Second)

	requireNothingClaimable(t, jobs)
}

func TestJobsFinishWithRetryRunsAgainLater(t *testing.T) {
	jobs, now := newJobs(t)
	id := enqueue(t, jobs, renderJob())
	claim(t, jobs, ownerA)

	finish(t, jobs, worker.FinishInput{
		JobID: id, Owner: ownerA, State: worker.StateQueued, Error: "flaky", RetryIn: 30 * time.Second,
	})

	requireNothingClaimable(t, jobs)
	now.Advance(30 * time.Second)
	if job, found := claim(t, jobs, ownerB); !found || job.Attempt != 2 {
		t.Fatalf("Claim = %+v, %v, want attempt 2", job, found)
	}
}

func TestJobsFinishWithRefundKeepsAttempt(t *testing.T) {
	jobs, _ := newJobs(t)
	id := enqueue(t, jobs, renderJob())
	claim(t, jobs, ownerA)

	finish(t, jobs, worker.FinishInput{JobID: id, Owner: ownerA, State: worker.StateQueued, RefundAttempt: true})

	if job, found := claim(t, jobs, ownerB); !found || job.Attempt != 1 {
		t.Fatalf("Claim = %+v, %v, want attempt 1", job, found)
	}
}

func TestJobsTerminalStatesAreFinal(t *testing.T) {
	for _, state := range []worker.State{worker.StateSucceeded, worker.StateFailed, worker.StateCancelled} {
		t.Run(string(state), func(t *testing.T) {
			jobs, now := newJobs(t)
			id := enqueue(t, jobs, renderJob())
			claim(t, jobs, ownerA)

			finish(t, jobs, worker.FinishInput{JobID: id, Owner: ownerA, State: state})

			now.Advance(24 * time.Hour)
			requireNothingClaimable(t, jobs)
		})
	}
}

func TestJobsRequestCancelOfQueuedJobCancelsIt(t *testing.T) {
	jobs, _ := newJobs(t)
	id := enqueue(t, jobs, renderJob())

	if err := jobs.RequestCancel(context.Background(), id); err != nil {
		t.Fatalf("RequestCancel: %v", err)
	}

	requireNothingClaimable(t, jobs)
}

func TestJobsRequestCancelOfRunningJobReachesOwner(t *testing.T) {
	jobs, now := newJobs(t)
	id := enqueue(t, jobs, renderJob())
	claim(t, jobs, ownerA)
	ctx := context.Background()

	if err := jobs.RequestCancel(ctx, id); err != nil {
		t.Fatalf("RequestCancel: %v", err)
	}

	got, err := jobs.Heartbeat(ctx, worker.HeartbeatInput{JobID: id, Owner: ownerA, Lease: lease})
	if err != nil || !got.CancelRequested {
		t.Fatalf("Heartbeat = %+v, %v, want cancel requested", got, err)
	}
	now.Advance(lease)
	if job, found := claim(t, jobs, ownerB); !found || !job.CancelRequested {
		t.Fatalf("reclaimed %+v, %v, want cancel requested", job, found)
	}
}

func TestJobsRequestCancelOfFinishedJobIsNoOp(t *testing.T) {
	jobs, _ := newJobs(t)
	id := enqueue(t, jobs, renderJob())
	claim(t, jobs, ownerA)
	finish(t, jobs, worker.FinishInput{JobID: id, Owner: ownerA, State: worker.StateSucceeded})

	if err := jobs.RequestCancel(context.Background(), id); err != nil {
		t.Fatalf("RequestCancel: %v", err)
	}
}

func TestJobsRequestCancelOfUnknownJob(t *testing.T) {
	jobs, _ := newJobs(t)

	err := jobs.RequestCancel(context.Background(), "0190f3c4-0000-7000-8000-000000000000")

	if !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("RequestCancel = %v, want ErrNotFound", err)
	}
}

func TestJobsEnqueueRejectsInvalidInput(t *testing.T) {
	jobs, _ := newJobs(t)

	_, err := jobs.Enqueue(context.Background(), worker.EnqueueInput{Kind: jobKind, Payload: []byte(`{}`)})

	if !errors.Is(err, fault.ErrInvalidInput) {
		t.Fatalf("Enqueue = %v, want ErrInvalidInput", err)
	}
}
