package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/mobqa/backend/internal/fault"
)

// heartbeatsPerLease leaves two missed heartbeats before a healthy job's lease lapses.
const heartbeatsPerLease = 3

var (
	errCancelRequested = errors.New("job cancellation requested")
	errShutdown        = errors.New("worker shutting down")
	errHandlerPanic    = errors.New("job handler panicked")
)

type Config struct {
	Kind string
	// Owner identifies this process in leases; it must be unique among running workers.
	Owner       string
	Concurrency int
	Lease       time.Duration
	// PollInterval is the safety net for wake-up notifications missed while the listener reconnects.
	PollInterval time.Duration
	DrainTimeout time.Duration
	RetryBase    time.Duration
	RetryMax     time.Duration
}

func (c Config) Validate() error {
	if err := validateKind(c.Kind); err != nil {
		return err
	}
	if c.Owner == "" {
		return fmt.Errorf("empty owner: %w", fault.ErrInvalidInput)
	}
	if c.Concurrency < 1 {
		return fmt.Errorf("concurrency %d: %w", c.Concurrency, fault.ErrInvalidInput)
	}
	if c.Lease <= 0 || c.PollInterval <= 0 || c.DrainTimeout < 0 {
		return fmt.Errorf("lease, poll interval and drain timeout must be positive: %w", fault.ErrInvalidInput)
	}
	if c.RetryBase <= 0 || c.RetryMax < c.RetryBase {
		return fmt.Errorf("retry base %s, max %s: %w", c.RetryBase, c.RetryMax, fault.ErrInvalidInput)
	}
	return nil
}

type Dependencies struct {
	Store   Store
	Signals Signals
	Handler Handler
	Logger  *slog.Logger
}

type Pool struct {
	cfg     Config
	store   Store
	signals Signals
	handler Handler
	logger  *slog.Logger
	running *runningJobs
}

func NewPool(cfg Config, deps Dependencies) (*Pool, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Pool{
		cfg:     cfg,
		store:   deps.Store,
		signals: deps.Signals,
		handler: deps.Handler,
		logger:  deps.Logger.With("component", "worker", "job_kind", cfg.Kind),
		running: &runningJobs{cancels: map[string]context.CancelCauseFunc{}},
	}, nil
}

// Run claims and handles jobs until ctx ends, then drains: in-flight jobs get DrainTimeout to finish
// before their contexts are cancelled and their leases released. It returns after every goroutine it
// started has exited.
func (p *Pool) Run(ctx context.Context) error {
	// Jobs run on their own context so that stopping the pool stops claiming without killing work.
	jobsCtx, stopJobs := context.WithCancelCause(context.WithoutCancel(ctx))
	defer stopJobs(nil)

	wake := make(chan struct{}, 1)
	var wg sync.WaitGroup
	wg.Add(1 + p.cfg.Concurrency)
	go func() {
		defer wg.Done()
		p.listen(ctx, dispatcher{kind: p.cfg.Kind, wake: wake, running: p.running})
	}()
	for range p.cfg.Concurrency {
		go func() {
			defer wg.Done()
			p.work(ctx, jobsCtx, wake)
		}()
	}

	<-ctx.Done()
	p.drain(&wg, stopJobs)
	return nil
}

func (p *Pool) drain(wg *sync.WaitGroup, stopJobs context.CancelCauseFunc) {
	stopped := make(chan struct{})
	// Owned by drain; it exits once every Run goroutine has, which stopJobs guarantees.
	go func() {
		wg.Wait()
		close(stopped)
	}()
	timer := time.NewTimer(p.cfg.DrainTimeout)
	defer timer.Stop()
	select {
	case <-stopped:
	case <-timer.C:
		stopJobs(errShutdown)
		<-stopped
	}
}

// listen keeps the notification subscription alive for Run, resubscribing after a break.
func (p *Pool) listen(ctx context.Context, receiver dispatcher) {
	channels := []string{WakeChannel(p.cfg.Kind), CancelChannel}
	for {
		err := p.signals.Listen(ctx, channels, receiver)
		if ctx.Err() != nil {
			return
		}
		p.logger.WarnContext(ctx, "job listener stopped", "error", err)
		if !sleep(ctx, p.cfg.PollInterval) {
			return
		}
	}
}

// work is one of Run's claim loops. It stops claiming when ctx ends; a job it already holds finishes
// on jobsCtx, which outlives ctx by the drain timeout.
func (p *Pool) work(ctx, jobsCtx context.Context, wake chan struct{}) {
	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		// A claim cancelled halfway could lease a job nobody runs, so it is not cut short by shutdown.
		in := ClaimInput{Kind: p.cfg.Kind, Owner: p.cfg.Owner, Lease: p.cfg.Lease}
		job, found, err := p.store.Claim(context.WithoutCancel(ctx), in)
		if err != nil {
			p.logger.ErrorContext(ctx, "job claim failed", "error", err)
		}
		if found {
			poke(wake)
			p.process(jobsCtx, job)
			continue
		}
		select {
		case <-ctx.Done():
		case <-wake:
		case <-ticker.C:
		}
	}
}

