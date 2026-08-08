package collect

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/emaori/ziba/internal/domain"
)

// maxRoundupBytes bounds how much of an issue page is read. A list of links is
// small; anything larger is not the page we were promised.
const maxRoundupBytes = 4 << 20 // 4 MiB

// Roundup expands one issue of a link digest into the articles it points at.
//
// It exists because a roundup feed lies about what it publishes. Each entry
// looks like an article and is in fact an index: follow it and you store a page
// of other people's headlines, which the analyzer then scores as though it were
// a piece of writing. What the reader wants is one level down.
//
// This is deliberately a separate stage rather than part of collection. The
// issue page has to be fetched to be useful, and a feed with five years of
// history would mean five years of fetches — so the entries are collected
// first, filtered by date like everything else, and only the survivors are
// opened.
type Roundup struct {
	client *http.Client
}

// NewRoundup builds the expander.
func NewRoundup(client *http.Client) *Roundup {
	return &Roundup{client: client}
}

// Links fetches one issue and returns the articles it links to.
//
// The issue's own date is carried onto each link. A roundup is published as a
// week's reading and the individual pieces rarely date themselves consistently,
// so the issue is the more useful date and the only one that is certainly known.
func (r *Roundup) Links(ctx context.Context, issue domain.RawItem) ([]domain.RawItem, error) {
	resp, err := get(ctx, r.client, issue.URL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	markup, err := io.ReadAll(io.LimitReader(resp.Body, maxRoundupBytes))
	if err != nil {
		return nil, fmt.Errorf("read roundup %s: %w", issue.URL, err)
	}

	// The same extraction a newsletter gets: a roundup page and a roundup email
	// are the same artefact delivered differently, and both are mostly
	// navigation, footers and sponsor slots around a handful of real links.
	links := editorialLinks(string(markup))

	now := time.Now().UTC()
	items := make([]domain.RawItem, 0, len(links))
	for _, link := range links {
		items = append(items, domain.RawItem{
			SourceID:    issue.SourceID,
			Kind:        domain.ItemKindArticle,
			Title:       link.text,
			URL:         link.url,
			PublishedAt: issue.PublishedAt,
			CollectedAt: now,
		})
	}
	return items, nil
}
