package worker_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/worker"
)

const (
	testKind  = "render"
	testOwner = "worker-a"
	// waitLimit bounds every wait so a broken pool fails the test instead of hanging it.
	waitLimit = 5 * time.Second
)

var errFlaky = errors.New("flaky dependency")

type fakeStore struct {
	mu        sync.Mutex
	queue     []worker.Job
	lease     worker.Lease
	leaseErr  error
	claims    chan struct{}
	finished  chan worker.FinishInput
	heartbeat chan struct{}
}

func newFakeStore(jobs ...worker.Job) *fakeStore {
	return &fakeStore{
		queue:     jobs,
		claims:    make(chan struct{}, 100),
		finished:  make(chan worker.FinishInput, 10),
		heartbeat: make(chan struct{}, 100),
	}
}

func (s *fakeStore) Claim(_ context.Context, in worker.ClaimInput) (worker.Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() { s.claims <- struct{}{} }()
	if in.Kind != testKind || in.Owner != testOwner || len(s.queue) == 0 {
		return worker.Job{}, false, nil
	}
	job := s.queue[0]
	s.queue = s.queue[1:]
	return job, true, nil
}

func (s *fakeStore) Heartbeat(context.Context, worker.HeartbeatInput) (worker.Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heartbeat <- struct{}{}
	return s.lease, s.leaseErr
}

func (s *fakeStore) Finish(_ context.Context, in worker.FinishInput) error {
	s.finished <- in
	return nil
}

func (s *fakeStore) push(job worker.Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = append(s.queue, job)
}

type fakeSignals struct {
	receivers chan worker.Receiver
}

func newFakeSignals() *fakeSignals {
	return &fakeSignals{receivers: make(chan worker.Receiver, 1)}
}

func (s *fakeSignals) Listen(ctx context.Context, _ []string, receiver worker.Receiver) error {
	receiver.Subscribed()
	s.receivers <- receiver
	<-ctx.Done()
	return ctx.Err()
}

type testHandler struct {
	err      error
	panics   bool
	block    bool
	release  chan struct{}
	started  chan worker.Job
	returned chan error
}

func newTestHandler() *testHandler {
	return &testHandler{
		release:  make(chan struct{}),
		started:  make(chan worker.Job, 10),
		returned: make(chan error, 10),
	}
}

func (h *testHandler) Handle(ctx context.Context, job worker.Job) (err error) {
	h.started <- job
	defer func() { h.returned <- err }()
	if h.panics {
		panic("handler bug")
	}
	if h.block {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-h.release:
		}
	}
	return h.err
}

func testConfig() worker.Config {
	return worker.Config{
		Kind:         testKind,
		Owner:        testOwner,
		Concurrency:  2,
		Lease:        time.Hour,
		PollInterval: time.Hour,
		DrainTimeout: time.Minute,
		RetryBase:    time.Second,
		RetryMax:     8 * time.Second,
	}
}

type runningPool struct {
	cancel context.CancelFunc
	done   chan error
}

func startPool(t *testing.T, cfg worker.Config, deps worker.Dependencies) *runningPool {
	t.Helper()
	deps.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := worker.NewPool(cfg, deps)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	running := &runningPool{cancel: cancel, done: make(chan error, 1)}
	go func() { running.done <- pool.Run(ctx) }()
	t.Cleanup(func() { running.stop(t) })
	return running
}

func (r *runningPool) stop(t *testing.T) {
	t.Helper()
	r.cancel()
	select {
	case err := <-r.done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		r.done <- nil
	case <-time.After(waitLimit):
		t.Fatal("pool did not stop")
	}
}

