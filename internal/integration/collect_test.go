//go:build integration

package integration

import (
	"testing"

	"github.com/emaori/ziba/internal/domain"
)

// TestEverySourceIsRead is the first question: does each configured source
// actually produce articles?
//
// A source that silently yields nothing is the worst failure mode here, because
// everything downstream still looks healthy — the digest is just quietly poorer
// than it should be. So this asserts per source, not in aggregate.
func TestEverySourceIsRead(t *testing.T) {
	h := sharedHarness(t)
	result := h.lastCollect

	if result.Failed > 0 {
		t.Errorf("%s failed during collection — see the warnings above", plural(result.Failed, "source"))
	}

	counts := h.countBySource(t)
	for _, src := range h.enabledSources() {
		n := counts[src.Name]
		switch {
		case n == 0:
			t.Errorf("source %q produced no articles at all", src.Name)
		case n < 5:
			// Not a failure — a quiet blog legitimately has few posts — but
			// worth surfacing, because it is also what a half-broken selector
			// looks like.
			t.Logf("source %q produced only %s: check whether that is genuine", src.Name, plural(n, "article"))
		default:
			t.Logf("source %-28s %s", src.Name, plural(n, "article"))
		}
	}
}

// TestArticlesAreStoredOnce covers the identity rule from three angles, because
// duplicates can arrive three different ways.
func TestArticlesAreStoredOnce(t *testing.T) {
	h := sharedHarness(t)

	if h.lastCollect.New == 0 {
		t.Fatal("collected nothing — cannot test deduplication")
	}
	afterFirst := h.scalar(t, `SELECT count(*) FROM articles`)

	// 1. The database itself must not hold two rows for one address. This is
	//    enforced by a UNIQUE constraint, so a failure here means the
	//    constraint is gone.
	if dupes := h.scalar(t, `
		SELECT count(*) FROM (
			SELECT url FROM articles GROUP BY url HAVING count(*) > 1
		) d`); dupes != 0 {
		t.Errorf("%s stored under more than one row", plural(dupes, "address"))
	}

	// 2. Collecting again must add nothing. Feeds republish their whole window
	//    on every poll, so this is the property that keeps a nightly run from
	//    growing the archive without bound.
	second := h.collectAll(t)
	afterSecond := h.scalar(t, `SELECT count(*) FROM articles`)

	if afterSecond != afterFirst {
		t.Errorf("second collection added %d articles (%d → %d); collection is not idempotent",
			afterSecond-afterFirst, afterFirst, afterSecond)
	}
	if second.New != 0 {
		t.Logf("second run reported %s new — acceptable only if the feeds genuinely published in between",
			plural(second.New, "item"))
	}

	// 3. Addresses must be stored normalized. An unnormalized one would compare
	//    unequal to its clean twin and slip past the constraint.
	if raw := h.scalar(t, `
		SELECT count(*) FROM articles
		WHERE url LIKE '%utm_%' OR url LIKE '%?fbclid%'
		   OR url LIKE 'https://www.%' OR url LIKE '%#%'`); raw != 0 {
		t.Errorf("%s stored unnormalized — deduplication cannot work reliably", plural(raw, "address"))
	}

	t.Logf("%s stored, no duplicates across two collection runs", plural(afterSecond, "article"))
}

// TestArticlesHaveText checks the step between collection and analysis: a link
// is only useful once its text has been retrieved.
func TestArticlesHaveText(t *testing.T) {
	h := sharedHarness(t)

	total := h.scalar(t, `SELECT count(*) FROM articles`)
	if total == 0 {
		t.Fatal("no articles collected")
	}

	// Some retrieval failures are normal — publishers block automated readers,
	// and those articles keep the feed's excerpt. A large share is not normal.
	thin := h.scalar(t, `SELECT count(*) FROM articles WHERE length(full_text) < 500`)
	if share := float64(thin) / float64(total); share > 0.35 {
		t.Errorf("%d of %d articles have almost no text (%.0f%%) — retrieval is failing widely",
			thin, total, share*100)
	} else {
		t.Logf("%d of %d articles are short (%.0f%%) — expected, publishers block readers",
			thin, total, share*100)
	}

	// One unbroken run of text is the bug that made the reader unusable once
	// before. It must not come back.
	if blobs := h.scalar(t, `
		SELECT count(*) FROM articles
		WHERE length(full_text) > 2000 AND position(E'\n' in full_text) = 0`); blobs != 0 {
		t.Errorf("%s stored as a single unbroken paragraph — the reader will be unreadable",
			plural(blobs, "article"))
	}
}

