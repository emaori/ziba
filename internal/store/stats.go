package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Tally is what became of the items collected from one source, or on one day.
//
// Collected counts what reached the database. It is deliberately not "what the
// sources offered": items rejected for predating a source's cutoff, and links
// filtered out of a newsletter as navigation, are discarded before anything is
// written and leave no row to count. The run reports them in its log; nothing
// stores them.
type Tally struct {
	Source string // empty on a by-day row
	Day    time.Time

	Provenance int // emails kept for the record, never destined to be articles
	Roundups   int // issues of a link digest, opened for the links they hold
	Links      int // everything else: an item meant to become an article

	Stored    int // became an article that did not exist before
	Duplicate int // the same address was already stored, often by another source
	Skipped   int // led somewhere deliberately not stored, such as a video
	Pending   int // collected, not yet processed
	Unknown   int // finished before outcomes were recorded
}

// Collected is everything that reached the database from this source or day.
func (t Tally) Collected() int { return t.Provenance + t.Roundups + t.Links }

// Discarded is the links that produced no article, for a reason we can name.
func (t Tally) Discarded() int { return t.Duplicate + t.Skipped }

// statsColumns is the same projection for both groupings.
const statsColumns = `
	count(*) FILTER (WHERE r.kind = 'provenance'),
	count(*) FILTER (WHERE r.kind = 'roundup'),
	count(*) FILTER (WHERE r.kind = 'article'),
	count(*) FILTER (WHERE r.outcome = 'stored'),
	count(*) FILTER (WHERE r.outcome = 'duplicate'),
	count(*) FILTER (WHERE r.outcome = 'skipped'),
	count(*) FILTER (WHERE r.processed_at IS NULL AND r.kind <> 'provenance'),
	count(*) FILTER (WHERE r.processed_at IS NOT NULL AND r.outcome IS NULL)`

func scanTally(row pgx.CollectableRow, t *Tally, lead ...any) error {
	dest := append(lead,
		&t.Provenance, &t.Roundups, &t.Links,
		&t.Stored, &t.Duplicate, &t.Skipped, &t.Pending, &t.Unknown)
	return row.Scan(dest...)
}

// TalliesBySource reports what each source has contributed, busiest first.
func (s *Store) TalliesBySource(ctx context.Context) ([]Tally, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT s.name,`+statsColumns+`
		FROM raw_items r JOIN sources s ON s.id = r.source_id
		GROUP BY s.name
		ORDER BY count(*) DESC, s.name`)
	if err != nil {
		return nil, fmt.Errorf("query stats by source: %w", err)
	}

	tallies, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Tally, error) {
		var t Tally
		return t, scanTally(row, &t, &t.Source)
	})
	if err != nil {
		return nil, fmt.Errorf("read stats by source: %w", err)
	}
	return tallies, nil
}

// DayTally is one day of collection: its totals, and the sources behind them.
type DayTally struct {
	Tally           // the day as a whole
	Sources []Tally // what each source contributed to it, busiest first
}

// TalliesByDay reports the same figures per day of collection, newest first,
// each day broken down by source.
//
// The day is when the item was collected, not when it was published: this
// answers "what did the run on Tuesday do", and an article published years ago
// still arrives on the day its feed was read.
//
// The rows come back grouped by day and source and are folded into days here.
// Totalling in Go rather than asking the database for both groupings means one
// query and no chance of the two disagreeing.
func (s *Store) TalliesByDay(ctx context.Context, limit int) ([]DayTally, error) {
	rows, err := s.pool.Query(ctx, `
		WITH recent AS (
			SELECT DISTINCT collected_at::date AS day
			FROM raw_items
			ORDER BY day DESC
			LIMIT $1
		)
		SELECT r.collected_at::date, s.name,`+statsColumns+`
		FROM raw_items r
		JOIN sources s ON s.id = r.source_id
		WHERE r.collected_at::date IN (SELECT day FROM recent)
		GROUP BY 1, 2
		ORDER BY 1 DESC, count(*) DESC, s.name`, limit)
	if err != nil {
		return nil, fmt.Errorf("query stats by day: %w", err)
	}

	perSource, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Tally, error) {
		var t Tally
		return t, scanTally(row, &t, &t.Day, &t.Source)
	})
	if err != nil {
		return nil, fmt.Errorf("read stats by day: %w", err)
	}

	var days []DayTally
	for _, source := range perSource {
		if len(days) == 0 || !days[len(days)-1].Day.Equal(source.Day) {
			days = append(days, DayTally{Tally: Tally{Day: source.Day}})
		}
		day := &days[len(days)-1]
		day.Sources = append(day.Sources, source)
		day.Tally.add(source)
	}
	return days, nil
}

// add folds one source's figures into a running total.
func (t *Tally) add(other Tally) {
	t.Provenance += other.Provenance
	t.Roundups += other.Roundups
	t.Links += other.Links
	t.Stored += other.Stored
	t.Duplicate += other.Duplicate
	t.Skipped += other.Skipped
	t.Pending += other.Pending
	t.Unknown += other.Unknown
}

// TokenTally is what the AI pipeline spent, for one interest, one day, or in
// total.
//
// Input and output are kept apart because they are priced apart — output costs
// several times input on every model this project has used — so a single total
// would hide the number that actually moves the bill.
//
// Articles counts how many articles the figures cover, which is what makes them
// comparable: a day that analyzed four hundred articles and a day that analyzed
// four are not usefully compared on totals alone.
type TokenTally struct {
	Label    string // the interest, or empty on a total
	Day      time.Time
	Articles int
	Input    int64
	Output   int64
}

// Total is the two added together.
func (t TokenTally) Total() int64 { return t.Input + t.Output }

// PerArticle is the average cost of an article in this group, or zero when the
// group is empty.
func (t TokenTally) PerArticle() int64 {
	if t.Articles == 0 {
		return 0
	}
	return t.Total() / int64(t.Articles)
}

// tokenColumns is the same projection wherever tokens are counted.
//
// Only analyzed articles are counted. An article that has never been through
// the pipeline has zero tokens for the same reason an offline one does, and
// including it would drag every average towards nothing.
const tokenColumns = `
	count(*),
	COALESCE(sum(input_tokens), 0),
	COALESCE(sum(output_tokens), 0)`

// Tokens reports what the pipeline has spent in total.
func (s *Store) Tokens(ctx context.Context) (TokenTally, error) {
	var t TokenTally
	err := s.pool.QueryRow(ctx, `
		SELECT`+tokenColumns+`
		FROM articles WHERE processed_at IS NOT NULL`).
		Scan(&t.Articles, &t.Input, &t.Output)
	if err != nil {
		return t, fmt.Errorf("query token totals: %w", err)
	}
	return t, nil
}

// TokensByInterest reports the same per configured interest, costliest first.
//
// An article in three interests is counted in all three, so these rows add up
// to more than the total above. That is the honest arrangement: the question is
// what each interest costs to maintain, and an article shared between two of
// them is a cost to both. The page says so rather than leaving it to be noticed.
//
// The interests are passed in because they live in configuration and the
// database should not hold a second opinion about which ones exist.
func (s *Store) TokensByInterest(ctx context.Context, interests []string) ([]TokenTally, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT interest,`+tokenColumns+`
		FROM articles, unnest(categories) AS interest
		WHERE processed_at IS NOT NULL AND interest = ANY($1::text[])
		GROUP BY interest
		ORDER BY sum(input_tokens) + sum(output_tokens) DESC, interest`, interests)
	if err != nil {
		return nil, fmt.Errorf("query tokens by interest: %w", err)
	}

	tallies, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (TokenTally, error) {
		var t TokenTally
		return t, row.Scan(&t.Label, &t.Articles, &t.Input, &t.Output)
	})
	if err != nil {
		return nil, fmt.Errorf("read tokens by interest: %w", err)
	}
	return tallies, nil
}

