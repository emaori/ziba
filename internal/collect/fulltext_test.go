package collect

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/emaori/ziba/internal/domain"
)

const samplePage = `<!doctype html>
<html><head><title>Building a CEL engine for .NET</title></head>
<body><article>
  <p>The Common Expression Language is a small, safe expression grammar.</p>
  <p>This post walks through implementing an evaluator for it in C#.</p>
</article></body></html>`

func TestFullTextTitle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, samplePage)
	}))
	defer server.Close()

	pageTitle := "Building a CEL engine for .NET"

	tests := []struct {
		name  string
		title string
		want  string
	}{
		// A real headline from the feed wins: it is chosen by the publisher,
		// while the page title often carries the site name as well.
		{"keeps a real title", "A CEL engine, from scratch", "A CEL engine, from scratch"},
		{"fills in a missing title", "", pageTitle},
		// The case a roundup produces when the bare-address anchor comes first.
		{"replaces a bare address", "https://bsid.io/writing/building-a-cel-engine-for-net", pageTitle},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			article, err := NewFullText(server.Client()).Article(context.Background(),
				domain.RawItem{URL: server.URL, Title: tt.title})
			if err != nil {
				t.Fatalf("Article returned error: %v", err)
			}
			if article.Title != tt.want {
				t.Errorf("Title = %q, want %q", article.Title, tt.want)
			}
			if article.FullText == "" {
				t.Error("FullText is empty, want the extracted body")
			}
		})
	}
}

func TestLooksLikeURL(t *testing.T) {
	urls := []string{"https://example.com/a", "http://example.com"}
	titles := []string{
		"", "A perfectly good headline",
		// A headline may legitimately mention an address; the space is what
		// distinguishes a sentence from a bare link.
		"Why https://example.com went down",
	}

	for _, s := range urls {
		if !looksLikeURL(s) {
			t.Errorf("looksLikeURL(%q) = false, want true", s)
		}
	}
	for _, s := range titles {
		if looksLikeURL(s) {
			t.Errorf("looksLikeURL(%q) = true, want false", s)
		}
	}
}
