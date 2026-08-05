package collect

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/emaori/ziba/internal/domain"
)

// defaultMaxLinks caps how many articles one visit to a site collects, when the
// source does not say.
const defaultMaxLinks = 30

// minLinkTextRunes filters out navigation. "Home", "Sport" and "Login" are
// links too; an article headline is longer than any of them.
const minLinkTextRunes = 20

// Website collects from sites that publish no feed, by reading a page and
// following the article links on it.
//
// It collects links only, not article text: each link then goes through the
// same full-text retrieval as everything else, so a scraped article and a feed
// article are indistinguishable downstream.
type Website struct {
	client   *http.Client
	renderer *Renderer
	log      *slog.Logger
}

// NewWebsite builds the scraping collector. The renderer may be nil, in which
// case sources asking to be rendered fail with a clear message rather than
// silently collecting an empty page.
func NewWebsite(client *http.Client, renderer *Renderer, log *slog.Logger) *Website {
	return &Website{client: client, renderer: renderer, log: log}
}

// Type implements domain.Collector.
func (c *Website) Type() domain.SourceType { return domain.SourceTypeWebsite }

// Collect reads the configured page and returns one raw item per article link.
func (c *Website) Collect(ctx context.Context, src domain.Source) ([]domain.RawItem, error) {
	opts := src.Website
	if opts == nil {
		opts = &domain.WebsiteOptions{}
	}

	var pattern *regexp.Regexp
	if opts.LinkPattern != "" {
		compiled, err := regexp.Compile(opts.LinkPattern)
		if err != nil {
			return nil, fmt.Errorf("link_pattern for %q: %w", src.Name, err)
		}
		pattern = compiled
	}

	body, err := c.fetch(ctx, src, opts)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	base, err := url.Parse(src.URL)
	if err != nil {
		return nil, fmt.Errorf("parse source url %s: %w", src.URL, err)
	}

	doc, err := html.Parse(io.LimitReader(body, maxArticleBytes))
	if err != nil {
		return nil, fmt.Errorf("parse page %s: %w", src.URL, err)
	}

	limit := opts.MaxLinks
	if limit <= 0 {
		limit = defaultMaxLinks
	}

	return c.itemsFrom(doc, src, base, pattern, limit), nil
}

func (c *Website) fetch(ctx context.Context, src domain.Source, opts *domain.WebsiteOptions) (io.ReadCloser, error) {
	if !opts.Render {
		resp, err := get(ctx, c.client, src.URL)
		if err != nil {
			return nil, err
		}
		return resp.Body, nil
	}

	if c.renderer == nil {
		return nil, fmt.Errorf("source %q asks to be rendered but no rendering sidecar is configured "+
			"(set ZIBA_RENDER_URL, or start it with `make up`)", src.Name)
	}
	return c.renderer.Render(ctx, src.URL)
}

// itemsFrom walks the page and turns the article links into raw items.
func (c *Website) itemsFrom(doc *html.Node, src domain.Source, base *url.URL,
	pattern *regexp.Regexp, limit int) []domain.RawItem {

	now := time.Now().UTC()
	seen := make(map[string]bool)
	items := make([]domain.RawItem, 0, limit)

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if len(items) >= limit {
			return
		}
		if n.Type == html.ElementNode && n.Data == "a" {
			if item, ok := c.linkItem(n, src, base, pattern, seen, now); ok {
				items = append(items, item)
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)

	return items
}

// linkItem decides whether one anchor is an article, and turns it into an item.
func (c *Website) linkItem(n *html.Node, src domain.Source, base *url.URL,
	pattern *regexp.Regexp, seen map[string]bool, now time.Time) (domain.RawItem, bool) {

	href := attr(n, "href")
	if href == "" || strings.HasPrefix(href, "#") {
		return domain.RawItem{}, false
	}

	// Resolve against the page, so relative links work.
	resolved, err := base.Parse(href)
	if err != nil {
		return domain.RawItem{}, false
	}

	// Stay on the site. An outbound link is someone else's article, and the
	// source that linked it is not its publisher.
	if !strings.EqualFold(strings.TrimPrefix(resolved.Hostname(), "www."),
		strings.TrimPrefix(base.Hostname(), "www.")) {
		return domain.RawItem{}, false
	}

	if pattern != nil && !pattern.MatchString(resolved.String()) {
		return domain.RawItem{}, false
	}

	normalized, err := domain.NormalizeURL(resolved.String())
	if err != nil || normalized == src.URL || seen[normalized] {
		return domain.RawItem{}, false
	}

	title := strings.Join(strings.Fields(textOf(n)), " ")
	if len([]rune(title)) < minLinkTextRunes {
		return domain.RawItem{}, false
	}

	seen[normalized] = true

	return domain.RawItem{
		SourceID: src.ID,
		Title:    title,
		URL:      normalized,
		// A listing page rarely dates its links. Collection time is a better
		// guess than the zero time, which would sort to the beginning forever;
		// full-text retrieval may improve on it later.
		PublishedAt: now,
		CollectedAt: now,
	}, true
}

func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

// textOf returns the visible text inside a node.
func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			b.WriteString(n.Data)
		case html.ElementNode:
			if n.Data == "script" || n.Data == "style" {
				return
			}
			b.WriteString(" ")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}
