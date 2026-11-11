package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/authoring"
	"github.com/mobqa/backend/internal/usecase/device"
	"github.com/mobqa/backend/internal/usecase/knowledge"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/test"
	"github.com/mobqa/backend/internal/usecase/ui"
	"github.com/mobqa/backend/internal/worker"
)

// authoringsListed bounds a test's history; older authorings stay readable by id.
const authoringsListed = 20

const authoringColumns = `a.id, a.project_id, a.test_id, a.test_version, a.platform, a.device_id, a.device_name,
	coalesce(a.environment_id::text, ''), a.environment_name, a.status, a.failure, a.created_by, a.created_at,
	a.started_at, a.finished_at, a.reviewed_at, a.knowledge_ids::text[]`

const unfinishedAuthoring = `('queued', 'running')`

type authoringRow struct {
	id              string
	projectID       string
	testID          string
	testVersion     int
	platform        string
	deviceID        string
	deviceName      string
	environmentID   string
	environmentName string
	status          string
	failure         string
	createdBy       string
	createdAt       time.Time
	startedAt       *time.Time
	finishedAt      *time.Time
	reviewedAt      *time.Time
	knowledgeIDs    []string
}

func (r *authoringRow) targets() []any {
	return []any{
		&r.id, &r.projectID, &r.testID, &r.testVersion, &r.platform, &r.deviceID, &r.deviceName, &r.environmentID,
		&r.environmentName, &r.status, &r.failure, &r.createdBy, &r.createdAt, &r.startedAt, &r.finishedAt,
		&r.reviewedAt, &r.knowledgeIDs,
	}
}

func (r authoringRow) authoring() authoring.Authoring {
	return authoring.Authoring{
		ID: r.id, ProjectID: r.projectID, TestID: r.testID, TestVersion: r.testVersion,
		Platform: project.Platform(r.platform), DeviceID: r.deviceID, DeviceName: r.deviceName,
		EnvironmentID: r.environmentID, EnvironmentName: r.environmentName, Status: authoring.Status(r.status),
		Failure: r.failure, CreatedBy: r.createdBy, CreatedAt: r.createdAt, StartedAt: r.startedAt,
		FinishedAt: r.finishedAt, ReviewedAt: r.reviewedAt, Knowledge: r.knowledgeIDs,
	}
}

type selectorDocument struct {
	ID    string `json:"id,omitempty"`
	Text  string `json:"text,omitempty"`
	Label string `json:"label,omitempty"`
	Index int    `json:"index,omitempty"`
}

type opDocument struct {
	Step      int               `json:"step"`
	Kind      string            `json:"kind"`
	Selector  *selectorDocument `json:"selector,omitempty"`
	Direction string            `json:"direction,omitempty"`
	Text      string            `json:"text,omitempty"`
}

type turnDocument struct {
	Step    int    `json:"step"`
	Action  string `json:"action"`
	Element string `json:"element,omitempty"`
	Text    string `json:"text,omitempty"`
	Thought string `json:"thought,omitempty"`
	Error   string `json:"error,omitempty"`
}

func opDocuments(ops []ui.Op) []opDocument {
	documents := make([]opDocument, len(ops))
	for i, op := range ops {
		documents[i] = opDocument{Step: op.Step, Kind: string(op.Kind), Direction: string(op.Direction), Text: op.Text}
		if s := op.Selector; s != nil {
			documents[i].Selector = &selectorDocument{ID: s.ID, Text: s.Text, Label: s.Label, Index: s.Index}
		}
	}
	return documents
}

func openOps(documents []opDocument) []ui.Op {
	ops := make([]ui.Op, len(documents))
	for i, d := range documents {
		ops[i] = ui.Op{Step: d.Step, Kind: ui.Kind(d.Kind), Direction: ui.Direction(d.Direction), Text: d.Text}
		if s := d.Selector; s != nil {
			ops[i].Selector = &ui.Selector{ID: s.ID, Text: s.Text, Label: s.Label, Index: s.Index}
		}
	}
	return ops
}

