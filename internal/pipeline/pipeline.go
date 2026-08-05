// Package pipeline turns a stored article into a curated one: what it is
// about, how relevant it is, and — only when it clears the threshold — a
// summary written for this reader.
//
// The three stages are separate interfaces on purpose. They are the seam that
// lets tests run without a network and without cost, and lets the expensive
// stage use a different model from the cheap ones.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/emaori/ziba/internal/domain"
)

// Extraction is what an article is about: the cheap first pass.
type Extraction struct {
	Categories []string
	Entities   []string
	Tone       string
}

// Score is how relevant an article is, and why.
type Score struct {
	Value  domain.RelevanceScore
	Reason string
}

// Extractor identifies what an article is about.
type Extractor interface {
	Extract(ctx context.Context, a domain.Article) (Extraction, error)
}

// Scorer rates an article against the configured interests.
type Scorer interface {
	Score(ctx context.Context, a domain.Article, e Extraction) (Score, error)
}

// Summarizer writes a summary aimed at this reader.
type Summarizer interface {
	Summarize(ctx context.Context, a domain.Article, e Extraction) (string, error)
}

// Analyzer is everything the pipeline needs from a model provider. Splitting it
// into three interfaces and joining them here means an implementation can
// satisfy all three, while a test can replace just one.
type Analyzer interface {
	Extractor
	Scorer
	Summarizer
}

// Pipeline runs the three stages in order.
type Pipeline struct {
	analyzer  Analyzer
	threshold domain.RelevanceScore
	log       *slog.Logger
}

// New builds a pipeline. Articles scoring below threshold are not summarized,
// which is where most of the cost is saved: the expensive model only ever sees
// what is worth reading.
func New(analyzer Analyzer, threshold int, log *slog.Logger) *Pipeline {
	return &Pipeline{
		analyzer:  analyzer,
		threshold: domain.RelevanceScore(threshold),
		log:       log,
	}
}

// Analyze returns the article enriched with categories, entities, tone, score
// and — above threshold — a summary.
func (p *Pipeline) Analyze(ctx context.Context, a domain.Article) (domain.Article, error) {
	extraction, err := p.analyzer.Extract(ctx, a)
	if err != nil {
		return a, fmt.Errorf("extract %s: %w", a.URL, err)
	}

	score, err := p.analyzer.Score(ctx, a, extraction)
	if err != nil {
		return a, fmt.Errorf("score %s: %w", a.URL, err)
	}

	a.Categories = extraction.Categories
	a.Entities = extraction.Entities
	a.Tone = extraction.Tone
	a.Score = score.Value
	a.ScoreReason = score.Reason
	a.AnalyzedAt = time.Now().UTC()

	if score.Value < p.threshold {
		p.log.Debug("below threshold, not summarized",
			"url", a.URL, "score", score.Value, "threshold", p.threshold)
		return a, nil
	}

	summary, err := p.analyzer.Summarize(ctx, a, extraction)
	if err != nil {
		// The article keeps its score and stays in the digest; only the summary
		// is missing, and that is worth far less than losing the analysis.
		p.log.Warn("summary unavailable", "url", a.URL, "error", err)
		return a, nil
	}
	a.Summary = summary
	return a, nil
}

// Threshold reports the score an article must reach to be summarized.
func (p *Pipeline) Threshold() domain.RelevanceScore { return p.threshold }
