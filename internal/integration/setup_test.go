//go:build integration

// Package integration holds tests that run against the real thing: a real
// database, real sources over the real network, and — when a key is present —
// the real model.
//
// They are behind a build tag because they are slow, they need infrastructure,
// and they fail for reasons outside this repository: a site goes down, a feed
// is restructured, a network is flaky. That is not flakiness to be engineered
// away, it is the information these tests exist to provide. The ordinary suite
// stays fast and hermetic; this one tells you whether Ziba actually works.
//
//	make test-integration
package integration

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/emaori/ziba/internal/config"
	"github.com/emaori/ziba/internal/domain"
	"github.com/emaori/ziba/internal/job"
	"github.com/emaori/ziba/internal/pipeline"
	"github.com/emaori/ziba/internal/store"
)

// testDatabase is the database these tests use. It is deliberately not the one
// the application uses: an integration run truncates everything, and doing that
// to a real archive would be unforgivable.
const testDatabase = "ziba_integration"

// configDir is where the real, hand-edited configuration lives, relative to
// this package. The tests read the actual files rather than fixtures, because
// "does the configured source list work" is one of the things under test.
const configDir = "../../config"

// shared is the one harness every test uses.
//
// Each test used to build its own and collect afresh. With six tests and four
// sources that meant fetching one publisher's feed and its eighty-odd article
// pages six times in a few minutes — several hundred requests to one site — and
// it got Ziba blocked by that publisher. Collecting once, in TestMain, is the
// fix. The tests are read-mostly and share the result.
var (
	shared     *harness
	sharedOnce sync.Once
)

// TestMain prepares the database and performs the single collection the whole
// suite shares.
func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

// sharedHarness returns the suite's harness, collecting on first use.
func sharedHarness(t *testing.T) *harness {
	t.Helper()

	sharedOnce.Do(func() {
		h := newHarness(t)
		h.collectAll(t)
		shared = h
	})
	if shared == nil {
		t.Skip("shared collection was not available")
	}
	return shared
}

// harness is everything a test needs.
type harness struct {
	store     *store.Store
	runner    *job.Runner
	sources   []domain.Source
	interests config.Interests

	// lastCollect is the result of the suite's single collection.
	lastCollect job.CollectResult

	// Analyzed reports whether a real model was used. Without a key the
	// deterministic analyzer stands in, which exercises the plumbing but says
	// nothing about the quality of the judgement.
	realAnalyzer bool
}

// newHarness prepares an empty test database and wires a runner against the
// real configuration.
func newHarness(t *testing.T) *harness {
	t.Helper()

	ctx := t.Context()

	adminURL := os.Getenv("ZIBA_DATABASE_URL")
	if adminURL == "" {
		t.Skip("ZIBA_DATABASE_URL is not set — run integration tests with `make test-integration`")
	}

	testURL := createTestDatabase(t, ctx, adminURL)

	db, err := store.Open(ctx, testURL)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	// Not closed per test: the harness is shared across the suite.

	if _, err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	truncateAll(t, ctx, db)

	sources, err := config.LoadSources(configDir + "/sources.yaml")
	if err != nil {
		t.Fatalf("load sources: %v", err)
	}
	interests, err := config.LoadInterests(configDir + "/interests.yaml")
	if err != nil {
		t.Fatalf("load interests: %v", err)
	}

	analyzer, real := buildAnalyzer(t, interests)

	cfg := config.Config{}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	return &harness{
		store:        db,
		runner:       job.New(cfg, sources, interests, db, log, job.Options{Analyzer: analyzer}),
		sources:      sources,
		interests:    interests,
		realAnalyzer: real,
	}
}

