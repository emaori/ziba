package collect

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrowserFeedFetch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/fetch" {
			t.Errorf("request = %s %s, want POST /fetch", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"url":"https://blocked.example/feed"`) {
			t.Errorf("body = %s", body)
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		io.WriteString(w, sampleFeed)
	}))
	defer server.Close()

	got, err := NewBrowserFeed(server.Client(), server.URL).Fetch(t.Context(), "https://blocked.example/feed")
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if string(got) != sampleFeed {
		t.Error("Fetch did not return the sidecar response body")
	}
}

func TestBrowserFeedReportsSidecarFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, `{"error":"upstream returned 403 Forbidden"}`)
	}))
	defer server.Close()

	_, err := NewBrowserFeed(server.Client(), server.URL).Fetch(context.Background(), "https://blocked.example/feed")
	if err == nil || !strings.Contains(err.Error(), "upstream returned 403 Forbidden") {
		t.Fatalf("Fetch error = %v, want sidecar detail", err)
	}
}

func TestBrowserFeedRequiresConfiguredSidecar(t *testing.T) {
	_, err := NewBrowserFeed(http.DefaultClient, "").Fetch(t.Context(), "https://example.com/feed")
	if err == nil || !strings.Contains(err.Error(), "ZIBA_BROWSER_URL") {
		t.Fatalf("Fetch error = %v, want missing configuration", err)
	}
}
