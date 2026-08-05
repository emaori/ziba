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
	extraction Extraction
	score      Score
	summary    string

	extractErr   error
	scoreErr     error
	summarizeErr error

	summarizeCalls int
}

func (s *stubAnalyzer) Extract(context.Context, domain.Article) (Extraction, error) {
	return s.extraction, s.extractErr
}

func (s *stubAnalyzer) Score(context.Context, domain.Article, Extraction) (Score, error) {
	return s.score, s.scoreErr
}

func (s *stubAnalyzer) Summarize(context.Context, domain.Article, Extraction) (string, error) {
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
				extraction: Extraction{Categories: []string{"Go"}, Tone: "tutorial"},
				score:      Score{Value: tt.score, Reason: "because"},
				summary:    "a summary",
			}
			p := New(stub, 60, testLogger())

			got, err := p.Analyze(context.Background(), domain.Article{URL: "https://example.com/a"})
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
		extraction:   Extraction{Categories: []string{"AI"}},
		score:        Score{Value: 88, Reason: "matches AI"},
		summarizeErr: errors.New("model unavailable"),
	}
	p := New(stub, 60, testLogger())

	got, err := p.Analyze(context.Background(), domain.Article{URL: "https://example.com/a"})
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

func TestAnalyzePropagatesStageErrors(t *testing.T) {
	tests := []struct {
		name string
		stub *stubAnalyzer
	}{
		{"extraction fails", &stubAnalyzer{extractErr: errors.New("boom")}},
		{"scoring fails", &stubAnalyzer{scoreErr: errors.New("boom")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New(tt.stub, 60, testLogger())
			if _, err := p.Analyze(context.Background(), domain.Article{URL: "https://example.com/a"}); err == nil {
				t.Error("Analyze returned no error, want one")
			}
			if tt.stub.summarizeCalls != 0 {
				t.Error("summarize was called after an earlier stage failed")
			}
		})
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
	first, err := analyzer.Extract(ctx, article)
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	second, _ := analyzer.Extract(ctx, article)

	if len(first.Categories) != 1 || first.Categories[0] != "Go programming" {
		t.Errorf("Categories = %v, want [Go programming]", first.Categories)
	}
	if len(second.Categories) != len(first.Categories) {
		t.Error("Extract is not deterministic")
	}

	scoreA, _ := analyzer.Score(ctx, article, first)
	scoreB, _ := analyzer.Score(ctx, article, first)
	if scoreA.Value != scoreB.Value {
		t.Errorf("Score is not deterministic: %d then %d", scoreA.Value, scoreB.Value)
	}
	if scoreA.Value < 60 {
		t.Errorf("Score = %d, want the top-priority match to clear the threshold", scoreA.Value)
	}

	// An article matching nothing must not score well.
	unrelated := domain.Article{URL: "https://example.com/x", Title: "Roman traffic", FullText: "Cars."}
	extraction, _ := analyzer.Extract(ctx, unrelated)
	score, _ := analyzer.Score(ctx, unrelated, extraction)
	if score.Value >= 60 {
		t.Errorf("Score = %d for an unrelated article, want below threshold", score.Value)
	}
}
