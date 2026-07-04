// Package store is the persistence layer: a pgx connection pool over
// Postgres/PostGIS. sqlc-generated query code (see internal/store/db) hangs off
// this pool; spatial queries are hand-written SQL.
package store

import (
	"context"
	"fmt"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/michaelpeterswa/washington-fish-api/internal/store/db"
)

// Store owns the pgx pool and the sqlc-generated query set.
type Store struct {
	Pool *pgxpool.Pool
	Q    *db.Queries
}

// New opens a pgx pool against databaseURL and verifies connectivity.
func New(ctx context.Context, databaseURL string) (*Store, error) {
	if databaseURL == "" {
		return nil, fmt.Errorf("store: DATABASE_URL is empty")
	}

	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("store: could not parse database url: %w", err)
	}
	// Trace every query through the otel tracer provider ootel installs.
	cfg.ConnConfig.Tracer = otelpgx.NewTracer()

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: could not create pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: could not ping database: %w", err)
	}

	// Export pgxpool stats (acquire counts, idle/total conns) as otel metrics.
	if err := otelpgx.RecordStats(pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: could not record pool stats: %w", err)
	}

	return &Store{Pool: pool, Q: db.New(pool)}, nil
}

// Ping reports whether the database is reachable — used by the readiness probe.
func (s *Store) Ping(ctx context.Context) error {
	return s.Pool.Ping(ctx)
}

// Close releases the pool.
func (s *Store) Close() {
	s.Pool.Close()
}
