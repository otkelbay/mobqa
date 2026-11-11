package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/device"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/run"
	"github.com/mobqa/backend/internal/usecase/test"
)

// runSelect reads runs as r with their result counts.
const runSelect = `
	SELECT r.id, r.project_id, r.platform, coalesce(r.build_id::text, ''), r.build_label,
		coalesce(r.environment_id::text, ''), r.environment_name, r.status, r.retries, r.created_by, r.created_at,
		r.started_at, r.finished_at, s.total, s.queued, s.running, s.passed, s.flaky, s.failed, s.errored, s.cancelled
	FROM runs r
	CROSS JOIN LATERAL (
		SELECT count(*) AS total,
			count(*) FILTER (WHERE status = 'queued') AS queued,
			count(*) FILTER (WHERE status = 'running') AS running,
			count(*) FILTER (WHERE status = 'passed') AS passed,
			count(*) FILTER (WHERE status = 'flaky') AS flaky,
			count(*) FILTER (WHERE status = 'failed') AS failed,
			count(*) FILTER (WHERE status = 'error') AS errored,
			count(*) FILTER (WHERE status = 'cancelled') AS cancelled
		FROM run_results WHERE run_id = r.id
	) s`

const resultColumns = `id, run_id, position, coalesce(test_id::text, ''), test_version, title, device_id, device_name,
	status, attempts, coalesce(failed_step, 0), error, started_at, finished_at`

const unfinished = `('queued', 'running')`

// latestScript is the script of the last succeeded authoring of test version v.
const latestScript = `SELECT script FROM authorings
	WHERE test_id = v.test_id AND test_version = v.version AND status = 'succeeded'
	ORDER BY finished_at DESC, id DESC LIMIT 1`

type runRow struct {
	run.Run
	status   string
	platform string
}

func (r *runRow) targets() []any {
	s := &r.Summary
	return []any{
		&r.ID, &r.ProjectID, &r.platform, &r.BuildID, &r.BuildLabel, &r.EnvironmentID, &r.EnvironmentName, &r.status,
		&r.Retries, &r.CreatedBy, &r.CreatedAt, &r.StartedAt, &r.FinishedAt, &s.Total, &s.Queued, &s.Running,
		&s.Passed, &s.Flaky, &s.Failed, &s.Errored, &s.Cancelled,
	}
}

func (r runRow) run() run.Run {
	found := r.Run
	found.Platform, found.Status = project.Platform(r.platform), run.Status(r.status)
	return found
}

type resultRow struct {
	run.Result
	status string
}

func (r *resultRow) targets() []any {
	return []any{
		&r.ID, &r.RunID, &r.Position, &r.TestID, &r.TestVersion, &r.Title, &r.DeviceID, &r.DeviceName, &r.status,
		&r.Attempts, &r.FailedStep, &r.Error, &r.StartedAt, &r.FinishedAt,
	}
}

func (r resultRow) result() run.Result {
	found := r.Result
	found.Status = run.ResultStatus(r.status)
	return found
}

type stepOutcomeDocument struct {
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

// nullableStep stores "no failed step" as NULL.
func nullableStep(step int) *int {
	if step == 0 {
		return nil
	}
	return &step
}

func validIDs(ids []string) []string {
	return slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return !validID(id) })
}

type Runs struct {
	db *DB
}

func NewRuns(db *DB) *Runs {
	return &Runs{db: db}
}

// Scripted returns the IDs of the tests whose version has a succeeded authoring.
func (s *Runs) Scripted(ctx context.Context, tests []test.Test) ([]string, error) {
	ids, versions := make([]string, len(tests)), make([]int, len(tests))
	for i, t := range tests {
		ids[i], versions[i] = t.ID, t.Version
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	rows, err := s.db.pool.Query(ctx, `
		SELECT DISTINCT a.test_id::text
		FROM unnest($1::uuid[], $2::int[]) AS v(test_id, version)
		JOIN authorings a ON a.test_id = v.test_id AND a.test_version = v.version AND a.status = 'succeeded'`,
		ids, versions)
	if err != nil {
		return nil, fmt.Errorf("select scripted tests: %w", err)
	}
	scripted, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read scripted tests: %w", err)
	}
	return scripted, nil
}

