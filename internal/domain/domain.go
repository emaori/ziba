// Package domain holds the core types of Ziba: the vocabulary every other
// package speaks. It depends on nothing but the standard library, so any
// package can import it without creating an import cycle.
package domain

import (
	"context"
	"time"
)

// SourceType identifies the kind of origin a Source is, and therefore which
// Collector knows how to read it.
type SourceType string

const (
	SourceTypeRSS        SourceType = "rss"
	SourceTypeWebsite    SourceType = "website"
	SourceTypeNewsletter SourceType = "newsletter"
	SourceTypePDF        SourceType = "pdf"
)

// Source is a configured origin of content: a feed, a website, a mailbox, a
// folder of PDF magazines.
type Source struct {
	ID      int64
	Name    string
	Type    SourceType
	URL     string
	Enabled bool

	// Website and Newsletter carry the settings only their own type needs, and
	// are nil otherwise. Settings live here rather than in the database because
	// the configuration file is their source of truth; storing them would give
	// them two.
	Website    *WebsiteOptions
	Newsletter *NewsletterOptions
}

// NewsletterOptions describes a mailbox of newsletters.
//
// Newsletters are read as link aggregators: the email is a list of links with
// short blurbs, and the value is in the articles it points at. Credentials are
// named here but never written here — the file holds the name of an environment
// variable, so a source list stays safe to commit.
type NewsletterOptions struct {
	Folder      string
	UsernameEnv string
	PasswordEnv string

	// UnreadOnly restricts collection to messages not yet seen, which is what
	// makes a nightly run cheap on a mailbox that has years of history.
	UnreadOnly bool

	// MaxMessages caps how many emails one run reads.
	MaxMessages int
}

// WebsiteOptions tunes how a site is scraped for article links.
type WebsiteOptions struct {
	// LinkPattern is a regular expression an article address must match. It is
	// the difference between collecting a site's articles and collecting its
	// navigation: most sites encode a date or a section in article addresses.
	LinkPattern string

	// Render fetches the page through the browser sidecar instead of over plain
	// HTTP, for sites that build their markup in the browser.
	Render bool

	// MaxLinks caps how many articles one visit collects.
	MaxLinks int
}

// ItemKind says whether a collected item is destined to become an article.
type ItemKind string

const (
	// ItemKindArticle is the default: the item becomes an Article.
	ItemKindArticle ItemKind = "article"

	// ItemKindProvenance is kept for the record but never becomes an Article.
	// A newsletter is the case this exists for: what belongs in Ziba are the
	// articles it links to, while the email itself only answers "where did this
	// come from".
	ItemKindProvenance ItemKind = "provenance"
)

// RawItem is a freshly collected element, before any AI processing. Its text
// may be missing or partial: feeds often carry only an excerpt, and the full
// text is retrieved in a later step.
type RawItem struct {
	ID          int64
	SourceID    int64
	Kind        ItemKind
	Title       string
	URL         string
	Author      string
	PublishedAt time.Time
	CollectedAt time.Time
	Text        string
}

// RelevanceScore expresses how relevant an article is to the configured
// interests, from 0 to 100.
type RelevanceScore int

// Article is the central entity: a normalized, processed item. Its identity is
// URL, normalized at collection time, never the title — two sources may publish
// the same story under different headlines, and the same headline may be reused
// by different articles.
type Article struct {
	ID       int64
	SourceID int64

	// SourceName is filled in by queries that join the source, because every
	// screen that shows an article shows where it came from. It is display
	// data, not part of the article's identity.
	SourceName string

	Title       string
	URL         string
	Author      string
	PublishedAt time.Time
	CollectedAt time.Time
	FullText    string

	// Filled in by the AI pipeline.
	Categories  []string
	Entities    []string
	Tone        string
	Summary     string
	Score       RelevanceScore
	ScoreReason string
	AnalyzedAt  time.Time
}

// Digest is the daily selection of the most relevant articles, ranked by score.
// Only the date part of Date is meaningful.
type Digest struct {
	Date     time.Time
	Articles []Article
}

// Collector knows how to collect from one type of Source. Adding support for a
// new type of source means implementing this interface, not changing the
// pipeline.
//
// Implementations must respect ctx: collection is I/O-bound and runs on a
// schedule, so a slow source has to be cancellable.
type Collector interface {
	// Type reports which SourceType this collector handles.
	Type() SourceType

	// Collect reads the source and returns what it found. Returning zero items
	// with a nil error is normal: it means nothing new was published.
	Collect(ctx context.Context, src Source) ([]RawItem, error)
}
