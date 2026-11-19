package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/emaori/ziba/internal/domain"
)

// NewsletterMessageExtracted is true only after all extracted items and their
// message associations have been committed together.
func (s *Store) NewsletterMessageExtracted(ctx context.Context, sourceID int64, key string) (bool, error) {
	var extracted bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (
        SELECT 1 FROM newsletter_messages
        WHERE source_id=$1 AND message_key=$2 AND extracted_at IS NOT NULL
    )`, sourceID, key).Scan(&extracted)
	if err != nil {
		return false, fmt.Errorf("read newsletter extraction: %w", err)
	}
	return extracted, nil
}

// SaveNewsletterMessage commits the retained email, its accepted candidates,
// their associations and extraction completion as one transaction. The count
// includes a newly inserted message and any newly inserted raw items.
func (s *Store) SaveNewsletterMessage(ctx context.Context, message domain.NewsletterMessage) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin newsletter extraction: %w", err)
	}
	defer tx.Rollback(ctx)
	var id int64
	inserted := 0
	err = tx.QueryRow(ctx, `INSERT INTO newsletter_messages
        (source_id, message_key, title, author, published_at, collected_at, text)
        VALUES ($1,$2,$3,$4,$5,$6,$7)
        ON CONFLICT (source_id, message_key) DO NOTHING RETURNING id`,
		message.SourceID, message.Key, message.Title, message.Author,
		nullableTime(message.PublishedAt), message.CollectedAt, message.Text).Scan(&id)
	if err == nil {
		inserted++
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("insert newsletter message: %w", err)
	}
	// Lock existing messages as well: concurrent collection must not replace
	// a committed extraction or attach a different set of links to it.
	var extractedAt *time.Time
	err = tx.QueryRow(ctx, `SELECT id, extracted_at FROM newsletter_messages
        WHERE source_id=$1 AND message_key=$2 FOR UPDATE`, message.SourceID, message.Key).Scan(&id, &extractedAt)
	if err != nil {
		return 0, fmt.Errorf("lock newsletter message: %w", err)
	}
	if extractedAt == nil {
		for _, item := range message.Items {
			if item.SourceID != message.SourceID || (item.Kind != "" && item.Kind != domain.ItemKindArticle) {
				return 0, fmt.Errorf("newsletter candidate must be an article from the message source")
			}
			rawID, created, err := saveRawItem(ctx, tx, item)
			if err != nil {
				return 0, err
			}
			if created {
				inserted++
			}
			if _, err := tx.Exec(ctx, `INSERT INTO newsletter_message_items (message_id,raw_item_id)
                VALUES ($1,$2) ON CONFLICT DO NOTHING`, id, rawID); err != nil {
				return 0, fmt.Errorf("link newsletter item: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE newsletter_messages SET extracted_at=now() WHERE id=$1`, id); err != nil {
			return 0, fmt.Errorf("complete newsletter extraction: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit newsletter extraction: %w", err)
	}
	return inserted, nil
}

func saveRawItem(ctx context.Context, tx pgx.Tx, item domain.RawItem) (int64, bool, error) {
	kind := item.Kind
	if kind == "" {
		kind = domain.ItemKindArticle
	}
	var id int64
	err := tx.QueryRow(ctx, `INSERT INTO raw_items
        (source_id,title,url,author,published_at,collected_at,text,kind)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
        ON CONFLICT (source_id,url) DO NOTHING RETURNING id`,
		item.SourceID, item.Title, item.URL, item.Author, nullableTime(item.PublishedAt), item.CollectedAt, item.Text, string(kind)).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, fmt.Errorf("insert newsletter item: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM raw_items WHERE source_id=$1 AND url=$2`, item.SourceID, item.URL).Scan(&id); err != nil {
		return 0, false, fmt.Errorf("find newsletter item: %w", err)
	}
	return id, false, nil
}

// SaveRetrievedArticle records the article, its origin and the item's outcome
// atomically. Analysis happens later and never changes this association.
func (s *Store) SaveRetrievedArticle(ctx context.Context, rawID int64, article domain.Article) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin retrieved article: %w", err)
	}
	defer tx.Rollback(ctx)
	var sourceID int64
	var kind string
	var processedAt *time.Time
	if err := tx.QueryRow(ctx, `SELECT source_id,kind,processed_at FROM raw_items WHERE id=$1 FOR UPDATE`, rawID).
		Scan(&sourceID, &kind, &processedAt); err != nil {
		return false, fmt.Errorf("lock article input: %w", err)
	}
	if kind != string(domain.ItemKindArticle) || sourceID != article.SourceID {
		return false, fmt.Errorf("article origin must be an article candidate from the same source")
	}
	if processedAt != nil {
		return false, nil
	}
	articleID, created, err := saveArticle(ctx, tx, article)
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO article_origins (raw_item_id,article_id) VALUES ($1,$2)`, rawID, articleID); err != nil {
		return false, fmt.Errorf("record article origin: %w", err)
	}
	outcome := domain.OutcomeDuplicate
	if created {
		outcome = domain.OutcomeStored
	}
	if _, err := tx.Exec(ctx, `UPDATE raw_items SET processed_at=now(),outcome=$2,
        failure_count=0,last_attempt_at=NULL,last_error='',failed_at=NULL WHERE id=$1`, rawID, string(outcome)); err != nil {
		return false, fmt.Errorf("complete article input: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit retrieved article: %w", err)
	}
	return created, nil
}