// Tests returns the project's tests among ids, without steps.
func (s *Runs) Tests(ctx context.Context, projectID string, ids []string) ([]test.Test, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	rows, err := s.db.pool.Query(ctx,
		`SELECT `+testColumns+` `+currentVersion+` WHERE t.project_id = $1 AND t.id = ANY($2::uuid[])`,
		projectID, validIDs(ids))
	if err != nil {
		return nil, fmt.Errorf("select run tests: %w", err)
	}
	tests, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (test.Test, error) {
		var r testRow
		err := row.Scan(r.targets()...)
		return r.test(nil), err
	})
	if err != nil {
		return nil, fmt.Errorf("read run tests: %w", err)
	}
	return tests, nil
}

// Devices returns the project's devices among ids.
func (s *Runs) Devices(ctx context.Context, projectID string, ids []string) ([]device.Device, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	rows, err := s.db.pool.Query(ctx, deviceSelect+` WHERE h.project_id = $1 AND d.id = ANY($2::uuid[])`,
		projectID, validIDs(ids))
	if err != nil {
		return nil, fmt.Errorf("select run devices: %w", err)
	}
	devices, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (device.Device, error) {
		var r deviceRow
		err := row.Scan(r.targets()...)
		return r.device(), err
	})
	if err != nil {
		return nil, fmt.Errorf("read run devices: %w", err)
	}
	return devices, nil
}

const buildPin = `SELECT id, version || ' (' || build_number || ')', platform FROM builds`

func (s *Runs) Build(ctx context.Context, projectID, id string) (run.Pin, error) {
	if !validID(id) {
		return run.Pin{}, fmt.Errorf("build %s: %w", id, fault.ErrNotFound)
	}
	return s.pin(ctx, buildPin+` WHERE project_id = $1 AND id = $2`, projectID, id)
}

func (s *Runs) CurrentBuild(ctx context.Context, projectID string, platform project.Platform) (run.Pin, error) {
	return s.pin(ctx, buildPin+` WHERE project_id = $1 AND platform = $2 AND is_current`, projectID, string(platform))
}

const environmentPin = `SELECT id, name, '' FROM environments`

func (s *Runs) Environment(ctx context.Context, projectID, id string) (run.Pin, error) {
	if !validID(id) {
		return run.Pin{}, fmt.Errorf("environment %s: %w", id, fault.ErrNotFound)
	}
	return s.pin(ctx, environmentPin+` WHERE project_id = $1 AND id = $2`, projectID, id)
}

func (s *Runs) DefaultEnvironment(ctx context.Context, projectID string) (run.Pin, error) {
	return s.pin(ctx, environmentPin+` WHERE project_id = $1 AND is_default`, projectID)
}

func (s *Runs) pin(ctx context.Context, statement string, args ...any) (run.Pin, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var pin run.Pin
	var platform string
	err := s.db.pool.QueryRow(ctx, statement, args...).Scan(&pin.ID, &pin.Label, &platform)
	if errors.Is(err, pgx.ErrNoRows) {
		return run.Pin{}, fmt.Errorf("pin: %w", fault.ErrNotFound)
	}
	if err != nil {
		return run.Pin{}, fmt.Errorf("query pin: %w", err)
	}
	pin.Platform = project.Platform(platform)
	return pin, nil
}

