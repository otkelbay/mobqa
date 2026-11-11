package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/test"
	"github.com/mobqa/backend/internal/worker"
)

const generationColumns = `id, project_id, platform, brief, status, test_ids, failure, created_by, created_at,
	finished_at`

type generationRow struct {
	id         string
	projectID  string
	platform   string
	brief      string
	status     string
	testIDs    []string
	failure    string
	createdBy  string
	createdAt  time.Time
	finishedAt *time.Time
}

func (r *generationRow) targets() []any {
	return []any{
		&r.id, &r.projectID, &r.platform, &r.brief, &r.status, &r.testIDs, &r.failure, &r.createdBy, &r.createdAt,
		&r.finishedAt,
	}
}

func (r generationRow) generation() test.Generation {
	return test.Generation{
		ID:         r.id,
		ProjectID:  r.projectID,
		Platform:   project.Platform(r.platform),
		Brief:      r.brief,
		Status:     test.GenerationStatus(r.status),
		TestIDs:    r.testIDs,
		Failure:    r.failure,
		CreatedBy:  r.createdBy,
		CreatedAt:  r.createdAt,
		FinishedAt: r.finishedAt,
	}
}

func (s *Tests) CreateGeneration(ctx context.Context, g test.Generation, job worker.EnqueueInput,
) (test.Generation, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var row generationRow
	err := pgx.BeginFunc(ctx, s.db.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO test_generations (id, project_id, platform, brief, status, test_ids, failure, created_by,
				created_at)
			VALUES ($1, $2, $3, $4, $5, '{}', '', $6, $7)
			RETURNING `+generationColumns,
			g.ID, g.ProjectID, string(g.Platform), g.Brief, string(g.Status), g.CreatedBy, s.db.clock.Now(),
		).Scan(row.targets()...)
		if err != nil {
			return fmt.Errorf("insert generation: %w", err)
		}
		_, err = s.db.enqueue(ctx, tx, job)
		return err
	})
	if err != nil {
		return test.Generation{}, fmt.Errorf("create generation: %w", err)
	}
	return row.generation(), nil
}

func (s *Tests) Generation(ctx context.Context, projectID, id string) (test.Generation, error) {
	if !validID(id) {
		return test.Generation{}, fmt.Errorf("generation %s: %w", id, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var row generationRow
	err := s.db.pool.QueryRow(ctx,
		`SELECT `+generationColumns+` FROM test_generations WHERE project_id = $1 AND id = $2`, projectID, id,
	).Scan(row.targets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return test.Generation{}, fmt.Errorf("generation %s: %w", id, fault.ErrNotFound)
	}
	if err != nil {
		return test.Generation{}, fmt.Errorf("query generation: %w", err)
	}
	return row.generation(), nil
}

// CompleteGeneration locks the pending generation first, so a job that runs twice creates the tests once.
func (s *Tests) CompleteGeneration(ctx context.Context, id string, tests []test.NewTest) (test.Generation, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	now := s.db.clock.Now()
	var row generationRow
	err := pgx.BeginFunc(ctx, s.db.pool, func(tx pgx.Tx) error {
		if err := lockPending(ctx, tx, id); err != nil {
			return err
		}
		ids := make([]string, len(tests))
		for i, in := range tests {
			var err error
			if ids[i], err = insertTest(ctx, tx, in, now); err != nil {
				return err
			}
		}
		return tx.QueryRow(ctx, `
			UPDATE test_generations SET status = 'succeeded', test_ids = $2, finished_at = $3
			WHERE id = $1
			RETURNING `+generationColumns,
			id, ids, now,
		).Scan(row.targets()...)
	})
	if err != nil {
		return test.Generation{}, fmt.Errorf("complete generation: %w", err)
	}
	return row.generation(), nil
}

func (s *Tests) FailGeneration(ctx context.Context, id, failure string) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tag, err := s.db.pool.Exec(ctx, `
		UPDATE test_generations SET status = 'failed', failure = $2, finished_at = $3
		WHERE id = $1 AND status = 'pending'`,
		id, failure, s.db.clock.Now())
	if err != nil {
		return fmt.Errorf("fail generation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("generation %s is not pending: %w", id, fault.ErrConflict)
	}
	return nil
}

func lockPending(ctx context.Context, tx pgx.Tx, id string) error {
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM test_generations WHERE id = $1 FOR UPDATE`, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("generation %s: %w", id, fault.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("lock generation: %w", err)
	}
	if status != string(test.GenerationPending) {
		return fmt.Errorf("generation %s is %s: %w", id, status, fault.ErrConflict)
	}
	return nil
}
