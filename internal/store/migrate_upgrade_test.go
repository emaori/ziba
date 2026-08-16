package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// An upgrade must add retry state without rewriting any existing content.
func TestRetryMigrationPreservesExistingData(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	defer conn.Release()

	schema := fmt.Sprintf("migration_upgrade_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer conn.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
	if _, err := conn.Exec(ctx, "SET search_path TO "+quoted); err != nil {
		t.Fatalf("set search path: %v", err)
	}
	if _, err := conn.Exec(ctx, `CREATE TABLE schema_migrations (
		version BIGINT PRIMARY KEY, name TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		t.Fatalf("create migration table: %v", err)
	}

	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	var retryMigration migration
	for _, migration := range migrations {
		if migration.version == 9 {
			retryMigration = migration
			continue
		}
		if migration.version > 9 {
			continue
		}
		if err := applyMigration(ctx, conn, migration); err != nil {
			t.Fatalf("apply old migration %s: %v", migration.name, err)
		}
	}
	if retryMigration.version == 0 {
		t.Fatal("retry migration 9 is missing")
	}

	var sourceID int64
	if err := conn.QueryRow(ctx, `INSERT INTO sources (name, type, url)
		VALUES ('Existing', 'rss', 'https://example.com/feed') RETURNING id`).Scan(&sourceID); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO raw_items (source_id, title, url, text, kind)
		VALUES ($1, 'Existing raw item', 'https://example.com/raw', 'raw text', 'article');
		INSERT INTO articles (source_id, url, title, full_text, categories, summary, score, processed_at)
		VALUES ($1, 'https://example.com/article', 'Existing article', 'article text',
		        ARRAY['AI'], 'existing summary', 88, now())`, sourceID); err != nil {
		t.Fatalf("seed old data: %v", err)
	}

	if err := applyMigration(ctx, conn, retryMigration); err != nil {
		t.Fatalf("apply retry migration: %v", err)
	}

	var rawTitle, articleTitle, summary string
	var score, failures int
	var failedAt *time.Time
	if err := conn.QueryRow(ctx, `SELECT title, failure_count, failed_at
		FROM raw_items WHERE url = 'https://example.com/raw'`).
		Scan(&rawTitle, &failures, &failedAt); err != nil {
		t.Fatalf("read upgraded raw item: %v", err)
	}
	if rawTitle != "Existing raw item" || failures != 0 || failedAt != nil {
		t.Errorf("upgraded raw item = %q, failures %d, failed %v", rawTitle, failures, failedAt)
	}
	if err := conn.QueryRow(ctx, `SELECT title, summary, score, failure_count
		FROM articles WHERE url = 'https://example.com/article'`).
		Scan(&articleTitle, &summary, &score, &failures); err != nil {
		t.Fatalf("read upgraded article: %v", err)
	}
	if articleTitle != "Existing article" || summary != "existing summary" || score != 88 || failures != 0 {
		t.Errorf("existing article changed: title=%q summary=%q score=%d failures=%d",
			articleTitle, summary, score, failures)
	}
}