// buildAnalyzer prefers the real provider and falls back to the deterministic
// one, saying loudly which it chose. A test run that silently used keyword
// matching and reported success would be worse than no test.
func buildAnalyzer(t *testing.T, interests config.Interests) (pipeline.Analyzer, bool) {
	t.Helper()

	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		analyzer, err := pipeline.NewClaude(pipeline.ClaudeOptions{
			APIKey:    key,
			Interests: interests,
		})
		if err != nil {
			t.Fatalf("build model analyzer: %v", err)
		}
		t.Log("analyzer: real provider (this run costs money)")
		return analyzer, true
	}

	t.Log("analyzer: OFFLINE keyword matcher — classification quality is NOT under test")
	return pipeline.NewDeterministic(interests), false
}

// createTestDatabase makes the test database if it is missing, and returns a
// connection string pointing at it.
func createTestDatabase(t *testing.T, ctx context.Context, adminURL string) string {
	t.Helper()

	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Skipf("cannot reach PostgreSQL at the configured address: %v", err)
	}
	defer conn.Close(ctx)

	var exists bool
	if err := conn.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, testDatabase).Scan(&exists); err != nil {
		t.Fatalf("check for test database: %v", err)
	}
	if !exists {
		// Identifiers cannot be parameterized; the name is a constant above, not
		// user input.
		if _, err := conn.Exec(ctx, `CREATE DATABASE `+testDatabase); err != nil {
			t.Fatalf("create test database: %v", err)
		}
	}

	return swapDatabaseName(adminURL, testDatabase)
}

// swapDatabaseName rewrites the path of a connection string.
func swapDatabaseName(url, name string) string {
	base, query, hasQuery := strings.Cut(url, "?")
	slash := strings.LastIndex(base, "/")
	if slash < 0 {
		return url
	}
	swapped := base[:slash+1] + name
	if hasQuery {
		return swapped + "?" + query
	}
	return swapped
}

func truncateAll(t *testing.T, ctx context.Context, db *store.Store) {
	t.Helper()
	if err := db.TruncateAll(ctx); err != nil {
		t.Fatalf("truncate test database: %v", err)
	}
}

// collectAll runs collection and full-text retrieval, and fails the test if
// collection itself could not run.
func (h *harness) collectAll(t *testing.T) job.CollectResult {
	t.Helper()

	ctx := t.Context()
	started := time.Now()

	result, err := h.runner.Collect(ctx)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if _, _, err := h.runner.Hydrate(ctx, 500); err != nil {
		t.Fatalf("retrieve full text: %v", err)
	}

	t.Logf("collected %d new items from %d sources (%d failed, %d too old) in %s",
		result.New, result.Sources, result.Failed, result.TooOld,
		time.Since(started).Round(time.Second))

	h.lastCollect = result
	return result
}

// enabledSources returns the sources the configuration expects to be read.
func (h *harness) enabledSources() []domain.Source {
	var enabled []domain.Source
	for _, s := range h.sources {
		if s.Enabled {
			enabled = append(enabled, s)
		}
	}
	return enabled
}

// countBySource reports how many articles each source produced.
func (h *harness) countBySource(t *testing.T) map[string]int {
	t.Helper()

	rows, err := h.store.Pool().Query(t.Context(), `
		SELECT s.name, count(a.id)
		FROM sources s LEFT JOIN articles a ON a.source_id = s.id
		WHERE s.enabled
		GROUP BY s.name`)
	if err != nil {
		t.Fatalf("count by source: %v", err)
	}
	defer rows.Close()

	counts := map[string]int{}
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			t.Fatalf("scan source count: %v", err)
		}
		counts[name] = n
	}
	return counts
}

// scalar runs a query returning one number — the shape most assertions here
// need.
func (h *harness) scalar(t *testing.T, query string, args ...any) int {
	t.Helper()

	var n int
	if err := h.store.Pool().QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}

func plural(n int, singular string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %ss", n, singular)
}

// timeNow is a seam kept deliberately small: tests build the digest for the
// current day, and having one place that says so makes it obvious where a
// fixed clock would go if one is ever needed.
func timeNow() time.Time { return time.Now() }
