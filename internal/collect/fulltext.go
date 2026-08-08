package collect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	readability "github.com/go-shiori/go-readability"
	"golang.org/x/net/html"

	"github.com/emaori/ziba/internal/domain"
)

// ErrNotArticle marks a link that turned out not to lead to reading material.
// It is not a failure: the item was collected correctly and there is simply
// nothing to store, so the caller should move on rather than retry.
var ErrNotArticle = errors.New("not an article")

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

	// A newsletter that is itself the article has a synthetic address and its
	// text already in hand. There is nothing to fetch, and trying would only
	// produce a warning about an unsupported scheme.
	if !strings.HasPrefix(item.URL, "http://") && !strings.HasPrefix(item.URL, "https://") {
		return article, nil
	}

	extracted, landed, err := f.extract(ctx, item.URL)

	// Where the link actually landed becomes the article's identity.
	//
	// Newsletters rarely link straight at an article. They link at a click
	// tracker that redirects, and that tracker's address is unique to the
	// recipient and often to the send — so storing it would give the same
	// article a different identity every time it arrived, which is precisely
	// what identity-by-address exists to prevent.
	//
	// Applied before the error is considered, because the two are independent:
	// a tracker resolves to a real address even when the page it names then
	// refuses us. Keeping the tracker in that case would be the worst of both,
	// an article filed under an address that identifies nothing and cannot be
	// retried.
	if landed != "" && landed != item.URL {
		article.URL = landed
	}

	// The link filters ran on the address in the email, which for most
	// newsletters is a tracker that reveals nothing. Now that the destination is
	// known, judge it too — otherwise a video reaches the archive simply by
	// having been linked through a redirect.
	if landed != "" {
		if parsed, perr := url.Parse(landed); perr == nil && isNonEditorial(parsed) {
			return article, fmt.Errorf("%w: %s", ErrNotArticle, landed)
		}
	}

	if err != nil {
		// Not fatal: the article is stored with the excerpt, and can be
		// retried later.
		return article, fmt.Errorf("full text for %s: %w", item.URL, err)
	}

	if text := readableText(extracted); text != "" {
		article.FullText = text
	}
	// The feed is the more trustworthy source of a title; the page only fills
	// in what is missing — or what is not really a title. A roundup links each
	// article twice, once behind its headline and once behind the bare address,
	// and when the address comes first that is the text we are left holding.
	if title := strings.TrimSpace(extracted.Title); title != "" &&
		(article.Title == "" || looksLikeURL(article.Title)) {
		article.Title = title
	}
	if article.Author == "" {
		article.Author = strings.TrimSpace(extracted.Byline)
	}
	return article, nil
}

// extract fetches a page and returns its readable content along with the
// address the request finally landed on, which is not the one asked for
// whenever a redirect was followed.
func (f *FullText) extract(ctx context.Context, rawURL string) (readability.Article, string, error) {
	resp, err := get(ctx, f.client, rawURL)
	if err != nil {
		// A refused page still tells us where the redirects ended.
		return readability.Article{}, landingOf(resp), err
	}
	defer resp.Body.Close()

	// The client follows redirects, and resp.Request is the last one made.
	parsed := resp.Request.URL
	landed := landingOf(resp)

	// LimitReader, not the whole body: a malformed or hostile page must not be
	// able to exhaust memory.
	body := io.LimitReader(resp.Body, maxArticleBytes)

	article, err := readability.FromReader(body, parsed)
	if err != nil {
		return readability.Article{}, landed, fmt.Errorf("extract readable content: %w", err)
	}
	return article, landed, nil
}

// landingOf reports the normalized address a response came from, or "" when
// there is nothing usable to report.
func landingOf(resp *http.Response) string {
	if resp == nil || resp.Request == nil || resp.Request.URL == nil {
		return ""
	}
	landed, err := domain.NormalizeURL(resp.Request.URL.String())
	if err != nil {
		return ""
	}
	return landed
}

// looksLikeURL reports whether a title is really just an address. Anchor text
// is not always a headline, and "https://example.com/writing/some-slug" tells a
// reader nothing that the link itself does not.
func looksLikeURL(title string) bool {
	title = strings.TrimSpace(title)
	if strings.ContainsAny(title, " \t") {
		return false
	}
	return strings.HasPrefix(title, "http://") || strings.HasPrefix(title, "https://")
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
	if text := readableText(article); text != "" {
		return text
	}
	return s
}

// blockElements end a line of text. Walking the extracted tree and breaking on
// these is what preserves paragraphs.
var blockElements = map[string]bool{
	"p": true, "div": true, "section": true, "article": true, "br": true,
	"li": true, "ul": true, "ol": true, "blockquote": true, "pre": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"figcaption": true, "table": true, "tr": true, "hr": true,
}

// readableText turns extracted content into plain text with one paragraph per
// line.
//
// The library's own TextContent concatenates every text node, which for most
// pages produces a single unbroken run of ten thousand characters — technically
// the article, but unreadable. Walking the tree and breaking on block elements
// keeps the structure that makes prose legible.
//
// The result is still plain text, not markup. That is deliberate: the reader
// escapes what it renders, so a hostile page cannot inject anything into it.
func readableText(article readability.Article) string {
	if article.Node == nil {
		return strings.TrimSpace(article.TextContent)
	}

	var raw strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			raw.WriteString(n.Data)
		case html.ElementNode:
			if n.Data == "script" || n.Data == "style" {
				return
			}
			if blockElements[n.Data] {
				raw.WriteByte('\n')
				defer raw.WriteByte('\n')
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(article.Node)

	// Collapse the whitespace inside each line, and drop the empty ones that
	// nested block elements inevitably produce.
	var lines []string
	for _, line := range strings.Split(raw.String(), "\n") {
		if collapsed := strings.Join(strings.Fields(line), " "); collapsed != "" {
			lines = append(lines, collapsed)
		}
	}
	return strings.Join(lines, "\n")
}
