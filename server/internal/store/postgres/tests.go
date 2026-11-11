package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/test"
)

// testColumns read a test with its current version's content, which the query joins as v.
const testColumns = `t.id, t.project_id, t.platform, t.priority, t.tags, t.source, t.current_version, v.title,
	v.description, jsonb_array_length(v.steps), t.created_by, t.updated_by, t.created_at, t.updated_at`

const currentVersion = `FROM tests t JOIN test_versions v ON v.test_id = t.id AND v.version = t.current_version`

// testsFilter matches a project's tests whose current title contains the search text literally.
const testsFilter = `WHERE t.project_id = $1
	AND ($2 = '' OR v.title ILIKE '%' || replace(replace(replace($2, '\', '\\'), '%', '\%'), '_', '\_') || '%')
	AND ($3 = '' OR t.tags @> ARRAY[$3]::text[])
	AND ($4 = '' OR t.priority = $4)
	AND ($5 = '' OR t.platform = $5)`

const versionColumns = `v.version, v.title, v.description, jsonb_array_length(v.steps), v.change_source, v.note,
	v.created_by, v.created_at`

type testRow struct {
	id          string
	projectID   string
	platform    string
	priority    string
	tags        []string
	source      string
	version     int
	title       string
	description string
	stepCount   int
	createdBy   string
	updatedBy   string
	createdAt   time.Time
	updatedAt   time.Time
}

func (r *testRow) targets() []any {
	return []any{
		&r.id, &r.projectID, &r.platform, &r.priority, &r.tags, &r.source, &r.version, &r.title, &r.description,
		&r.stepCount, &r.createdBy, &r.updatedBy, &r.createdAt, &r.updatedAt,
	}
}

func (r testRow) test(steps []stepDocument) test.Test {
	return test.Test{
		ID:        r.id,
		ProjectID: r.projectID,
		Platform:  project.Platform(r.platform),
		Priority:  test.Priority(r.priority),
		Tags:      r.tags,
		Source:    test.Source(r.source),
		Content:   test.Content{Title: r.title, Description: r.description, Steps: openSteps(steps)},
		Version:   r.version,
		StepCount: r.stepCount,
		CreatedBy: r.createdBy,
		UpdatedBy: r.updatedBy,
		CreatedAt: r.createdAt,
		UpdatedAt: r.updatedAt,
	}
}

type versionRow struct {
	number      int
	title       string
	description string
	stepCount   int
	source      string
	note        string
	createdBy   string
	createdAt   time.Time
}

func (r *versionRow) targets() []any {
	return []any{
		&r.number, &r.title, &r.description, &r.stepCount, &r.source, &r.note, &r.createdBy, &r.createdAt,
	}
}

func (r versionRow) version(steps []stepDocument) test.Version {
	return test.Version{
		Number:    r.number,
		Content:   test.Content{Title: r.title, Description: r.description, Steps: openSteps(steps)},
		StepCount: r.stepCount,
		Source:    test.ChangeSource(r.source),
		Note:      r.note,
		CreatedBy: r.createdBy,
		CreatedAt: r.createdAt,
	}
}

type stepDocument struct {
	Action         string `json:"action"`
	Target         string `json:"target,omitempty"`
	Description    string `json:"description,omitempty"`
	ExpectedResult string `json:"expected_result,omitempty"`
}

func stepDocuments(steps []test.Step) []stepDocument {
	documents := make([]stepDocument, len(steps))
	for i, step := range steps {
		documents[i] = stepDocument{
			Action: string(step.Action), Target: step.Target, Description: step.Description,
			ExpectedResult: step.ExpectedResult,
		}
	}
	return documents
}

func openSteps(documents []stepDocument) []test.Step {
	if documents == nil {
		return nil
	}
	steps := make([]test.Step, len(documents))
	for i, document := range documents {
		steps[i] = test.Step{
			Action: test.Action(document.Action), Target: document.Target, Description: document.Description,
			ExpectedResult: document.ExpectedResult,
		}
	}
	return steps
}

