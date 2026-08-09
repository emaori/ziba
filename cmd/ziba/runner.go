package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/emaori/ziba/internal/config"
	"github.com/emaori/ziba/internal/job"
	"github.com/emaori/ziba/internal/pipeline"
	"github.com/emaori/ziba/internal/store"
)

// setup is everything a command needs: configuration, an open database, and a
// runner wired from both. Every command builds it the same way, so they cannot
// drift apart in how they interpret the configuration.
type setup struct {
	cfg       config.Config
	store     *store.Store
	runner    *job.Runner
	interests config.Interests
}

// analyzerMode says which analyzer a command wants.
type analyzerMode int

const (
	// analyzerNone skips the AI entirely: for commands that never analyze.
	analyzerNone analyzerMode = iota
	// analyzerReal requires a working provider.
	analyzerReal
	// analyzerOffline uses the deterministic one: no model, no network, no cost.
	analyzerOffline
)

func newSetup(ctx context.Context, mode analyzerMode) (*setup, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}

	// Interests first: a source may declare which of them it belongs to, and
	// those names are validated against this list.
	interests, err := config.LoadInterests(cfg.InterestsPath)
	if err != nil {
		return nil, err
	}

	sources, err := config.LoadSources(cfg.SourcesPath, interests)
	if err != nil {
		return nil, err
	}

	var analyzer pipeline.Analyzer
	switch mode {
	case analyzerOffline:
		analyzer = pipeline.NewDeterministic(interests)
	case analyzerReal:
		analyzer, err = newAnalyzer(cfg, interests)
		if err != nil {
			return nil, err
		}
	}

	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}

	log := slog.Default()
	return &setup{
		cfg:       cfg,
		store:     db,
		runner:    job.New(cfg, sources, interests, db, log, job.Options{Analyzer: analyzer}),
		interests: interests,
	}, nil
}

func (s *setup) Close() { s.store.Close() }

// newAnalyzer builds the configured provider.
//
// Both implement the same interface and answer the same prompts, so this is the
// only place in the program that knows which company is being asked.
func newAnalyzer(cfg config.Config, interests config.Interests) (pipeline.Analyzer, error) {
	key, variable := cfg.APIKey()
	if key == "" {
		return nil, fmt.Errorf("%s is not set, and ZIBA_AI_PROVIDER is %q", variable, cfg.Provider)
	}

	switch cfg.Provider {
	case config.ProviderOpenAI:
		return pipeline.NewOpenAI(pipeline.OpenAIOptions{
			APIKey:       key,
			FastModel:    cfg.FastModel,
			CapableModel: cfg.CapableModel,
			Interests:    interests,
		})
	default:
		return pipeline.NewClaude(pipeline.ClaudeOptions{
			APIKey:       key,
			FastModel:    cfg.FastModel,
			CapableModel: cfg.CapableModel,
			Interests:    interests,
		})
	}
}