func (s *Runs) CreateRun(ctx context.Context, in run.NewRun) (run.Run, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	now := s.db.clock.Now()
	var created run.Run
	err := pgx.BeginFunc(ctx, s.db.pool, func(tx pgx.Tx) error {
		r := in.Run
		_, err := tx.Exec(ctx, `
			INSERT INTO runs (id, project_id, platform, build_id, build_label, environment_id, environment_name,
				status, retries, created_by, created_at)
			VALUES ($1, $2, $3, nullif($4, '')::uuid, $5, nullif($6, '')::uuid, $7, $8, $9, $10, $11)`,
			r.ID, r.ProjectID, string(r.Platform), r.BuildID, r.BuildLabel, r.EnvironmentID, r.EnvironmentName,
			string(r.Status), r.Retries, r.CreatedBy, now)
		if err != nil {
			return fmt.Errorf("insert run: %w", err)
		}
		jobIDs := make(map[string]string, len(in.Jobs))
		for deviceID, job := range in.Jobs {
			if jobIDs[deviceID], err = s.db.enqueue(ctx, tx, job); err != nil {
				return err
			}
		}
		for _, result := range in.Results {
			row := resultInsert{NewResult: result, run: r, jobID: jobIDs[result.DeviceID]}
			if err := insertResult(ctx, tx, row); err != nil {
				return err
			}
		}
		created, err = oneRun(ctx, tx, r.ProjectID, r.ID)
		return err
	})
	if err != nil {
		return run.Run{}, fmt.Errorf("create run: %w", err)
	}
	return created, nil
}

type resultInsert struct {
	run.NewResult
	run   run.Run
	jobID string
}

// insertResult copies the steps and latest script of the result's test version, which must still belong to
// the project.
func insertResult(ctx context.Context, tx pgx.Tx, result resultInsert) error {
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate result id: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO run_results (id, run_id, position, test_id, test_version, title, steps, script, device_id,
			device_name, device_serial, hub_id, job_id, status, attempts, error)
		SELECT $1, $2, $3, t.id, v.version, $6, v.steps, a.script, $7, $8, $9, $10, nullif($11, '')::uuid, 'queued',
			0, ''
		FROM tests t JOIN test_versions v ON v.test_id = t.id
		JOIN LATERAL (`+latestScript+`) a ON true
		WHERE t.project_id = $12 AND t.id = $4 AND v.version = $5`,
		id, result.run.ID, result.Position, result.TestID, result.TestVersion, result.Title, result.DeviceID,
		result.DeviceName, result.DeviceSerial, result.HubID, result.jobID, result.run.ProjectID)
	if err != nil {
		return fmt.Errorf("insert result: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("test %s version %d: %w", result.TestID, result.TestVersion, fault.ErrNotFound)
	}
	return nil
}

func (s *Runs) Runs(ctx context.Context, query run.Query) ([]run.Run, int, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var total int
	err := s.db.pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE project_id = $1 AND ($2 = '' OR status = $2)`,
		query.ProjectID, string(query.Status)).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("count runs: %w", err)
	}
	rows, err := s.db.pool.Query(ctx, runSelect+`
		WHERE r.project_id = $1 AND ($2 = '' OR r.status = $2)
		ORDER BY r.created_at DESC, r.id DESC
		LIMIT $3 OFFSET $4`,
		query.ProjectID, string(query.Status), query.Limit, query.Offset)
	if err != nil {
		return nil, 0, fmt.Errorf("select runs: %w", err)
	}
	runs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (run.Run, error) {
		var r runRow
		err := row.Scan(r.targets()...)
		return r.run(), err
	})
	if err != nil {
		return nil, 0, fmt.Errorf("read runs: %w", err)
	}
	return runs, total, nil
}