func receive[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(waitLimit):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

func TestPoolRecordsHandlerOutcome(t *testing.T) {
	tests := []struct {
		name       string
		attempt    int
		maxAttempt int
		handlerErr error
		want       worker.FinishInput
	}{
		{
			name: "success", attempt: 1, maxAttempt: 3,
			want: worker.FinishInput{State: worker.StateSucceeded},
		},
		{
			name: "first failure retries after base delay", attempt: 1, maxAttempt: 5, handlerErr: errFlaky,
			want: worker.FinishInput{State: worker.StateQueued, Error: "flaky dependency", RetryIn: time.Second},
		},
		{
			name: "third failure doubles twice", attempt: 3, maxAttempt: 5, handlerErr: errFlaky,
			want: worker.FinishInput{State: worker.StateQueued, Error: "flaky dependency", RetryIn: 4 * time.Second},
		},
		{
			name: "delay is capped", attempt: 6, maxAttempt: 9, handlerErr: errFlaky,
			want: worker.FinishInput{State: worker.StateQueued, Error: "flaky dependency", RetryIn: 8 * time.Second},
		},
		{
			name: "last attempt fails", attempt: 3, maxAttempt: 3, handlerErr: errFlaky,
			want: worker.FinishInput{State: worker.StateFailed, Error: "flaky dependency"},
		},
		{
			name: "permanent error fails", attempt: 1, maxAttempt: 3, handlerErr: worker.Permanent(errFlaky),
			want: worker.FinishInput{State: worker.StateFailed, Error: "flaky dependency"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore(worker.Job{ID: "job-1", Kind: testKind, Attempt: tt.attempt, MaxAttempts: tt.maxAttempt})
			handler := newTestHandler()
			handler.err = tt.handlerErr
			startPool(t, testConfig(), worker.Dependencies{Store: store, Signals: newFakeSignals(), Handler: handler})

			got := receive(t, store.finished, "finish")

			tt.want.JobID = "job-1"
			tt.want.Owner = testOwner
			if got != tt.want {
				t.Fatalf("finish = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestPoolFailsJobWhoseAttemptsLostLeasesExhausted(t *testing.T) {
	store := newFakeStore(worker.Job{ID: "job-1", Kind: testKind, Attempt: 4, MaxAttempts: 3})
	handler := newTestHandler()
	startPool(t, testConfig(), worker.Dependencies{Store: store, Signals: newFakeSignals(), Handler: handler})

	got := receive(t, store.finished, "finish")

	want := worker.FinishInput{
		JobID: "job-1", Owner: testOwner, State: worker.StateFailed, Error: "attempts exhausted by lost leases",
	}
	if got != want {
		t.Fatalf("finish = %+v, want %+v", got, want)
	}
	if len(handler.started) != 0 {
		t.Fatal("handler ran for an exhausted job")
	}
}

func TestPoolCancelsClaimedJobWithPendingCancellation(t *testing.T) {
	store := newFakeStore(worker.Job{ID: "job-1", Kind: testKind, Attempt: 2, MaxAttempts: 3, CancelRequested: true})
	handler := newTestHandler()
	startPool(t, testConfig(), worker.Dependencies{Store: store, Signals: newFakeSignals(), Handler: handler})

	got := receive(t, store.finished, "finish")

	want := worker.FinishInput{JobID: "job-1", Owner: testOwner, State: worker.StateCancelled}
	if got != want {
		t.Fatalf("finish = %+v, want %+v", got, want)
	}
	if len(handler.started) != 0 {
		t.Fatal("handler ran for a cancelled job")
	}
}

func TestPoolFailsPanickingJobPermanently(t *testing.T) {
	store := newFakeStore(worker.Job{ID: "job-1", Kind: testKind, Attempt: 1, MaxAttempts: 3})
	handler := newTestHandler()
	handler.panics = true
	startPool(t, testConfig(), worker.Dependencies{Store: store, Signals: newFakeSignals(), Handler: handler})

	got := receive(t, store.finished, "finish")

	if got.State != worker.StateFailed || got.Error != "job handler panicked: handler bug" {
		t.Fatalf("finish = %+v, want failed with the panic", got)
	}
}

func TestPoolCancelsJobOnNotification(t *testing.T) {
	store := newFakeStore(worker.Job{ID: "job-1", Kind: testKind, Attempt: 1, MaxAttempts: 3})
	signals := newFakeSignals()
	handler := newTestHandler()
	handler.block = true
	startPool(t, testConfig(), worker.Dependencies{Store: store, Signals: signals, Handler: handler})
	receiver := receive(t, signals.receivers, "subscription")
	receive(t, handler.started, "handler start")

	receiver.Receive(worker.Notification{Channel: worker.CancelChannel, Payload: "other-job"})
	receiver.Receive(worker.Notification{Channel: worker.CancelChannel, Payload: "job-1"})

	got := receive(t, store.finished, "finish")
	if got.State != worker.StateCancelled {
		t.Fatalf("state = %s, want cancelled", got.State)
	}
}

func TestPoolCancelsJobWhenHeartbeatReportsCancellation(t *testing.T) {
	store := newFakeStore(worker.Job{ID: "job-1", Kind: testKind, Attempt: 1, MaxAttempts: 3})
	store.lease = worker.Lease{CancelRequested: true}
	handler := newTestHandler()
	handler.block = true
	cfg := testConfig()
	cfg.Lease = 30 * time.Millisecond
	startPool(t, cfg, worker.Dependencies{Store: store, Signals: newFakeSignals(), Handler: handler})

	got := receive(t, store.finished, "finish")

	if got.State != worker.StateCancelled {
		t.Fatalf("state = %s, want cancelled", got.State)
	}
}

func TestPoolAbandonsJobWhenLeaseIsLost(t *testing.T) {
	store := newFakeStore(worker.Job{ID: "job-1", Kind: testKind, Attempt: 1, MaxAttempts: 3})
	store.leaseErr = worker.ErrLeaseLost
	handler := newTestHandler()
	handler.block = true
	cfg := testConfig()
	cfg.Lease = 30 * time.Millisecond
	pool := startPool(t, cfg, worker.Dependencies{Store: store, Signals: newFakeSignals(), Handler: handler})

	if err := receive(t, handler.returned, "handler return"); !errors.Is(err, context.Canceled) {
		t.Fatalf("handler returned %v, want context.Canceled", err)
	}
	pool.stop(t)

	if len(store.finished) != 0 {
		t.Fatalf("finished a job owned by someone else: %+v", <-store.finished)
	}
}

func TestPoolDrainLetsRunningJobFinish(t *testing.T) {
	store := newFakeStore(worker.Job{ID: "job-1", Kind: testKind, Attempt: 1, MaxAttempts: 3})
	handler := newTestHandler()
	handler.block = true
	pool := startPool(t, testConfig(), worker.Dependencies{Store: store, Signals: newFakeSignals(), Handler: handler})
	receive(t, handler.started, "handler start")

	pool.cancel()
	close(handler.release)

	got := receive(t, store.finished, "finish")
	if got.State != worker.StateSucceeded {
		t.Fatalf("state = %s, want succeeded", got.State)
	}
}

func TestPoolDrainTimeoutReleasesJob(t *testing.T) {
	store := newFakeStore(worker.Job{ID: "job-1", Kind: testKind, Attempt: 2, MaxAttempts: 3})
	handler := newTestHandler()
	handler.block = true
	cfg := testConfig()
	cfg.DrainTimeout = 10 * time.Millisecond
	pool := startPool(t, cfg, worker.Dependencies{Store: store, Signals: newFakeSignals(), Handler: handler})
	receive(t, handler.started, "handler start")

	pool.stop(t)

	got := receive(t, store.finished, "finish")
	want := worker.FinishInput{JobID: "job-1", Owner: testOwner, State: worker.StateQueued, RefundAttempt: true}
	if got != want {
		t.Fatalf("finish = %+v, want %+v", got, want)
	}
}

func TestPoolClaimsOnWakeNotification(t *testing.T) {
	store := newFakeStore()
	signals := newFakeSignals()
	handler := newTestHandler()
	cfg := testConfig()
	cfg.Concurrency = 1
	startPool(t, cfg, worker.Dependencies{Store: store, Signals: signals, Handler: handler})
	receiver := receive(t, signals.receivers, "subscription")
	receive(t, store.claims, "first claim")

	store.push(worker.Job{ID: "job-1", Kind: testKind, Attempt: 1, MaxAttempts: 3})
	receiver.Receive(worker.Notification{Channel: worker.WakeChannel(testKind)})

	if job := receive(t, handler.started, "handler start"); job.ID != "job-1" {
		t.Fatalf("handled %s, want job-1", job.ID)
	}
}

func TestPoolClaimsOnceSubscribed(t *testing.T) {
	store := newFakeStore()
	handler := newTestHandler()
	cfg := testConfig()
	cfg.Concurrency = 1
	signals := &gatedSignals{fakeSignals: newFakeSignals(), open: make(chan struct{})}
	startPool(t, cfg, worker.Dependencies{Store: store, Signals: signals, Handler: handler})
	receive(t, store.claims, "first claim")

	store.push(worker.Job{ID: "job-1", Kind: testKind, Attempt: 1, MaxAttempts: 3})
	close(signals.open)

	if job := receive(t, handler.started, "handler start"); job.ID != "job-1" {
		t.Fatalf("handled %s, want job-1", job.ID)
	}
}

// gatedSignals holds the subscription back until open is closed.
type gatedSignals struct {
	*fakeSignals
	open chan struct{}
}

func (s *gatedSignals) Listen(ctx context.Context, channels []string, receiver worker.Receiver) error {
	select {
	case <-s.open:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.fakeSignals.Listen(ctx, channels, receiver)
}

func TestPoolRunsJobsConcurrently(t *testing.T) {
	store := newFakeStore(
		worker.Job{ID: "job-1", Kind: testKind, Attempt: 1, MaxAttempts: 3},
		worker.Job{ID: "job-2", Kind: testKind, Attempt: 1, MaxAttempts: 3},
	)
	handler := newTestHandler()
	handler.block = true
	startPool(t, testConfig(), worker.Dependencies{Store: store, Signals: newFakeSignals(), Handler: handler})

	receive(t, handler.started, "first handler start")
	receive(t, handler.started, "second handler start")
	close(handler.release)

	for range 2 {
		if got := receive(t, store.finished, "finish"); got.State != worker.StateSucceeded {
			t.Fatalf("state = %s, want succeeded", got.State)
		}
	}
}
