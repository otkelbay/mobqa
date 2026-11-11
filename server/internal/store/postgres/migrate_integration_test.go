//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/mobqa/backend/internal/clock"
	"github.com/mobqa/backend/internal/dbtest"
)

func TestMigrateIsIdempotent(t *testing.T) {
	db := dbtest.New(t, clock.System{})

	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if err := db.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}
