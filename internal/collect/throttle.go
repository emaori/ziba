package collect

import (
	"net/http"
	"sync"
	"time"
)

// PolitenessInterval is the minimum gap between requests to the same host.
//
// Retrieving full text means fetching every article a source listed — eighty-odd
// pages from one publisher in a single run. Doing that as fast as the network
// allows is what got Ziba blocked by one of them. One second per host costs a
// nightly job a minute or two and keeps it welcome.
const PolitenessInterval = time.Second

// throttledTransport spaces out requests per host.
//
// It sits in the transport rather than at each call site so that everything
// using the client is covered — feeds, scraped pages and article retrieval
// alike — and so no future collector can forget.
type throttledTransport struct {
	base     http.RoundTripper
	interval time.Duration

	mu   sync.Mutex
	next map[string]time.Time
}

func (t *throttledTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if wait := t.reserve(req.URL.Hostname()); wait > 0 {
		select {
		case <-time.After(wait):
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
	return t.base.RoundTrip(req)
}

// reserve claims the next slot for a host and reports how long to wait for it.
// The slot is taken under the lock but waited for outside it, so concurrent
// requests to different hosts never block each other.
func (t *throttledTransport) reserve(host string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	earliest := t.next[host]
	if earliest.Before(now) {
		earliest = now
	}
	t.next[host] = earliest.Add(t.interval)

	return earliest.Sub(now)
}

// NewHTTPClient returns the client used for outbound requests. The timeout is
// per request including body: without it a source that never answers would hold
// a slot until the process is killed.
//
// A non-zero interval spaces requests to the same host apart. Pass zero for
// hosts that are ours — the rendering sidecar is not a stranger to be polite to.
func NewHTTPClient(timeout, interval time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = newSafeDialer().DialContext
	return newHTTPClient(timeout, interval, transport)
}

func newHTTPClient(timeout, interval time.Duration, base http.RoundTripper) *http.Client {
	client := &http.Client{Timeout: timeout, Transport: base}
	if interval > 0 {
		client.Transport = &throttledTransport{
			base:     base,
			interval: interval,
			next:     make(map[string]time.Time),
		}
	}
	return client
}
