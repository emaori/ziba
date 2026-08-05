package collect

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emaori/ziba/internal/domain"
)

// listingPage exercises everything the link filter has to get right: real
// articles, navigation, an outbound link, a duplicate, and a fragment.
const listingPage = `<!doctype html>
<html><body>
  <nav>
    <a href="/">Home</a>
    <a href="/sport">Sport</a>
  </nav>
  <main>
    <a href="/2026/08/05/first-real-article-about-something/">A properly long headline about something</a>
    <a href="/2026/08/04/second-real-article/">Another headline that is comfortably long enough</a>
    <a href="https://www.example.com/2026/08/05/first-real-article-about-something/">A properly long headline about something</a>
    <a href="https://elsewhere.example.org/2026/08/05/not-ours/">Someone else's article with a long headline</a>
    <a href="/about-us">Information about our newsroom and staff</a>
    <a href="#top">Back to the top of this very page indeed</a>
  </main>
</body></html>`

func websiteSource(url string, opts *domain.WebsiteOptions) domain.Source {
	return domain.Source{ID: 3, Name: "Example", Type: domain.SourceTypeWebsite, URL: url, Website: opts}
}

func TestWebsiteCollect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, listingPage)
	}))
	defer server.Close()

	collector := NewWebsite(server.Client(), nil, testLogger())
	src := websiteSource(server.URL, &domain.WebsiteOptions{
		LinkPattern: `/[0-9]{4}/[0-9]{2}/[0-9]{2}/`,
	})

	items, err := collector.Collect(context.Background(), src)
	if err != nil {
		t.Fatalf("Collect returned error: %v", err)
	}

	// Two articles: navigation is too short, /about-us fails the pattern, the
	// outbound link is another site's, and the absolute duplicate of the first
	// article collapses onto it.
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2:\n%s", len(items), format(items))
	}
	if items[0].SourceID != 3 {
		t.Errorf("SourceID = %d, want 3", items[0].SourceID)
	}
	if !strings.Contains(items[0].Title, "properly long headline") {
		t.Errorf("Title = %q, want the link text", items[0].Title)
	}
	if items[0].PublishedAt.IsZero() {
		t.Error("PublishedAt is zero — a listing page rarely dates its links, so collection time stands in")
	}
	for _, item := range items {
		if strings.Contains(item.URL, "elsewhere.example.org") {
			t.Errorf("collected an outbound link: %s", item.URL)
		}
	}
}

// Without a pattern everything that reads like a headline is collected, which
// is why the pattern matters on a real site.
func TestWebsiteCollectWithoutPattern(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, listingPage)
	}))
	defer server.Close()

	collector := NewWebsite(server.Client(), nil, testLogger())
	items, err := collector.Collect(context.Background(), websiteSource(server.URL, nil))
	if err != nil {
		t.Fatalf("Collect returned error: %v", err)
	}
	if len(items) != 3 {
		t.Errorf("got %d items, want 3 (the two articles plus the about page)", len(items))
	}
}

func TestWebsiteRespectsMaxLinks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, listingPage)
	}))
	defer server.Close()

	collector := NewWebsite(server.Client(), nil, testLogger())
	items, err := collector.Collect(context.Background(),
		websiteSource(server.URL, &domain.WebsiteOptions{MaxLinks: 1}))
	if err != nil {
		t.Fatalf("Collect returned error: %v", err)
	}
	if len(items) != 1 {
		t.Errorf("got %d items, want 1", len(items))
	}
}

// A source that asks to be rendered without a configured sidecar must say so,
// rather than quietly collecting whatever the unrendered page happened to hold.
func TestWebsiteRenderWithoutSidecar(t *testing.T) {
	collector := NewWebsite(http.DefaultClient, nil, testLogger())
	src := websiteSource("https://example.com/", &domain.WebsiteOptions{Render: true})

	_, err := collector.Collect(context.Background(), src)
	if err == nil {
		t.Fatal("Collect returned no error, want one")
	}
	if !strings.Contains(err.Error(), "rendering sidecar") {
		t.Errorf("error = %q, want it to name the missing sidecar", err)
	}
}

func TestWebsiteRejectsBadPattern(t *testing.T) {
	collector := NewWebsite(http.DefaultClient, nil, testLogger())
	src := websiteSource("https://example.com/", &domain.WebsiteOptions{LinkPattern: "([unclosed"})

	if _, err := collector.Collect(context.Background(), src); err == nil {
		t.Error("Collect returned no error for an invalid pattern, want one")
	}
}

// The renderer is used when asked, and its output is what gets parsed.
func TestWebsiteUsesRenderer(t *testing.T) {
	var rendered bool
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rendered = true
		if r.URL.Path != "/content" {
			t.Errorf("sidecar path = %q, want /content", r.URL.Path)
		}
		io.WriteString(w, `<html><body><a href="https://example.com/2026/08/05/rendered-article/">`+
			`A headline that only exists after rendering</a></body></html>`)
	}))
	defer sidecar.Close()

	collector := NewWebsite(sidecar.Client(), NewRenderer(sidecar.Client(), sidecar.URL), testLogger())
	src := websiteSource("https://example.com/", &domain.WebsiteOptions{Render: true})

	items, err := collector.Collect(context.Background(), src)
	if err != nil {
		t.Fatalf("Collect returned error: %v", err)
	}
	if !rendered {
		t.Error("the sidecar was never called")
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if !strings.Contains(items[0].URL, "rendered-article") {
		t.Errorf("URL = %q, want the rendered link", items[0].URL)
	}
}

func TestNewRendererEmptyEndpoint(t *testing.T) {
	if r := NewRenderer(http.DefaultClient, "   "); r != nil {
		t.Error("NewRenderer returned a renderer for an empty endpoint, want nil")
	}
}

func format(items []domain.RawItem) string {
	var b strings.Builder
	for _, item := range items {
		b.WriteString("  " + item.URL + "  " + item.Title + "\n")
	}
	return b.String()
}
