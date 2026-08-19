package store

import (
	"context"
	"testing"
	"time"

	"github.com/emaori/ziba/internal/domain"
)

func TestCalibratedScoreLearnsConservatively(t *testing.T) {
	tests := []struct {
		name           string
		base           domain.RelevanceScore
		count, balance int
		want           domain.RelevanceScore
	}{
		{"not enough feedback", 60, 2, 2, 60},
		{"three higher", 60, 3, 3, 65},
		{"three mostly higher rounds", 60, 3, 1, 62},
		{"five higher confidence ramp", 60, 5, 5, 68},
		{"ten higher", 60, 10, 10, 75},
		{"confidence capped after ten", 60, 20, 20, 75},
		{"ten lower", 60, 10, -10, 45},
		{"mixed direction", 60, 10, 2, 63},
		{"mixed cancels", 60, 10, 0, 60},
		{"upper clamp", 95, 10, 10, 100},
		{"lower clamp", 5, 10, -10, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := calibratedScore(tt.base, tt.count, tt.balance); got != tt.want {
				t.Errorf("score = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestFeedbackLifecycleAndReset(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	sourceID := seedSource(t, db, "feedback", nil)
	ids := make([]int64, 3)
	for i := range ids {
		ids[i] = seedArticle(t, db, sourceID, "https://example.com/feedback/"+string(rune('a'+i)), 60, []string{"AI"}, time.Now())
		if _, err := db.pool.Exec(ctx, `UPDATE articles SET base_score=score WHERE id=$1`, ids[i]); err != nil {
			t.Fatal(err)
		}
		if err := db.SetScoreFeedback(ctx, ids[i], domain.FeedbackHigher); err != nil {
			t.Fatalf("save feedback: %v", err)
		}
	}
	summary, err := db.ScoreFeedbackSummary(ctx)
	if err != nil || summary.Count != 3 || !summary.Active {
		t.Fatalf("summary = %+v, err %v", summary, err)
	}
	if got, err := db.CalibrateScore(ctx, []string{"AI"}, 60); err != nil || got != 65 {
		t.Fatalf("calibrated score = %d, err %v", got, err)
	}
	if got, err := db.CalibrateScore(ctx, []string{"Robotics"}, 60); err != nil || got != 60 {
		t.Fatalf("unrelated category score = %d, err %v; want unchanged 60", got, err)
	}
	if err := db.SetScoreFeedback(ctx, ids[0], domain.FeedbackLower); err != nil {
		t.Fatalf("replace feedback: %v", err)
	}
	summary, _ = db.ScoreFeedbackSummary(ctx)
	if summary.Count != 3 {
		t.Fatalf("replacement created another row: count = %d", summary.Count)
	}
	if got, _ := db.CalibrateScore(ctx, []string{"AI"}, 60); got != 62 {
		t.Fatalf("score after replacement = %d, want 62", got)
	}
	if err := db.SetScoreFeedback(ctx, ids[0], ""); err != nil {
		t.Fatalf("clear feedback: %v", err)
	}
	summary, _ = db.ScoreFeedbackSummary(ctx)
	if summary.Count != 2 || summary.Active {
		t.Fatalf("summary after clear = %+v, want count 2 inactive", summary)
	}
	if err := db.SetScoreFeedback(ctx, ids[0], domain.FeedbackHigher); err != nil {
		t.Fatalf("restore feedback: %v", err)
	}
	if _, err := db.pool.Exec(ctx, `UPDATE articles SET score=65 WHERE id=$1`, ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := db.ResetPersonalizedScoring(ctx); err != nil {
		t.Fatalf("reset: %v", err)
	}
	summary, _ = db.ScoreFeedbackSummary(ctx)
	if summary.Count != 0 {
		t.Fatalf("feedback count after reset = %d", summary.Count)
	}
	var restored int
	if err := db.pool.QueryRow(ctx, `SELECT score FROM articles WHERE id=$1`, ids[0]).Scan(&restored); err != nil {
		t.Fatal(err)
	}
	if restored != 60 {
		t.Fatalf("score after reset = %d, want base score 60", restored)
	}
}

func TestZeroBaseScoreRoundTripsAndResets(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	sourceID := seedSource(t, db, "zero-base", nil)
	id, _, err := db.SaveArticle(ctx, domain.Article{
		SourceID: sourceID, URL: "https://example.com/zero-base", Title: "Zero base",
		FullText: "Text", CollectedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("save article: %v", err)
	}
	if err := db.SaveAnalysis(ctx, domain.Article{
		ID: id, Categories: []string{"AI"}, Score: 5,
		BaseScore: 0, BaseScoreSet: true, ScoreReason: "provider scored zero",
		ContentQuality: domain.ContentComplete, AnalyzedAt: time.Now(),
	}); err != nil {
		t.Fatalf("save analysis: %v", err)
	}

	article, err := db.Article(ctx, id)
	if err != nil {
		t.Fatalf("read article: %v", err)
	}
	if !article.BaseScoreSet || article.BaseScore != 0 || !article.PersonalizedScore() {
		t.Fatalf("read scores = base %d set %v score %d personalized %v", article.BaseScore, article.BaseScoreSet, article.Score, article.PersonalizedScore())
	}
	if err := db.ResetPersonalizedScoring(ctx); err != nil {
		t.Fatalf("reset: %v", err)
	}
	article, err = db.Article(ctx, id)
	if err != nil {
		t.Fatalf("read reset article: %v", err)
	}
	if article.Score != 0 || !article.BaseScoreSet || article.PersonalizedScore() {
		t.Fatalf("after reset = base %d set %v score %d personalized %v", article.BaseScore, article.BaseScoreSet, article.Score, article.PersonalizedScore())
	}
}
