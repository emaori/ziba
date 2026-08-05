package collect

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	readability "github.com/go-shiori/go-readability"

	"github.com/emaori/ziba/internal/domain"
)

// maxArticleBytes caps how much of a page is read. Some pages are enormous, and
// an article that needs more than this is not an article.
const maxArticleBytes = 8 << 20 // 8 MiB

// FullText retrieves the readable body of a page: it strips navigation,
// sidebars, adverts and footers, keeping what a reader would call the article.
//
// Storing this locally is what makes content survive a paywall appearing, a
// site disappearing, or being offline.
type FullText struct {
	client *http.Client
}

// NewFullText builds the retriever.
func NewFullText(client *http.Client) *FullText {
	return &FullText{client: client}
}

// Article turns a raw item into an Article by following its link and extracting
// the readable content. The item's own text is kept as a fallback, so a page
// that cannot be fetched still yields whatever the feed provided.
func (f *FullText) Article(ctx context.Context, item domain.RawItem) (domain.Article, error) {
	article := domain.Article{
		SourceID:    item.SourceID,
		URL:         item.URL,
		Title:       item.Title,
		Author:      item.Author,
		PublishedAt: item.PublishedAt,
		CollectedAt: time.Now().UTC(),
		FullText:    plainText(item.Text),
	}

	extracted, err := f.extract(ctx, item.URL)
	if err != nil {
		// Not fatal: the article is stored with the excerpt, and can be
		// retried later.
		return article, fmt.Errorf("full text for %s: %w", item.URL, err)
	}

	if text := strings.TrimSpace(extracted.TextContent); text != "" {
		article.FullText = text
	}
	// The feed is the more trustworthy source of a title; the page only fills
	// in what is missing.
	if article.Title == "" {
		article.Title = strings.TrimSpace(extracted.Title)
	}
	if article.Author == "" {
		article.Author = strings.TrimSpace(extracted.Byline)
	}
	return article, nil
}

func (f *FullText) extract(ctx context.Context, rawURL string) (readability.Article, error) {
	resp, err := get(ctx, f.client, rawURL)
	if err != nil {
		return readability.Article{}, err
	}
	defer resp.Body.Close()

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return readability.Article{}, fmt.Errorf("parse url: %w", err)
	}

	// LimitReader, not the whole body: a malformed or hostile page must not be
	// able to exhaust memory.
	body := io.LimitReader(resp.Body, maxArticleBytes)

	article, err := readability.FromReader(body, parsed)
	if err != nil {
		return readability.Article{}, fmt.Errorf("extract readable content: %w", err)
	}
	return article, nil
}

// plainText reduces feed content, which is usually HTML, to something readable.
// It is a fallback only: when extraction works, its text is used instead.
func plainText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	article, err := readability.FromReader(strings.NewReader(s), nil)
	if err != nil {
		return s
	}
	if text := strings.TrimSpace(article.TextContent); text != "" {
		return text
	}
	return s
}
