package collect

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"

	"github.com/emaori/ziba/internal/domain"
)

// RSS collects from RSS and Atom feeds. Both formats, and the several dialects
// of each, are handled by gofeed: parsing them by hand is a well-known way to
// lose an afternoon.
type RSS struct {
	client *http.Client
	log    *slog.Logger
}

// NewRSS builds the feed collector.
func NewRSS(client *http.Client, log *slog.Logger) *RSS {
	return &RSS{client: client, log: log}
}

// Type implements domain.Collector.
func (c *RSS) Type() domain.SourceType { return domain.SourceTypeRSS }

// Collect fetches the feed and returns one raw item per entry. The text of each
// item is whatever the feed carried, which is usually an excerpt — the full
// text is retrieved later, following the link.
func (c *RSS) Collect(ctx context.Context, src domain.Source) ([]domain.RawItem, error) {
	resp, err := get(ctx, c.client, src.URL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	feed, err := gofeed.NewParser().Parse(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse feed %s: %w", src.URL, err)
	}

	now := time.Now().UTC()
	items := make([]domain.RawItem, 0, len(feed.Items))

	for _, entry := range feed.Items {
		item, err := c.toRawItem(src, entry, now)
		if err != nil {
			// One malformed entry must not cost the whole feed.
			c.log.Warn("skipping feed entry", "source", src.Name, "title", entry.Title, "error", err)
			continue
		}
		items = append(items, item)
	}
	return items, nil
}

func (c *RSS) toRawItem(src domain.Source, entry *gofeed.Item, now time.Time) (domain.RawItem, error) {
	if entry.Link == "" {
		return domain.RawItem{}, fmt.Errorf("%w: no link", errSkipItem)
	}
	url, err := domain.NormalizeURL(entry.Link)
	if err != nil {
		return domain.RawItem{}, err
	}

	published := now
	if entry.PublishedParsed != nil {
		published = entry.PublishedParsed.UTC()
	} else if entry.UpdatedParsed != nil {
		// Some feeds only carry an update date; it is a better guess than now.
		published = entry.UpdatedParsed.UTC()
	}

	// A roundup feed's entries are issues, not articles: they are marked so the
	// expansion stage opens them instead of the full-text stage storing them.
	kind := domain.ItemKindArticle
	if src.Roundup {
		kind = domain.ItemKindRoundup
	}

	return domain.RawItem{
		SourceID:    src.ID,
		Kind:        kind,
		Title:       strings.TrimSpace(entry.Title),
		URL:         url,
		Author:      firstAuthor(entry),
		PublishedAt: published,
		CollectedAt: now,
		Text:        firstNonEmpty(entry.Content, entry.Description),
	}, nil
}

func firstAuthor(entry *gofeed.Item) string {
	for _, a := range entry.Authors {
		if a != nil && strings.TrimSpace(a.Name) != "" {
			return strings.TrimSpace(a.Name)
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