func turnDocuments(turns []authoring.Turn) []turnDocument {
	documents := make([]turnDocument, len(turns))
	for i, t := range turns {
		documents[i] = turnDocument{
			Step: t.Step, Action: t.Action, Element: t.Element, Text: t.Text, Thought: t.Thought, Error: t.Error,
		}
	}
	return documents
}

func openTurns(documents []turnDocument) []authoring.Turn {
	turns := make([]authoring.Turn, len(documents))
	for i, d := range documents {
		turns[i] = authoring.Turn{
			Step: d.Step, Action: d.Action, Element: d.Element, Text: d.Text, Thought: d.Thought, Error: d.Error,
		}
	}
	return turns
}

type Authorings struct {
	db *DB
}

func NewAuthorings(db *DB) *Authorings {
	return &Authorings{db: db}
}

func (s *Authorings) Test(ctx context.Context, projectID, id string) (test.Test, error) {
	return NewTests(s.db).Test(ctx, projectID, id)
}

func (s *Authorings) Device(ctx context.Context, projectID, id string) (device.Device, error) {
	return NewDevices(s.db).Device(ctx, projectID, id)
}

func (s *Authorings) Environment(ctx context.Context, projectID, id string) (authoring.Pin, error) {
	if !validID(id) {
		return authoring.Pin{}, fmt.Errorf("environment %s: %w", id, fault.ErrNotFound)
	}
	return s.pin(ctx, `SELECT id, name FROM environments WHERE project_id = $1 AND id = $2`, projectID, id)
}

func (s *Authorings) DefaultEnvironment(ctx context.Context, projectID string) (authoring.Pin, error) {
	return s.pin(ctx, `SELECT id, name FROM environments WHERE project_id = $1 AND is_default`, projectID)
}

func (s *Authorings) pin(ctx context.Context, statement string, args ...any) (authoring.Pin, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var pin authoring.Pin
	err := s.db.pool.QueryRow(ctx, statement, args...).Scan(&pin.ID, &pin.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return authoring.Pin{}, fmt.Errorf("environment: %w", fault.ErrNotFound)
	}
	if err != nil {
		return authoring.Pin{}, fmt.Errorf("query environment: %w", err)
	}
	return pin, nil
}

