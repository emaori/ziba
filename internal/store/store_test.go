package store

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/emaori/ziba/internal/domain"
)

// These tests run against a real PostgreSQL, because what they are testing is
// SQL. A fake would only prove that a fake behaves as written; the threshold
// bypass, the outcome counters and the day navigation are all expressed as
// queries, and a query is either right against a database or not tested.
//
// They use their own database, created on demand, and skip when PostgreSQL is
// not reachable — so `go test ./...` stays usable without infrastructure while
// still exercising the SQL whenever there is somewhere to run it.
const testDatabase = "ziba_store_test"

// testStore returns a store on an empty schema. Each test gets a clean slate:
// these are counting tests, and a leftover row is a wrong answer.
func testStore(t *testing.T) *Store {
	t.Helper()

	adminURL := os.Getenv("ZIBA_DATABASE_URL")
	if adminURL == "" {
		t.Skip("ZIBA_DATABASE_URL is not set; skipping the tests that need a database")
	}
	ctx := context.Background()

	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Skipf("cannot reach PostgreSQL: %v", err)
	}
	var exists bool
	err = conn.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, testDatabase).Scan(&exists)
	if err != nil {
		t.Fatalf("look for the test database: %v", err)
	}
	if !exists {
		// An identifier cannot be a parameter; the name is the constant above.
		if _, err := conn.Exec(ctx, `CREATE DATABASE `+testDatabase); err != nil {
			t.Fatalf("create the test database: %v", err)
		}
	}
	conn.Close(ctx)

	db, err := Open(ctx, swapDatabase(adminURL, testDatabase))
	if err != nil {
		t.Fatalf("open the test database: %v", err)
	}
	t.Cleanup(db.Close)

	if _, err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.pool.Exec(ctx,
		`TRUNCATE digest_articles, digests, articles, raw_items, sources, interests RESTART IDENTITY CASCADE;
		 UPDATE app_settings SET configured=FALSE, threshold=60,
		 linkwarden_enabled=FALSE, linkwarden_url='', linkwarden_auth='credentials',
		 linkwarden_username='', linkwarden_password='', linkwarden_token='',
		 updated_at=now() WHERE singleton`); err != nil {
		t.Fatalf("empty the test database: %v", err)
	}
	return db
}

// swapDatabase rewrites the database name in a connection string.
func swapDatabase(url, name string) string {
	base, query, hasQuery := strings.Cut(url, "?")
	if slash := strings.LastIndex(base, "/"); slash >= 0 {
		base = base[:slash+1] + name
	}
	if hasQuery {
		return base + "?" + query
	}
	return base
}

// seedSource adds one source and returns its id.
func seedSource(t *testing.T, db *Store, name string, categories []string) int64 {
	t.Helper()
	synced, err := db.SyncSources(context.Background(), []domain.Source{{
		Name: name, Type: domain.SourceTypeRSS,
		URL:        "https://example.com/" + name,
		Enabled:    true,
		Categories: categories,
	}})
	if err != nil {
		t.Fatalf("sync sources: %v", err)
	}
	return synced[0].ID
}

// seedArticle stores one analyzed article and returns its id.
func seedArticle(t *testing.T, db *Store, sourceID int64, url string,
	score int, categories []string, when time.Time) int64 {

	t.Helper()
	ctx := context.Background()

	id, _, err := db.SaveArticle(ctx, domain.Article{
		SourceID: sourceID, URL: url, Title: "Title of " + url,
		PublishedAt: when, CollectedAt: when, FullText: "Some words.",
	})
	if err != nil {
		t.Fatalf("save article: %v", err)
	}
	if _, err := db.pool.Exec(ctx, `
		UPDATE articles
		SET score = $2, categories = $3, processed_at = now(),
		    collected_at = $4, published_at = $4
		WHERE id = $1`, id, int16(score), categories, when); err != nil {
		t.Fatalf("analyze article: %v", err)
	}
	return id
}

