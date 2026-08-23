package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/emaori/ziba/internal/domain"
)

// SyncSources makes the sources table match the configured list and returns the
// sources with their database IDs filled in.
//
// PostgreSQL is the source of truth. This keeps IDs and collection metadata
// stable while filling the runner's snapshot with database IDs.
func (s *Store) SyncSources(ctx context.Context, configured []domain.Source) ([]domain.Source, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	synced := make([]domain.Source, 0, len(configured))
	keep := make([]int64, 0, len(configured))

	for _, src := range configured {
		// A nil slice is sent as NULL, and the column is NOT NULL. "Declares
		// nothing" is an empty list, not an absent one.
		categories := src.Categories
		if categories == nil {
			categories = []string{}
		}

		// (type, url) is the natural key: it is what the user actually edits.
		row := tx.QueryRow(ctx, `
			INSERT INTO sources (name, type, url, enabled, categories, roundup, browser_fetch, collect_from)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (type, url) DO UPDATE
			SET name = EXCLUDED.name, enabled = EXCLUDED.enabled,
			    categories = EXCLUDED.categories,
			    roundup = EXCLUDED.roundup,
			    browser_fetch = EXCLUDED.browser_fetch,
			    collect_from = EXCLUDED.collect_from
			RETURNING id, created_at`,
			src.Name, string(src.Type), src.URL, src.Enabled, categories,
			src.Roundup, src.BrowserFetch, formatCollectFrom(src.CollectFrom))

		// created_at is deliberately not in the UPDATE list: it records when the
		// source was first seen, and CollectFrom anchors to it.
		if err := row.Scan(&src.ID, &src.CreatedAt); err != nil {
			return nil, fmt.Errorf("upsert source %q: %w", src.Name, err)
		}
		synced = append(synced, src)
		keep = append(keep, src.ID)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE sources SET enabled = FALSE WHERE enabled AND NOT (id = ANY($1))`,
		keep); err != nil {
		return nil, fmt.Errorf("disable removed sources: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit source sync: %w", err)
	}
	return synced, nil
}

// SaveRawItems stores freshly collected items and reports how many were new.
// Items already collected from the same source are ignored, which is what makes
// re-running collection harmless: a feed republishes its whole window every
// time it is polled.
func (s *Store) SaveRawItems(ctx context.Context, items []domain.RawItem) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}

	batch := &pgx.Batch{}
	for _, item := range items {
		kind := item.Kind
		if kind == "" {
			kind = domain.ItemKindArticle
		}
		batch.Queue(`
			INSERT INTO raw_items (source_id, title, url, author, published_at, collected_at, text, kind)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (source_id, url) DO NOTHING`,
			item.SourceID, item.Title, item.URL, item.Author,
			nullableTime(item.PublishedAt), item.CollectedAt, item.Text, string(kind))
	}

	// One round trip for the whole batch instead of one per item.
	results := s.pool.SendBatch(ctx, batch)
	defer results.Close()

	inserted := 0
	for range items {
		tag, err := results.Exec()
		if err != nil {
			return inserted, fmt.Errorf("insert raw item: %w", err)
		}
		inserted += int(tag.RowsAffected())
	}
	return inserted, nil
}

// UnprocessedRawItems returns items that have not yet become articles, oldest
// first, up to limit.
//
// Provenance items are excluded: a newsletter is kept so that a link has an
// origin, but it is not itself reading material and must never reach the
// archive as an article.
func (s *Store) UnprocessedRawItems(ctx context.Context, limit int) ([]domain.RawItem, error) {
	return s.queueOf(ctx, domain.ItemKindArticle, limit, time.Now())
}

// UnexpandedRoundups returns the collected issues of link digests that have not
// yet been opened, oldest first, up to limit.
//
// They queue separately from articles because they are drained by a different
// stage: opening one produces articles rather than consuming them.
func (s *Store) UnexpandedRoundups(ctx context.Context, limit int) ([]domain.RawItem, error) {
	return s.UnexpandedRoundupsBefore(ctx, limit, time.Now())
}

// UnexpandedRoundupsBefore excludes issues attempted during the current drain.
func (s *Store) UnexpandedRoundupsBefore(ctx context.Context, limit int, before time.Time) ([]domain.RawItem, error) {
	return s.queueOf(ctx, domain.ItemKindRoundup, limit, before)
}

// queueOf reads one kind's backlog.
//
// The kind is written into the statement rather than bound as a parameter, and
// deliberately so: each queue has a partial index that names the kind as a
// constant, and an index like that is only usable when the query names it as a
// constant too. The value comes from a package constant chosen here, never from
// input, so there is nothing to inject.
func (s *Store) queueOf(ctx context.Context, kind domain.ItemKind, limit int, before time.Time) ([]domain.RawItem, error) {
	var query string
	switch kind {
	case domain.ItemKindArticle:
		query = queueQuery("article")
	case domain.ItemKindRoundup:
		query = queueQuery("roundup")
	default:
		return nil, fmt.Errorf("no queue for item kind %q", kind)
	}

	rows, err := s.pool.Query(ctx, query, limit, before)
	if err != nil {
		return nil, fmt.Errorf("query unprocessed %s items: %w", kind, err)
	}

	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.RawItem, error) {
		item := domain.RawItem{Kind: kind}
		err := row.Scan(&item.ID, &item.SourceID, &item.Title, &item.URL, &item.Author,
			&item.PublishedAt, &item.CollectedAt, &item.Text)
		return item, err
	})
	if err != nil {
		return nil, fmt.Errorf("read unprocessed %s items: %w", kind, err)
	}
	return items, nil
}

// queueQuery builds the backlog statement for one kind. Only queueOf calls it,
// and only with a literal.
func queueQuery(kind string) string {
	return `
		SELECT id, source_id, title, url, author,
		       COALESCE(published_at, collected_at), collected_at, text
		FROM raw_items
		WHERE processed_at IS NULL
		  AND failed_at IS NULL
		  AND (last_attempt_at IS NULL OR last_attempt_at < $2)
		  AND kind = '` + kind + `'
		ORDER BY collected_at
		LIMIT $1`
}

// RecordRawItemFailure records a failed attempt and marks the item terminal
// after maxFailures.
func (s *Store) RecordRawItemFailure(ctx context.Context, id int64, message string, maxFailures int) error {
	return s.recordFailure(ctx, "raw_items", id, message, maxFailures)
}

// DeclaredCategories returns, per source id, the categories that source
// declares. Sources that declare none are absent from the map.
//
// The analysis stage needs this to assign categories instead of inferring them,
// and it comes from the database rather than the configuration because the two
// are kept in step by SyncSources — the file remains the truth, and this is a
// copy written from it on every run.
func (s *Store) DeclaredCategories(ctx context.Context) (map[int64][]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, categories FROM sources WHERE cardinality(categories) > 0`)
	if err != nil {
		return nil, fmt.Errorf("query declared categories: %w", err)
	}
	defer rows.Close()

	declared := make(map[int64][]string)
	for rows.Next() {
		var id int64
		var categories []string
		if err := rows.Scan(&id, &categories); err != nil {
			return nil, fmt.Errorf("read declared categories: %w", err)
		}
		declared[id] = categories
	}
	return declared, rows.Err()
}