func (s *Authorings) CreateAuthoring(ctx context.Context, in authoring.NewAuthoring) (authoring.Authoring, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	a := in.Authoring
	var created authoring.Authoring
	err := pgx.BeginFunc(ctx, s.db.pool, func(tx pgx.Tx) error {
		jobID, err := s.db.enqueue(ctx, tx, in.Job)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO authorings (id, project_id, test_id, test_version, platform, device_id, device_name,
				device_serial, hub_id, environment_id, environment_name, status, script, log, failure, job_id,
				created_by, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, nullif($10, '')::uuid, $11, $12, '[]', '[]', '', $13, $14, $15)`,
			a.ID, a.ProjectID, a.TestID, a.TestVersion, string(a.Platform), a.DeviceID, a.DeviceName, in.Serial,
			in.HubID, a.EnvironmentID, a.EnvironmentName, string(a.Status), jobID, a.CreatedBy, s.db.clock.Now())
		if isUniqueViolation(err) {
			return fmt.Errorf("test %s is being authored: %w", a.TestID, fault.ErrConflict)
		}
		if err != nil {
			return fmt.Errorf("insert authoring: %w", err)
		}
		created, err = oneAuthoring(ctx, tx, a.ProjectID, a.ID)
		return err
	})
	if err != nil {
		return authoring.Authoring{}, fmt.Errorf("create authoring: %w", err)
	}
	return created, nil
}

func (s *Authorings) Authorings(ctx context.Context, projectID, testID string) ([]authoring.Authoring, error) {
	if !validID(testID) {
		return []authoring.Authoring{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	rows, err := s.db.pool.Query(ctx, `
		SELECT `+authoringColumns+` FROM authorings a
		WHERE a.project_id = $1 AND a.test_id = $2
		ORDER BY a.created_at DESC, a.id DESC
		LIMIT $3`,
		projectID, testID, authoringsListed)
	if err != nil {
		return nil, fmt.Errorf("select authorings: %w", err)
	}
	found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (authoring.Authoring, error) {
		var r authoringRow
		err := row.Scan(r.targets()...)
		return r.authoring(), err
	})
	if err != nil {
		return nil, fmt.Errorf("read authorings: %w", err)
	}
	return found, nil
}

func (s *Authorings) Authoring(ctx context.Context, projectID, id string) (authoring.Authoring, error) {
	if !validID(id) {
		return authoring.Authoring{}, fmt.Errorf("authoring %s: %w", id, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	return oneAuthoring(ctx, s.db.pool, projectID, id)
}

func oneAuthoring(ctx context.Context, q querier, projectID, id string) (authoring.Authoring, error) {
	var r authoringRow
	var script []opDocument
	var log []turnDocument
	err := q.QueryRow(ctx, `
		SELECT `+authoringColumns+`, a.script, a.log FROM authorings a WHERE a.project_id = $1 AND a.id = $2`,
		projectID, id,
	).Scan(append(r.targets(), &script, &log)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return authoring.Authoring{}, fmt.Errorf("authoring %s: %w", id, fault.ErrNotFound)
	}
	if err != nil {
		return authoring.Authoring{}, fmt.Errorf("query authoring: %w", err)
	}
	found := r.authoring()
	found.Script, found.Log = openOps(script), openTurns(log)
	return found, nil
}

func (s *Authorings) CancelAuthoring(ctx context.Context, projectID, id string) (authoring.Authoring, error) {
	if !validID(id) {
		return authoring.Authoring{}, fmt.Errorf("authoring %s: %w", id, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var cancelled authoring.Authoring
	err := pgx.BeginFunc(ctx, s.db.pool, func(tx pgx.Tx) error {
		var jobID string
		err := tx.QueryRow(ctx, `
			UPDATE authorings SET status = 'cancelled', finished_at = $3
			WHERE project_id = $1 AND id = $2 AND status IN `+unfinishedAuthoring+`
			RETURNING coalesce(job_id::text, '')`,
			projectID, id, s.db.clock.Now(),
		).Scan(&jobID)
		if errors.Is(err, pgx.ErrNoRows) {
			if _, err := oneAuthoring(ctx, tx, projectID, id); err != nil {
				return err
			}
			return fmt.Errorf("authoring %s has finished: %w", id, fault.ErrConflict)
		}
		if err != nil {
			return fmt.Errorf("cancel authoring: %w", err)
		}
		if jobID != "" {
			if _, err := s.db.cancelJob(ctx, tx, jobID); err != nil {
				return err
			}
		}
		cancelled, err = oneAuthoring(ctx, tx, projectID, id)
		return err
	})
	if err != nil {
		return authoring.Authoring{}, fmt.Errorf("cancel authoring: %w", err)
	}
	return cancelled, nil
}

// Work loads an authoring with the steps of its test version.
func (s *Authorings) Work(ctx context.Context, id string) (authoring.Work, error) {
	if !validID(id) {
		return authoring.Work{}, fmt.Errorf("authoring %s: %w", id, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var r authoringRow
	var w authoring.Work
	var steps []stepDocument
	var script []opDocument
	var log []turnDocument
	err := s.db.pool.QueryRow(ctx, `
		SELECT `+authoringColumns+`, a.script, a.log, a.hub_id, a.device_serial, v.title, v.steps
		FROM authorings a JOIN test_versions v ON v.test_id = a.test_id AND v.version = a.test_version
		WHERE a.id = $1`,
		id,
	).Scan(append(r.targets(), &script, &log, &w.HubID, &w.Serial, &w.Title, &steps)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return authoring.Work{}, fmt.Errorf("authoring %s: %w", id, fault.ErrNotFound)
	}
	if err != nil {
		return authoring.Work{}, fmt.Errorf("query authoring work: %w", err)
	}
	w.Authoring, w.Steps = r.authoring(), openSteps(steps)
	w.Authoring.Script, w.Authoring.Log = openOps(script), openTurns(log)
	return w, nil
}

func (s *Authorings) StartAuthoring(ctx context.Context, id string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tag, err := s.db.pool.Exec(ctx, `
		UPDATE authorings SET status = 'running', started_at = coalesce(started_at, $2)
		WHERE id = $1 AND status IN `+unfinishedAuthoring,
		id, s.db.clock.Now())
	if err != nil {
		return false, fmt.Errorf("start authoring: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *Authorings) FinishAuthoring(ctx context.Context, id string, finish authoring.Finish,
	jobs ...worker.EnqueueInput,
) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	err := pgx.BeginFunc(ctx, s.db.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE authorings SET status = $2, script = $3, log = $4, failure = $5, finished_at = $6,
				knowledge_ids = coalesce($7::uuid[], '{}')
			WHERE id = $1 AND status IN `+unfinishedAuthoring,
			id, string(finish.Status), opDocuments(finish.Script), turnDocuments(finish.Log), finish.Failure,
			s.db.clock.Now(), validIDs(finish.Knowledge))
		if err != nil {
			return fmt.Errorf("update authoring: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("authoring %s has finished: %w", id, fault.ErrConflict)
		}
		for _, job := range jobs {
			if _, err := s.db.enqueue(ctx, tx, job); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("finish authoring: %w", err)
	}
	return nil
}

func (s *Authorings) AgentNotes(ctx context.Context, projectID string, limit int) ([]knowledge.Note, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	rows, err := s.db.pool.Query(ctx, `
		SELECT `+noteColumns+` FROM knowledge_notes WHERE project_id = $1 AND source = 'agent'
		ORDER BY updated_at DESC, id DESC
		LIMIT $2`,
		projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("query agent notes: %w", err)
	}
	return collectNotes(rows)
}

func (s *Authorings) SaveLessons(ctx context.Context, authoringID string, lessons []authoring.Lesson,
	retired []string,
) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	now := s.db.clock.Now()
	err := pgx.BeginFunc(ctx, s.db.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE authorings SET reviewed_at = $2 WHERE id = $1 AND reviewed_at IS NULL`,
			authoringID, now)
		if err != nil {
			return fmt.Errorf("mark authoring reviewed: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("authoring %s has been reviewed: %w", authoringID, fault.ErrConflict)
		}
		for _, lesson := range lessons {
			if err := s.saveLesson(ctx, tx, lesson, now); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `
			DELETE FROM knowledge_notes n USING authorings a
			WHERE a.id = $1 AND n.project_id = a.project_id AND n.source = 'agent' AND n.id = ANY($2::uuid[])`,
			authoringID, validIDs(retired))
		if err != nil {
			return fmt.Errorf("retire notes: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("save lessons: %w", err)
	}
	return nil
}

// saveLesson inserts the lesson's note, or rewrites it when the agent wrote it before. A note of the
// same id that people wrote, or that belongs to another project, is left alone and not indexed again.
func (s *Authorings) saveLesson(ctx context.Context, tx pgx.Tx, lesson authoring.Lesson, now time.Time) error {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO knowledge_notes AS n (id, project_id, kind, title, content, source, pinned, revision,
			indexed_revision, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 'agent', false, 1, 0, $6, $7, $7)
		ON CONFLICT (id) DO UPDATE SET kind = excluded.kind, title = excluded.title, content = excluded.content,
			revision = n.revision + 1, updated_at = excluded.updated_at
		WHERE n.source = 'agent' AND n.project_id = excluded.project_id
		RETURNING n.id`,
		lesson.NoteID, lesson.ProjectID, string(lesson.Kind), lesson.Title, lesson.Content, lesson.CreatedBy, now,
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("save lesson: %w", err)
	}
	_, err = s.db.enqueue(ctx, tx, lesson.Index)
	return err
}