// TestArticlesAreClassified checks that analysis actually labelled things, and
// that the labels correspond to the configured interests rather than to
// whatever the model felt like inventing.
func TestArticlesAreClassified(t *testing.T) {
	h := sharedHarness(t)

	analyzed, above, failed, err := h.runner.Analyze(t.Context(), 500)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if failed > 0 {
		t.Errorf("%s failed analysis", plural(failed, "article"))
	}
	if analyzed == 0 {
		t.Fatal("nothing was analyzed")
	}
	t.Logf("analyzed %d articles, %d above threshold %d", analyzed, above, h.interests.Threshold)

	if unlabelled := h.scalar(t, `
		SELECT count(*) FROM articles
		WHERE processed_at IS NOT NULL AND cardinality(categories) = 0`); unlabelled != 0 {
		t.Errorf("%s analyzed but given no category", plural(unlabelled, "article"))
	}

	if unscored := h.scalar(t, `
		SELECT count(*) FROM articles WHERE processed_at IS NOT NULL AND score IS NULL`); unscored != 0 {
		t.Errorf("%s analyzed but left unscored", plural(unscored, "article"))
	}

	// Every score must carry its reason. A ranking you cannot argue with is a
	// ranking you cannot debug.
	if unexplained := h.scalar(t, `
		SELECT count(*) FROM articles
		WHERE processed_at IS NOT NULL AND score_reason = ''`); unexplained != 0 {
		t.Errorf("%s scored without a stated reason", plural(unexplained, "article"))
	}

	reportCategories(t, h)
	assertPlausibleSelection(t, analyzed, above, h.realAnalyzer)
	assertGeneralNewsIsNotAllTechnology(t, h)
}

// assertPlausibleSelection checks that the threshold is doing something.
//
// The obvious failure is that nothing is selected. The failure this suite
// originally missed is the opposite: on the first real run, 393 of 411 articles
// cleared the threshold, and the tests reported success. A daily selection
// containing almost the whole archive is not curation, and it is exactly what a
// broken classifier looks like from the outside — so both ends are failures.
func assertPlausibleSelection(t *testing.T, analyzed, above int, realAnalyzer bool) {
	t.Helper()

	share := float64(above) / float64(analyzed)
	t.Logf("%d of %d articles above threshold (%.0f%%)", above, analyzed, share*100)

	// An empty selection is a failure whichever analyzer ran: it means nothing
	// connects the sources to the interests.
	if above == 0 {
		t.Errorf("not one article of %d reached the threshold — the interests and the sources disagree", analyzed)
		return
	}

	// The upper bound only binds the real model. The offline analyzer scores by
	// which interest matched and nothing else, so every article from an
	// on-topic source lands on the same number and clears the threshold
	// together. Holding keyword matching to a curation standard would be
	// testing the wrong thing — but the number is still worth printing.
	if !realAnalyzer {
		if share > 0.60 {
			t.Logf("NOTE: %.0f%% cleared the threshold. Expected from the offline analyzer, "+
				"which cannot grade within a topic — but it means this run says nothing "+
				"about curation quality", share*100)
		}
		return
	}

	switch {
	case share > 0.60:
		t.Errorf("%.0f%% of articles cleared the threshold: that is an archive, not a selection. "+
			"Either the model is scoring too generously or the threshold is too low", share*100)
	case share > 0.35:
		t.Logf("%.0f%% cleared the threshold — high for a daily magazine; worth a look", share*100)
	}
}

// assertGeneralNewsIsNotAllTechnology is a sanity check with real teeth.
//
// Il Post is an Italian general-interest newspaper: politics, society, sport,
// culture. A handful of its articles being about technology is right; most of
// them being so means the classifier is matching noise. This is the check that
// would have caught the substring bug immediately — it had 69 of 84 Il Post
// articles filed under "AI", because "ai" appears inside "mai" and "assai".
func assertGeneralNewsIsNotAllTechnology(t *testing.T, h *harness) {
	t.Helper()

	const generalNews = "Il Post"

	total := h.scalar(t, `
		SELECT count(*) FROM articles a JOIN sources s ON s.id = a.source_id
		WHERE s.name = $1 AND a.processed_at IS NOT NULL`, generalNews)
	if total == 0 {
		t.Skipf("%s produced no articles; skipping the sanity check", generalNews)
	}

	tech := h.scalar(t, `
		SELECT count(*) FROM articles a JOIN sources s ON s.id = a.source_id
		WHERE s.name = $1 AND a.processed_at IS NOT NULL
		  AND ('AI' = ANY(a.categories) OR '.NET' = ANY(a.categories))`, generalNews)

	share := float64(tech) / float64(total)
	t.Logf("%s: %d of %d filed under AI or .NET (%.0f%%)", generalNews, tech, total, share*100)

	if share > 0.40 {
		t.Errorf("%.0f%% of a general-news source was classified as AI or .NET — "+
			"the classifier is matching noise, not subject matter", share*100)
	}
}

