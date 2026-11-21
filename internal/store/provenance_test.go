package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emaori/ziba/internal/domain"
)

func assertCount(t *testing.T, db *Store, query string, want int, args ...any) {
	t.Helper()
	var got int
	if err := db.pool.QueryRow(t.Context(), query, args...).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s: got %d, want %d", query, got, want)
	}
}

func TestNewsletterOriginsSurviveDeduplication(t *testing.T) {
	db := testStore(t)
	ctx := t.Context()
	sources, err := db.SyncSources(ctx, []domain.Source{
		{Name: "Feed", Type: domain.SourceTypeRSS, URL: "https://example.com/feed", Enabled: true},
		{Name: "Mail", Type: domain.SourceTypeNewsletter, URL: "imaps://mail.example", Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	item := domain.RawItem{SourceID: sources[0].ID, URL: "https://example.com/story", CollectedAt: now}
	ids := seedRawItems(t, db, []domain.RawItem{item})
	article := domain.Article{SourceID: item.SourceID, URL: item.URL, Title: "Story", CollectedAt: now, FullText: "Original text"}
	if created, err := db.SaveRetrievedArticle(ctx, ids[0], article); err != nil || !created {
		t.Fatalf("first article: created=%v err=%v", created, err)
	}
	item.SourceID = sources[1].ID
	for _, key := range []string{"id:weekly-42", "id:weekly-43"} {
		message := domain.NewsletterMessage{SourceID: item.SourceID, Key: key, Title: key, CollectedAt: now, Text: "Email body", Items: []domain.RawItem{item}}
		if _, err := db.SaveNewsletterMessage(ctx, message); err != nil {
			t.Fatal(err)
		}
		if n, err := db.SaveNewsletterMessage(ctx, message); err != nil || n != 0 {
			t.Fatalf("repeat: n=%d err=%v", n, err)
		}
	}
	candidates, err := db.UnprocessedRawItems(ctx, 10)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates=%+v err=%v", candidates, err)
	}
	article.SourceID = item.SourceID
	if created, err := db.SaveRetrievedArticle(ctx, candidates[0].ID, article); err != nil || created {
		t.Fatalf("duplicate: created=%v err=%v", created, err)
	}
	// Repeating retrieval must preserve the first outcome and origin.
	if created, err := db.SaveRetrievedArticle(ctx, ids[0], articleWithSource(article, sources[0].ID)); err != nil || created {
		t.Fatalf("repeat retrieval: created=%v err=%v", created, err)
	}
	assertCount(t, db, `SELECT count(*) FROM articles`, 1)
	assertCount(t, db, `SELECT count(*) FROM article_origins`, 2)
	assertCount(t, db, `SELECT count(*) FROM newsletter_messages m
        JOIN newsletter_message_items mi ON mi.message_id=m.id
        JOIN article_origins o ON o.raw_item_id=mi.raw_item_id
        JOIN articles a ON a.id=o.article_id WHERE a.url=$1`, 2, item.URL)
	assertCount(t, db, `SELECT count(*) FROM raw_items WHERE outcome='stored'`, 1)
	assertCount(t, db, `SELECT count(*) FROM raw_items WHERE outcome='duplicate'`, 1)
	assertCount(t, db, `SELECT count(*) FROM articles WHERE processed_at IS NULL AND source_id=$1`, 1, sources[0].ID)
	if _, err := db.pool.Exec(ctx, `DELETE FROM raw_items WHERE id=$1`, candidates[0].ID); err == nil {
		t.Fatal("linked input could be deleted")
	}
}

func articleWithSource(a domain.Article, sourceID int64) domain.Article {
	a.SourceID = sourceID
	return a
}

func TestNewsletterExtractionRollsBackAndRetries(t *testing.T) {
	db := testStore(t)
	source := seedSource(t, db, "mail", nil)
	now := time.Now()
	item := domain.RawItem{SourceID: source, URL: "https://example.com/one", CollectedAt: now}
	// Fail the association after its input was inserted. Nothing,
	// including the extraction completion marker, may escape the transaction.
	if _, err := db.pool.Exec(t.Context(), `CREATE FUNCTION reject_message_item() RETURNS trigger LANGUAGE plpgsql AS $$
        BEGIN RAISE EXCEPTION 'injected association failure'; END $$;
        CREATE TRIGGER reject_message_item BEFORE INSERT ON newsletter_message_items
        FOR EACH ROW EXECUTE FUNCTION reject_message_item()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS reject_message_item ON newsletter_message_items; DROP FUNCTION IF EXISTS reject_message_item()`)
	})
	msg := domain.NewsletterMessage{SourceID: source, Key: "id:retry", CollectedAt: now, Text: "Body", Items: []domain.RawItem{item}}
	if _, err := db.SaveNewsletterMessage(t.Context(), msg); err == nil {
		t.Fatal("expected injected failure")
	}
	assertCount(t, db, `SELECT count(*) FROM newsletter_messages`, 0)
	assertCount(t, db, `SELECT count(*) FROM raw_items`, 0)
	if done, err := db.NewsletterMessageExtracted(t.Context(), source, msg.Key); err != nil || done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	if _, err := db.pool.Exec(t.Context(), `DROP TRIGGER reject_message_item ON newsletter_message_items; DROP FUNCTION reject_message_item()`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveNewsletterMessage(t.Context(), msg); err != nil {
		t.Fatal(err)
	}
	if done, err := db.NewsletterMessageExtracted(t.Context(), source, msg.Key); err != nil || !done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	// A completed extraction is immutable even if a retry supplies more items.
	msg.Items = append(msg.Items, domain.RawItem{SourceID: source, URL: "https://example.com/unexpected", CollectedAt: now})
	if n, err := db.SaveNewsletterMessage(t.Context(), msg); err != nil || n != 0 {
		t.Fatalf("repeat n=%d err=%v", n, err)
	}
	assertCount(t, db, `SELECT count(*) FROM raw_items`, 1)
	// A retained message with no completion marker must still be retryable.
	msg.Key = "id:unfinished"
	if _, err := db.pool.Exec(t.Context(), `INSERT INTO newsletter_messages(source_id,message_key,text) VALUES ($1,$2,$3)`, source, msg.Key, msg.Text); err != nil {
		t.Fatal(err)
	}
	if done, err := db.NewsletterMessageExtracted(t.Context(), source, msg.Key); err != nil || done {
		t.Fatalf("unfinished done=%v err=%v", done, err)
	}
	if _, err := db.SaveNewsletterMessage(t.Context(), msg); err != nil {
		t.Fatal(err)
	}
	assertCount(t, db, `SELECT count(*) FROM newsletter_messages WHERE extracted_at IS NOT NULL`, 2)
	assertCount(t, db, `SELECT count(*) FROM newsletter_message_items`, 3)
}

func TestArticleOriginFailureRollsBackArticleAndCompletion(t *testing.T) {
	db := testStore(t)
	source := seedSource(t, db, "feed", nil)
	now := time.Now()
	ids := seedRawItems(t, db, []domain.RawItem{{SourceID: source, URL: "https://example.com/article", CollectedAt: now}})
	if _, err := db.pool.Exec(t.Context(), `CREATE FUNCTION reject_origin() RETURNS trigger LANGUAGE plpgsql AS $$
        BEGIN RAISE EXCEPTION 'injected origin failure'; END $$;
        CREATE TRIGGER reject_origin BEFORE INSERT ON article_origins FOR EACH ROW EXECUTE FUNCTION reject_origin()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS reject_origin ON article_origins; DROP FUNCTION IF EXISTS reject_origin()`)
	})
	article := domain.Article{SourceID: source, URL: "https://example.com/article", Title: "Article", CollectedAt: now}
	if _, err := db.SaveRetrievedArticle(t.Context(), ids[0], article); err == nil {
		t.Fatal("expected origin failure")
	}
	assertCount(t, db, `SELECT count(*) FROM articles`, 0)
	assertCount(t, db, `SELECT count(*) FROM raw_items WHERE processed_at IS NULL AND outcome IS NULL`, 1)
	if _, err := db.pool.Exec(t.Context(), `DROP TRIGGER reject_origin ON article_origins; DROP FUNCTION reject_origin()`); err != nil {
		t.Fatal(err)
	}
	if created, err := db.SaveRetrievedArticle(t.Context(), ids[0], article); err != nil || !created {
		t.Fatalf("retry created=%v err=%v", created, err)
	}
	assertCount(t, db, `SELECT count(*) FROM article_origins`, 1)
}

