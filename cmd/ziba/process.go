package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/emaori/ziba/internal/config"
	"github.com/emaori/ziba/internal/domain"
	"github.com/emaori/ziba/internal/pipeline"
	"github.com/emaori/ziba/internal/store"
)

// maxParallelAnalyses caps concurrent model calls. Small on purpose: this is a
// personal tool with no deadline, and a burst of requests only invites rate
// limiting.
const maxParallelAnalyses = 4

// processCmd runs the AI pipeline over articles that have not been analyzed.
func processCmd(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("process", flag.ContinueOnError)
	batchSize := flags.Int("batch", 50, "maximum number of articles to analyze in one run")
	offline := flags.Bool("offline", false, "use the deterministic analyzer: no model, no network, no cost")
	if err := flags.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	interests, err := config.LoadInterests(cfg.InterestsPath)
	if err != nil {
		return err
	}

	analyzer, err := buildAnalyzer(cfg, interests, *offline)
	if err != nil {
		return err
	}

	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	log := slog.Default()
	p := pipeline.New(analyzer, interests.Threshold, log)

	articles, err := db.UnanalyzedArticles(ctx, *batchSize)
	if err != nil {
		return err
	}
	if len(articles) == 0 {
		fmt.Println("no articles waiting to be analyzed")
		return nil
	}

	analyzed, failed := analyzeAll(ctx, p, log, articles)

	aboveThreshold := 0
	for _, a := range analyzed {
		if err := db.SaveAnalysis(ctx, a); err != nil {
			return err
		}
		if a.Score >= p.Threshold() {
			aboveThreshold++
		}
	}

	fmt.Printf("analyzed %d articles, %d above threshold %d (%d failed)\n",
		len(analyzed), aboveThreshold, p.Threshold(), failed)
	return nil
}

func buildAnalyzer(cfg config.Config, interests config.Interests, offline bool) (pipeline.Analyzer, error) {
	if offline {
		return pipeline.NewDeterministic(interests), nil
	}
	return pipeline.NewClaude(pipeline.ClaudeOptions{
		APIKey:       cfg.AnthropicAPIKey,
		FastModel:    cfg.FastModel,
		CapableModel: cfg.CapableModel,
		Interests:    interests,
	})
}

// analyzeAll runs the pipeline over the batch, a few articles at a time, and
// returns the ones that succeeded. A failure costs that article, not the run:
// it stays unanalyzed and is picked up again next time.
func analyzeAll(ctx context.Context, p *pipeline.Pipeline, log *slog.Logger,
	articles []domain.Article) (analyzed []domain.Article, failed int) {

	var mu sync.Mutex

	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(maxParallelAnalyses)

	for _, article := range articles {
		group.Go(func() error {
			result, err := p.Analyze(ctx, article)

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				log.Error("analysis failed", "url", article.URL, "error", err)
				failed++
				return nil
			}
			analyzed = append(analyzed, result)
			return nil
		})
	}
	_ = group.Wait()

	return analyzed, failed
}
