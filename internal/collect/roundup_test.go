package collect

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/emaori/ziba/internal/domain"
)

// sampleIssue is shaped like a real link digest: every article is linked twice,
// once behind its headline and once behind the bare address, mixed in with
// navigation, a video, a sponsor and an unsubscribe footer.
const sampleIssue = `<!doctype html>
<html><body>
  <nav><a href="/?year=2026&week=31">Week 31, 2026</a></nav>

  <a href="https://devblogs.example.com/testing-platform-reporting/">Test reporting in the Testing Platform: from red build to root cause</a>
  <a href="https://devblogs.example.com/testing-platform-reporting/">https://devblogs.example.com/testing-platform-reporting/</a>

  <a href="https://andrewlock.example.net/csrf-fetch-metadata/">Automatic CSRF protection based on Fetch Metadata headers</a>

  <a href="https://www.youtube.com/watch?v=6E7l2CRGtos">Self-Host Your .NET App with Dokploy (full guide)</a>
  <a href="https://youtu.be/Oz0rKKgqv2Q">Community Toolkit Monthly Standup, August 2026</a>

  <a href="https://sponsor.example.com/advertise/here">A message from our sponsor, thank you for reading</a>
  <a href="https://twitter.com/example">Follow us on Twitter for more updates</a>
  <a href="https://example.com/unsubscribe">Unsubscribe from this newsletter</a>
  <a href="https://devblogs.example.com/short">Read more</a>
</body></html>`

func serveIssue(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestRoundupLinks(t *testing.T) {
	server := serveIssue(t, sampleIssue)

	issued := time.Date(2026, 8, 8, 17, 2, 43, 0, time.UTC)
	issue := domain.RawItem{
		SourceID: 4, Kind: domain.ItemKindRoundup,
		Title: ".NET Ketchup - Week 32, 2026", URL: server.URL, PublishedAt: issued,
	}

	items, err := NewRoundup(server.Client()).Links(context.Background(), issue)
	if err != nil {
		t.Fatalf("Links returned error: %v", err)
	}

	// Two real articles. The videos, the sponsor, the social button, the
	// unsubscribe footer and the "Read more" stub are all excluded, and the
	// article linked twice appears once.
	want := []string{
		"https://devblogs.example.com/testing-platform-reporting",
		"https://andrewlock.example.net/csrf-fetch-metadata",
	}
	if len(items) != len(want) {
		for _, item := range items {
			t.Logf("got %s — %q", item.URL, item.Title)
		}
		t.Fatalf("got %d links, want %d", len(items), len(want))
	}

	for i, item := range items {
		if item.URL != want[i] {
			t.Errorf("link %d = %q, want %q", i, item.URL, want[i])
		}
		if item.Kind != domain.ItemKindArticle {
			t.Errorf("link %d kind = %q, want an article", i, item.Kind)
		}
		if item.SourceID != issue.SourceID {
			t.Errorf("link %d SourceID = %d, want %d", i, item.SourceID, issue.SourceID)
		}
		// The issue's date, not the moment of collection: the digest is the only
		// date reliably known for the pieces it lists.
		if !item.PublishedAt.Equal(issued) {
			t.Errorf("link %d PublishedAt = %v, want the issue date %v", i, item.PublishedAt, issued)
		}
	}

	// The headline is what a reader recognises, so the titled anchor must win
	// over the bare-address one that follows it.
	if want := "Test reporting"; len(items[0].Title) < len(want) || items[0].Title[:len(want)] != want {
		t.Errorf("Title = %q, want the headline rather than the address", items[0].Title)
	}
}

func TestRoundupLinksFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer server.Close()

	_, err := NewRoundup(server.Client()).Links(context.Background(),
		domain.RawItem{URL: server.URL, Kind: domain.ItemKindRoundup})
	if err == nil {
		t.Fatal("Links succeeded on a 404, want an error so the issue is retried")
	}
}

// A roundup feed's entries must be marked, or the full-text stage stores each
// issue as though the table of contents were an article.
func TestRSSCollectMarksRoundupEntries(t *testing.T) {
	server := serveIssue(t, sampleFeed)

	for _, roundup := range []bool{false, true} {
		src := domain.Source{ID: 7, Type: domain.SourceTypeRSS, URL: server.URL, Roundup: roundup}

		items, err := NewRSS(server.Client(), testLogger()).Collect(context.Background(), src)
		if err != nil {
			t.Fatalf("roundup=%v: Collect returned error: %v", roundup, err)
		}

		want := domain.ItemKindArticle
		if roundup {
			want = domain.ItemKindRoundup
		}
		for _, item := range items {
			if item.Kind != want {
				t.Errorf("roundup=%v: kind = %q, want %q", roundup, item.Kind, want)
			}
		}
	}
}