// SaveArticle stores an article and reports whether it was new. An article
// already known under the same normalized URL is left untouched: identity is
// the URL, so this is the same article arriving from a second source.
func (s *Store) SaveArticle(ctx context.Context, a domain.Article) (id int64, created bool, err error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO articles (source_id, url, title, author, published_at, collected_at, full_text)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (url) DO NOTHING
		RETURNING id`,
		a.SourceID, a.URL, a.Title, a.Author,
		nullableTime(a.PublishedAt), a.CollectedAt, a.FullText)

	switch err := row.Scan(&id); {
	case err == nil:
		return id, true, nil
	case errors.Is(err, pgx.ErrNoRows):
		// DO NOTHING returns no row; look up the article that won.
		if err := s.pool.QueryRow(ctx, `SELECT id FROM articles WHERE url = $1`, a.URL).Scan(&id); err != nil {
			return 0, false, fmt.Errorf("find existing article %s: %w", a.URL, err)
		}
		return id, false, nil
	default:
		return 0, false, fmt.Errorf("insert article %s: %w", a.URL, err)
	}
}

// MarkRawItemsProcessed records that the given items have been turned into
// articles, so the next run skips them.
func (s *Store) MarkRawItemsProcessed(ctx context.Context, finished map[domain.Outcome][]int64) error {
	batch := &pgx.Batch{}
	for outcome, ids := range finished {
		if len(ids) == 0 {
			continue
		}
		batch.Queue(`UPDATE raw_items
		             SET processed_at = now(), outcome = $2,
		                 failure_count = 0, last_attempt_at = NULL,
		                 last_error = '', failed_at = NULL
		             WHERE id = ANY($1)`, ids, string(outcome))
	}
	if batch.Len() == 0 {
		return nil
	}

	results := s.pool.SendBatch(ctx, batch)
	defer results.Close()
	for range batch.Len() {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("mark items processed: %w", err)
		}
	}
	return nil
}

// UnanalyzedArticles returns articles the AI pipeline has not seen yet, oldest
// first, up to limit.
func (s *Store) UnanalyzedArticles(ctx context.Context, limit int) ([]domain.Article, error) {
	return s.UnanalyzedArticlesBefore(ctx, limit, time.Now())
}

// UnanalyzedArticlesBefore excludes articles attempted during the current drain.
func (s *Store) UnanalyzedArticlesBefore(ctx context.Context, limit int, before time.Time) ([]domain.Article, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, source_id, url, title, author,
		       COALESCE(published_at, collected_at), collected_at, full_text
		FROM articles
		WHERE processed_at IS NULL
		  AND failed_at IS NULL
		  AND (last_attempt_at IS NULL OR last_attempt_at < $2)
		ORDER BY collected_at
		LIMIT $1`, limit, before)
	if err != nil {
		return nil, fmt.Errorf("query unanalyzed articles: %w", err)
	}

	articles, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Article, error) {
		var a domain.Article
		err := row.Scan(&a.ID, &a.SourceID, &a.URL, &a.Title, &a.Author,
			&a.PublishedAt, &a.CollectedAt, &a.FullText)
		return a, err
	})
	if err != nil {
		return nil, fmt.Errorf("read unanalyzed articles: %w", err)
	}
	return articles, nil
}

