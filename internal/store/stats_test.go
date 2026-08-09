package store

import (
	"context"
	"testing"
	"time"

	"github.com/emaori/ziba/internal/domain"
)

// seedRawItems stores items and returns their ids in order.
func seedRawItems(t *testing.T, db *Store, items []domain.RawItem) []int64 {
	t.Helper()
	ctx := context.Background()

	if _, err := db.SaveRawItems(ctx, items); err != nil {
		t.Fatalf("save raw items: %v", err)
	}

	ids := make([]int64, 0, len(items))
	for _, item := range items {
		var id int64
		err := db.pool.QueryRow(ctx,
			`SELECT id FROM raw_items WHERE source_id = $1 AND url = $2`,
			item.SourceID, item.URL).Scan(&id)
		if err != nil {
			t.Fatalf("find raw item %s: %v", item.URL, err)
		}
		ids = append(ids, id)
	}
	return ids
}

// The counters have to agree with what actually happened, which is the whole
// point of recording outcomes: "processed" used to hide three different endings
// behind one timestamp and the totals did not reconcile.
func TestTalliesCountEveryOutcome(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	now := time.Now()

	source := seedSource(t, db, "feed", nil)
	ids := seedRawItems(t, db, []domain.RawItem{
		{SourceID: source, Kind: domain.ItemKindArticle, URL: "https://example.com/1", CollectedAt: now},
		{SourceID: source, Kind: domain.ItemKindArticle, URL: "https://example.com/2", CollectedAt: now},
		{SourceID: source, Kind: domain.ItemKindArticle, URL: "https://example.com/3", CollectedAt: now},
		{SourceID: source, Kind: domain.ItemKindArticle, URL: "https://example.com/4", CollectedAt: now},
		{SourceID: source, Kind: domain.ItemKindProvenance, URL: "imap://feed/one", CollectedAt: now},
		{SourceID: source, Kind: domain.ItemKindRoundup, URL: "https://example.com/week-1", CollectedAt: now},
	})

	err := db.MarkRawItemsProcessed(ctx, map[domain.Outcome][]int64{
		domain.OutcomeStored:    {ids[0], ids[1]},
		domain.OutcomeDuplicate: {ids[2]},
		domain.OutcomeSkipped:   {ids[3]},
		domain.OutcomeExpanded:  {ids[5]},
	})
	if err != nil {
		t.Fatalf("MarkRawItemsProcessed: %v", err)
	}

	bySource, err := db.TalliesBySource(ctx)
	if err != nil {
		t.Fatalf("TalliesBySource: %v", err)
	}
	if len(bySource) != 1 {
		t.Fatalf("got %d source rows, want 1", len(bySource))
	}

	got := bySource[0]
	want := Tally{
		Source: "feed", Provenance: 1, Roundups: 1, Links: 4,
		Stored: 2, Duplicate: 1, Skipped: 1,
	}
	if got.Provenance != want.Provenance || got.Roundups != want.Roundups || got.Links != want.Links {
		t.Errorf("kinds = provenance %d, roundups %d, links %d; want 1, 1, 4",
			got.Provenance, got.Roundups, got.Links)
	}
	if got.Stored != 2 || got.Duplicate != 1 || got.Skipped != 1 {
		t.Errorf("outcomes = stored %d, duplicate %d, skipped %d; want 2, 1, 1",
			got.Stored, got.Duplicate, got.Skipped)
	}
	// Provenance is never processed and must not be counted as work waiting.
	if got.Pending != 0 {
		t.Errorf("pending = %d, want 0", got.Pending)
	}
	if got.Collected() != 6 {
		t.Errorf("collected = %d, want 6", got.Collected())
	}
	if got.Discarded() != 2 {
		t.Errorf("discarded = %d, want 2 (one duplicate, one skipped)", got.Discarded())
	}
}

// An item that has not been through the pipeline is pending; one that finished
// before outcomes were recorded is neither pending nor counted as an outcome,
// and must be visible as unknown rather than quietly lost.
func TestTalliesSeparatePendingFromUnknown(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	now := time.Now()

	source := seedSource(t, db, "feed", nil)
	ids := seedRawItems(t, db, []domain.RawItem{
		{SourceID: source, Kind: domain.ItemKindArticle, URL: "https://example.com/waiting", CollectedAt: now},
		{SourceID: source, Kind: domain.ItemKindArticle, URL: "https://example.com/old", CollectedAt: now},
	})

	// An old row: processed, but from before the outcome column existed.
	if _, err := db.pool.Exec(ctx,
		`UPDATE raw_items SET processed_at = now() WHERE id = $1`, ids[1]); err != nil {
		t.Fatalf("age a row: %v", err)
	}

	bySource, err := db.TalliesBySource(ctx)
	if err != nil {
		t.Fatalf("TalliesBySource: %v", err)
	}
	got := bySource[0]

	if got.Pending != 1 {
		t.Errorf("pending = %d, want 1", got.Pending)
	}
	if got.Unknown != 1 {
		t.Errorf("unknown = %d, want 1", got.Unknown)
	}
	if got.Stored+got.Duplicate+got.Skipped != 0 {
		t.Error("an outcome was counted for rows that have none")
	}
}

