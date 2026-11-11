package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/build"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/worker"
)

const buildColumns = `id, project_id, platform, version, build_number, file_name, size, sha256, blob_key,
	uploaded_by, status, analysis, is_current, profile_name, team_id, bundle_id, profile_expires_at, uploaded_at`

type buildRow struct {
	id               string
	projectID        string
	platform         string
	version          string
	buildNumber      string
	fileName         string
	size             int64
	sha256           string
	blobKey          string
	uploadedBy       string
	status           string
	analysis         *analysisDocument
	isCurrent        bool
	profileName      *string
	teamID           *string
	bundleID         *string
	profileExpiresAt *time.Time
	uploadedAt       time.Time
}

func (r *buildRow) targets() []any {
	return []any{
		&r.id, &r.projectID, &r.platform, &r.version, &r.buildNumber, &r.fileName, &r.size, &r.sha256, &r.blobKey,
		&r.uploadedBy, &r.status, &r.analysis, &r.isCurrent, &r.profileName, &r.teamID, &r.bundleID,
		&r.profileExpiresAt, &r.uploadedAt,
	}
}

func (r buildRow) build() build.Build {
	b := build.Build{
		ID:          r.id,
		ProjectID:   r.projectID,
		Platform:    project.Platform(r.platform),
		Version:     r.version,
		BuildNumber: r.buildNumber,
		FileName:    r.fileName,
		Size:        r.size,
		SHA256:      r.sha256,
		BlobKey:     r.blobKey,
		UploadedBy:  r.uploadedBy,
		Status:      build.Status(r.status),
		IsCurrent:   r.isCurrent,
		UploadedAt:  r.uploadedAt,
	}
	if r.analysis != nil {
		b.Analysis = &build.Analysis{PreviousID: r.analysis.PreviousID, Changes: r.analysis.Changes}
	}
	if r.profileExpiresAt != nil {
		b.Provisioning = &build.Provisioning{
			ProfileName: text(r.profileName), TeamID: text(r.teamID), BundleID: text(r.bundleID),
			ExpiresAt: *r.profileExpiresAt,
		}
	}
	return b
}

