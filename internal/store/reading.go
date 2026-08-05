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
func (s *Store) GenerateDigest(ctx context.Context, date time.Time, threshold domain.RelevanceScore) (int, error) {
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
		  AND score >= $2
		  AND collected_at::date = $3::date`,
		digestID, int16(threshold), date)
	if err != nil {
		return 0, fmt.Errorf("select digest articles: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit digest: %w", err)
	}
	return int(tag.RowsAffected()), nil
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

// Category is one subject area and how much is filed under it.
type Category struct {
	Name  string
	Count int
}

// Categories lists the subject areas in use, most populated first.
func (s *Store) Categories(ctx context.Context) ([]Category, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT category, count(*)
		FROM articles, unnest(categories) AS category
		WHERE processed_at IS NOT NULL
		GROUP BY category
		ORDER BY count(*) DESC, category`)
	if err != nil {
		return nil, fmt.Errorf("query categories: %w", err)
	}

	categories, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Category, error) {
		var c Category
		err := row.Scan(&c.Name, &c.Count)
		return c, err
	})
	if err != nil {
		return nil, fmt.Errorf("read categories: %w", err)
	}
	return categories, nil
}

// ArticlesByCategory returns the articles filed under a category, most relevant
// first. It includes articles below the threshold: the digest is a selection,
// the archive is everything.
func (s *Store) ArticlesByCategory(ctx context.Context, category string, limit int) ([]domain.Article, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT`+articleColumns+`
		FROM articles a
		JOIN sources s ON s.id = a.source_id
		WHERE $1 = ANY (a.categories)
		ORDER BY a.score DESC NULLS LAST, a.collected_at DESC
		LIMIT $2`, category, limit)
	if err != nil {
		return nil, fmt.Errorf("query category %q: %w", category, err)
	}

	articles, err := pgx.CollectRows(rows, scanArticle)
	if err != nil {
		return nil, fmt.Errorf("read category %q: %w", category, err)
	}
	return articles, nil
}

// Archive returns everything collected, newest first — including articles that
// never cleared the threshold. The AI curates, it does not censor.
func (s *Store) Archive(ctx context.Context, limit, offset int) ([]domain.Article, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT`+articleColumns+`
		FROM articles a
		JOIN sources s ON s.id = a.source_id
		ORDER BY a.collected_at DESC, a.id DESC
		LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("query archive: %w", err)
	}

	articles, err := pgx.CollectRows(rows, scanArticle)
	if err != nil {
		return nil, fmt.Errorf("read archive: %w", err)
	}
	return articles, nil
}
