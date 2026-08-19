package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/emaori/ziba/internal/domain"
)

// ScoreFeedbackSummary describes the personalization data without exposing
// individual reading choices on the settings page.
type ScoreFeedbackSummary struct {
	Count  int
	Active bool
}

// SetScoreFeedback creates, changes, or clears the one correction for an article.
func (s *Store) SetScoreFeedback(ctx context.Context, articleID int64, feedback domain.ScoreFeedback) error {
	if feedback == "" {
		tag, err := s.pool.Exec(ctx, `DELETE FROM article_score_feedback WHERE article_id = $1`, articleID)
		if err != nil {
			return fmt.Errorf("remove score feedback for article %d: %w", articleID, err)
		}
		if tag.RowsAffected() == 0 {
			var exists bool
			if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM articles WHERE id=$1)`, articleID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return pgx.ErrNoRows
			}
		}
		return nil
	}
	if feedback != domain.FeedbackHigher && feedback != domain.FeedbackLower {
		return fmt.Errorf("invalid score feedback %q", feedback)
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO article_score_feedback (article_id, direction)
		SELECT $1, $2 WHERE EXISTS (SELECT 1 FROM articles WHERE id = $1)
		ON CONFLICT (article_id) DO UPDATE
		SET direction=EXCLUDED.direction, updated_at=now()`, articleID, string(feedback))
	if err != nil {
		return fmt.Errorf("save score feedback for article %d: %w", articleID, err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *Store) ScoreFeedbackSummary(ctx context.Context) (ScoreFeedbackSummary, error) {
	var out ScoreFeedbackSummary
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM article_score_feedback`).Scan(&out.Count); err != nil {
		return out, fmt.Errorf("count score feedback: %w", err)
	}
	out.Active = out.Count >= 3
	return out, nil
}

// ResetPersonalizedScoring removes every correction and restores provider
// scores on articles that had already received a local adjustment.
func (s *Store) ResetPersonalizedScoring(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin scoring reset: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM article_score_feedback`); err != nil {
		return fmt.Errorf("delete score feedback: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE articles SET score=base_score WHERE base_score IS NOT NULL`); err != nil {
		return fmt.Errorf("restore provider scores: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit scoring reset: %w", err)
	}
	return nil
}
