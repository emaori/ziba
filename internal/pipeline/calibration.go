package pipeline

import (
	"context"

	"github.com/emaori/ziba/internal/domain"
)

type calibrationSample struct {
	categories map[string]struct{}
	direction  int
}

// NewFeedbackCalibration builds an immutable local model from the feedback
// rows read at the beginning of one analysis batch.
func NewFeedbackCalibration(samples []domain.ScoreFeedbackSample) ScoreCalibrator {
	prepared := make([]calibrationSample, 0, len(samples))
	for _, sample := range samples {
		categories := make(map[string]struct{}, len(sample.Categories))
		for _, category := range sample.Categories {
			categories[category] = struct{}{}
		}
		direction := -1
		if sample.Feedback == domain.FeedbackHigher {
			direction = 1
		}
		prepared = append(prepared, calibrationSample{categories: categories, direction: direction})
	}
	return feedbackCalibration{samples: prepared}
}

type feedbackCalibration struct{ samples []calibrationSample }

func (c feedbackCalibration) CalibrateScore(_ context.Context, categories []string, base domain.RelevanceScore) (domain.RelevanceScore, error) {
	count, balance := 0, 0
	for _, sample := range c.samples {
		if overlaps(categories, sample.categories) {
			count++
			balance += sample.direction
		}
	}
	return calibratedScore(base, count, balance), nil
}

func overlaps(categories []string, candidate map[string]struct{}) bool {
	for _, category := range categories {
		if _, ok := candidate[category]; ok {
			return true
		}
	}
	return false
}

func calibratedScore(base domain.RelevanceScore, count, balance int) domain.RelevanceScore {
	if count < 3 {
		return base
	}
	confidence := min(float64(count)/10, 1)
	adjustment := int(15*float64(balance)/float64(count)*confidence + float64(sign(balance))*0.5)
	return domain.RelevanceScore(min(max(int(base)+adjustment, 0), 100))
}

func sign(n int) int {
	if n < 0 {
		return -1
	}
	if n > 0 {
		return 1
	}
	return 0
}
