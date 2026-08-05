package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/emaori/ziba/internal/domain"
	"github.com/emaori/ziba/internal/store"
)

// fakeStore serves fixed data, so the handlers can be exercised without a
// database. This is what declaring the Store interface in this package buys.
type fakeStore struct {
	digest   domain.Digest
	article  domain.Article
	articles []domain.Article
	missing  bool
}

func (f *fakeStore) LatestDigest(context.Context) (domain.Digest, error) {
	return f.digest, nil
}

func (f *fakeStore) Article(_ context.Context, id int64) (domain.Article, error) {
	if f.missing {
		return domain.Article{}, pgx.ErrNoRows
	}
	return f.article, nil
}

func (f *fakeStore) Categories(context.Context) ([]store.Category, error) {
	return []store.Category{{Name: "Go programming", Count: 7}}, nil
}

func (f *fakeStore) ArticlesByCategory(context.Context, string, int) ([]domain.Article, error) {
	return f.articles, nil
}

func (f *fakeStore) Archive(context.Context, int, int) ([]domain.Article, error) {
	return f.articles, nil
}

func newTestServer(t *testing.T, s Store) http.Handler {
	t.Helper()
	server, err := New(s)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	return server.Handler()
}

func get(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Code, rec.Body.String()
}

func sampleArticle() domain.Article {
	return domain.Article{
		ID:          42,
		SourceName:  "IEEE Spectrum",
		Title:       "A robot walks into a bar",
		URL:         "https://spectrum.ieee.org/robot",
		Author:      "Jane Writer",
		PublishedAt: time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC),
		Categories:  []string{"Robotics"},
		Summary:     "A summary of the piece.",
		Score:       82,
		ScoreReason: "matches robotics",
		FullText:    "First paragraph.\nSecond paragraph.\nThird paragraph.",
	}
}

// Each page must render its own body. Every page template defines a template
// called "content", so parsing them into one shared set would make the last one
// win and serve the same body everywhere — which is exactly what happened
// before the template sets were separated.
func TestEachPageRendersItsOwnContent(t *testing.T) {
	article := sampleArticle()
	handler := newTestServer(t, &fakeStore{
		digest:   domain.Digest{Date: time.Now(), Articles: []domain.Article{article}},
		article:  article,
		articles: []domain.Article{article},
	})

	tests := []struct {
		name   string
		path   string
		expect string // markup unique to that page
	}{
		{"digest", "/", `class="card"`},
		{"article reader", "/article/42", `class="reader"`},
		{"category", "/category/Robotics", `class="card"`},
		{"archive", "/archive", `class="pager"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, body := get(t, handler, tt.path)
			if code != http.StatusOK {
				t.Fatalf("status = %d, want 200", code)
			}
			if !strings.Contains(body, tt.expect) {
				t.Errorf("page does not contain %q — wrong template rendered?", tt.expect)
			}
			// The frame is shared, so every page must carry it.
			if !strings.Contains(body, `class="masthead"`) {
				t.Error("page is missing the layout")
			}
		})
	}
}

// The reader turns stored text into paragraphs rather than one wall of text.
func TestReaderRendersParagraphs(t *testing.T) {
	article := sampleArticle()
	handler := newTestServer(t, &fakeStore{article: article})

	code, body := get(t, handler, "/article/42")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if got := strings.Count(body, "<p>"); got < 3 {
		t.Errorf("got %d body paragraphs, want at least 3", got)
	}
	if !strings.Contains(body, "Second paragraph.") {
		t.Error("article text is missing")
	}
}

// Stored text is plain, and the reader must escape it. A page that could inject
// markup into the reader would be a hole straight through to every reader.
func TestReaderEscapesArticleText(t *testing.T) {
	article := sampleArticle()
	article.FullText = `<script>alert("xss")</script>`
	article.Title = `<img src=x onerror=alert(1)>`

	handler := newTestServer(t, &fakeStore{article: article})

	_, body := get(t, handler, "/article/42")

	// The test is whether a tag survives, not whether the words do: once the
	// angle brackets are escaped, "onerror=" is inert text on the page.
	for _, tag := range []string{"<script", "<img"} {
		if strings.Contains(body, tag) {
			t.Errorf("%q was rendered as markup, not escaped", tag)
		}
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("expected the article text to appear escaped")
	}
}

func TestMissingArticleIs404(t *testing.T) {
	handler := newTestServer(t, &fakeStore{missing: true})

	if code, _ := get(t, handler, "/article/42"); code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", code)
	}
	if code, _ := get(t, handler, "/article/not-a-number"); code != http.StatusNotFound {
		t.Errorf("status = %d for a non-numeric id, want 404", code)
	}
}

// An empty digest is a normal state — the day's collection may not have cleared
// the threshold — so it must render, not fail.
func TestEmptyDigestRenders(t *testing.T) {
	handler := newTestServer(t, &fakeStore{})

	code, body := get(t, handler, "/")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if !strings.Contains(body, "ziba digest") {
		t.Error("empty digest page does not explain how to generate one")
	}
}

func TestParagraphs(t *testing.T) {
	got := paragraphs("First.\n\n  Second.  \n\nThird.\n")
	want := []string{"First.", "Second.", "Third."}

	if len(got) != len(want) {
		t.Fatalf("got %d paragraphs %q, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("paragraph %d = %q, want %q", i, got[i], want[i])
		}
	}
}
