package collect

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

// A newsletter link is usually a click tracker that redirects. The article's
// identity must be where it landed, not the tracker: the tracker's address is
// unique per recipient and often per send, so keeping it would give the same
// article a new identity every time it arrived and defeat deduplication.
func TestFullTextFollowsRedirectsForIdentity(t *testing.T) {
	article := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, samplePage)
	}))
	defer article.Close()

	// Stands in for app.alphasignal.ai/c?cid=... and its kind.
	tracker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, article.URL+"/the-real-article", http.StatusFound)
	}))
	defer tracker.Close()

	trackerURL := tracker.URL + "/c?cid=abc123&uid=recipient42"
	got, err := NewFullText(tracker.Client()).Article(context.Background(),
		domain.RawItem{URL: trackerURL, Title: "A CEL engine, from scratch"})
	if err != nil {
		t.Fatalf("Article returned error: %v", err)
	}

	if strings.Contains(got.URL, "cid=abc123") {
		t.Errorf("URL = %q, want the article's own address, not the tracker's", got.URL)
	}
	if want := article.URL + "/the-real-article"; got.URL != want {
		t.Errorf("URL = %q, want %q", got.URL, want)
	}
	if got.FullText == "" {
		t.Error("FullText is empty, want the article behind the redirect")
	}
}

// A link that does not redirect keeps the address it came with.
func TestFullTextKeepsTheAddressWhenThereIsNoRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, samplePage)
	}))
	defer server.Close()

	got, err := NewFullText(server.Client()).Article(context.Background(),
		domain.RawItem{URL: server.URL, Title: "Something"})
	if err != nil {
		t.Fatalf("Article returned error: %v", err)
	}
	if got.URL != server.URL {
		t.Errorf("URL = %q, want it unchanged at %q", got.URL, server.URL)
	}
}

// A tracker that resolves to a page which then refuses the request must still
// leave the article under the real address. Keeping the tracker would be the
// worst of both: no text, and an identity that identifies nothing.
func TestFullTextKeepsTheLandingAddressWhenThePageRefuses(t *testing.T) {
	refuser := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer refuser.Close()

	tracker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, refuser.URL+"/the-article", http.StatusFound)
	}))
	defer tracker.Close()

	got, err := NewFullText(tracker.Client()).Article(context.Background(),
		domain.RawItem{URL: tracker.URL + "/c?cid=abc", Title: "Something worth reading"})
	if err == nil {
		t.Fatal("expected an error for a refused page")
	}

	if want := refuser.URL + "/the-article"; got.URL != want {
		t.Errorf("URL = %q, want the address behind the tracker %q", got.URL, want)
	}
	if got.Title != "Something worth reading" {
		t.Errorf("Title = %q, want the one the newsletter gave", got.Title)
	}
}

// The link filters see the address in the email, which for most newsletters is
// an opaque tracker. A video reached through one would otherwise walk straight
// past the video exclusion, so the destination is judged as well.
func TestFullTextRejectsWhatTheRedirectRevealsAsNotAnArticle(t *testing.T) {
	const video = "https://www.youtube.com/watch?v=DXlmLrkP90E"

	// A stub transport rather than a test server: the destination has to be a
	// real blocked host to exercise the rule, and no test should call it.
	client := &http.Client{Transport: redirectOnceTo(video)}

	_, err := NewFullText(client).Article(context.Background(),
		domain.RawItem{URL: "https://tracker.example/c?cid=abc", Title: "A long enough headline to keep"})

	if !errors.Is(err, ErrNotArticle) {
		t.Errorf("err = %v, want ErrNotArticle for a link landing on a video", err)
	}
}

// An article behind a tracker is still an article.
func TestFullTextAcceptsAnOrdinaryDestination(t *testing.T) {
	client := &http.Client{Transport: redirectOnceTo("https://andrewlock.net/some-post")}

	got, err := NewFullText(client).Article(context.Background(),
		domain.RawItem{URL: "https://tracker.example/c?cid=abc", Title: "A long enough headline to keep"})
	if err != nil {
		t.Fatalf("Article returned error: %v", err)
	}
	if want := "https://andrewlock.net/some-post"; got.URL != want {
		t.Errorf("URL = %q, want %q", got.URL, want)
	}
}

// redirectOnceTo answers anything but the target with a redirect to it, and the
// target with a page.
type redirectOnceTo string

func (target redirectOnceTo) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.String() == string(target) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/html"}},
			Body:       io.NopCloser(strings.NewReader(samplePage)),
			Request:    req,
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusFound,
		Header:     http.Header{"Location": {string(target)}},
		Body:       http.NoBody,
		Request:    req,
	}, nil
}
