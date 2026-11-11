package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/worker"
)

type Jobs struct {
	db *DB
}

func NewJobs(db *DB) *Jobs {
	return &Jobs{db: db}
}

// Enqueue stores a job and wakes the workers of its kind once the insert commits.
func (j *Jobs) Enqueue(ctx context.Context, in worker.EnqueueInput) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var id string
	err := pgx.BeginFunc(ctx, j.db.pool, func(tx pgx.Tx) error {
		var err error
		id, err = j.db.enqueue(ctx, tx, in)
		return err
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// enqueue inserts a job inside tx, so any store can queue follow-up work atomically with its own
// writes. The wake-up notification is delivered when tx commits.
func (db *DB) enqueue(ctx context.Context, tx pgx.Tx, in worker.EnqueueInput) (string, error) {
	if err := in.Validate(); err != nil {
		return "", err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate job id: %w", err)
	}
	now := db.clock.Now()
	_, err = tx.Exec(ctx, `
		INSERT INTO jobs (id, kind, payload, state, attempt, max_attempts, run_after, created_at, updated_at)
		VALUES ($1, $2, $3, 'queued', 0, $4, $5, $6, $6)`,
		id, in.Kind, in.Payload, in.MaxAttempts, now.Add(in.Delay), now)
	if err != nil {
		return "", fmt.Errorf("insert job: %w", err)
	}
	return id.String(), notify(ctx, tx, worker.WakeChannel(in.Kind), "")
}

// RequestCancel cancels a queued job at once and asks the owner of a running one to stop. Cancelling
// a finished job is a no-op.
func (j *Jobs) RequestCancel(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	return pgx.BeginFunc(ctx, j.db.pool, func(tx pgx.Tx) error {
		pending, err := j.db.cancelJob(ctx, tx, id)
		if err != nil || pending {
			return err
		}
		return j.requireJob(ctx, tx, id)
	})
}

// cancelJob requests cancellation inside tx and reports whether the job was still queued or running.
// The owner of a running job hears about it when tx commits.
func (db *DB) cancelJob(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	now := db.clock.Now()
	var state worker.State
	err := tx.QueryRow(ctx, `
		UPDATE jobs SET
			state = CASE WHEN state = 'queued' THEN 'cancelled' ELSE state END,
			finished_at = CASE WHEN state = 'queued' THEN $2::timestamptz END,
			cancel_requested_at = coalesce(cancel_requested_at, $2),
			updated_at = $2
		WHERE id = $1 AND state IN ('queued', 'running')
		RETURNING state`, id, now).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("request job cancel: %w", err)
	}
	if state != worker.StateRunning {
		return true, nil
	}
	return true, notify(ctx, tx, worker.CancelChannel, id)
}

func (j *Jobs) requireJob(ctx context.Context, tx pgx.Tx, id string) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM jobs WHERE id = $1)`, id).Scan(&exists); err != nil {
		return fmt.Errorf("check job: %w", err)
	}
	if !exists {
		return fmt.Errorf("job %s: %w", id, fault.ErrNotFound)
	}
	return nil
}

func (j *Jobs) Claim(ctx context.Context, in worker.ClaimInput) (worker.Job, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	now := j.db.clock.Now()
	var job worker.Job
	err := j.db.pool.QueryRow(ctx, `
		UPDATE jobs SET state = 'running', attempt = attempt + 1, lease_owner = $2, lease_until = $3, updated_at = $4
		WHERE id = (
			SELECT id FROM jobs
			WHERE kind = $1
				AND ((state = 'queued' AND run_after <= $4) OR (state = 'running' AND lease_until <= $4))
			ORDER BY run_after, id
			LIMIT 1
			FOR UPDATE SKIP LOCKED)
		RETURNING id, kind, payload, attempt, max_attempts, cancel_requested_at IS NOT NULL`,
		in.Kind, in.Owner, now.Add(in.Lease), now,
	).Scan(&job.ID, &job.Kind, &job.Payload, &job.Attempt, &job.MaxAttempts, &job.CancelRequested)
	if errors.Is(err, pgx.ErrNoRows) {
		return worker.Job{}, false, nil
	}
	if err != nil {
		return worker.Job{}, false, fmt.Errorf("claim %s job: %w", in.Kind, err)
	}
	return job, true, nil
}

func (j *Jobs) Heartbeat(ctx context.Context, in worker.HeartbeatInput) (worker.Lease, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	now := j.db.clock.Now()
	var lease worker.Lease
	err := j.db.pool.QueryRow(ctx, `
		UPDATE jobs SET lease_until = $3, updated_at = $4
		WHERE id = $1 AND lease_owner = $2 AND state = 'running'
		RETURNING cancel_requested_at IS NOT NULL`,
		in.JobID, in.Owner, now.Add(in.Lease), now,
	).Scan(&lease.CancelRequested)
	if errors.Is(err, pgx.ErrNoRows) {
		return worker.Lease{}, worker.ErrLeaseLost
	}
	if err != nil {
		return worker.Lease{}, fmt.Errorf("extend job lease: %w", err)
	}
	return lease, nil
}

func (j *Jobs) Finish(ctx context.Context, in worker.FinishInput) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	now := j.db.clock.Now()
	runAfter := now.Add(in.RetryIn)
	var finishedAt *time.Time
	if in.State != worker.StateQueued {
		finishedAt = &now
	}
	refund := 0
	if in.RefundAttempt {
		refund = 1
	}
	tag, err := j.db.pool.Exec(ctx, `
		UPDATE jobs SET
			state = $3,
			last_error = nullif($4, ''),
			run_after = CASE WHEN $3 = 'queued' THEN $5 ELSE run_after END,
			attempt = attempt - $6,
			lease_owner = NULL,
			lease_until = NULL,
			finished_at = $7,
			updated_at = $8
		WHERE id = $1 AND lease_owner = $2 AND state = 'running'`,
		in.JobID, in.Owner, in.State, in.Error, runAfter, refund, finishedAt, now)
	if err != nil {
		return fmt.Errorf("finish job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return worker.ErrLeaseLost
	}
	return nil
}

func notify(ctx context.Context, tx pgx.Tx, channel, payload string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, channel, payload); err != nil {
		return fmt.Errorf("notify %s: %w", channel, err)
	}
	return nil
}
