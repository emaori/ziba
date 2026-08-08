// Package store owns PostgreSQL persistence: the connection pool, the schema
// migrations, and the queries over the domain entities.
package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the entry point to the database. It wraps a connection pool, which
// is safe for concurrent use, so a single Store is shared by the whole program.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects to PostgreSQL and verifies the connection is usable. The caller
// must Close the returned Store.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	// pgxpool.New is lazy: it does not open a connection, so a wrong password
	// or a database that is still starting would only surface much later. Ping
	// makes the failure happen here, where the error is understandable.
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases every connection. It is safe to call more than once.
func (s *Store) Close() {
	s.pool.Close()
}

// Pool exposes the connection pool.
//
// This is an escape hatch for integration tests, which need to ask the database
// questions no application query answers — "are there duplicates", "how many
// per source". Application code should use the methods on Store instead; a
// query worth running twice is a query worth naming.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// TruncateAll empties every table except the migration record.
//
// It exists for integration tests, which need a known-empty database. Nothing
// in the application calls it, and nothing should: it is one statement away
// from destroying an archive that took months to accumulate.
func (s *Store) TruncateAll(ctx context.Context) error {
	// One statement, so the foreign keys never see an inconsistent moment.
	_, err := s.pool.Exec(ctx,
		`TRUNCATE digest_articles, digests, articles, raw_items, sources RESTART IDENTITY CASCADE`)
	if err != nil {
		return fmt.Errorf("truncate: %w", err)
	}
	return nil
}
