package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/emaori/ziba/internal/domain"
)

// articleColumns is the projection every reading query shares. Full text is
// deliberately absent: a list of thirty articles does not need thirty article
// bodies, and fetching them would dominate the query.
const articleColumns = `
	a.id, a.source_id, s.name, a.url, a.title, a.author,
	COALESCE(a.published_at, a.collected_at), a.collected_at,
	a.categories, a.entities, a.tone,
	a.summary, COALESCE(a.score, 0), a.score_reason`

func scanArticle(row pgx.CollectableRow) (domain.Article, error) {
	var a domain.Article
	err := row.Scan(&a.ID, &a.SourceID, &a.SourceName, &a.URL, &a.Title, &a.Author,
		&a.PublishedAt, &a.CollectedAt,
		&a.Categories, &a.Entities, &a.Tone,
		&a.Summary, &a.Score, &a.ScoreReason)
	return a, err
}

// GenerateDigest builds the selection for one day and stores it, replacing any
// previous selection for that date. It returns how many articles were selected.
//
// The ranking is computed in SQL rather than in Go: the database is already
// sorting the rows, so numbering them there avoids reading the whole set into
// memory only to write it straight back.
func (s *Store) GenerateDigest(ctx context.Context, date time.Time,
	threshold domain.RelevanceScore, interests []string) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var digestID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO digests (date, threshold)
		VALUES ($1::date, $2)
		ON CONFLICT (date) DO UPDATE
		SET threshold = EXCLUDED.threshold, generated_at = now()
		RETURNING id`, date, int16(threshold)).Scan(&digestID)
	if err != nil {
		return 0, fmt.Errorf("upsert digest: %w", err)
	}

	// Regenerating a day replaces its selection rather than adding to it.
	if _, err := tx.Exec(ctx, `DELETE FROM digest_articles WHERE digest_id = $1`, digestID); err != nil {
		return 0, fmt.Errorf("clear previous selection: %w", err)
	}

	tag, err := tx.Exec(ctx, `
		INSERT INTO digest_articles (digest_id, article_id, ordinal)
		SELECT $1, id, row_number() OVER (ORDER BY score DESC, published_at DESC NULLS LAST)
		FROM articles
		WHERE processed_at IS NOT NULL
		  AND archived_at IS NULL
		  AND score >= $2
		  AND collected_at::date = $3::date
		  -- An article matching none of the reader's interests is not shown
		  -- anywhere, so it does not belong in the day's selection either.
		  AND categories && $4::text[]`,
		digestID, int16(threshold), date, interests)
	if err != nil {
		return 0, fmt.Errorf("select digest articles: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit digest: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// HasDigest reports whether a selection has already been built for a day.
//
// The scheduler asks this on startup: a process that was down at the appointed
// time should still build the day's selection, but one that merely restarted
// afterwards must not rebuild a selection the reader may already be reading.
func (s *Store) HasDigest(ctx context.Context, date time.Time) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM digests WHERE date = $1::date)`, date).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check digest for %s: %w", date.Format(time.DateOnly), err)
	}
	return exists, nil
}

// LatestDigest returns the most recent stored digest with its articles in rank
// order. It reports pgx.ErrNoRows when none has been generated yet.
func (s *Store) LatestDigest(ctx context.Context) (domain.Digest, error) {
	var digest domain.Digest
	var id int64

	err := s.pool.QueryRow(ctx,
		`SELECT id, date FROM digests ORDER BY date DESC LIMIT 1`).Scan(&id, &digest.Date)
	if err != nil {
		return domain.Digest{}, err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT`+articleColumns+`
		FROM digest_articles da
		JOIN articles a ON a.id = da.article_id
		JOIN sources s ON s.id = a.source_id
		WHERE da.digest_id = $1
		  -- Also filtered here, not only at generation: marking an article read
		  -- must take it off today's page now, not tomorrow.
		  AND a.archived_at IS NULL
		ORDER BY da.ordinal`, id)
	if err != nil {
		return domain.Digest{}, fmt.Errorf("query digest articles: %w", err)
	}

	digest.Articles, err = pgx.CollectRows(rows, scanArticle)
	if err != nil {
		return domain.Digest{}, fmt.Errorf("read digest articles: %w", err)
	}
	return digest, nil
}

// Article returns one article with its full text, for the reading view.
func (s *Store) Article(ctx context.Context, id int64) (domain.Article, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT`+articleColumns+`, a.full_text
		FROM articles a
		JOIN sources s ON s.id = a.source_id
		WHERE a.id = $1`, id)

	var a domain.Article
	err := row.Scan(&a.ID, &a.SourceID, &a.SourceName, &a.URL, &a.Title, &a.Author,
		&a.PublishedAt, &a.CollectedAt,
		&a.Categories, &a.Entities, &a.Tone,
		&a.Summary, &a.Score, &a.ScoreReason, &a.FullText)
	if err != nil {
		return domain.Article{}, err
	}
	return a, nil
}

