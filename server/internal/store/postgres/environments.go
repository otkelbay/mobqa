package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/secret"
	"github.com/mobqa/backend/internal/usecase/environment"
)

const environmentColumns = `id, project_id, name, is_default, position, variables, created_at, updated_at`

type environmentRow struct {
	id        string
	projectID string
	name      string
	isDefault bool
	position  int
	variables []variableDocument
	createdAt time.Time
	updatedAt time.Time
}

func (r *environmentRow) targets() []any {
	return []any{&r.id, &r.projectID, &r.name, &r.isDefault, &r.position, &r.variables, &r.createdAt, &r.updatedAt}
}

// variableDocument is one stored variable; a secret's value is sealed and bound to its environment and name.
type variableDocument struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Label string `json:"label,omitempty"`
	Rule  string `json:"rule,omitempty"`
	Value string `json:"value"`
}

type Environments struct {
	db  *DB
	box *secret.Box
}

func NewEnvironments(db *DB, box *secret.Box) *Environments {
	return &Environments{db: db, box: box}
}

func secretBinding(environmentID, name string) []byte {
	return []byte(environmentID + "/" + name)
}

func (e *Environments) seal(environmentID string, variables []environment.Variable) []variableDocument {
	documents := make([]variableDocument, len(variables))
	for i, v := range variables {
		value := v.Value
		if v.Type == environment.TypeSecret && value != "" {
			value = e.box.Seal(value, secretBinding(environmentID, v.Name))
		}
		documents[i] = variableDocument{Name: v.Name, Type: string(v.Type), Label: v.Label, Rule: v.Rule, Value: value}
	}
	return documents
}

func (e *Environments) open(row environmentRow) (environment.Environment, error) {
	variables := make([]environment.Variable, len(row.variables))
	for i, doc := range row.variables {
		value := doc.Value
		if doc.Type == string(environment.TypeSecret) && value != "" {
			opened, err := e.box.Open(value, secretBinding(row.id, doc.Name))
			if err != nil {
				return environment.Environment{}, fmt.Errorf("open secret %s of environment %s: %w", doc.Name, row.id, err)
			}
			value = opened
		}
		variables[i] = environment.Variable{
			Name: doc.Name, Type: environment.VariableType(doc.Type), Label: doc.Label, Rule: doc.Rule, Value: value,
		}
	}
	return environment.Environment{
		ID:        row.id,
		ProjectID: row.projectID,
		Name:      row.name,
		IsDefault: row.isDefault,
		Position:  row.position,
		Variables: variables,
		CreatedAt: row.createdAt,
		UpdatedAt: row.updatedAt,
	}, nil
}

func (e *Environments) Environments(ctx context.Context, projectID string) ([]environment.Environment, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	rows, err := e.db.pool.Query(ctx,
		`SELECT `+environmentColumns+` FROM environments WHERE project_id = $1 ORDER BY position, created_at, id`,
		projectID)
	if err != nil {
		return nil, fmt.Errorf("select environments: %w", err)
	}
	defer rows.Close()
	var environments []environment.Environment
	for rows.Next() {
		var row environmentRow
		if err := rows.Scan(row.targets()...); err != nil {
			return nil, fmt.Errorf("scan environment: %w", err)
		}
		opened, err := e.open(row)
		if err != nil {
			return nil, err
		}
		environments = append(environments, opened)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read environments: %w", err)
	}
	return environments, nil
}

func (e *Environments) Environment(ctx context.Context, projectID, id string) (environment.Environment, error) {
	if !validID(id) {
		return environment.Environment{}, fmt.Errorf("environment %s: %w", id, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var row environmentRow
	err := e.db.pool.QueryRow(ctx,
		`SELECT `+environmentColumns+` FROM environments WHERE project_id = $1 AND id = $2`, projectID, id,
	).Scan(row.targets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return environment.Environment{}, fmt.Errorf("environment %s: %w", id, fault.ErrNotFound)
	}
	if err != nil {
		return environment.Environment{}, fmt.Errorf("select environment: %w", err)
	}
	return e.open(row)
}

// CreateEnvironment appends the environment after the project's others.
func (e *Environments) CreateEnvironment(ctx context.Context, in environment.NewEnvironment,
) (environment.Environment, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return environment.Environment{}, fmt.Errorf("generate environment id: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	now := e.db.clock.Now()
	var row environmentRow
	err = pgx.BeginFunc(ctx, e.db.pool, func(tx pgx.Tx) error {
		if in.IsDefault {
			if err := clearDefault(ctx, tx, in.ProjectID); err != nil {
				return err
			}
		}
		return tx.QueryRow(ctx, `
			INSERT INTO environments (id, project_id, name, is_default, position, variables, created_at, updated_at)
			SELECT $1, $2, $3, $4, coalesce(max(position) + 1, 0), $5, $6, $6
			FROM environments WHERE project_id = $2
			RETURNING `+environmentColumns,
			id, in.ProjectID, in.Name, in.IsDefault, e.seal(id.String(), in.Variables), now,
		).Scan(row.targets()...)
	})
	if isUniqueViolation(err) {
		return environment.Environment{}, fmt.Errorf("environment name taken: %w", fault.ErrConflict)
	}
	if err != nil {
		return environment.Environment{}, fmt.Errorf("insert environment: %w", err)
	}
	return e.open(row)
}

func (e *Environments) UpdateEnvironment(ctx context.Context, change environment.Change,
) (environment.Environment, error) {
	if !validID(change.EnvironmentID) {
		return environment.Environment{}, fmt.Errorf("environment %s: %w", change.EnvironmentID, fault.ErrNotFound)
	}
	var variables any
	if change.Variables != nil {
		variables = e.seal(change.EnvironmentID, change.Variables)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var row environmentRow
	err := pgx.BeginFunc(ctx, e.db.pool, func(tx pgx.Tx) error {
		if change.IsDefault != nil && *change.IsDefault {
			if err := clearDefault(ctx, tx, change.ProjectID); err != nil {
				return err
			}
		}
		return tx.QueryRow(ctx, `
			UPDATE environments SET
				name = coalesce($3, name),
				is_default = coalesce($4, is_default),
				position = coalesce($5, position),
				variables = coalesce($6, variables),
				updated_at = $7
			WHERE project_id = $1 AND id = $2
			RETURNING `+environmentColumns,
			change.ProjectID, change.EnvironmentID, change.Name, change.IsDefault, change.Position, variables,
			e.db.clock.Now(),
		).Scan(row.targets()...)
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return environment.Environment{}, fmt.Errorf("environment %s: %w", change.EnvironmentID, fault.ErrNotFound)
	case isUniqueViolation(err):
		return environment.Environment{}, fmt.Errorf("environment name taken: %w", fault.ErrConflict)
	case err != nil:
		return environment.Environment{}, fmt.Errorf("update environment: %w", err)
	}
	return e.open(row)
}

func (e *Environments) DeleteEnvironment(ctx context.Context, projectID, id string) error {
	if !validID(id) {
		return fmt.Errorf("environment %s: %w", id, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tag, err := e.db.pool.Exec(ctx, `DELETE FROM environments WHERE project_id = $1 AND id = $2`, projectID, id)
	if err != nil {
		return fmt.Errorf("delete environment: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("environment %s: %w", id, fault.ErrNotFound)
	}
	return nil
}

func clearDefault(ctx context.Context, tx pgx.Tx, projectID string) error {
	if _, err := tx.Exec(ctx,
		`UPDATE environments SET is_default = false WHERE project_id = $1 AND is_default`, projectID); err != nil {
		return fmt.Errorf("clear default environment: %w", err)
	}
	return nil
}
