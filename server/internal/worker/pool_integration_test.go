//go:build integration

package worker_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/clock"
	"github.com/mobqa/backend/internal/dbtest"
	"github.com/mobqa/backend/internal/store/postgres"
	"github.com/mobqa/backend/internal/worker"
)

func startPostgresPool(t *testing.T, handler worker.Handler) *postgres.Jobs {
	t.Helper()
	db := dbtest.New(t, clock.System{})
	jobs := postgres.NewJobs(db)
	cfg := testConfig()
	cfg.Lease = 3 * time.Second
	startPool(t, cfg, worker.Dependencies{Store: jobs, Signals: db, Handler: handler})
	return jobs
}

func TestPostgresPoolRunsEnqueuedJob(t *testing.T) {
	handler := newTestHandler()
	jobs := startPostgresPool(t, handler)

	id, err := jobs.Enqueue(context.Background(),
		worker.EnqueueInput{Kind: testKind, Payload: []byte(`{"frame":1}`), MaxAttempts: 1})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	job := receive(t, handler.started, "handler start")
	if job.ID != id || string(job.Payload) != `{"frame": 1}` || job.Attempt != 1 {
		t.Fatalf("handled %+v, want job %s at attempt 1", job, id)
	}
	if err := receive(t, handler.returned, "handler return"); err != nil {
		t.Fatalf("handler returned %v", err)
	}
}

func TestPostgresPoolCancelsRunningJobOnRequest(t *testing.T) {
	handler := newTestHandler()
	handler.block = true
	jobs := startPostgresPool(t, handler)
	id, err := jobs.Enqueue(context.Background(),
		worker.EnqueueInput{Kind: testKind, Payload: []byte(`{}`), MaxAttempts: 1})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	receive(t, handler.started, "handler start")

	if err := jobs.RequestCancel(context.Background(), id); err != nil {
		t.Fatalf("RequestCancel: %v", err)
	}

	if err := receive(t, handler.returned, "handler return"); !errors.Is(err, context.Canceled) {
		t.Fatalf("handler returned %v, want context.Canceled", err)
	}
}
