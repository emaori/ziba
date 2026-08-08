package pipeline

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/emaori/ziba/internal/config"
	"github.com/emaori/ziba/internal/domain"
)

// stubAnalyzer records what it was asked and returns what the test tells it to.
type stubAnalyzer struct {
	assessment Assessment
	summary    string

	assessErr    error
	summarizeErr error

	summarizeCalls int
	declared       []string
}

func (s *stubAnalyzer) Assess(_ context.Context, _ domain.Article, declared []string) (Assessment, error) {
	s.declared = declared
	return s.assessment, s.assessErr
}

func (s *stubAnalyzer) Summarize(context.Context, domain.Article, Assessment) (string, error) {
	s.summarizeCalls++
	return s.summary, s.summarizeErr
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestAnalyzeSummarizesOnlyAboveThreshold(t *testing.T) {
	tests := []struct {
		name          string
		score         domain.RelevanceScore
		wantSummarize bool
	}{
		{"well below threshold", 10, false},
		{"just below threshold", 59, false},
		{"exactly at threshold", 60, true},
		{"above threshold", 95, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &stubAnalyzer{
				assessment: Assessment{
					Categories: []string{"Go"},
					Tone:       "tutorial",
					Score:      tt.score,
					Reason:     "because",
				},
				summary: "a summary",
			}
			p := New(stub, 60, testLogger())

			got, err := p.Analyze(context.Background(), domain.Article{URL: "https://example.com/a"}, nil)
			if err != nil {
				t.Fatalf("Analyze returned error: %v", err)
			}

			if tt.wantSummarize && stub.summarizeCalls != 1 {
				t.Errorf("summarize called %d times, want 1", stub.summarizeCalls)
			}
			if !tt.wantSummarize && stub.summarizeCalls != 0 {
				t.Errorf("summarize called %d times, want 0 — the expensive stage must not run below threshold",
					stub.summarizeCalls)
			}
			if tt.wantSummarize && got.Summary != "a summary" {
				t.Errorf("Summary = %q, want %q", got.Summary, "a summary")
			}
			if !tt.wantSummarize && got.Summary != "" {
				t.Errorf("Summary = %q, want empty", got.Summary)
			}

			// The analysis is kept either way: below threshold is browsable,
			// not discarded.
			if got.Score != tt.score {
				t.Errorf("Score = %d, want %d", got.Score, tt.score)
			}
			if got.ScoreReason != "because" {
				t.Errorf("ScoreReason = %q, want %q", got.ScoreReason, "because")
			}
			if got.AnalyzedAt.IsZero() {
				t.Error("AnalyzedAt is zero, want the analysis timestamp")
			}
		})
	}
}

// A failing summary must not throw away a perfectly good score.
func TestAnalyzeKeepsScoreWhenSummaryFails(t *testing.T) {
	stub := &stubAnalyzer{
		assessment:   Assessment{Categories: []string{"AI"}, Score: 88, Reason: "matches AI"},
		summarizeErr: errors.New("model unavailable"),
	}
	p := New(stub, 60, testLogger())

	got, err := p.Analyze(context.Background(), domain.Article{URL: "https://example.com/a"}, nil)
	if err != nil {
		t.Fatalf("Analyze returned error: %v, want the failure to be tolerated", err)
	}
	if got.Score != 88 {
		t.Errorf("Score = %d, want 88", got.Score)
	}
	if got.Summary != "" {
		t.Errorf("Summary = %q, want empty", got.Summary)
	}
}

// Assessment failing aborts the article: everything after it depends on the
// score, so there is nothing worth storing.
func TestAnalyzePropagatesAssessmentError(t *testing.T) {
	stub := &stubAnalyzer{assessErr: errors.New("boom")}
	p := New(stub, 60, testLogger())

	if _, err := p.Analyze(context.Background(), domain.Article{URL: "https://example.com/a"}, nil); err == nil {
		t.Error("Analyze returned no error, want one")
	}
	if stub.summarizeCalls != 0 {
		t.Error("summarize was called after assessment failed")
	}
}

func TestDeterministicIsDeterministic(t *testing.T) {
	interests := config.Interests{
		Threshold: 60,
		Topics: []config.Interest{
			{Topic: "Go programming", Priority: 1, Subtopics: []string{"concurrency"}},
			{Topic: "Cooking", Priority: 3},
		},
	}
	analyzer := NewDeterministic(interests)
	article := domain.Article{
		URL:      "https://example.com/goroutines",
		Title:    "Understanding concurrency in Go",
		FullText: "Goroutines are cheap. This post explains concurrency patterns.",
	}

	ctx := context.Background()
	first, err := analyzer.Assess(ctx, article, nil)
	if err != nil {
		t.Fatalf("Assess returned error: %v", err)
	}
	second, _ := analyzer.Assess(ctx, article, nil)

	if len(first.Categories) != 1 || first.Categories[0] != "Go programming" {
		t.Errorf("Categories = %v, want [Go programming]", first.Categories)
	}
	if first.Score != second.Score || len(first.Categories) != len(second.Categories) {
		t.Error("Assess is not deterministic")
	}
	if first.Score < 60 {
		t.Errorf("Score = %d, want the top-priority match to clear the threshold", first.Score)
	}

	// An article matching nothing must not score well.
	unrelated := domain.Article{URL: "https://example.com/x", Title: "Roman traffic", FullText: "Cars."}
	assessment, _ := analyzer.Assess(ctx, unrelated, nil)
	if assessment.Score >= 60 {
		t.Errorf("Score = %d for an unrelated article, want below threshold", assessment.Score)
	}
}