// The by-day table shows a total per day and the sources beneath it. The two
// levels come from one query folded in Go, so the test that matters is that
// they agree.
func TestTalliesByDayAddUpToTheirSources(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()

	today := time.Now()
	yesterday := today.AddDate(0, 0, -1)

	first := seedSource(t, db, "first", nil)
	second := seedSource(t, db, "second", nil)

	seedRawItems(t, db, []domain.RawItem{
		{SourceID: first, Kind: domain.ItemKindArticle, URL: "https://example.com/a", CollectedAt: today},
		{SourceID: first, Kind: domain.ItemKindArticle, URL: "https://example.com/b", CollectedAt: today},
		{SourceID: second, Kind: domain.ItemKindArticle, URL: "https://example.com/c", CollectedAt: today},
		{SourceID: second, Kind: domain.ItemKindArticle, URL: "https://example.com/d", CollectedAt: yesterday},
	})

	days, err := db.TalliesByDay(ctx, 30)
	if err != nil {
		t.Fatalf("TalliesByDay: %v", err)
	}
	if len(days) != 2 {
		t.Fatalf("got %d days, want 2", len(days))
	}

	// Newest first.
	if !days[0].Day.After(days[1].Day) {
		t.Error("days are not in newest-first order")
	}

	if got := days[0].Collected(); got != 3 {
		t.Errorf("today collected %d, want 3", got)
	}
	if len(days[0].Sources) != 2 {
		t.Errorf("today has %d source rows, want 2", len(days[0].Sources))
	}
	if len(days[1].Sources) != 1 {
		t.Errorf("yesterday has %d source rows, want 1", len(days[1].Sources))
	}

	// The invariant worth protecting: a total is the sum of its rows.
	for _, day := range days {
		sum := 0
		for _, source := range day.Sources {
			sum += source.Collected()
		}
		if sum != day.Collected() {
			t.Errorf("%s: total %d but its sources add to %d",
				day.Day.Format(time.DateOnly), day.Collected(), sum)
		}
	}

	// The limit counts days, not rows: one day back must yield only today.
	oneDay, err := db.TalliesByDay(ctx, 1)
	if err != nil {
		t.Fatalf("TalliesByDay: %v", err)
	}
	if len(oneDay) != 1 {
		t.Errorf("limit 1 returned %d days, want 1", len(oneDay))
	}
}

// The library figures drive the headline numbers on the statistics page.
func TestArticleStats(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	now := time.Now()

	source := seedSource(t, db, "feed", nil)
	seedArticle(t, db, source, "https://example.com/shown", 80, []string{".NET"}, now)
	seedArticle(t, db, source, "https://example.com/hidden", 80, []string{"Uncategorized"}, now)
	seedArticle(t, db, source, "https://example.com/weak", 10, []string{".NET"}, now)

	// One with no text and one marked read.
	if _, err := db.pool.Exec(ctx,
		`UPDATE articles SET full_text = '' WHERE url = 'https://example.com/weak'`); err != nil {
		t.Fatalf("empty an article: %v", err)
	}
	if err := db.SetArchived(ctx, mustID(t, db, "https://example.com/shown"), true); err != nil {
		t.Fatalf("archive: %v", err)
	}

	got, err := db.Articles(ctx, []string{".NET"}, 60)
	if err != nil {
		t.Fatalf("Articles: %v", err)
	}

	for _, c := range []struct {
		name      string
		got, want int
	}{
		{"total", got.Total, 3},
		{"analyzed", got.Analyzed, 3},
		{"shown", got.Shown, 2},
		{"hidden", got.Hidden, 1},
		{"no text", got.NoText, 1},
		{"archived", got.Archived, 1},
		{"above score", got.AboveScore, 2},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}

func mustID(t *testing.T, db *Store, url string) int64 {
	t.Helper()
	var id int64
	if err := db.pool.QueryRow(context.Background(),
		`SELECT id FROM articles WHERE url = $1`, url).Scan(&id); err != nil {
		t.Fatalf("find article %s: %v", url, err)
	}
	return id
}

// The date picker needs the span worth offering and the nearest populated day
// on each side; stepping through empty days is not navigation.
func TestDayNavigation(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()

	day := func(d int) time.Time {
		return time.Date(2026, 8, d, 12, 0, 0, 0, time.UTC)
	}
	source := seedSource(t, db, "feed", nil)
	for _, d := range []int{1, 5, 9} {
		seedArticle(t, db, source,
			"https://example.com/"+time.Month(8).String()+"-"+day(d).Format("02"),
			80, []string{".NET"}, day(d))
	}

	nav, err := db.DayNavigation(ctx, "", day(5), []string{".NET"})
	if err != nil {
		t.Fatalf("DayNavigation: %v", err)
	}
	if !nav.First.Equal(day(1).Truncate(24*time.Hour)) && nav.First.Format(time.DateOnly) != "2026-08-01" {
		t.Errorf("First = %v, want 2026-08-01", nav.First)
	}
	if nav.Last.Format(time.DateOnly) != "2026-08-09" {
		t.Errorf("Last = %v, want 2026-08-09", nav.Last)
	}
	if nav.Prev.Format(time.DateOnly) != "2026-08-01" {
		t.Errorf("Prev = %v, want 2026-08-01, skipping the empty days", nav.Prev)
	}
	if nav.Next.Format(time.DateOnly) != "2026-08-09" {
		t.Errorf("Next = %v, want 2026-08-09", nav.Next)
	}

	// At the newest day there is nowhere further forward.
	nav, err = db.DayNavigation(ctx, "", day(9), []string{".NET"})
	if err != nil {
		t.Fatalf("DayNavigation: %v", err)
	}
	if !nav.Next.IsZero() {
		t.Errorf("Next = %v at the newest day, want nothing", nav.Next)
	}
	if nav.Prev.Format(time.DateOnly) != "2026-08-05" {
		t.Errorf("Prev = %v, want 2026-08-05", nav.Prev)
	}
}
