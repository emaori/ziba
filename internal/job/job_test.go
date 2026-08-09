package job

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/emaori/ziba/internal/config"
	"github.com/emaori/ziba/internal/domain"
	"github.com/emaori/ziba/internal/pipeline"
	"github.com/emaori/ziba/internal/store"
)

// The runner is the part that joins collection, expansion, retrieval and
// analysis, and each stage's contract with the next is a row in the database.
// Testing it against a real one is the only way to see that the stages agree:
// the interesting failures here are "the item was marked done but nothing was
// stored" and "the expansion produced work the next stage never saw".
const testDatabase = "ziba_job_test"

func testStore(t *testing.T) *store.Store {
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
	if err := conn.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`,
		testDatabase).Scan(&exists); err != nil {
		t.Fatalf("look for the test database: %v", err)
	}
	if !exists {
		if _, err := conn.Exec(ctx, `CREATE DATABASE `+testDatabase); err != nil {
			t.Fatalf("create the test database: %v", err)
		}
	}
	conn.Close(ctx)

	db, err := store.Open(ctx, swapDatabase(adminURL, testDatabase))
	if err != nil {
		t.Fatalf("open the test database: %v", err)
	}
	t.Cleanup(db.Close)

	if _, err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Emptied over a plain connection rather than through the store: wiping a
	// database is a test's business, not something production code should offer.
	empty, err := pgx.Connect(ctx, swapDatabase(adminURL, testDatabase))
	if err != nil {
		t.Fatalf("connect to empty the test database: %v", err)
	}
	defer empty.Close(ctx)
	if _, err := empty.Exec(ctx,
		`TRUNCATE digest_articles, digests, articles, raw_items, sources RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("empty the test database: %v", err)
	}
	return db
}

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

func testRunner(t *testing.T, db *store.Store) *Runner {
	t.Helper()
	interests := config.Interests{
		Threshold: 60,
		Topics:    []config.Interest{{Topic: "AI", Priority: 1}},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(config.Config{}, nil, interests, db, log, Options{})
}

// seedSource registers one source and returns its id.
func seedSource(t *testing.T, db *store.Store, url string) int64 {
	t.Helper()
	synced, err := db.SyncSources(context.Background(), []domain.Source{{
		Name: "Test feed", Type: domain.SourceTypeRSS, URL: url, Enabled: true,
	}})
	if err != nil {
		t.Fatalf("sync sources: %v", err)
	}
	return synced[0].ID
}

const issuePage = `<!doctype html><html><body>
  <a href="%s/first">The first article, with a headline long enough</a>
  <a href="%s/second">The second article, also with a real headline</a>
  <a href="https://example.com/unsubscribe">Unsubscribe from this newsletter</a>
</body></html>`

// Expanding an issue must queue its links for the next stage and mark the issue
// done, so that a second run does not open it again.
func TestExpandQueuesLinksAndFinishesTheIssue(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, strings.ReplaceAll(issuePage, "%s", server.URL))
	}))
	defer server.Close()

	source := seedSource(t, db, server.URL)
	if _, err := db.SaveRawItems(ctx, []domain.RawItem{{
		SourceID: source, Kind: domain.ItemKindRoundup,
		Title: "Week 32", URL: server.URL + "/week-32", CollectedAt: time.Now(),
	}}); err != nil {
		t.Fatalf("save the issue: %v", err)
	}

	runner := testRunner(t, db)
	opened, queued, err := runner.Expand(ctx, 10)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if opened != 1 {
		t.Errorf("opened %d issues, want 1", opened)
	}
	if queued != 2 {
		t.Errorf("queued %d links, want 2 (the unsubscribe link is not one)", queued)
	}

	// The issue is finished and recorded as expanded, so the next run skips it.
	again, _, err := runner.Expand(ctx, 10)
	if err != nil {
		t.Fatalf("second Expand: %v", err)
	}
	if again != 0 {
		t.Errorf("the second run opened %d issues, want 0", again)
	}

	tallies, err := db.TalliesBySource(ctx)
	if err != nil {
		t.Fatalf("TalliesBySource: %v", err)
	}
	if got := tallies[0].Roundups; got != 1 {
		t.Errorf("roundups counted = %d, want 1", got)
	}
	if got := tallies[0].Links; got != 2 {
		t.Errorf("links counted = %d, want 2", got)
	}
}

