package postgres

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationLockID serialises concurrent migrators (api and worker starting together).
const migrationLockID = 7_160_501

type migration struct {
	version string
	sql     string
}

// Migrate applies every embedded migration that has not run yet, each in its own transaction.
// Migrations are forward-only.
func (db *DB) Migrate(ctx context.Context) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migrationLockID)
	}()

	if err := ensureMigrationTable(ctx, conn.Conn()); err != nil {
		return err
	}
	for _, m := range migrations {
		if err := db.applyMigration(ctx, conn.Conn(), m); err != nil {
			return err
		}
	}
	return nil
}

func loadMigrations() ([]migration, error) {
	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(names)
	migrations := make([]migration, 0, len(names))
	for _, name := range names {
		body, err := migrationFiles.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", name, err)
		}
		version := strings.TrimSuffix(strings.TrimPrefix(name, "migrations/"), ".sql")
		migrations = append(migrations, migration{version: version, sql: string(body)})
	}
	return migrations, nil
}

func ensureMigrationTable(ctx context.Context, conn *pgx.Conn) error {
	_, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    text PRIMARY KEY,
			applied_at timestamptz NOT NULL
		)`)
	if err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	return nil
}

func (db *DB) applyMigration(ctx context.Context, conn *pgx.Conn, m migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", m.version, err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var applied bool
	err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, m.version).
		Scan(&applied)
	if err != nil {
		return fmt.Errorf("check migration %s: %w", m.version, err)
	}
	if applied {
		return nil
	}
	if _, err := tx.Exec(ctx, m.sql); err != nil {
		return fmt.Errorf("apply migration %s: %w", m.version, err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES ($1, $2)`,
		m.version, db.clock.Now()); err != nil {
		return fmt.Errorf("record migration %s: %w", m.version, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration %s: %w", m.version, err)
	}
	return nil
}
