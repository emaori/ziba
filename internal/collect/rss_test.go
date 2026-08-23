package collect

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/emaori/ziba/internal/domain"
)

type fakeFeedBrowser struct {
	body  []byte
	err   error
	calls []string
}

func (f *fakeFeedBrowser) Fetch(_ context.Context, address string) ([]byte, error) {
	f.calls = append(f.calls, address)
	return f.body, f.err
}

const sampleFeed = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Example</title>
    <item>
      <title>First story</title>
      <link>https://www.example.com/first/?utm_source=feed</link>
      <description>An excerpt of the first story.</description>
      <author>writer@example.com (Jane Writer)</author>
      <pubDate>Mon, 04 Aug 2026 09:30:00 +0000</pubDate>
    </item>
    <item>
      <title>Second story</title>
      <link>https://example.com/second</link>
      <description>An excerpt of the second story.</description>
    </item>
    <item>
      <title>Broken entry with no link</title>
      <description>Should be skipped.</description>
    </item>
  </channel>
</rss>`

// testLogger discards output: the tests assert on values, not on logs.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRSSCollect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != userAgent {
			t.Errorf("User-Agent = %q, want %q", got, userAgent)
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		io.WriteString(w, sampleFeed)
	}))
	defer server.Close()

	collector := NewRSS(server.Client(), nil, testLogger())
	src := domain.Source{ID: 7, Name: "Example", Type: domain.SourceTypeRSS, URL: server.URL}

	items, err := collector.Collect(context.Background(), src)
	if err != nil {
		t.Fatalf("Collect returned error: %v", err)
	}

	// The entry without a link is dropped, not fatal.
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}

	first := items[0]
	if first.SourceID != 7 {
		t.Errorf("SourceID = %d, want 7", first.SourceID)
	}
	if first.Title != "First story" {
		t.Errorf("Title = %q, want %q", first.Title, "First story")
	}
	// Normalized on the way in: www and tracking parameters gone.
	if want := "https://example.com/first"; first.URL != want {
		t.Errorf("URL = %q, want %q", first.URL, want)
	}
	if first.Text == "" {
		t.Error("Text is empty, want the feed excerpt")
	}
	if want := time.Date(2026, 8, 4, 9, 30, 0, 0, time.UTC); !first.PublishedAt.Equal(want) {
		t.Errorf("PublishedAt = %v, want %v", first.PublishedAt, want)
	}

	// An entry without a date falls back to collection time rather than the
	// zero time, which would sort it to the beginning of the archive.
	if items[1].PublishedAt.IsZero() {
		t.Error("PublishedAt is zero for an entry without a date")
	}
}

func TestRSSCollectUsesBrowserOnlyWhenRequested(t *testing.T) {
	directCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		directCalls++
		io.WriteString(w, sampleFeed)
	}))
	defer server.Close()

	browser := &fakeFeedBrowser{body: []byte(sampleFeed)}
	collector := NewRSS(server.Client(), browser, testLogger())
	src := domain.Source{ID: 7, Name: "Example", Type: domain.SourceTypeRSS, URL: server.URL, BrowserFetch: true}

	items, err := collector.Collect(t.Context(), src)
	if err != nil {
		t.Fatalf("Collect returned error: %v", err)
	}
	if len(items) != 2 || len(browser.calls) != 1 || directCalls != 0 {
		t.Fatalf("items=%d browser calls=%d direct calls=%d, want 2, 1, 0", len(items), len(browser.calls), directCalls)
	}

	src.BrowserFetch = false
	if _, err := collector.Collect(t.Context(), src); err != nil {
		t.Fatalf("direct Collect returned error: %v", err)
	}
	if len(browser.calls) != 1 || directCalls != 1 {
		t.Fatalf("browser calls=%d direct calls=%d after direct fetch, want 1, 1", len(browser.calls), directCalls)
	}
}

func TestRSSCollectFailures(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"server error", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}},
		{"not found", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}},
		{"not a feed", func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "<html><body>this is a web page</body></html>")
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(tt.handler)
			defer server.Close()

			collector := NewRSS(server.Client(), nil, testLogger())
			src := domain.Source{Name: "Example", Type: domain.SourceTypeRSS, URL: server.URL}

			if _, err := collector.Collect(context.Background(), src); err == nil {
				t.Error("Collect returned no error, want one")
			}
		})
	}
}

// A source that fails must not prevent the others from being collected.
func TestRegistryRunIsolatesFailures(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, sampleFeed)
	}))
	defer good.Close()

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()

	registry := NewRegistry(NewRSS(good.Client(), nil, testLogger()))
	sources := []domain.Source{
		{Name: "good", Type: domain.SourceTypeRSS, URL: good.URL},
		{Name: "bad", Type: domain.SourceTypeRSS, URL: bad.URL},
		{Name: "unsupported", Type: domain.SourceTypePDF, URL: "https://example.com/magazine.pdf"},
	}

	results := registry.Run(context.Background(), testLogger(), sources)
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}

	byName := make(map[string]Result, len(results))
	for _, r := range results {
		byName[r.Source.Name] = r
	}

	if r := byName["good"]; r.Err != nil || len(r.Items) != 2 {
		t.Errorf("good source: err=%v items=%d, want nil and 2", r.Err, len(r.Items))
	}
	if r := byName["bad"]; r.Err == nil {
		t.Error("bad source: got no error, want one")
	}
	if r := byName["unsupported"]; r.Err == nil {
		t.Error("unsupported source type: got no error, want one")
	}
}
