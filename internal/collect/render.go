package collect

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Renderer fetches a page through a headless browser, for sites that build
// their markup in the browser rather than on the server.
//
// The browser runs in its own container. That is not an architectural boundary
// — Ziba is a monolith — but keeping Chromium in a separate process keeps its
// crashes and its appetite for memory away from everything else.
type Renderer struct {
	client   *http.Client
	endpoint string
}

// NewRenderer builds a client for the rendering sidecar. An empty endpoint
// returns nil: rendering is optional, and most sites do not need it.
func NewRenderer(client *http.Client, endpoint string) *Renderer {
	if strings.TrimSpace(endpoint) == "" {
		return nil
	}
	return &Renderer{client: client, endpoint: strings.TrimRight(endpoint, "/")}
}

// Render returns the page's markup after scripts have run. The caller closes
// the body.
func (r *Renderer) Render(ctx context.Context, pageURL string) (io.ReadCloser, error) {
	body, err := json.Marshal(map[string]any{
		"url": pageURL,
		// Wait for the network to settle rather than for the load event: a page
		// that fetches its content after load is exactly the case this exists
		// for, and the load event fires before that content arrives.
		"gotoOptions": map[string]any{"waitUntil": "networkidle2"},
	})
	if err != nil {
		return nil, fmt.Errorf("build render request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint+"/content", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build render request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("render %s: %w", pageURL, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		// The sidecar explains its own failures; passing that through is far
		// more useful than reporting only the status.
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("render %s: %s: %s", pageURL, resp.Status, strings.TrimSpace(string(detail)))
	}
	return resp.Body, nil
}