func (p *Pool) process(jobsCtx context.Context, job Job) {
	logger := p.logger.With("job_id", job.ID, "attempt", job.Attempt)
	if job.CancelRequested {
		p.finish(jobsCtx, logger, FinishInput{JobID: job.ID, Owner: p.cfg.Owner, State: StateCancelled})
		return
	}
	if job.Attempt > job.MaxAttempts {
		p.finish(jobsCtx, logger, FinishInput{
			JobID: job.ID, Owner: p.cfg.Owner, State: StateFailed, Error: "attempts exhausted by lost leases",
		})
		return
	}

	ctx, cancel := context.WithCancelCause(jobsCtx)
	defer cancel(nil)
	p.running.add(job.ID, cancel)
	defer p.running.remove(job.ID)

	stop := make(chan struct{})
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		p.heartbeat(ctx, job, cancel, stop)
	}()
	started := time.Now()
	err := p.handle(ctx, job)
	close(stop)
	<-heartbeatDone

	in, owned := p.outcome(job, err, context.Cause(ctx))
	if !owned {
		logger.WarnContext(ctx, "job lease lost", "duration_ms", time.Since(started).Milliseconds())
		return
	}
	p.finish(jobsCtx, logger.With("duration_ms", time.Since(started).Milliseconds()), in)
}

// heartbeat runs beside one job and is owned by process, which closes stop when the handler returns.
// It extends the lease and cancels the job when the lease is lost or cancellation was requested.
func (p *Pool) heartbeat(ctx context.Context, job Job, cancel context.CancelCauseFunc, stop <-chan struct{}) {
	ticker := time.NewTicker(p.cfg.Lease / heartbeatsPerLease)
	defer ticker.Stop()
	in := HeartbeatInput{JobID: job.ID, Owner: p.cfg.Owner, Lease: p.cfg.Lease}
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		// The lease must keep extending while a cancelled handler winds down.
		lease, err := p.store.Heartbeat(context.WithoutCancel(ctx), in)
		switch {
		case errors.Is(err, ErrLeaseLost):
			cancel(ErrLeaseLost)
			return
		case err != nil:
			p.logger.WarnContext(ctx, "job heartbeat failed", "job_id", job.ID, "error", err)
		case lease.CancelRequested:
			cancel(errCancelRequested)
		}
	}
}

func (p *Pool) handle(ctx context.Context, job Job) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = Permanent(fmt.Errorf("%w: %v", errHandlerPanic, recovered))
		}
	}()
	return p.handler.Handle(ctx, job)
}

// outcome reports false when the job now belongs to another owner and must not be touched.
func (p *Pool) outcome(job Job, err, cause error) (FinishInput, bool) {
	in := FinishInput{JobID: job.ID, Owner: p.cfg.Owner}
	switch {
	case errors.Is(cause, ErrLeaseLost):
		return in, false
	case err == nil:
		in.State = StateSucceeded
	case errors.Is(cause, errCancelRequested):
		in.State = StateCancelled
	case errors.Is(cause, errShutdown):
		in.State = StateQueued
		in.RefundAttempt = true
	case IsPermanent(err) || job.Attempt >= job.MaxAttempts:
		in.State = StateFailed
		in.Error = err.Error()
	default:
		in.State = StateQueued
		in.Error = err.Error()
		in.RetryIn = retryDelay(job.Attempt, p.cfg.RetryBase, p.cfg.RetryMax)
	}
	return in, true
}

// finish records the outcome even during shutdown, so it runs without the jobs' cancellation.
func (p *Pool) finish(jobsCtx context.Context, logger *slog.Logger, in FinishInput) {
	ctx := context.WithoutCancel(jobsCtx)
	if err := p.store.Finish(ctx, in); err != nil {
		logger.ErrorContext(ctx, "job finish failed", "error", err)
		return
	}
	switch {
	case in.State == StateSucceeded:
		logger.InfoContext(ctx, "job succeeded")
	case in.State == StateCancelled:
		logger.InfoContext(ctx, "job cancelled")
	case in.RefundAttempt:
		logger.InfoContext(ctx, "job released")
	case in.State == StateQueued:
		logger.WarnContext(ctx, "job retry scheduled", "error", in.Error)
	default:
		logger.ErrorContext(ctx, "job failed", "error", in.Error)
	}
}

func retryDelay(attempt int, base, limit time.Duration) time.Duration {
	delay := base
	for i := 1; i < attempt && delay < limit; i++ {
		delay *= 2
	}
	return min(delay, limit)
}

func poke(wake chan<- struct{}) {
	select {
	case wake <- struct{}{}:
	default:
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
