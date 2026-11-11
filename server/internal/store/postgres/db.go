// Package postgres implements every storage interface the usecases declare. All SQL lives here.
// Store methods return usecase sentinel errors for missing rows and conflicts and wrap every other
// database error, so no driver type leaks upward.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mobqa/backend/internal/clock"
)

// operationTimeout bounds one store call so a stuck database cannot hold a request or a worker.
const operationTimeout = 10 * time.Second

type DB struct {
	pool  *pgxpool.Pool
	clock clock.Clock
}

type Options struct {
	// SearchPath, when set, scopes every connection to these schemas; tests use it for isolation.
	SearchPath string
	Clock      clock.Clock
}

func Open(ctx context.Context, url string, opts Options) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	if opts.SearchPath != "" {
		cfg.ConnConfig.RuntimeParams["search_path"] = opts.SearchPath
	}
	cfg.AfterConnect = scanTimesInUTC
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	db := &DB{pool: pool, clock: opts.Clock}
	if db.clock == nil {
		db.clock = clock.System{}
	}
	if err := db.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return db, nil
}

func (db *DB) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := db.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	return nil
}

func (db *DB) Close() {
	db.pool.Close()
}

// scanTimesInUTC makes every scanned timestamptz UTC, matching the clock, instead of the process zone.
func scanTimesInUTC(_ context.Context, conn *pgx.Conn) error {
	conn.TypeMap().RegisterType(&pgtype.Type{
		Name:  "timestamptz",
		OID:   pgtype.TimestamptzOID,
		Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC},
	})
	return nil
}