func TestNewsletterMessageOnlyStatistics(t *testing.T) {
	db := testStore(t)
	source := seedSource(t, db, "notices", nil)
	if _, err := db.SaveNewsletterMessage(t.Context(), domain.NewsletterMessage{SourceID: source, Key: "id:notice", CollectedAt: time.Now(), Text: "Short notice"}); err != nil {
		t.Fatal(err)
	}
	bySource, err := db.TalliesBySource(t.Context())
	if err != nil || len(bySource) != 1 || bySource[0].Messages != 1 || bySource[0].Pending != 0 {
		t.Fatalf("source tallies=%+v err=%v", bySource, err)
	}
	byDay, err := db.TalliesByDay(t.Context(), 10)
	if err != nil || len(byDay) != 1 || byDay[0].Messages != 1 || byDay[0].Links != 0 {
		t.Fatalf("day tallies=%+v err=%v", byDay, err)
	}
}

func TestNewsletterProvenanceMigrationPreservesContent(t *testing.T) {
	db := testStore(t)
	ctx := t.Context()
	schema := pgx.Identifier{fmt.Sprintf("provenance_upgrade_%d", time.Now().UnixNano())}.Sanitize()
	if _, err := db.pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer db.pool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
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
	if _, err := conn.Exec(ctx, `CREATE TABLE schema_migrations(version BIGINT PRIMARY KEY,name TEXT NOT NULL,applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.version < 20 {
			if err := applyMigration(ctx, conn, m); err != nil {
				t.Fatal(err)
			}
		}
	}
	conn.Release()
	if _, err := pool.Exec(ctx, `INSERT INTO sources(name,type,url) VALUES ('Mail','newsletter','imaps://mail.example');
        INSERT INTO raw_items(source_id,title,url,text,kind) VALUES
        (1,'Weekly Digest 42','imap://Mail/42','Preserved email body','provenance'),
        (1,'Essay','imap://Mail/essay','Preserved essay','article');
        INSERT INTO articles(source_id,url,title,full_text,summary,score) VALUES
        (1,'imap://Mail/essay','Essay','Preserved essay','Summary',80);
        INSERT INTO digests(date,threshold) VALUES ('2026-11-19',60);
        INSERT INTO digest_articles(digest_id,article_id,ordinal) VALUES (1,1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if applied, err := legacy.Migrate(ctx); err != nil || len(applied) != 0 {
		t.Fatalf("repeat applied=%v err=%v", applied, err)
	}
	assertCount(t, legacy, `SELECT count(*) FROM newsletter_messages WHERE title='Weekly Digest 42' AND text='Preserved email body' AND extracted_at IS NULL`, 1)
	assertCount(t, legacy, `SELECT count(*) FROM raw_items WHERE kind='article' AND text='Preserved essay'`, 1)
	assertCount(t, legacy, `SELECT count(*) FROM articles a JOIN digest_articles d ON d.article_id=a.id WHERE a.full_text='Preserved essay' AND a.score=80 AND a.summary='Summary'`, 1)
	assertCount(t, legacy, `SELECT count(*) FROM article_origins`, 0)
	assertCount(t, legacy, `SELECT count(*) FROM newsletter_message_items`, 0)
	if _, err := pool.Exec(ctx, `INSERT INTO raw_items(source_id,url,kind) VALUES (1,'imap://invalid','provenance')`); err == nil {
		t.Fatal("retired provenance kind was accepted")
	}
}

func TestConcurrentNewsletterExtractionIsIdempotent(t *testing.T) {
	db := testStore(t)
	source := seedSource(t, db, "concurrent-mail", nil)
	now := time.Now()
	message := domain.NewsletterMessage{SourceID: source, Key: "id:concurrent", CollectedAt: now, Text: "Retained email", Items: []domain.RawItem{{SourceID: source, URL: "https://example.com/shared", CollectedAt: now}}}
	type result struct {
		inserted int
		err      error
	}
	results := make(chan result, 8)
	for range 8 {
		go func() { n, err := db.SaveNewsletterMessage(t.Context(), message); results <- result{n, err} }()
	}
	total := 0
	for range 8 {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		total += r.inserted
	}
	if total != 2 {
		t.Fatalf("inserted records=%d, want one message and one candidate", total)
	}
	assertCount(t, db, `SELECT count(*) FROM newsletter_message_items`, 1)
	assertCount(t, db, `SELECT count(*) FROM newsletter_messages WHERE extracted_at IS NOT NULL`, 1)
}
