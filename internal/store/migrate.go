package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Migrations are embedded in the binary, so deploying is copying one file: no
// separate migration tool and no risk of schema and code drifting apart.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// advisoryLockID is an arbitrary but fixed number identifying the migration
// lock. Any process running migrations takes it first, so two instances
// starting at once cannot apply the same migration twice.
const advisoryLockID int64 = 8748261

// migration is one numbered SQL file.
type migration struct {
	version int64
	name    string
	sql     string
}

// Migrate applies every migration that has not been applied yet, in version
// order, and returns the names of the ones it applied. It is safe to call on
// every start: with nothing to do it is a no-op.
func Migrate(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	migrations, err := loadMigrations()
	if err != nil {
		return nil, err
	}

	// Everything below runs on one connection, because an advisory lock belongs
	// to the session that took it.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockID); err != nil {
		return nil, fmt.Errorf("take migration lock: %w", err)
	}
	defer func() {
		// context.WithoutCancel: the lock must be released even when ctx is
		// already cancelled, or the next run would block on it.
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", advisoryLockID)
	}()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    BIGINT      PRIMARY KEY,
			name       TEXT        NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := appliedVersions(ctx, conn)
	if err != nil {
		return nil, err
	}

	var names []string
	for _, m := range migrations {
		if slices.Contains(applied, m.version) {
			continue
		}
		if err := applyMigration(ctx, conn, m); err != nil {
			return names, err
		}
		names = append(names, m.name)
	}
	return names, nil
}

// applyMigration runs one migration and records it, both inside a single
// transaction: PostgreSQL supports transactional DDL, so a migration that fails
// halfway leaves no partial schema behind.
func applyMigration(ctx context.Context, conn *pgxpool.Conn, m migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction for migration %s: %w", m.name, err)
	}
	defer tx.Rollback(ctx) // no-op once the transaction is committed

	if _, err := tx.Exec(ctx, m.sql); err != nil {
		return fmt.Errorf("apply migration %s: %w", m.name, err)
	}
	if _, err := tx.Exec(ctx,
		"INSERT INTO schema_migrations (version, name) VALUES ($1, $2)",
		m.version, m.name); err != nil {
		return fmt.Errorf("record migration %s: %w", m.name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration %s: %w", m.name, err)
	}
	return nil
}

func appliedVersions(ctx context.Context, conn *pgxpool.Conn) ([]int64, error) {
	rows, err := conn.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("read applied migrations: %w", err)
	}
	versions, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, fmt.Errorf("read applied migrations: %w", err)
	}
	return versions, nil
}

// loadMigrations reads the embedded files, sorted by version. File names look
// like 0001_initial_schema.sql: the number orders them, the rest is a label.
func loadMigrations() ([]migration, error) {
	entries, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}

	migrations := make([]migration, 0, len(entries))
	seen := make(map[int64]string, len(entries))

	for _, entry := range entries {
		name := path.Base(entry)

		prefix, _, found := strings.Cut(name, "_")
		if !found {
			return nil, fmt.Errorf("migration %s: name must be <version>_<label>.sql", name)
		}
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("migration %s: version %q is not a number", name, prefix)
		}
		if other, duplicate := seen[version]; duplicate {
			return nil, fmt.Errorf("migrations %s and %s share version %d", other, name, version)
		}
		seen[version] = name

		content, err := migrationsFS.ReadFile(entry)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", name, err)
		}
		migrations = append(migrations, migration{version: version, name: name, sql: string(content)})
	}

	slices.SortFunc(migrations, func(a, b migration) int {
		return int(a.version - b.version)
	})
	return migrations, nil
}
