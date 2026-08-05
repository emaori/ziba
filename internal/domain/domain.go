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
}

// RawItem is a freshly collected element, before any AI processing. Its text
// may be missing or partial: feeds often carry only an excerpt, and the full
// text is retrieved in a later step.
type RawItem struct {
	ID          int64
	SourceID    int64
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
	ID          int64
	SourceID    int64
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
