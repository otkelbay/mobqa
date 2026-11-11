package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/action"
)

const actionColumns = `id, project_id, name, description, method, url, headers, body, expected_status, extract,
	created_at, updated_at`

type actionRow struct {
	id             string
	projectID      string
	name           string
	description    string
	method         string
	url            string
	headers        []headerDocument
	body           string
	expectedStatus int
	extract        []extractionDocument
	createdAt      time.Time
	updatedAt      time.Time
}

func (r *actionRow) targets() []any {
	return []any{
		&r.id, &r.projectID, &r.name, &r.description, &r.method, &r.url, &r.headers, &r.body, &r.expectedStatus,
		&r.extract, &r.createdAt, &r.updatedAt,
	}
}

func (r actionRow) action() action.Action {
	headers := make([]action.Header, len(r.headers))
	for i, h := range r.headers {
		headers[i] = action.Header{Name: h.Name, Value: h.Value}
	}
	extract := make([]action.Extraction, len(r.extract))
	for i, e := range r.extract {
		extract[i] = action.Extraction{Variable: e.Variable, Pattern: e.Pattern}
	}
	return action.Action{
		ID: r.id, ProjectID: r.projectID, Name: r.name, Description: r.description,
		Request: action.Request{
			Method: action.Method(r.method), URL: r.url, Headers: headers, Body: r.body,
			ExpectedStatus: r.expectedStatus, Extract: extract,
		},
		CreatedAt: r.createdAt, UpdatedAt: r.updatedAt,
	}
}

type headerDocument struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type extractionDocument struct {
	Variable string `json:"variable"`
	Pattern  string `json:"pattern"`
}

func headerDocuments(headers []action.Header) []headerDocument {
	documents := make([]headerDocument, len(headers))
	for i, h := range headers {
		documents[i] = headerDocument{Name: h.Name, Value: h.Value}
	}
	return documents
}

func extractionDocuments(extract []action.Extraction) []extractionDocument {
	documents := make([]extractionDocument, len(extract))
	for i, e := range extract {
		documents[i] = extractionDocument{Variable: e.Variable, Pattern: e.Pattern}
	}
	return documents
}

type Actions struct {
	db *DB
}

func NewActions(db *DB) *Actions {
	return &Actions{db: db}
}

func (s *Actions) Actions(ctx context.Context, projectID string) ([]action.Action, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	rows, err := s.db.pool.Query(ctx,
		`SELECT `+actionColumns+` FROM actions WHERE project_id = $1 ORDER BY name, id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("query actions: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (action.Action, error) {
		var r actionRow
		err := row.Scan(r.targets()...)
		return r.action(), err
	})
}

func (s *Actions) Action(ctx context.Context, projectID, id string) (action.Action, error) {
	if !validID(id) {
		return action.Action{}, fmt.Errorf("action %s: %w", id, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var r actionRow
	err := s.db.pool.QueryRow(ctx,
		`SELECT `+actionColumns+` FROM actions WHERE project_id = $1 AND id = $2`, projectID, id,
	).Scan(r.targets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return action.Action{}, fmt.Errorf("action %s: %w", id, fault.ErrNotFound)
	}
	if err != nil {
		return action.Action{}, fmt.Errorf("query action: %w", err)
	}
	return r.action(), nil
}

func (s *Actions) CreateAction(ctx context.Context, projectID string, d action.Definition) (action.Action, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return action.Action{}, fmt.Errorf("generate action id: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var r actionRow
	err = s.db.pool.QueryRow(ctx, `
		INSERT INTO actions (id, project_id, name, description, method, url, headers, body, expected_status,
			extract, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11)
		RETURNING `+actionColumns,
		id, projectID, d.Name, d.Description, string(d.Request.Method), d.Request.URL,
		headerDocuments(d.Request.Headers), d.Request.Body, d.Request.ExpectedStatus,
		extractionDocuments(d.Request.Extract), s.db.clock.Now(),
	).Scan(r.targets()...)
	if isUniqueViolation(err) {
		return action.Action{}, fmt.Errorf("action name taken: %w", fault.ErrConflict)
	}
	if err != nil {
		return action.Action{}, fmt.Errorf("insert action: %w", err)
	}
	return r.action(), nil
}

func (s *Actions) UpdateAction(ctx context.Context, projectID, id string, d action.Definition) (action.Action, error) {
	if !validID(id) {
		return action.Action{}, fmt.Errorf("action %s: %w", id, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var r actionRow
	err := s.db.pool.QueryRow(ctx, `
		UPDATE actions SET name = $3, description = $4, method = $5, url = $6, headers = $7, body = $8,
			expected_status = $9, extract = $10, updated_at = $11
		WHERE project_id = $1 AND id = $2
		RETURNING `+actionColumns,
		projectID, id, d.Name, d.Description, string(d.Request.Method), d.Request.URL,
		headerDocuments(d.Request.Headers), d.Request.Body, d.Request.ExpectedStatus,
		extractionDocuments(d.Request.Extract), s.db.clock.Now(),
	).Scan(r.targets()...)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return action.Action{}, fmt.Errorf("action %s: %w", id, fault.ErrNotFound)
	case isUniqueViolation(err):
		return action.Action{}, fmt.Errorf("action name taken: %w", fault.ErrConflict)
	case err != nil:
		return action.Action{}, fmt.Errorf("update action: %w", err)
	}
	return r.action(), nil
}

func (s *Actions) DeleteAction(ctx context.Context, projectID, id string) error {
	if !validID(id) {
		return fmt.Errorf("action %s: %w", id, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tag, err := s.db.pool.Exec(ctx, `DELETE FROM actions WHERE project_id = $1 AND id = $2`, projectID, id)
	if err != nil {
		return fmt.Errorf("delete action: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("action %s: %w", id, fault.ErrNotFound)
	}
	return nil
}