// Hydrate has three endings and the statistics depend on telling them apart.
func TestHydrateRecordsWhatBecameOfEachItem(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()

	article := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<html><head><title>A piece</title></head>
			<body><article><p>Some words, enough of them to be a paragraph.</p></article></body></html>`)
	}))
	defer article.Close()

	// A tracker that lands on a video: collected in good faith, not stored.
	video := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://www.youtube.com/watch?v=abcdefghijk", http.StatusFound)
	}))
	defer video.Close()

	source := seedSource(t, db, article.URL)
	if _, err := db.SaveRawItems(ctx, []domain.RawItem{
		{SourceID: source, Kind: domain.ItemKindArticle, Title: "One", URL: article.URL + "/one", CollectedAt: time.Now()},
		{SourceID: source, Kind: domain.ItemKindArticle, Title: "Video", URL: video.URL + "/c?cid=1", CollectedAt: time.Now()},
	}); err != nil {
		t.Fatalf("save raw items: %v", err)
	}

	runner := testRunner(t, db)
	processed, created, err := runner.Hydrate(ctx, 10)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if processed != 2 {
		t.Errorf("processed %d, want 2", processed)
	}
	if created != 1 {
		t.Errorf("created %d articles, want 1", created)
	}

	// The same link arriving again is a duplicate, not a second article.
	if _, err := db.SaveRawItems(ctx, []domain.RawItem{{
		SourceID: source, Kind: domain.ItemKindArticle,
		Title: "One again", URL: article.URL + "/one?utm_source=elsewhere", CollectedAt: time.Now(),
	}}); err != nil {
		t.Fatalf("save the repeat: %v", err)
	}
	if _, created, err = runner.Hydrate(ctx, 10); err != nil {
		t.Fatalf("second Hydrate: %v", err)
	}
	if created != 0 {
		t.Errorf("created %d articles on the repeat, want 0", created)
	}

	tallies, err := db.TalliesBySource(ctx)
	if err != nil {
		t.Fatalf("TalliesBySource: %v", err)
	}
	got := tallies[0]
	if got.Stored != 1 {
		t.Errorf("stored = %d, want 1", got.Stored)
	}
	if got.Skipped != 1 {
		t.Errorf("skipped = %d, want 1 (the video)", got.Skipped)
	}
	if got.Duplicate != 1 {
		t.Errorf("duplicate = %d, want 1", got.Duplicate)
	}
	if got.Pending != 0 {
		t.Errorf("pending = %d, want 0: everything was finished", got.Pending)
	}
}

// A source that declares its subject has it assigned rather than inferred, and
// the assignment has to survive the whole analysis stage.
func TestAnalyzeAssignsDeclaredCategories(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()

	synced, err := db.SyncSources(ctx, []domain.Source{{
		Name: "Declared", Type: domain.SourceTypeRSS, URL: "https://example.com/declared",
		Enabled: true, Categories: []string{"AI"},
	}})
	if err != nil {
		t.Fatalf("sync sources: %v", err)
	}

	// Text that mentions no configured interest at all: inferred, it would be
	// uncategorised — which is the case that motivated declaring.
	if _, _, err := db.SaveArticle(ctx, domain.Article{
		SourceID: synced[0].ID, URL: "https://example.com/piece",
		Title: "Validation in FastEndpoints", CollectedAt: time.Now(),
		FullText: "You inherit Validator and add a couple of RuleFor lines.",
	}); err != nil {
		t.Fatalf("save article: %v", err)
	}

	interests := config.Interests{Threshold: 60, Topics: []config.Interest{{Topic: "AI", Priority: 1}}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner := New(config.Config{}, nil, interests, db, log,
		Options{Analyzer: pipeline.NewDeterministic(interests)})

	analyzed, _, failed, err := runner.Analyze(ctx, 10)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if analyzed != 1 || failed != 0 {
		t.Fatalf("analyzed %d, failed %d; want 1 and 0", analyzed, failed)
	}

	stats, err := db.Articles(ctx, []string{"AI"}, 60)
	if err != nil {
		t.Fatalf("Articles: %v", err)
	}
	if stats.Shown != 1 {
		t.Errorf("shown = %d, want 1: the declared category should have been assigned", stats.Shown)
	}
	if stats.Hidden != 0 {
		t.Errorf("hidden = %d, want 0", stats.Hidden)
	}
}
