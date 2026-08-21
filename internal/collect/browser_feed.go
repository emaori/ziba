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

const maxBrowserFeedBytes = 8 << 20 // 8 MiB

// FeedBrowser retrieves a feed through Ziba's Firefox sidecar. It is used only
// by RSS sources that explicitly opt in; ordinary sources keep the smaller and
// faster direct HTTP path.
type FeedBrowser interface {
	Fetch(context.Context, string) ([]byte, error)
}

// BrowserFeed is the HTTP client for the deliberately narrow browser service.
// The service returns raw RSS bytes, never rendered page markup.
type BrowserFeed struct {
	client   *http.Client
	endpoint string
}

// NewBrowserFeed builds a browser feed client. An empty endpoint is allowed so
// Ziba can continue serving and collecting ordinary sources when the optional
// sidecar is not configured.
func NewBrowserFeed(client *http.Client, endpoint string) *BrowserFeed {
	return &BrowserFeed{client: client, endpoint: strings.TrimRight(endpoint, "/")}
}

// Fetch asks Firefox for one RSS or Atom document.
func (b *BrowserFeed) Fetch(ctx context.Context, address string) ([]byte, error) {
	if b == nil || b.endpoint == "" {
		return nil, fmt.Errorf("browser feed fetching is enabled but ZIBA_BROWSER_URL is not set")
	}
	payload, err := json.Marshal(struct {
		URL string `json:"url"`
	}{URL: address})
	if err != nil {
		return nil, fmt.Errorf("encode browser feed request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint+"/fetch", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build browser feed request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("browser fetch %s: %w", address, err)
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, maxBrowserFeedBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read browser feed %s: %w", address, err)
	}
	if len(body) > maxBrowserFeedBytes {
		return nil, fmt.Errorf("browser feed %s exceeds %d bytes", address, maxBrowserFeedBytes)
	}
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		message := strings.TrimSpace(string(body))
		if json.Unmarshal(body, &failure) == nil && failure.Error != "" {
			message = failure.Error
		}
		return nil, fmt.Errorf("browser fetch %s: sidecar returned %s: %s", address, resp.Status, message)
	}
	return body, nil
}
