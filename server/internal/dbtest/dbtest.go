// Package dbtest gives each integration test a migrated database in its own schema.
package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/mobqa/backend/internal/clock"
	"github.com/mobqa/backend/internal/store/postgres"
)

const urlVariable = "MOBQA_TEST_DATABASE_URL"

// New skips the test when MOBQA_TEST_DATABASE_URL is unset, so plain `go test` stays hermetic.
func New(t testing.TB, now clock.Clock) *postgres.DB {
	t.Helper()
	address, schema := newSchema(t)
	ctx := context.Background()
	db, err := postgres.Open(ctx, address, postgres.Options{SearchPath: schema + ", public", Clock: now})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(db.Close)
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	return db
}

// URL is the address of an empty schema of its own, for a process that migrates the database itself.
func URL(t testing.TB) string {
	t.Helper()
	address, schema := newSchema(t)
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatalf("parse %s: %v", urlVariable, err)
	}
	query := parsed.Query()
	query.Set("search_path", schema+",public")
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// newSchema creates a schema that is dropped when the test ends and returns it with the database address.
func newSchema(t testing.TB) (string, string) {
	t.Helper()
	address := os.Getenv(urlVariable)
	if address == "" {
		t.Skipf("%s is not set", urlVariable)
	}
	schema := "test_" + randomSuffix(t)
	execAdmin(t, address, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize())
	t.Cleanup(func() {
		execAdmin(t, address, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
	})
	return address, schema
}

func execAdmin(t testing.TB, address, statement string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, address)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, statement); err != nil {
		t.Fatalf("%s: %v", statement, err)
	}
}

func randomSuffix(t testing.TB) string {
	t.Helper()
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("random schema suffix: %v", err)
	}
	return hex.EncodeToString(buf)
}