// Archive returns every article that belongs to at least one of the reader's
// interests, newest first, whatever its score and whether or not it has been
// read.
//
// Articles matching no interest are excluded here as everywhere else. They are
// still collected and stored — interests change, and re-analysing brings them
// back — but they are never shown.
func (s *Store) Archive(ctx context.Context, limit, offset int, interests []string) ([]domain.Article, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT`+articleColumnsArchived+`
		FROM articles a
		JOIN sources s ON s.id = a.source_id
		WHERE a.categories && $3::text[]
		ORDER BY a.collected_at DESC, a.id DESC
		LIMIT $1 OFFSET $2`, limit, offset, interests)
	if err != nil {
		return nil, fmt.Errorf("query archive: %w", err)
	}

	return collectArchivable(rows, "archive")
}

// SetArchived marks an article read, or puts it back.
//
// Archiving is the reader's own act, not something time does. While archived an
// article is out of the interest tabs and the daily selection, but the
// day-by-day view still shows it, which is what makes un-archiving reachable.
func (s *Store) SetArchived(ctx context.Context, id int64, archived bool) error {
	var when any
	if archived {
		when = time.Now()
	}

	tag, err := s.pool.Exec(ctx, `UPDATE articles SET archived_at = $2 WHERE id = $1`, id, when)
	if err != nil {
		return fmt.Errorf("set archived on article %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// ArticlesByInterest returns the unread articles filed under one interest, best
// first. Only those above the threshold: the tabs are a reading list, not the
// archive.
//
// The order is by relevance, matching the home page. Sorting by date instead
// buried the strongest article of the week under anything published since, so
// the two views now agree on what "first" means; the date view is /day.
func (s *Store) ArticlesByInterest(ctx context.Context, interest string,
	threshold domain.RelevanceScore, limit, offset int) ([]domain.Article, error) {

	rows, err := s.pool.Query(ctx, `
		SELECT`+articleColumnsArchived+`
		FROM articles a JOIN sources s ON s.id = a.source_id
		WHERE $1 = ANY (a.categories)
		  AND a.processed_at IS NOT NULL
		  AND a.archived_at IS NULL
		  AND a.score >= $2
		ORDER BY a.score DESC, a.published_at DESC NULLS LAST, a.id DESC
		LIMIT $3 OFFSET $4`, interest, int16(threshold), limit, offset)
	if err != nil {
		return nil, fmt.Errorf("query interest %q: %w", interest, err)
	}
	return collectArchivable(rows, "interest "+interest)
}

// ArticlesOnDay returns everything published on one day for an interest —
// including what has been read and what never cleared the threshold. This view
// deliberately hides nothing; it is where an article goes to be found again.
func (s *Store) ArticlesOnDay(ctx context.Context, interest string, day time.Time,
	interests []string) ([]domain.Article, error) {

	rows, err := s.pool.Query(ctx, `
		SELECT`+articleColumnsArchived+`
		FROM articles a JOIN sources s ON s.id = a.source_id
		WHERE ($1 = '' OR $1 = ANY (a.categories))
		  AND a.categories && $3::text[]
		  AND a.published_at::date = $2::date
		ORDER BY a.score DESC NULLS LAST, a.published_at DESC`, interest, day, interests)
	if err != nil {
		return nil, fmt.Errorf("query day %s: %w", day.Format(time.DateOnly), err)
	}
	return collectArchivable(rows, "day "+day.Format(time.DateOnly))
}

// DaysWithArticles lists the days that have anything to show for an interest,
// newest first, so the day picker only offers days that exist.
func (s *Store) DaysWithArticles(ctx context.Context, interest string, limit int,
	interests []string) ([]DayCount, error) {

	rows, err := s.pool.Query(ctx, `
		SELECT published_at::date AS day, count(*)
		FROM articles
		WHERE ($1 = '' OR $1 = ANY (categories))
		  AND categories && $3::text[]
		  AND published_at IS NOT NULL
		GROUP BY day ORDER BY day DESC LIMIT $2`, interest, limit, interests)
	if err != nil {
		return nil, fmt.Errorf("query days: %w", err)
	}

	days, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (DayCount, error) {
		var d DayCount
		err := row.Scan(&d.Day, &d.Count)
		return d, err
	})
	if err != nil {
		return nil, fmt.Errorf("read days: %w", err)
	}
	return days, nil
}

// DayCount is one day and how much it holds.
type DayCount struct {
	Day   time.Time
	Count int
}

// articleColumnsArchived is articleColumns plus the archived marker, for the
// screens that show a read/unread state.
const articleColumnsArchived = articleColumns + `, a.archived_at`

func collectArchivable(rows pgx.Rows, what string) ([]domain.Article, error) {
	articles, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Article, error) {
		var a domain.Article
		// Null for anything unread; see LatestPerInterest.
		var archived *time.Time
		err := row.Scan(&a.ID, &a.SourceID, &a.SourceName, &a.URL, &a.Title, &a.Author,
			&a.PublishedAt, &a.CollectedAt, &a.Categories, &a.Entities, &a.Tone,
			&a.Summary, &a.Score, &a.ScoreReason, &archived)
		if archived != nil {
			a.ArchivedAt = *archived
		}
		return a, err
	})
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", what, err)
	}
	return articles, nil
}