type Tests struct {
	db *DB
}

func NewTests(db *DB) *Tests {
	return &Tests{db: db}
}

// CreateTest stores the test together with its first version.
func (s *Tests) CreateTest(ctx context.Context, in test.NewTest) (test.Test, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var created test.Test
	err := pgx.BeginFunc(ctx, s.db.pool, func(tx pgx.Tx) error {
		id, err := insertTest(ctx, tx, in, s.db.clock.Now())
		if err != nil {
			return err
		}
		created, err = detail(ctx, tx, in.ProjectID, id)
		return err
	})
	if err != nil {
		return test.Test{}, fmt.Errorf("create test: %w", err)
	}
	return created, nil
}

func insertTest(ctx context.Context, tx pgx.Tx, in test.NewTest, now time.Time) (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate test id: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO tests (id, project_id, platform, priority, tags, source, current_version, created_by,
			updated_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, coalesce($5::text[], '{}'), $6, 1, $7, $7, $8, $8)`,
		id, in.ProjectID, string(in.Platform), string(in.Priority), in.Tags, string(in.Source), in.CreatedBy, now)
	if err != nil {
		return "", fmt.Errorf("insert test: %w", err)
	}
	err = insertVersion(ctx, tx, versionInput{
		testID: id.String(), number: 1, content: in.Content, source: in.Change, createdBy: in.CreatedBy, createdAt: now,
	})
	return id.String(), err
}

func (s *Tests) Tests(ctx context.Context, query test.Query) ([]test.Test, int, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	filters := []any{
		query.ProjectID, query.Search, query.Tag, string(query.Priority), string(query.Platform),
	}
	var total int
	if err := s.db.pool.QueryRow(ctx, `SELECT count(*) `+currentVersion+` `+testsFilter, filters...,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count tests: %w", err)
	}
	rows, err := s.db.pool.Query(ctx, `
		SELECT `+testColumns+` `+currentVersion+` `+testsFilter+`
		ORDER BY t.updated_at DESC, t.id DESC
		LIMIT $6 OFFSET $7`,
		append(filters, query.Limit, query.Offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("select tests: %w", err)
	}
	tests, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (test.Test, error) {
		var r testRow
		err := row.Scan(r.targets()...)
		return r.test(nil), err
	})
	if err != nil {
		return nil, 0, fmt.Errorf("read tests: %w", err)
	}
	return tests, total, nil
}

func (s *Tests) Test(ctx context.Context, projectID, id string) (test.Test, error) {
	if !validID(id) {
		return test.Test{}, fmt.Errorf("test %s: %w", id, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	return detail(ctx, s.db.pool, projectID, id)
}

// UpdateTest locks the test so that concurrent content changes are serialised and the second one sees
// that the test has moved past its base version.
func (s *Tests) UpdateTest(ctx context.Context, change test.Change) (test.Test, error) {
	if !validID(change.TestID) {
		return test.Test{}, fmt.Errorf("test %s: %w", change.TestID, fault.ErrNotFound)
	}
	var priority *string
	if change.Priority != nil {
		priority = (*string)(change.Priority)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	now := s.db.clock.Now()
	var updated test.Test
	err := pgx.BeginFunc(ctx, s.db.pool, func(tx pgx.Tx) error {
		var version int
		if err := tx.QueryRow(ctx, `SELECT current_version FROM tests WHERE project_id = $1 AND id = $2 FOR UPDATE`,
			change.ProjectID, change.TestID).Scan(&version); err != nil {
			return err
		}
		if change.Content != nil {
			if version != change.BaseVersion {
				return fmt.Errorf("test %s is at version %d: %w", change.TestID, version, fault.ErrConflict)
			}
			version++
			if err := insertVersion(ctx, tx, versionInput{
				testID: change.TestID, number: version, content: *change.Content, source: change.Source,
				note: change.Note, createdBy: change.UserID, createdAt: now,
			}); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE tests SET priority = coalesce($3, priority), tags = coalesce($4, tags), current_version = $5,
				updated_by = $6, updated_at = $7
			WHERE project_id = $1 AND id = $2`,
			change.ProjectID, change.TestID, priority, change.Tags, version, change.UserID, now); err != nil {
			return fmt.Errorf("update test: %w", err)
		}
		var err error
		updated, err = detail(ctx, tx, change.ProjectID, change.TestID)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return test.Test{}, fmt.Errorf("test %s: %w", change.TestID, fault.ErrNotFound)
	}
	if err != nil {
		return test.Test{}, fmt.Errorf("update test: %w", err)
	}
	return updated, nil
}

