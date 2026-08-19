package pipeline

import (
	"context"
	"testing"

	"github.com/emaori/ziba/internal/domain"
)

func TestFeedbackCalibrationLearnsConservatively(t *testing.T) {
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
			samples := make([]domain.ScoreFeedbackSample, 0, tt.count)
			higher := (tt.count + tt.balance) / 2
			for i := range tt.count {
				feedback := domain.FeedbackLower
				if i < higher {
					feedback = domain.FeedbackHigher
				}
				samples = append(samples, domain.ScoreFeedbackSample{Categories: []string{"AI"}, Feedback: feedback})
			}
			got, err := NewFeedbackCalibration(samples).CalibrateScore(context.Background(), []string{"AI"}, tt.base)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("score = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestFeedbackCalibrationIsAnImmutableCategorySnapshot(t *testing.T) {
	samples := []domain.ScoreFeedbackSample{
		{Categories: []string{"AI", "Go"}, Feedback: domain.FeedbackHigher},
		{Categories: []string{"AI"}, Feedback: domain.FeedbackHigher},
		{Categories: []string{"AI"}, Feedback: domain.FeedbackHigher},
	}
	calibrator := NewFeedbackCalibration(samples)
	// Mutating the input after construction simulates feedback changing while a
	// batch is running. The batch snapshot must not change with it.
	samples[0].Categories[0] = "Robotics"
	samples[0].Feedback = domain.FeedbackLower

	got, _ := calibrator.CalibrateScore(context.Background(), []string{"AI", "Go"}, 60)
	if got != 65 {
		t.Fatalf("snapshot score = %d, want 65", got)
	}
	unrelated, _ := calibrator.CalibrateScore(context.Background(), []string{"Robotics"}, 60)
	if unrelated != 60 {
		t.Fatalf("unrelated score = %d, want unchanged 60", unrelated)
	}
}

func TestFeedbackWithSeveralOverlappingCategoriesCountsOnce(t *testing.T) {
	samples := []domain.ScoreFeedbackSample{
		{Categories: []string{"AI", "Go"}, Feedback: domain.FeedbackHigher},
		{Categories: []string{"AI", "Go"}, Feedback: domain.FeedbackHigher},
		{Categories: []string{"AI", "Go"}, Feedback: domain.FeedbackHigher},
	}
	got, _ := NewFeedbackCalibration(samples).CalibrateScore(context.Background(), []string{"AI", "Go"}, 60)
	if got != 65 {
		t.Fatalf("score = %d, want three votes once each = 65", got)
	}
}