// RecordAnalysisFailure records a failed analysis and marks it terminal after
// maxFailures.
func (s *Store) RecordAnalysisFailure(ctx context.Context, id int64, message string, maxFailures int) error {
	return s.recordFailure(ctx, "articles", id, message, maxFailures)
}

func (s *Store) recordFailure(ctx context.Context, table string, id int64, message string, maxFailures int) error {
	if maxFailures < 1 {
		return fmt.Errorf("max failures must be positive")
	}
	var query string
	switch table {
	case "raw_items", "articles":
		query = `UPDATE ` + table + `
			SET failure_count = failure_count + 1,
			    last_attempt_at = now(),
			    last_error = $2,
			    failed_at = CASE WHEN failure_count + 1 >= $3 THEN now() ELSE NULL END
			WHERE id = $1`
	default:
		return fmt.Errorf("unsupported failure table %q", table)
	}

	tag, err := s.pool.Exec(ctx, query, id, message, maxFailures)
	if err != nil {
		return fmt.Errorf("record failure for %s %d: %w", table, id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("record failure: %s %d not found", table, id)
	}
	return nil
}

// SaveAnalysis stores what the pipeline produced for an article. Setting
// processed_at is what takes the article out of the backlog, so it is written
// in the same statement as the results: either both land or neither does.
func (s *Store) SaveAnalysis(ctx context.Context, a domain.Article) error {
	quality := a.ContentQuality
	if quality == "" {
		// Compatibility for callers compiled against the older Article shape.
		// New pipeline results always carry an explicit value.
		quality = domain.ContentComplete
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE articles
		SET categories = $2, entities = $3, tone = $4,
		    summary = $5, content_quality = $6, content_quality_reason = $7,
		    score = $8, base_score = $9, score_reason = $10, processed_at = $11,
		    input_tokens = $12, output_tokens = $13,
		    failure_count = 0, last_attempt_at = NULL,
		    last_error = '', failed_at = NULL
		WHERE id = $1`,
		a.ID, a.Categories, a.Entities, a.Tone,
		a.Summary, quality, a.ContentQualityReason,
		int16(a.Score), nullableScore(a.BaseScore, a.BaseScoreSet), a.ScoreReason, a.AnalyzedAt,
		a.InputTokens, a.OutputTokens)
	if err != nil {
		return fmt.Errorf("save analysis for article %d: %w", a.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("save analysis: article %d not found", a.ID)
	}
	return nil
}

// CountArticles returns how many articles are stored. Used for reporting.
func (s *Store) CountArticles(ctx context.Context) (int64, error) {
	var n int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM articles`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count articles: %w", err)
	}
	return n, nil
}

// nullableTime maps the zero time to NULL: the column means "unknown", and the
// year 1 is not a date any article was published on.
func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func nullableScore(score domain.RelevanceScore, valid bool) any {
	if !valid {
		return nil
	}
	return int16(score)
}
