package job

import (
	"context"
	"fmt"
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

func TestDrainAnalyzesMoreThanOneBatch(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	source := seedSource(t, db, "https://example.com/feed")

	for i := range 5 {
		if _, _, err := db.SaveArticle(ctx, domain.Article{
			SourceID:    source,
			URL:         fmt.Sprintf("https://example.com/article-%d", i),
			Title:       "AI article",
			FullText:    "An article about AI agents.",
			CollectedAt: time.Now().Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("save article %d: %v", i, err)
		}
	}

	interests := config.Interests{Threshold: 60, Topics: []config.Interest{{Topic: "AI", Priority: 1, Subtopics: []string{"AI"}}}}
	runner := New(config.Config{}, nil, interests, db,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		Options{Analyzer: pipeline.NewDeterministic(interests)})

	if err := runner.Drain(ctx, 2); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	stats, err := db.Articles(ctx, []string{"AI"}, 60)
	if err != nil {
		t.Fatalf("Articles: %v", err)
	}
	if stats.Analyzed != 5 {
		t.Errorf("analyzed = %d, want all 5 across three chunks", stats.Analyzed)
	}
}

func TestDrainHydratesMoreThanOneBatch(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	source := seedSource(t, db, "https://example.com/feed")
	items := make([]domain.RawItem, 5)
	for i := range items {
		items[i] = domain.RawItem{
			SourceID: source, Kind: domain.ItemKindArticle,
			URL:   fmt.Sprintf("imap://newsletter/message-%d", i),
			Title: "Newsletter essay", Text: "Stored newsletter text",
			CollectedAt: time.Now().Add(time.Duration(i) * time.Second),
		}
	}
	if _, err := db.SaveRawItems(ctx, items); err != nil {
		t.Fatalf("SaveRawItems: %v", err)
	}

	if err := testRunner(t, db).Drain(ctx, 2); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if count, err := db.CountArticles(ctx); err != nil {
		t.Fatalf("CountArticles: %v", err)
	} else if count != 5 {
		t.Errorf("articles = %d, want all 5 across three chunks", count)
	}
}

func TestManualAndScheduledRunsShareTheDrainingWorkflow(t *testing.T) {
	entryPoints := []struct {
		name string
		run  func(*Runner, context.Context, int) error
	}{
		{"manual", func(r *Runner, ctx context.Context, batch int) error { return r.Daily(ctx, batch) }},
		{"scheduled", func(r *Runner, ctx context.Context, batch int) error { return r.ScheduledCollection(ctx, batch) }},
	}

	for _, entry := range entryPoints {
		t.Run(entry.name, func(t *testing.T) {
			db := testStore(t)
			ctx := context.Background()
			feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/rss+xml")
				io.WriteString(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>Empty</title><link>https://example.com</link><description>Empty test feed</description></channel></rss>`)
			}))
			defer feed.Close()

			sourceConfig := domain.Source{Name: "Workflow feed", Type: domain.SourceTypeRSS, URL: feed.URL, Enabled: true}
			synced, err := db.SyncSources(ctx, []domain.Source{sourceConfig})
			if err != nil {
				t.Fatalf("sync source: %v", err)
			}
			for i := range 5 {
				if _, _, err := db.SaveArticle(ctx, domain.Article{
					SourceID: synced[0].ID, URL: fmt.Sprintf("https://example.com/%s-%d", entry.name, i),
					Title: "AI article", FullText: "A practical article about AI agents.",
					CollectedAt: time.Now().Add(time.Duration(i) * time.Millisecond),
				}); err != nil {
					t.Fatalf("save article %d: %v", i, err)
				}
			}

			interests := config.Interests{Threshold: 60, Topics: []config.Interest{{Topic: "AI", Priority: 1, Subtopics: []string{"AI"}}}}
			runner := New(config.Config{}, []domain.Source{sourceConfig}, interests, db,
				slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Analyzer: pipeline.NewDeterministic(interests)})
			if err := entry.run(runner, ctx, 2); err != nil {
				t.Fatalf("run: %v", err)
			}
			stats, err := db.Articles(ctx, []string{"AI"}, 60)
			if err != nil {
				t.Fatalf("article stats: %v", err)
			}
			if stats.Analyzed != 5 {
				t.Fatalf("analyzed = %d, want all 5 across chunks", stats.Analyzed)
			}
		})
	}
}

func TestCollectUsesConfiguredBrowserSidecar(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	called := 0
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		if r.Method != http.MethodPost || r.URL.Path != "/fetch" {
			t.Errorf("sidecar request = %s %s, want POST /fetch", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		io.WriteString(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>Protected</title><link>https://publisher.example</link><description>Test</description><item><title>Article</title><link>https://publisher.example/article</link><description>Text</description></item></channel></rss>`)
	}))
	defer sidecar.Close()

	source := domain.Source{
		Name: "Protected feed", Type: domain.SourceTypeRSS,
		URL: "https://publisher.example/feed", Enabled: true, BrowserFetch: true,
	}
	interests := config.Interests{Threshold: 60, Topics: []config.Interest{{Topic: "Systems", Priority: 1}}}
	runner := New(config.Config{BrowserURL: sidecar.URL}, []domain.Source{source}, interests, db,
		slog.New(slog.NewTextHandler(io.Discard, nil)), Options{})

	result, err := runner.Collect(ctx)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if called != 1 || result.Failed != 0 || result.Found != 1 || result.New != 1 {
		t.Fatalf("sidecar calls=%d result=%+v, want one collected item", called, result)
	}
}
