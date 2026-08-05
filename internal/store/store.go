// Package store owns PostgreSQL persistence: the connection pool, the schema
// migrations, and the queries over the domain entities.
package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Open connects to PostgreSQL and verifies the connection is usable. The
// returned pool is safe for concurrent use and must be closed by the caller.
func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
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
	return pool, nil
}
