package pipeline

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/emaori/ziba/internal/config"
	"github.com/emaori/ziba/internal/domain"
)

// stubAnalyzer records what it was asked and returns what the test tells it to.
type stubAnalyzer struct {
	assessment   Assessment
	summary      string
	summaryUsage Usage

	assessErr    error
	summarizeErr error

	summarizeCalls int
	declared       []string
}

func TestAnalyzeKeepsProviderScoreUnadjusted(t *testing.T) {
	analyzer := &stubAnalyzer{
		assessment: Assessment{Categories: []string{"AI"}, Score: 65, Reason: "relevant"},
		summary:    "Provider-scored summary",
	}
	p := New(analyzer, 60, slog.Default())
	got, err := p.Analyze(context.Background(), domain.Article{URL: "https://example.com", FullText: "text"}, nil)
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if !got.BaseScoreSet || got.BaseScore != 65 || got.Score != got.BaseScore {
		t.Fatalf("scores = base %d final %d, want both provider score 65", got.BaseScore, got.Score)
	}
	if got.Summary == "" {
		t.Error("provider score at or above threshold was not summarized")
	}
}

func (s *stubAnalyzer) Assess(_ context.Context, _ domain.Article, declared []string) (Assessment, error) {
	s.declared = declared
	return s.assessment, s.assessErr
}

func (s *stubAnalyzer) Summarize(context.Context, domain.Article, Assessment) (string, Usage, error) {
	s.summarizeCalls++
	return s.summary, s.summaryUsage, s.summarizeErr
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

			// The text matters: an article with none is never summarized
			// whatever it scores, which is a different test below.
			got, err := p.Analyze(context.Background(), domain.Article{
				URL: "https://example.com/a", FullText: "The article's text.",
			}, nil)
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

// An article whose page could not be read has nothing to summarize, and the
// call must not be built at all.
//
// Every provider already refused it, but only after the request had been
// assembled and sent — and a source that declares its categories bypasses the
// threshold, so this happened on every textless article it produced. The first
// full run over the archive paid for two such calls.
func TestAnalyzeDoesNotSummarizeAnArticleWithNoText(t *testing.T) {
	tests := []struct {
		name     string
		declared []string
	}{
		{"inferred", nil},
		{"declared, which otherwise always gets a summary", []string{"AI"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &stubAnalyzer{
				assessment: Assessment{
					Score: 90, // well above the threshold, so only the text stops it
					Usage: Usage{Input: 1200, Output: 80},
				},
				summary: "a summary that should never be asked for",
			}
			p := New(stub, 60, testLogger())

			got, err := p.Analyze(context.Background(),
				domain.Article{URL: "https://example.com/paywalled", FullText: "  \n "}, tt.declared)
			if err != nil {
				t.Fatalf("Analyze: %v", err)
			}
			if stub.summarizeCalls != 0 {
				t.Errorf("the summarizer was called %d times for an article with no text",
					stub.summarizeCalls)
			}
			if got.Summary != "" {
				t.Errorf("summary = %q, want empty", got.Summary)
			}
			// The assessment still happened and still cost something.
			if got.InputTokens != 1200 || got.OutputTokens != 80 {
				t.Errorf("tokens = %d/%d, want 1200/80: the assessment was still paid for",
					got.InputTokens, got.OutputTokens)
			}
			if got.Score != 90 {
				t.Errorf("score = %d, want 90: the article keeps its assessment", got.Score)
			}
		})
	}
}

func TestAnalyzeMarksAndSummarizesMismatchedContentAsLimited(t *testing.T) {
	stub := &stubAnalyzer{
		assessment: Assessment{
			Categories:     []string{"Economics"},
			Score:          80,
			ContentQuality: domain.ContentMismatched,
			ContentReason:  "the body is an index of unrelated newsletters",
		},
		summary: "A cautious overview based on trustworthy metadata.",
	}
	p := New(stub, 60, testLogger())

	got, err := p.Analyze(context.Background(), domain.Article{
		Title:    "Effects of banking consolidation",
		URL:      "https://example.com/banking",
		FullText: "Latest newsletters. Cooking. Sport. Continue reading.",
	}, nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if stub.summarizeCalls != 1 {
		t.Fatalf("summarize called %d times, want 1", stub.summarizeCalls)
	}
	if got.ContentQuality != domain.ContentMismatched || !got.LimitedOverview() {
		t.Errorf("quality = %q, limited = %v", got.ContentQuality, got.LimitedOverview())
	}
	if got.ContentQualityReason != "the body is an index of unrelated newsletters" {
		t.Errorf("content reason = %q", got.ContentQualityReason)
	}
}

func TestSummaryPromptExcludesMismatchedBody(t *testing.T) {
	a := domain.Article{
		Title:    "Effects of banking consolidation",
		URL:      "https://example.com/banking",
		FullText: "UNRELATED NEWSLETTER INDEX",
	}
	prompt := summaryArticlePrompt(a, domain.ContentMismatched)
	if strings.Contains(prompt, a.FullText) {
		t.Error("mismatched body was sent to the summarizer")
	}
	if !strings.Contains(prompt, a.Title) {
		t.Error("trustworthy title is missing from the limited-summary prompt")
	}
	if got := summaryArticlePrompt(a, domain.ContentComplete); !strings.Contains(got, a.FullText) {
		t.Error("complete body was omitted from an ordinary summary prompt")
	}
}