func (s *Runs) Run(ctx context.Context, projectID, id string) (run.Run, error) {
	if !validID(id) {
		return run.Run{}, fmt.Errorf("run %s: %w", id, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	return oneRun(ctx, s.db.pool, projectID, id)
}

func oneRun(ctx context.Context, q querier, projectID, id string) (run.Run, error) {
	var row runRow
	err := q.QueryRow(ctx, runSelect+` WHERE r.project_id = $1 AND r.id = $2`, projectID, id).Scan(row.targets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return run.Run{}, fmt.Errorf("run %s: %w", id, fault.ErrNotFound)
	}
	if err != nil {
		return run.Run{}, fmt.Errorf("query run: %w", err)
	}
	return row.run(), nil
}

func (s *Runs) Results(ctx context.Context, runID string) ([]run.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	rows, err := s.db.pool.Query(ctx, `SELECT `+resultColumns+` FROM run_results WHERE run_id = $1
		ORDER BY device_name, device_id, position`, runID)
	if err != nil {
		return nil, fmt.Errorf("select results: %w", err)
	}
	results, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (run.Result, error) {
		var r resultRow
		err := row.Scan(r.targets()...)
		return r.result(), err
	})
	if err != nil {
		return nil, fmt.Errorf("read results: %w", err)
	}
	return results, nil
}

func (s *Runs) RunningResult(ctx context.Context, hubID, resultID string) (run.Result, error) {
	if !validID(resultID) || !validID(hubID) {
		return run.Result{}, fmt.Errorf("result %s: %w", resultID, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var r resultRow
	err := s.db.pool.QueryRow(ctx, `SELECT `+resultColumns+` FROM run_results
		WHERE id = $1 AND hub_id = $2 AND status = 'running'`, resultID, hubID).Scan(r.targets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return run.Result{}, fmt.Errorf("result %s: %w", resultID, fault.ErrNotFound)
	}
	if err != nil {
		return run.Result{}, fmt.Errorf("select result: %w", err)
	}
	return r.result(), nil
}

func (s *Runs) Attempts(ctx context.Context, runID, resultID string) ([]run.Attempt, error) {
	if !validID(resultID) {
		return nil, fmt.Errorf("result %s: %w", resultID, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var exists bool
	err := s.db.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM run_results WHERE run_id = $1 AND id = $2)`,
		runID, resultID).Scan(&exists)
	if err != nil {
		return nil, fmt.Errorf("check result: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("result %s: %w", resultID, fault.ErrNotFound)
	}
	rows, err := s.db.pool.Query(ctx, `
		SELECT number, status, coalesce(failed_step, 0), error, steps, video_ms, video_problem, started_at,
			finished_at
		FROM run_attempts WHERE result_id = $1 ORDER BY number`, resultID)
	if err != nil {
		return nil, fmt.Errorf("select attempts: %w", err)
	}
	attempts, err := pgx.CollectRows(rows, scanAttempt)
	if err != nil {
		return nil, fmt.Errorf("read attempts: %w", err)
	}
	return attempts, nil
}

func scanAttempt(row pgx.CollectableRow) (run.Attempt, error) {
	var a run.Attempt
	var status string
	var steps []stepOutcomeDocument
	var videoMS *int64
	var videoProblem *string
	err := row.Scan(&a.Number, &status, &a.FailedStep, &a.Error, &steps, &videoMS, &videoProblem, &a.StartedAt,
		&a.FinishedAt)
	if err != nil {
		return run.Attempt{}, err
	}
	a.Status = run.AttemptStatus(status)
	if videoProblem != nil {
		a.Video = &run.Video{Problem: *videoProblem}
	}
	if videoMS != nil {
		a.Video = &run.Video{Duration: time.Duration(*videoMS) * time.Millisecond}
	}
	a.Steps = make([]run.StepOutcome, len(steps))
	for i, step := range steps {
		a.Steps[i] = run.StepOutcome{
			Status: run.StepStatus(step.Status), Error: step.Error,
			Duration: time.Duration(step.DurationMS) * time.Millisecond,
		}
	}
	return a, nil
}

func (s *Runs) CancelRun(ctx context.Context, projectID, id string) (run.Run, error) {
	if !validID(id) {
		return run.Run{}, fmt.Errorf("run %s: %w", id, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	now := s.db.clock.Now()
	var cancelled run.Run
	err := pgx.BeginFunc(ctx, s.db.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE runs SET status = 'cancelled', finished_at = $3
			WHERE project_id = $1 AND id = $2 AND status IN `+unfinished,
			projectID, id, now)
		if err != nil {
			return fmt.Errorf("cancel run: %w", err)
		}
		if tag.RowsAffected() == 0 {
			if _, err := oneRun(ctx, tx, projectID, id); err != nil {
				return err
			}
			return fmt.Errorf("run %s has finished: %w", id, fault.ErrConflict)
		}
		if err := s.cancelResults(ctx, tx, id, now); err != nil {
			return err
		}
		cancelled, err = oneRun(ctx, tx, projectID, id)
		return err
	})
	if err != nil {
		return run.Run{}, fmt.Errorf("cancel run: %w", err)
	}
	return cancelled, nil
}

func (s *Runs) cancelResults(ctx context.Context, tx pgx.Tx, runID string, now time.Time) error {
	rows, err := tx.Query(ctx, `
		UPDATE run_results SET status = 'cancelled', finished_at = $2
		WHERE run_id = $1 AND status IN `+unfinished+`
		RETURNING coalesce(job_id::text, '')`,
		runID, now)
	if err != nil {
		return fmt.Errorf("cancel results: %w", err)
	}
	jobIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("read cancelled jobs: %w", err)
	}
	slices.Sort(jobIDs)
	for _, jobID := range slices.Compact(jobIDs) {
		if jobID == "" {
			continue
		}
		if _, err := s.db.cancelJob(ctx, tx, jobID); err != nil {
			return err
		}
	}
	return nil
}

