// Package domain holds the core types of Ziba: the vocabulary every other
// package speaks. It depends on nothing but the standard library, so any
// package can import it without creating an import cycle.
package domain

import (
	"context"
	"strings"
	"time"
)

// SourceType identifies the kind of origin a Source is, and therefore which
// Collector knows how to read it.
type SourceType string

const (
	SourceTypeRSS        SourceType = "rss"
	SourceTypeNewsletter SourceType = "newsletter"
	SourceTypePDF        SourceType = "pdf"

	// SourceTypeWebsite is retired. Scraping was removed: it needed a bespoke
	// selector per site and broke whenever one was redesigned, while almost
	// every worthwhile source publishes a feed or a newsletter. The constant
	// remains only so configuration can reject it with an explanation, and so
	// rows collected before the removal still describe themselves.
	SourceTypeWebsite SourceType = "website"
)

// Source is a configured origin of content: a feed, a website, a mailbox, a
// folder of PDF magazines.
type Source struct {
	ID      int64
	Name    string
	Type    SourceType
	URL     string
	Enabled bool

	// CreatedAt is when this source was first seen. It is set by the database on
	// first insert and never touched again, which is what makes it a stable
	// anchor for CollectFrom.
	CreatedAt time.Time

	// CollectFrom bounds how much history a source contributes.
	CollectFrom CollectFrom

	// Roundup marks a feed whose entries are not articles but issues of a link
	// digest: each entry points at a page that lists other people's articles.
	// Such a feed is read one level deeper — the entry is kept as provenance and
	// the links it leads to become the articles.
	//
	// It cannot be detected reliably from the feed itself, so it is configured.
	Roundup bool

	// Newsletter carries the settings only a mailbox needs, and is nil
	// otherwise. Settings live here rather than in the database because the
	// configuration file is their source of truth; storing them would give them
	// two.
	Newsletter *NewsletterOptions
}

// DefaultCollectGrace is how much history a source contributes on first contact
// when nothing else is configured.
//
// A week is enough that a source has something to show immediately, and short
// enough that a feed carrying years of backlog does not arrive all at once —
// one configured feed offers two hundred and seventy-seven entries reaching
// back five years, of which a week keeps two.
const DefaultCollectGrace = 7 * 24 * time.Hour

// CollectFrom says how far back a source may reach.
//
// The cutoff is anchored to when the source was first seen, not to now. That is
// deliberate: a rolling window would silently discard everything older than the
// window whenever collection had been paused, so an outage would compound into
// lost articles. A fixed anchor costs only what the source's own window dropped.
type CollectFrom struct {
	// Grace is how far before the source was first seen to accept. Ignored when
	// Date is set or All is true.
	Grace time.Duration

	// Date is an absolute cutoff, and wins over Grace when set.
	Date time.Time

	// All disables the filter, accepting whatever the source offers.
	All bool
}

// Cutoff returns the earliest publication date this source accepts, and whether
// any filtering applies at all.
func (c CollectFrom) Cutoff(firstSeen time.Time) (time.Time, bool) {
	switch {
	case c.All:
		return time.Time{}, false
	case !c.Date.IsZero():
		return c.Date, true
	case c.Grace > 0:
		return firstSeen.Add(-c.Grace), true
	default:
		return firstSeen.Add(-DefaultCollectGrace), true
	}
}

// Accepts reports whether an item is recent enough for this source.
//
// An item with no date is accepted. Collectors substitute the collection time
// when a source gives no date, so this is mostly theoretical — but the choice
// matters: letting a little through is better than silently discarding content
// because a publisher omitted a timestamp.
func (c CollectFrom) Accepts(firstSeen, published time.Time) bool {
	cutoff, filtering := c.Cutoff(firstSeen)
	if !filtering || published.IsZero() {
		return true
	}
	return !published.Before(cutoff)
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

	// LookBackDays is how many days of mail each run reads.
	//
	// A mailbox is read the way a feed is: take a recent window every time and
	// let deduplication discard what is already known. Ziba deliberately does
	// not use the read/unread flag for this. That flag belongs to the reader's
	// mail client, so relying on it meant Ziba missed any newsletter its owner
	// happened to open first, and re-read the rest for as long as they stayed
	// unread.
	//
	// The window must comfortably exceed the collection interval, or mail that
	// arrives during an outage is never seen.
	LookBackDays int

	// MaxMessages caps how many emails one run reads.
	MaxMessages int
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

	// ItemKindRoundup is a page that lists other people's articles: one issue of
	// a link digest. It is not reading material either, but unlike provenance it
	// still has work to do — it is fetched once so the links can be pulled out
	// of it, and becomes provenance in spirit thereafter.
	ItemKindRoundup ItemKind = "roundup"
)

// RawItem is a freshly collected element, before any AI processing.
type RawItem struct {
	ID          int64
	SourceID    int64
	Kind        ItemKind
	Title       string
	URL         string
	Author      string
	PublishedAt time.Time
	CollectedAt time.Time

	// Text is whatever the source itself supplied, before anything was fetched
	// from the article's own page. What that amounts to varies enormously, and
	// it is stored exactly as given rather than cleaned up:
	//
	//   - a feed gives whatever it chose to publish, as markup. That ranges from
	//     one sentence to the entire post — measured across the configured
	//     feeds, from about 50 characters to 27,000.
	//   - a newsletter's provenance row holds the email body, already reduced to
	//     plain text.
	//   - a link extracted from a newsletter has none: only the page it points
	//     at can supply the text.
	//
	// It serves two quite different purposes depending on Kind. For an article
	// it is a *fallback*, read once when the article's own page is fetched and
	// used only if that fetch fails — which is what keeps a blocked publisher
	// from costing an entry entirely. For a provenance row it is the *record*:
	// the only copy of the email Ziba keeps, and never replaced by anything.
	Text string
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

	// ArchivedAt is when the reader marked this read. While it is set the
	// article is out of circulation: gone from the interest tabs and from the
	// daily selection, though still present in the day-by-day view, which shows
	// everything on purpose.
	ArchivedAt time.Time
}

// Archived reports whether the reader has marked this article read.
func (a Article) Archived() bool { return !a.ArchivedAt.IsZero() }

// HasOriginal reports whether there is somewhere else to read this.
//
// Usually there is, and the reader offers it. An essay sent by email has no
// external original — the email is the article — and its address is the
// synthetic one that identifies the message, which no browser can open.
func (a Article) HasOriginal() bool {
	return strings.HasPrefix(a.URL, "http://") || strings.HasPrefix(a.URL, "https://")
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
