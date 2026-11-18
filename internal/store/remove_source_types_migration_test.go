package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRetiredSourceTypesMigration(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	schema := pgx.Identifier{fmt.Sprintf("source_removal_%d", time.Now().UnixNano())}.Sanitize()
	if _, err := db.pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer db.pool.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")

	cfg := db.pool.Config()
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	legacy := &Store{pool: pool}
	defer legacy.Close()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `CREATE TABLE schema_migrations (
		version BIGINT PRIMARY KEY, name TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.version < 19 {
			if err := applyMigration(ctx, conn, m); err != nil {
				t.Fatal(err)
			}
		}
	}
	conn.Release()
	if _, err := pool.Exec(ctx, `
		INSERT INTO sources (name, type, url) VALUES
			('Feed', 'rss', 'https://example.com/feed'),
			('Mail', 'newsletter', 'imaps://mail.example'),
			('Magazine', 'pdf', '/magazines'),
			('Site', 'website', 'https://example.com');
		INSERT INTO raw_items (source_id, title, url, text)
			SELECT id, name, url, 'original raw text' FROM sources;
		INSERT INTO articles (source_id, url, title, full_text, summary, score)
			SELECT id, 'https://example.com/article/' || id, name, 'original body', 'original summary', 88 FROM sources;
		INSERT INTO digests (date, threshold) VALUES ('2026-11-18', 60);
		INSERT INTO digest_articles (digest_id, article_id, ordinal)
			SELECT 1, id, id FROM articles;
	`); err != nil {
		t.Fatalf("seed old source types: %v", err)
	}
	if _, err := legacy.Migrate(ctx); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if names, err := legacy.Migrate(ctx); err != nil || len(names) != 0 {
		t.Fatalf("repeat migration: applied %v, err %v", names, err)
	}
	configuration, err := legacy.Configuration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(configuration.Sources) != 2 {
		t.Fatalf("configured sources = %+v, want only feed and mailbox", configuration.Sources)
	}
	for _, src := range configuration.Sources {
		if src.ID > 2 || !src.Enabled {
			t.Errorf("supported source changed: %+v", src)
		}
	}
	if err := legacy.SaveConfiguration(ctx, configuration.Interests, configuration.Sources); err != nil {
		t.Fatalf("save Settings after upgrade: %v", err)
	}
	if _, err := legacy.SyncSources(ctx, configuration.Sources); err != nil {
		t.Fatalf("sync sources after upgrade: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sources WHERE id IN (3,4) AND NOT enabled`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("disabled historical sources = %d, err %v", count, err)
	}
	for id := int64(1); id <= 4; id++ {
		article, err := legacy.Article(ctx, id)
		if err != nil || article.SourceID != id || article.FullText != "original body" || article.Summary != "original summary" || article.Score != 88 {
			t.Fatalf("article %d changed: %+v, err %v", id, article, err)
		}
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM raw_items r
		JOIN articles a ON a.source_id=r.source_id
		JOIN digest_articles d ON d.article_id=a.id
		WHERE r.text='original raw text' AND d.ordinal=a.id`).Scan(&count); err != nil || count != 4 {
		t.Fatalf("preserved raw items and digest membership = %d, err %v", count, err)
	}
	for _, typ := range []string{"pdf", "website", "unsupported"} {
		_, err := pool.Exec(ctx, `INSERT INTO sources (name, type, url) VALUES ('Invalid', $1, 'https://example.com/new')`, typ)
		var constraintErr *pgconn.PgError
		if !errors.As(err, &constraintErr) || constraintErr.Code != "23514" {
			t.Errorf("insert %q: got %v, want check constraint violation", typ, err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE sources SET enabled=TRUE WHERE id=3`); err == nil {
		t.Fatal("historical source could be re-enabled")
	}
	if _, err := pool.Exec(ctx, `UPDATE sources SET type='website' WHERE id=1`); err == nil {
		t.Fatal("supported source could be changed to a retired type")
	}
}