// reportCategories prints what the analyzer actually produced, and flags
// categories that bear no relation to the configured interests.
func reportCategories(t *testing.T, h *harness) {
	t.Helper()

	rows, err := h.store.Pool().Query(t.Context(), `
		SELECT category, count(*) FROM articles, unnest(categories) AS category
		WHERE processed_at IS NOT NULL
		GROUP BY category ORDER BY count(*) DESC LIMIT 25`)
	if err != nil {
		t.Fatalf("read categories: %v", err)
	}
	defer rows.Close()

	t.Log("categories produced:")
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			t.Fatalf("scan category: %v", err)
		}
		t.Logf("   %-42s %s", name, plural(n, "article"))
	}
}

// TestSummariesExistAboveThreshold checks the rule the whole cost model rests
// on: summarized above the threshold, not summarized below it.
func TestSummariesExistAboveThreshold(t *testing.T) {
	h := sharedHarness(t)

	if _, above, _, err := h.runner.Analyze(t.Context(), 500); err != nil {
		t.Fatalf("analyze: %v", err)
	} else if above == 0 {
		t.Skip("nothing reached the threshold, so there is nothing to summarize")
	}

	threshold := h.interests.Threshold

	missing := h.scalar(t, `
		SELECT count(*) FROM articles
		WHERE processed_at IS NOT NULL AND score >= $1 AND summary = ''`, threshold)
	if missing != 0 {
		t.Errorf("%s above the threshold has no summary", plural(missing, "article"))
	}

	// The other direction is the expensive one to get wrong: a summary below
	// the threshold means we paid the capable model for something nobody reads.
	wasted := h.scalar(t, `
		SELECT count(*) FROM articles
		WHERE processed_at IS NOT NULL AND score < $1 AND summary <> ''`, threshold)
	if wasted != 0 {
		t.Errorf("%s below the threshold was summarized anyway — that is money spent for nothing",
			plural(wasted, "article"))
	}

	if !h.realAnalyzer {
		t.Log("summaries came from the offline analyzer; their usefulness is not under test")
		return
	}

	// With a real model, a summary that is too short to be a summary is a
	// failure the structural checks above would miss.
	if stub := h.scalar(t, `
		SELECT count(*) FROM articles
		WHERE processed_at IS NOT NULL AND summary <> '' AND length(summary) < 80`); stub != 0 {
		t.Errorf("%s has a suspiciously short summary", plural(stub, "article"))
	}

	showTop(t, h)
}

// showTop prints the highest-scoring articles with their summaries. No
// assertion: this is for a person to read and judge, which is the only way to
// find out whether the curation is any good.
func showTop(t *testing.T, h *harness) {
	t.Helper()

	rows, err := h.store.Pool().Query(t.Context(), `
		SELECT title, score, score_reason, left(summary, 240)
		FROM articles WHERE processed_at IS NOT NULL
		ORDER BY score DESC LIMIT 10`)
	if err != nil {
		t.Fatalf("read top articles: %v", err)
	}
	defer rows.Close()

	t.Log("highest scoring — read these and judge for yourself:")
	for rows.Next() {
		var title, reason, summary string
		var score domain.RelevanceScore
		if err := rows.Scan(&title, &score, &reason, &summary); err != nil {
			t.Fatalf("scan article: %v", err)
		}
		t.Logf("  [%3d] %s\n        why: %s\n        %s", score, title, reason, summary)
	}
}

// TestDigestIsBuiltFromRealData closes the loop: everything above, ending in a
// selection a person could read.
func TestDigestIsBuiltFromRealData(t *testing.T) {
	h := sharedHarness(t)

	if _, _, _, err := h.runner.Analyze(t.Context(), 500); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	selected, err := h.runner.Digest(t.Context(), timeNow())
	if err != nil {
		t.Fatalf("build digest: %v", err)
	}
	if selected == 0 {
		t.Fatal("the digest is empty — nothing cleared the threshold")
	}

	digest, err := h.store.LatestDigest(t.Context())
	if err != nil {
		t.Fatalf("read digest: %v", err)
	}
	if len(digest.Articles) != selected {
		t.Errorf("digest reports %d articles but stored %d", selected, len(digest.Articles))
	}

	// The ranking is the product. If it is not in score order, the front page
	// is wrong however good the analysis was.
	for i := 1; i < len(digest.Articles); i++ {
		if digest.Articles[i].Score > digest.Articles[i-1].Score {
			t.Errorf("digest is not ordered by score: position %d (%d) outranks position %d (%d)",
				i+1, digest.Articles[i].Score, i, digest.Articles[i-1].Score)
			break
		}
	}

	// Every article in the digest must carry what the card needs to render.
	for _, a := range digest.Articles {
		if a.Title == "" || a.SourceName == "" {
			t.Errorf("article %d is missing a title or a source name", a.ID)
		}
	}

	t.Logf("digest holds %s from %s",
		plural(len(digest.Articles), "article"), plural(len(h.enabledSources()), "source"))
}
