package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestScoreFeedbackRemovalMigration(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	// Cover upgrades from both the original feedback feature and its partially
	// disabled version. The current schema is exercised by the other store tests.
	for _, oldVersion := range []int64{16, 17} {
		t.Run(fmt.Sprintf("from_version_%d", oldVersion), func(t *testing.T) {
			conn, err := db.pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Release()
			schema := fmt.Sprintf("feedback_removal_%d", time.Now().UnixNano())
			quoted := pgx.Identifier{schema}.Sanitize()
			if _, err := conn.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
				t.Fatal(err)
			}
			defer conn.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE")
			if _, err := conn.Exec(ctx, "SET search_path TO "+quoted); err != nil {
				t.Fatal(err)
			}
			defer conn.Exec(ctx, "RESET search_path")
			if _, err := conn.Exec(ctx, `CREATE TABLE schema_migrations (
				version BIGINT PRIMARY KEY, name TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
			)`); err != nil {
				t.Fatal(err)
			}
			for _, m := range migrations {
				if m.version <= oldVersion {
					if err := applyMigration(ctx, conn, m); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := conn.Exec(ctx, `
				INSERT INTO sources (name, type, url) VALUES ('Feed', 'rss', 'https://example.com/feed');
				INSERT INTO articles (source_id, url, title, summary, score, base_score) VALUES
					(1, 'https://example.com/adjusted', 'Adjusted', 'Keep summary', 73, 88),
					(1, 'https://example.com/zero', 'Zero', '', 5, 0),
					(1, 'https://example.com/no-base', 'No base', '', 42, NULL),
					(1, 'https://example.com/unchanged', 'Unchanged', '', 60, 60),
					(1, 'https://example.com/unanalyzed', 'Unanalyzed', '', NULL, NULL);
				INSERT INTO article_score_feedback (article_id, direction) VALUES (1, 'higher'), (2, 'lower');
				INSERT INTO digests (date, threshold) VALUES ('2026-11-15', 60);
				INSERT INTO digest_articles (digest_id, article_id, ordinal) VALUES (1, 1, 1);
			`); err != nil {
				t.Fatalf("seed legacy state: %v", err)
			}
			for _, m := range migrations {
				if m.version > oldVersion {
					if err := applyMigration(ctx, conn, m); err != nil {
						t.Fatal(err)
					}
				}
			}
			var count int
			if err := conn.QueryRow(ctx, `SELECT count(*) FROM articles WHERE
				(id=1 AND score=88 AND title='Adjusted' AND summary='Keep summary') OR
				(id=2 AND score=0) OR (id=3 AND score=42) OR
				(id=4 AND score=60) OR (id=5 AND score IS NULL)`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 5 {
				t.Errorf("preserved articles and scores = %d, want 5", count)
			}
			if err := conn.QueryRow(ctx, `SELECT count(*) FROM digest_articles WHERE article_id=1`).Scan(&count); err != nil || count != 1 {
				t.Errorf("preserved digest membership = %d, err %v", count, err)
			}
			var tableExists, columnExists bool
			if err := conn.QueryRow(ctx, `SELECT
				to_regclass('article_score_feedback') IS NOT NULL,
				EXISTS (SELECT 1 FROM information_schema.columns
					WHERE table_schema=current_schema() AND table_name='articles' AND column_name='base_score')`).Scan(&tableExists, &columnExists); err != nil {
				t.Fatal(err)
			}
			if tableExists || columnExists {
				t.Errorf("obsolete state remains: feedback table=%v, base_score column=%v", tableExists, columnExists)
			}
		})
	}
}