// TokensByDay reports the same per day of analysis, newest first.
//
// The day is when the model ran, not when the article was collected — the two
// other tables on this page group by collection, and this one cannot: a
// backfill spends every token on one afternoon for articles gathered over
// weeks, and reporting that against their collection dates would spread a cost
// across days on which nothing was spent.
func (s *Store) TokensByDay(ctx context.Context, limit int) ([]TokenTally, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT processed_at::date AS day,`+tokenColumns+`
		FROM articles
		WHERE processed_at IS NOT NULL
		GROUP BY day
		ORDER BY day DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("query tokens by day: %w", err)
	}

	tallies, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (TokenTally, error) {
		var t TokenTally
		return t, row.Scan(&t.Day, &t.Articles, &t.Input, &t.Output)
	})
	if err != nil {
		return nil, fmt.Errorf("read tokens by day: %w", err)
	}
	return tallies, nil
}

// ArticleStats is what happened to the articles themselves, after collection.
type ArticleStats struct {
	Total      int
	Analyzed   int
	Shown      int // belongs to at least one configured interest
	Hidden     int // matches none, so never appears
	NoText     int // the page could not be read: paywalled, or it refused us
	Archived   int // marked read
	AboveScore int // would clear the threshold on its own
}

// Articles reports the state of the library. interests and threshold are passed
// in because both live in configuration, and the database should not hold a
// second opinion about either.
func (s *Store) Articles(ctx context.Context, interests []string, threshold int) (ArticleStats, error) {
	var a ArticleStats
	err := s.pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE processed_at IS NOT NULL),
		       count(*) FILTER (WHERE categories && $1::text[]),
		       count(*) FILTER (WHERE processed_at IS NOT NULL AND NOT (categories && $1::text[])),
		       count(*) FILTER (WHERE full_text = ''),
		       count(*) FILTER (WHERE archived_at IS NOT NULL),
		       count(*) FILTER (WHERE score >= $2)
		FROM articles`, interests, threshold).
		Scan(&a.Total, &a.Analyzed, &a.Shown, &a.Hidden, &a.NoText, &a.Archived, &a.AboveScore)
	if err != nil {
		return a, fmt.Errorf("query article stats: %w", err)
	}
	return a, nil
}