func (s *Tests) DeleteTest(ctx context.Context, projectID, id string) error {
	if !validID(id) {
		return fmt.Errorf("test %s: %w", id, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tag, err := s.db.pool.Exec(ctx, `DELETE FROM tests WHERE project_id = $1 AND id = $2`, projectID, id)
	if err != nil {
		return fmt.Errorf("delete test: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("test %s: %w", id, fault.ErrNotFound)
	}
	return nil
}

// Versions reports ErrNotFound when the test has no versions, which happens only when it does not exist.
func (s *Tests) Versions(ctx context.Context, projectID, testID string) ([]test.Version, error) {
	if !validID(testID) {
		return nil, fmt.Errorf("test %s: %w", testID, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	rows, err := s.db.pool.Query(ctx, `
		SELECT `+versionColumns+`
		FROM test_versions v JOIN tests t ON t.id = v.test_id
		WHERE t.project_id = $1 AND t.id = $2
		ORDER BY v.version DESC`,
		projectID, testID)
	if err != nil {
		return nil, fmt.Errorf("select test versions: %w", err)
	}
	versions, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (test.Version, error) {
		var r versionRow
		err := row.Scan(r.targets()...)
		return r.version(nil), err
	})
	if err != nil {
		return nil, fmt.Errorf("read test versions: %w", err)
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("test %s: %w", testID, fault.ErrNotFound)
	}
	return versions, nil
}

func (s *Tests) Version(ctx context.Context, projectID, testID string, number int) (test.Version, error) {
	if !validID(testID) {
		return test.Version{}, fmt.Errorf("test %s: %w", testID, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var r versionRow
	var steps []stepDocument
	err := s.db.pool.QueryRow(ctx, `
		SELECT `+versionColumns+`, v.steps
		FROM test_versions v JOIN tests t ON t.id = v.test_id
		WHERE t.project_id = $1 AND t.id = $2 AND v.version = $3`,
		projectID, testID, number,
	).Scan(append(r.targets(), &steps)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return test.Version{}, fmt.Errorf("test %s version %d: %w", testID, number, fault.ErrNotFound)
	}
	if err != nil {
		return test.Version{}, fmt.Errorf("query test version: %w", err)
	}
	return r.version(steps), nil
}

type versionInput struct {
	testID    string
	number    int
	content   test.Content
	source    test.ChangeSource
	note      string
	createdBy string
	createdAt time.Time
}

func insertVersion(ctx context.Context, tx pgx.Tx, in versionInput) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO test_versions (test_id, version, title, description, steps, change_source, note, created_by,
			created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		in.testID, in.number, in.content.Title, in.content.Description, stepDocuments(in.content.Steps), string(in.source),
		in.note, in.createdBy, in.createdAt)
	if err != nil {
		return fmt.Errorf("insert test version: %w", err)
	}
	return nil
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// detail reads a test with the steps of its current version.
func detail(ctx context.Context, q querier, projectID, id string) (test.Test, error) {
	var r testRow
	var steps []stepDocument
	err := q.QueryRow(ctx, `SELECT `+testColumns+`, v.steps `+currentVersion+` WHERE t.project_id = $1 AND t.id = $2`,
		projectID, id,
	).Scan(append(r.targets(), &steps)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return test.Test{}, fmt.Errorf("test %s: %w", id, fault.ErrNotFound)
	}
	if err != nil {
		return test.Test{}, fmt.Errorf("query test: %w", err)
	}
	return r.test(steps), nil
}