// Work loads a device's unfinished results with the package its hub installs.
func (s *Runs) Work(ctx context.Context, runID, deviceID string) (run.Work, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var work run.Work
	var row runRow
	err := s.db.pool.QueryRow(ctx, runSelect+` WHERE r.id = $1`, runID).Scan(row.targets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return run.Work{}, fmt.Errorf("run %s: %w", runID, fault.ErrNotFound)
	}
	if err != nil {
		return run.Work{}, fmt.Errorf("query run work: %w", err)
	}
	work.Run = row.run()
	if work.Run.BuildID != "" {
		work.Package = &run.Package{BuildID: work.Run.BuildID}
		err := s.db.pool.QueryRow(ctx, `SELECT sha256, size FROM builds WHERE id = $1`, work.Run.BuildID).
			Scan(&work.Package.SHA256, &work.Package.Size)
		if err != nil {
			return run.Work{}, fmt.Errorf("query run package: %w", err)
		}
	}
	rows, err := s.db.pool.Query(ctx, `
		SELECT `+resultColumns+`, steps, script, device_serial, hub_id
		FROM run_results WHERE run_id = $1 AND device_id = $2 AND status IN `+unfinished+`
		ORDER BY position`, runID, deviceID)
	if err != nil {
		return run.Work{}, fmt.Errorf("select pending results: %w", err)
	}
	work.Results, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (run.Pending, error) {
		var r resultRow
		var steps []stepDocument
		var script []opDocument
		err := row.Scan(append(r.targets(), &steps, &script, &work.Serial, &work.HubID)...)
		return run.Pending{Result: r.result(), Steps: openSteps(steps), Script: openOps(script)}, err
	})
	if err != nil {
		return run.Work{}, fmt.Errorf("read pending results: %w", err)
	}
	return work, nil
}