func text(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

type analysisDocument struct {
	PreviousID string   `json:"previous_id,omitempty"`
	Changes    []string `json:"changes"`
}

type Builds struct {
	db *DB
}

func NewBuilds(db *DB) *Builds {
	return &Builds{db: db}
}

func (s *Builds) CreateBuild(ctx context.Context, b build.Build, analysis worker.EnqueueInput) (build.Build, error) {
	var profileName, teamID, bundleID *string
	var expiresAt *time.Time
	if p := b.Provisioning; p != nil {
		profileName, teamID, bundleID, expiresAt = &p.ProfileName, &p.TeamID, &p.BundleID, &p.ExpiresAt
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var row buildRow
	err := pgx.BeginFunc(ctx, s.db.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO builds (id, project_id, platform, version, build_number, file_name, size, sha256, blob_key,
				uploaded_by, status, is_current, profile_name, team_id, bundle_id, profile_expires_at, uploaded_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, false, $12, $13, $14, $15, $16)
			RETURNING `+buildColumns,
			b.ID, b.ProjectID, string(b.Platform), b.Version, b.BuildNumber, b.FileName, b.Size, b.SHA256, b.BlobKey,
			b.UploadedBy, string(b.Status), profileName, teamID, bundleID, expiresAt, s.db.clock.Now(),
		).Scan(row.targets()...)
		if err != nil {
			return fmt.Errorf("insert build: %w", err)
		}
		_, err = s.db.enqueue(ctx, tx, analysis)
		return err
	})
	if err != nil {
		return build.Build{}, err
	}
	return row.build(), nil
}

func (s *Builds) Builds(ctx context.Context, query build.Query) ([]build.Build, int, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var total int
	err := s.db.pool.QueryRow(ctx,
		`SELECT count(*) FROM builds WHERE project_id = $1 AND ($2 = '' OR platform = $2)`,
		query.ProjectID, string(query.Platform),
	).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("count builds: %w", err)
	}
	rows, err := s.db.pool.Query(ctx, `
		SELECT `+buildColumns+` FROM builds
		WHERE project_id = $1 AND ($2 = '' OR platform = $2)
		ORDER BY uploaded_at DESC, id DESC
		LIMIT $3 OFFSET $4`,
		query.ProjectID, string(query.Platform), query.Limit, query.Offset)
	if err != nil {
		return nil, 0, fmt.Errorf("select builds: %w", err)
	}
	builds, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (build.Build, error) {
		var r buildRow
		err := row.Scan(r.targets()...)
		return r.build(), err
	})
	if err != nil {
		return nil, 0, fmt.Errorf("read builds: %w", err)
	}
	return builds, total, nil
}

func (s *Builds) Build(ctx context.Context, projectID, id string) (build.Build, error) {
	return s.one(ctx, id, `SELECT `+buildColumns+` FROM builds WHERE project_id = $1 AND id = $2`, projectID, id)
}

func (s *Builds) PreviousBuild(ctx context.Context, b build.Build) (build.Build, error) {
	return s.one(ctx, b.ID, `
		SELECT `+buildColumns+` FROM builds
		WHERE project_id = $1 AND platform = $3 AND (uploaded_at, id) < ($4, $2)
		ORDER BY uploaded_at DESC, id DESC
		LIMIT 1`,
		b.ProjectID, b.ID, string(b.Platform), b.UploadedAt)
}

// SetCurrent clears the flag before setting it so the one-current-per-platform index never sees two.
func (s *Builds) SetCurrent(ctx context.Context, projectID, id string) (build.Build, error) {
	if !validID(id) {
		return build.Build{}, fmt.Errorf("build %s: %w", id, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var row buildRow
	err := pgx.BeginFunc(ctx, s.db.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE builds SET is_current = false
			WHERE is_current AND (project_id, platform) = (SELECT project_id, platform FROM builds WHERE id = $2)
				AND project_id = $1`,
			projectID, id)
		if err != nil {
			return fmt.Errorf("clear current build: %w", err)
		}
		return tx.QueryRow(ctx,
			`UPDATE builds SET is_current = true WHERE project_id = $1 AND id = $2 RETURNING `+buildColumns,
			projectID, id,
		).Scan(row.targets()...)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return build.Build{}, fmt.Errorf("build %s: %w", id, fault.ErrNotFound)
	}
	if err != nil {
		return build.Build{}, fmt.Errorf("set current build: %w", err)
	}
	return row.build(), nil
}

func (s *Builds) SetAnalysis(ctx context.Context, buildID string, analysis build.Analysis) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tag, err := s.db.pool.Exec(ctx, `UPDATE builds SET analysis = $2, status = 'ready' WHERE id = $1`,
		buildID, analysisDocument{PreviousID: analysis.PreviousID, Changes: analysis.Changes})
	if err != nil {
		return fmt.Errorf("store build analysis: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("build %s: %w", buildID, fault.ErrNotFound)
	}
	return nil
}

func (s *Builds) DeleteBuild(ctx context.Context, projectID, id string) (build.Build, error) {
	return s.one(ctx, id,
		`DELETE FROM builds WHERE project_id = $1 AND id = $2 RETURNING `+buildColumns, projectID, id)
}

// one runs a statement that returns the build with the given id, if it exists.
func (s *Builds) one(ctx context.Context, id, statement string, args ...any) (build.Build, error) {
	if !validID(id) {
		return build.Build{}, fmt.Errorf("build %s: %w", id, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var row buildRow
	err := s.db.pool.QueryRow(ctx, statement, args...).Scan(row.targets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return build.Build{}, fmt.Errorf("build %s: %w", id, fault.ErrNotFound)
	}
	if err != nil {
		return build.Build{}, fmt.Errorf("query build: %w", err)
	}
	return row.build(), nil
}
