package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

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

	// Open the journal before analyzer construction so an unwritable log path is
	// fatal rather than mistaken for an unavailable analyzer.
	var journal *pipeline.Journal
	if cfg.ModelJournal {
		if journal, err = pipeline.OpenJournal(cfg.LogDir); err != nil {
			return nil, err
		}
		slog.Info("recording every model request", "file", journal.Path())
	}

	var analyzer pipeline.Analyzer
	switch mode {
	case analyzerOffline:
		analyzer = pipeline.NewDeterministic(interests)
	case analyzerReal:
		analyzer, err = newAnalyzer(cfg, interests, journal)
		if err != nil {
			// Mark the only startup failure that serve may continue past.
			return nil, fmt.Errorf("%w: %w", errAnalyzerUnavailable, err)
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

// errAnalyzerUnavailable marks the one startup failure that serve tolerates:
// the AI provider could not be built, usually because no API key is set.
// Collection, retrieval and reading all still work without it, so refusing to
// start would cost more than it saves. Nothing else is recoverable that way.
var errAnalyzerUnavailable = errors.New("analysis unavailable")

// newAnalyzer builds the configured provider.
//
// Both implement the same interface and answer the same prompts, so this is the
// only place in the program that knows which company is being asked.
func newAnalyzer(cfg config.Config, interests config.Interests, journal *pipeline.Journal) (pipeline.Analyzer, error) {
	key, variable := cfg.APIKey()
	if key == "" {
		return nil, fmt.Errorf("%s is not set, and ZIBA_AI_PROVIDER is %q", variable, cfg.Provider)
	}

	// Wrapping the provider transport records every model request. The journal
	// writes synchronously and needs no close lifecycle.
	var client *http.Client
	if journal != nil {
		client = &http.Client{Transport: journal.Transport(nil)}
	}

	switch cfg.Provider {
	case config.ProviderOpenAI:
		return pipeline.NewOpenAI(pipeline.OpenAIOptions{
			APIKey:        key,
			FastModel:     cfg.FastModel,
			CapableModel:  cfg.CapableModel,
			FastEffort:    cfg.FastEffort,
			CapableEffort: cfg.CapableEffort,
			Interests:     interests,
			HTTPClient:    client,
		})
	default:
		return pipeline.NewClaude(pipeline.ClaudeOptions{
			APIKey:       key,
			FastModel:    cfg.FastModel,
			CapableModel: cfg.CapableModel,
			Interests:    interests,
			HTTPClient:   client,
		})
	}
}
