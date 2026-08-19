// Package pipeline turns a stored article into a curated one: what it is
// about, how relevant it is, and — only when it clears the threshold — a
// summary written for this reader.
//
// The two stages are separate interfaces on purpose. They are the seam that
// lets tests run without a network and without cost, and lets the expensive
// stage use a different model from the cheap one.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/emaori/ziba/internal/domain"
)

// Assessment identifies the article and rates it against configured interests
// in one model call.
type Assessment struct {
	Categories     []string
	Entities       []string
	Tone           string
	ContentQuality domain.ContentQuality
	ContentReason  string
	Score          domain.RelevanceScore
	Reason         string
	Usage          Usage
}

// Usage is the provider-reported token count for one or more calls.
type Usage struct {
	Input  int
	Output int
}

// Plus returns the two added together, so a stage can report its whole cost.
func (u Usage) Plus(other Usage) Usage {
	return Usage{Input: u.Input + other.Input, Output: u.Output + other.Output}
}

// Total is what the two come to. Input and output are priced differently, so
// this is a size and not a cost — the page that shows it says so.
func (u Usage) Total() int { return u.Input + u.Output }

// Assessor identifies and rates an article in one pass.
//
// declared, when not empty, are the categories the article's source states it
// publishes. The assessor must use them rather than choose its own, and score
// how interesting the piece is rather than how well it fits the reader's
// interests — the fit is already known, which is the point of declaring it.
type Assessor interface {
	Assess(ctx context.Context, a domain.Article, declared []string) (Assessment, error)
}

// Summarizer writes a summary aimed at this reader, and reports what it cost.
type Summarizer interface {
	Summarize(ctx context.Context, a domain.Article, as Assessment) (string, Usage, error)
}

// Analyzer is everything the pipeline needs from a model provider. Keeping the
// two stages as separate interfaces and joining them here means an
// implementation can satisfy both, while a test can replace just one.
type Analyzer interface {
	Assessor
	Summarizer
}

// ScoreCalibrator personalizes a provider score from local feedback. It is
// deliberately independent of Analyzer: no feedback is sent to an AI vendor.
type ScoreCalibrator interface {
	CalibrateScore(ctx context.Context, categories []string, base domain.RelevanceScore) (domain.RelevanceScore, error)
}

// Pipeline runs the three stages in order.
type Pipeline struct {
	analyzer   Analyzer
	threshold  domain.RelevanceScore
	log        *slog.Logger
	calibrator ScoreCalibrator
}

// New builds a pipeline. Articles scoring below threshold are not summarized,
// which is where most of the cost is saved: the expensive model only ever sees
// what is worth reading.
func New(analyzer Analyzer, threshold int, log *slog.Logger) *Pipeline {
	return NewPersonalized(analyzer, threshold, log, nil)
}

func NewPersonalized(analyzer Analyzer, threshold int, log *slog.Logger, calibrator ScoreCalibrator) *Pipeline {
	return &Pipeline{
		analyzer:   analyzer,
		threshold:  domain.RelevanceScore(threshold),
		log:        log,
		calibrator: calibrator,
	}
}

// Analyze returns the article enriched with categories, entities, tone, score
// and — above threshold — a summary.
//
// declared carries the source's stated categories, or nothing when the source
// leaves the judgement to the analyzer.
func (p *Pipeline) Analyze(ctx context.Context, a domain.Article, declared []string) (domain.Article, error) {
	assessment, err := p.analyzer.Assess(ctx, a, declared)
	if err != nil {
		return a, fmt.Errorf("assess %s: %w", a.URL, err)
	}
	a.BaseScore = assessment.Score
	a.BaseScoreSet = true
	if p.calibrator != nil {
		assessment.Score, err = p.calibrator.CalibrateScore(ctx, assessment.Categories, a.BaseScore)
		if err != nil {
			return a, fmt.Errorf("calibrate score for %s: %w", a.URL, err)
		}
	}

	a.Categories = assessment.Categories
	a.Entities = assessment.Entities
	a.Tone = assessment.Tone
	a.ContentQuality = normalizedContentQuality(assessment.ContentQuality, a.FullText)
	a.ContentQualityReason = assessment.ContentReason
	a.Score = assessment.Score
	a.ScoreReason = assessment.Reason
	a.AnalyzedAt = time.Now().UTC()

	// Recorded before the threshold is tested, because an article that is judged
	// and then dropped still cost something to judge, and a page reporting only
	// what was summarized would understate the bill by every article that was
	// looked at and set aside.
	spent := assessment.Usage

	// Avoid a model call when retrieval produced no text.
	if strings.TrimSpace(a.FullText) == "" || a.ContentQuality == domain.ContentUnavailable {
		p.log.Debug("no text retrieved, not summarized", "url", a.URL)
		a.InputTokens, a.OutputTokens = spent.Input, spent.Output
		return a, nil
	}

	// A declared source is always shown, so it always gets a summary: leaving
	// it out would put an article on the page with nothing to read beneath it.
	if assessment.Score < p.threshold && len(declared) == 0 {
		p.log.Debug("below threshold, not summarized",
			"url", a.URL, "score", assessment.Score, "threshold", p.threshold)
		a.InputTokens, a.OutputTokens = spent.Input, spent.Output
		return a, nil
	}

	summary, summaryUsage, err := p.analyzer.Summarize(ctx, a, assessment)
	spent = spent.Plus(summaryUsage)
	a.InputTokens, a.OutputTokens = spent.Input, spent.Output
	if err != nil {
		// The article keeps its score and stays in the digest; only the summary
		// is missing, and that is worth far less than losing the analysis.
		p.log.Warn("summary unavailable", "url", a.URL, "error", err)
		return a, nil
	}
	a.Summary = summary
	return a, nil
}

func normalizedContentQuality(quality domain.ContentQuality, text string) domain.ContentQuality {
	if quality != "" {
		return quality
	}
	if strings.TrimSpace(text) == "" {
		return domain.ContentUnavailable
	}
	return domain.ContentComplete
}

// Threshold reports the score an article must reach to be summarized.
func (p *Pipeline) Threshold() domain.RelevanceScore { return p.threshold }
