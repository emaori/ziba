package collect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Requests to one host must be spaced out — this is what keeps Ziba from being
// blocked when it fetches eighty articles from one publisher.
func TestThrottlePacesRequestsToOneHost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()

	const interval = 50 * time.Millisecond
	client := NewHTTPClient(5*time.Second, interval)

	started := time.Now()
	for range 4 {
		resp, err := client.Get(server.URL)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		resp.Body.Close()
	}
	elapsed := time.Since(started)

	// Four requests means three gaps.
	if min := 3 * interval; elapsed < min {
		t.Errorf("four requests took %v, want at least %v — they were not spaced out", elapsed, min)
	}
}

// Different hosts must not wait for each other, or one slow publisher would
// hold up the whole run.
func TestThrottleDoesNotBlockAcrossHosts(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer second.Close()

	// Both test servers listen on 127.0.0.1, so pace by port to keep them apart.
	transport := &throttledTransport{
		base:     http.DefaultTransport,
		interval: time.Hour,
		next:     map[string]time.Time{},
	}

	if wait := transport.reserve("a.example.com"); wait != 0 {
		t.Errorf("first request to a host waited %v, want none", wait)
	}
	if wait := transport.reserve("b.example.com"); wait != 0 {
		t.Errorf("a different host waited %v, want none", wait)
	}
	if wait := transport.reserve("a.example.com"); wait <= 0 {
		t.Error("a second request to the same host did not wait")
	}
}

// Reserving is what concurrent callers race on, so it must be safe.
func TestThrottleReserveIsConcurrencySafe(t *testing.T) {
	transport := &throttledTransport{
		base:     http.DefaultTransport,
		interval: time.Millisecond,
		next:     map[string]time.Time{},
	}

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			transport.reserve("example.com")
		}()
	}
	wg.Wait()
}

// A cancelled request must not sit out its delay.
func TestThrottleHonoursCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()

	client := NewHTTPClient(5*time.Second, time.Hour)

	resp, err := client.Get(server.URL) // first request takes the slot immediately
	if err != nil {
		t.Fatalf("first request failed: %v", err)
	}
	resp.Body.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	started := time.Now()
	if _, err := client.Do(req); err == nil {
		t.Error("the second request succeeded, want it cancelled while waiting")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("cancellation took %v — it waited out the delay instead of giving up", elapsed)
	}
}
