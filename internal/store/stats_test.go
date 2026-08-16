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

func TestAnalysisFailuresAreDeferredThenBecomeTerminal(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	source := seedSource(t, db, "feed", nil)

	id, _, err := db.SaveArticle(ctx, domain.Article{
		SourceID: source, URL: "https://example.com/broken", Title: "Broken",
		CollectedAt: time.Now(), FullText: "text",
	})
	if err != nil {
		t.Fatalf("SaveArticle: %v", err)
	}

	runStarted := time.Now()
	if err := db.RecordAnalysisFailure(ctx, id, "temporary failure", 4); err != nil {
		t.Fatalf("first failure: %v", err)
	}
	if eligible, err := db.UnanalyzedArticlesBefore(ctx, 10, runStarted); err != nil {
		t.Fatalf("UnanalyzedArticlesBefore: %v", err)
	} else if len(eligible) != 0 {
		t.Errorf("failed article was retried in the same run: %v", eligible)
	}
	if eligible, err := db.UnanalyzedArticles(ctx, 10); err != nil {
		t.Fatalf("UnanalyzedArticles: %v", err)
	} else if len(eligible) != 1 {
		t.Errorf("article is not eligible on the next run: %v", eligible)
	}

	for attempt := 2; attempt <= 4; attempt++ {
		if err := db.RecordAnalysisFailure(ctx, id, "still broken", 4); err != nil {
			t.Fatalf("failure %d: %v", attempt, err)
		}
	}

	var failures int
	var failedAt *time.Time
	if err := db.pool.QueryRow(ctx,
		`SELECT failure_count, failed_at FROM articles WHERE id = $1`, id).
		Scan(&failures, &failedAt); err != nil {
		t.Fatalf("read retry state: %v", err)
	}
	if failures != 4 || failedAt == nil {
		t.Errorf("failures = %d, failed_at = %v; want 4 and a terminal timestamp", failures, failedAt)
	}

	backlogs, err := db.Backlogs(ctx)
	if err != nil {
		t.Fatalf("Backlogs: %v", err)
	}
	if got := backlogs[2]; got.Stage != "Analysis" || got.Pending != 0 || got.Failed != 1 {
		t.Errorf("analysis backlog = %+v, want one terminal failure and nothing pending", got)
	}
}

func TestRoundupFailuresBecomeTerminal(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	source := seedSource(t, db, "feed", nil)
	ids := seedRawItems(t, db, []domain.RawItem{{
		SourceID: source, Kind: domain.ItemKindRoundup,
		URL: "https://example.com/roundup", CollectedAt: time.Now(),
	}})

	for attempt := 1; attempt <= 4; attempt++ {
		if err := db.RecordRawItemFailure(ctx, ids[0], "unavailable", 4); err != nil {
			t.Fatalf("failure %d: %v", attempt, err)
		}
	}

	if eligible, err := db.UnexpandedRoundups(ctx, 10); err != nil {
		t.Fatalf("UnexpandedRoundups: %v", err)
	} else if len(eligible) != 0 {
		t.Errorf("terminal roundup is still eligible: %v", eligible)
	}
	backlogs, err := db.Backlogs(ctx)
	if err != nil {
		t.Fatalf("Backlogs: %v", err)
	}
	if got := backlogs[0]; got.Failed != 1 || got.Pending != 0 {
		t.Errorf("roundup backlog = %+v, want one terminal failure", got)
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

// The token figures are three different questions over one column pair, and the
// interesting one is the interest breakdown: an article in two interests must
// count towards both, so the rows deliberately add up to more than the total.
func TestTokenTallies(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()

	source, err := db.SyncSources(ctx, []domain.Source{{
		Name: "Feed", Type: domain.SourceTypeRSS, URL: "https://example.com/feed", Enabled: true,
	}})
	if err != nil {
		t.Fatalf("sync sources: %v", err)
	}

	analyzed := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	articles := []struct {
		url           string
		categories    []string
		in, out       int
		processed     bool
		processedWhen time.Time
	}{
		{"https://example.com/1", []string{"AI"}, 1000, 100, true, analyzed},
		{"https://example.com/2", []string{"AI", "Computer Science"}, 2000, 200, true, analyzed},
		{"https://example.com/3", []string{"Computer Science"}, 500, 50, true, analyzed.AddDate(0, 0, -1)},
		// Analyzed offline: no tokens, but it is analyzed and must be counted.
		{"https://example.com/4", []string{"AI"}, 0, 0, true, analyzed},
		// Never analyzed: must not be counted at all, or it drags the averages.
		{"https://example.com/5", nil, 0, 0, false, time.Time{}},
	}
	for _, a := range articles {
		article := domain.Article{
			SourceID: source[0].ID, URL: a.url, Title: "T", CollectedAt: analyzed,
			FullText: "text",
		}
		id, _, err := db.SaveArticle(ctx, article)
		if err != nil {
			t.Fatalf("save %s: %v", a.url, err)
		}
		if !a.processed {
			continue
		}
		article.ID, article.Categories = id, a.categories
		article.Entities, article.Tone = []string{}, "analysis"
		article.InputTokens, article.OutputTokens = a.in, a.out
		article.AnalyzedAt = a.processedWhen
		if err := db.SaveAnalysis(ctx, article); err != nil {
			t.Fatalf("save analysis for %s: %v", a.url, err)
		}
	}

	total, err := db.Tokens(ctx)
	if err != nil {
		t.Fatalf("Tokens: %v", err)
	}
	if total.Articles != 4 {
		t.Errorf("articles = %d, want 4: the unanalyzed one is not counted", total.Articles)
	}
	if total.Input != 3500 || total.Output != 350 {
		t.Errorf("total = %d in / %d out, want 3500 / 350", total.Input, total.Output)
	}
	if got := total.PerArticle(); got != 962 {
		t.Errorf("per article = %d, want 962 (3850 over 4)", got)
	}

	byInterest, err := db.TokensByInterest(ctx, []string{"AI", "Computer Science"})
	if err != nil {
		t.Fatalf("TokensByInterest: %v", err)
	}
	if len(byInterest) != 2 {
		t.Fatalf("got %d interests, want 2", len(byInterest))
	}
	// Costliest first, and article 2 counts towards both.
	if byInterest[0].Label != "AI" || byInterest[0].Articles != 3 || byInterest[0].Input != 3000 {
		t.Errorf("AI row = %+v, want 3 articles and 3000 in", byInterest[0])
	}
	if byInterest[1].Label != "Computer Science" || byInterest[1].Articles != 2 || byInterest[1].Input != 2500 {
		t.Errorf("Computer Science row = %+v, want 2 articles and 2500 in", byInterest[1])
	}

	byDay, err := db.TokensByDay(ctx, 30)
	if err != nil {
		t.Fatalf("TokensByDay: %v", err)
	}
	if len(byDay) != 2 {
		t.Fatalf("got %d days, want 2", len(byDay))
	}
	// Newest first, and grouped on the day the model ran rather than the day
	// everything was collected — all five share a collection date.
	if byDay[0].Articles != 3 || byDay[0].Input != 3000 {
		t.Errorf("newest day = %+v, want 3 articles and 3000 in", byDay[0])
	}
	if byDay[1].Articles != 1 || byDay[1].Input != 500 {
		t.Errorf("older day = %+v, want 1 article and 500 in", byDay[1])
	}
}
