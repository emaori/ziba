// Package collect turns configured sources into raw items. It holds one
// Collector implementation per source type and the fan-out that runs them.
package collect

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/emaori/ziba/internal/domain"
)

// userAgent identifies Ziba to the sites it reads. Being honest about who is
// calling is what keeps a personal aggregator welcome.
const userAgent = "Ziba/1.0 (personal content aggregator)"

// maxParallelSources caps how many sources are read at once. Collection is
// almost entirely waiting on the network, so a small number already saturates
// it while staying polite to the servers involved.
const maxParallelSources = 4

// get performs a GET carrying the Ziba user agent and honouring ctx.
//
// On a bad status it returns both the error and the response, with the body
// already closed. That is unusual, and deliberate: the response records where
// the request finally landed after redirects, which is worth knowing even when
// the page itself was refused — a newsletter's click tracker resolves to a real
// article address whether or not that article lets us read it.
func get(ctx context.Context, client *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", url, err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return resp, fmt.Errorf("get %s: unexpected status %s", url, resp.Status)
	}
	return resp, nil
}

// Registry maps each source type to the collector that handles it.
type Registry struct {
	collectors map[domain.SourceType]domain.Collector
}

// NewRegistry builds a registry from the given collectors. It is the one place
// that knows which types are supported.
func NewRegistry(collectors ...domain.Collector) *Registry {
	r := &Registry{collectors: make(map[domain.SourceType]domain.Collector, len(collectors))}
	for _, c := range collectors {
		r.collectors[c.Type()] = c
	}
	return r
}

// For returns the collector handling a source type.
func (r *Registry) For(t domain.SourceType) (domain.Collector, bool) {
	c, ok := r.collectors[t]
	return c, ok
}

// Result is what one source produced.
type Result struct {
	Source domain.Source
	Items  []domain.RawItem
	Err    error
}

// Run collects from every source in parallel and returns one Result each, in
// no particular order.
//
// A failing source does not stop the others: one feed being down is normal, and
// losing the whole run over it would be worse than losing that feed. Errors are
// carried in the Result so the caller decides what to report.
func (r *Registry) Run(ctx context.Context, log *slog.Logger, sources []domain.Source) []Result {
	var (
		mu      sync.Mutex
		results = make([]Result, 0, len(sources))
	)

	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(maxParallelSources)

	for _, src := range sources {
		group.Go(func() error {
			items, err := r.collectOne(ctx, log, src)

			mu.Lock()
			defer mu.Unlock()
			results = append(results, Result{Source: src, Items: items, Err: err})

			// Always nil: errors travel in the Result, so one bad source never
			// cancels the group.
			return nil
		})
	}
	_ = group.Wait()

	return results
}

func (r *Registry) collectOne(ctx context.Context, log *slog.Logger, src domain.Source) ([]domain.RawItem, error) {
	collector, ok := r.For(src.Type)
	if !ok {
		return nil, fmt.Errorf("no collector for source type %q", src.Type)
	}

	log.Debug("collecting", "source", src.Name, "type", src.Type)
	items, err := collector.Collect(ctx, src)
	if err != nil {
		return nil, err
	}
	return items, nil
}

// errSkipItem marks an entry that is not worth collecting — a feed entry
// without a usable link, for instance. It is not a failure of the source.
var errSkipItem = errors.New("skip item")