// A source that declares its categories is shown whatever it scored: the reader
// subscribed to it on purpose, so the score orders rather than gates.
func TestDeclaredSourcesIgnoreTheThreshold(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	today := time.Now()

	inferred := seedSource(t, db, "inferred", nil)
	declared := seedSource(t, db, "declared", []string{".NET"})

	seedArticle(t, db, inferred, "https://example.com/weak-inferred", 40, []string{".NET"}, today)
	seedArticle(t, db, inferred, "https://example.com/strong-inferred", 80, []string{".NET"}, today)
	seedArticle(t, db, declared, "https://example.com/weak-declared", 40, []string{".NET"}, today)

	interests := []string{".NET"}

	got, err := db.ArticlesByInterest(ctx, ".NET", 60, 50, 0)
	if err != nil {
		t.Fatalf("ArticlesByInterest: %v", err)
	}
	if !containsURL(got, "https://example.com/weak-declared") {
		t.Error("a declared source scoring 40 is missing from its interest tab")
	}
	if containsURL(got, "https://example.com/weak-inferred") {
		t.Error("an inferred article scoring 40 reached the tab; the threshold is not applied")
	}
	if !containsURL(got, "https://example.com/strong-inferred") {
		t.Error("an inferred article scoring 80 is missing from the tab")
	}

	// And the same rule in the latest selection.
	selected, err := db.GenerateDigest(ctx, today, 60, interests)
	if err != nil {
		t.Fatalf("GenerateDigest: %v", err)
	}
	if selected != 2 {
		t.Errorf("digest selected %d articles, want 2 (the strong one and the declared one)", selected)
	}

	digest, err := db.LatestDigest(ctx)
	if err != nil {
		t.Fatalf("LatestDigest: %v", err)
	}
	if !containsURL(digest.Articles, "https://example.com/weak-declared") {
		t.Error("a declared source scoring 40 is missing from the latest selection")
	}
}

func TestDigestUsesTheLastTwentyFourHours(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	end := time.Date(2026, 8, 16, 5, 0, 0, 0, time.UTC)
	source := seedSource(t, db, "feed", nil)

	inside := seedArticle(t, db, source, "https://example.com/inside", 80,
		[]string{".NET"}, end.Add(-23*time.Hour))
	seedArticle(t, db, source, "https://example.com/outside", 90,
		[]string{".NET"}, end.Add(-25*time.Hour))
	seedArticle(t, db, source, "https://example.com/future", 95,
		[]string{".NET"}, end.Add(time.Minute))

	selected, err := db.GenerateDigest(ctx, end, 60, []string{".NET"})
	if err != nil {
		t.Fatalf("GenerateDigest: %v", err)
	}
	if selected != 1 {
		t.Fatalf("selected %d articles, want 1 from the last 24 hours", selected)
	}
	digest, err := db.LatestDigest(ctx)
	if err != nil {
		t.Fatalf("LatestDigest: %v", err)
	}
	if len(digest.Articles) != 1 || digest.Articles[0].ID != inside {
		t.Errorf("digest articles = %+v, want only article %d", digest.Articles, inside)
	}
}

// An article matching none of the reader's interests is never shown, whatever
// it scored and whoever published it.
func TestArticlesOutsideEveryInterestAreNeverShown(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	today := time.Now()

	declared := seedSource(t, db, "declared", []string{".NET"})
	seedArticle(t, db, declared, "https://example.com/off-topic", 95, []string{"Gardening"}, today)

	selected, err := db.GenerateDigest(ctx, today, 60, []string{".NET"})
	if err != nil {
		t.Fatalf("GenerateDigest: %v", err)
	}
	if selected != 0 {
		t.Errorf("digest selected %d, want 0: the article matches no interest", selected)
	}

	archive, err := db.Archive(ctx, 50, 0, []string{".NET"})
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if len(archive) != 0 {
		t.Errorf("archive returned %d articles, want 0", len(archive))
	}
}

// Declared categories reach the database from the configuration, and are
// overwritten from it on every sync rather than accumulating.
func TestDeclaredCategoriesFollowTheConfiguration(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()

	id := seedSource(t, db, "feed", []string{".NET"})

	declared, err := db.DeclaredCategories(ctx)
	if err != nil {
		t.Fatalf("DeclaredCategories: %v", err)
	}
	if got := declared[id]; len(got) != 1 || got[0] != ".NET" {
		t.Errorf("declared = %v, want [.NET]", got)
	}

	// Remove it from the configuration: the database must follow.
	seedSource(t, db, "feed", nil)
	declared, err = db.DeclaredCategories(ctx)
	if err != nil {
		t.Fatalf("DeclaredCategories: %v", err)
	}
	if _, still := declared[id]; still {
		t.Error("the source still declares categories after they were removed from the configuration")
	}
}

func containsURL(articles []domain.Article, url string) bool {
	for _, a := range articles {
		if a.URL == url {
			return true
		}
	}
	return false
}