func (s *Runs) StartResult(ctx context.Context, resultID string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	now := s.db.clock.Now()
	var started bool
	err := pgx.BeginFunc(ctx, s.db.pool, func(tx pgx.Tx) error {
		runID, err := lockRunOf(ctx, tx, resultID)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE run_results SET status = 'running', started_at = coalesce(started_at, $2)
			WHERE id = $1 AND status IN `+unfinished, resultID, now)
		if err != nil {
			return fmt.Errorf("start result: %w", err)
		}
		if started = tag.RowsAffected() == 1; !started {
			return nil
		}
		_, err = tx.Exec(ctx, `UPDATE runs SET status = 'running', started_at = $2 WHERE id = $1 AND status = 'queued'`,
			runID, now)
		if err != nil {
			return fmt.Errorf("start run: %w", err)
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("start result: %w", err)
	}
	return started, nil
}

// lockRunOf locks the run a result belongs to. Every write that moves a result between unfinished and
// finished takes this lock first, so the check for a run's last result never races, and the lock order
// (run, then results) matches CancelRun's.
func lockRunOf(ctx context.Context, tx pgx.Tx, resultID string) (string, error) {
	var runID string
	err := tx.QueryRow(ctx, `
		SELECT r.id FROM runs r JOIN run_results rr ON rr.run_id = r.id
		WHERE rr.id = $1
		FOR UPDATE OF r`, resultID).Scan(&runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("result %s: %w", resultID, fault.ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("lock run: %w", err)
	}
	return runID, nil
}

func (s *Runs) RecordAttempt(ctx context.Context, resultID string, attempt run.Attempt) error {
	steps := make([]stepOutcomeDocument, len(attempt.Steps))
	for i, step := range attempt.Steps {
		steps[i] = stepOutcomeDocument{
			Status: string(step.Status), Error: step.Error, DurationMS: step.Duration.Milliseconds(),
		}
	}
	encoded, err := json.Marshal(steps)
	if err != nil {
		return fmt.Errorf("encode step outcomes: %w", err)
	}
	var videoMS *int64
	var videoProblem *string
	switch v := attempt.Video; {
	case v == nil:
	case v.Problem != "":
		videoProblem = &v.Problem
	default:
		ms := v.Duration.Milliseconds()
		videoMS = &ms
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	return pgx.BeginFunc(ctx, s.db.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE run_results SET attempts = $2, failed_step = $3, error = $4
			WHERE id = $1 AND status = 'running' AND attempts = $2 - 1`,
			resultID, attempt.Number, nullableStep(attempt.FailedStep), attempt.Error)
		if err != nil {
			return fmt.Errorf("count attempt: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("result %s is not running attempt %d: %w", resultID, attempt.Number, fault.ErrConflict)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO run_attempts (result_id, number, status, failed_step, error, steps, video_ms, video_problem,
				started_at, finished_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			resultID, attempt.Number, string(attempt.Status), nullableStep(attempt.FailedStep), attempt.Error, encoded,
			videoMS, videoProblem, attempt.StartedAt, attempt.FinishedAt)
		if err != nil {
			return fmt.Errorf("insert attempt: %w", err)
		}
		return nil
	})
}

func (s *Runs) FinishResult(ctx context.Context, resultID string, finish run.Finish) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	now := s.db.clock.Now()
	return pgx.BeginFunc(ctx, s.db.pool, func(tx pgx.Tx) error {
		runID, err := lockRunOf(ctx, tx, resultID)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE run_results SET status = $2, failed_step = $3, error = $4, finished_at = $5
			WHERE id = $1 AND status IN `+unfinished,
			resultID, string(finish.Status), nullableStep(finish.FailedStep), finish.Error, now)
		if err != nil {
			return fmt.Errorf("finish result: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("result %s has finished: %w", resultID, fault.ErrConflict)
		}
		_, err = tx.Exec(ctx, `
			UPDATE runs SET
				status = CASE WHEN EXISTS (
					SELECT 1 FROM run_results WHERE run_id = $1 AND status IN ('failed', 'error')
				) THEN 'failed' ELSE 'passed' END,
				started_at = coalesce(started_at, $2),
				finished_at = $2
			WHERE id = $1 AND status IN `+unfinished+`
				AND NOT EXISTS (SELECT 1 FROM run_results WHERE run_id = $1 AND status IN `+unfinished+`)`,
			runID, now)
		if err != nil {
			return fmt.Errorf("finish run: %w", err)
		}
		return nil
	})
}
