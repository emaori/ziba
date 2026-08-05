package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"time"

	"github.com/emaori/ziba/internal/collect"
	"github.com/emaori/ziba/internal/config"
	"github.com/emaori/ziba/internal/domain"
	"github.com/emaori/ziba/internal/store"
)

// httpTimeout bounds every outbound request. Generous enough for a slow site,
// short enough that one bad source does not stall the run.
const httpTimeout = 30 * time.Second

// renderTimeout bounds a request to the browser sidecar, which has to start a
// page and wait for its scripts before it can answer.
const renderTimeout = 90 * time.Second

// collectCmd reads every enabled source, stores what is new, and turns the new
// raw items into articles by retrieving their full text.
func collectCmd(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("collect", flag.ContinueOnError)
	batchSize := flags.Int("batch", 100, "maximum number of raw items to turn into articles in one run")
	skipFetch := flags.Bool("no-fetch", false, "only collect raw items, do not retrieve full text")
	if err := flags.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	configured, err := config.LoadSources(cfg.SourcesPath)
	if err != nil {
		return err
	}

	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	log := slog.Default()

	sources, err := db.SyncSources(ctx, configured)
	if err != nil {
		return err
	}

	enabled := enabledOnly(sources)
	if len(enabled) == 0 {
		return fmt.Errorf("no enabled sources in %s", cfg.SourcesPath)
	}

	client := collect.NewHTTPClient(httpTimeout)

	// Rendering takes longer than a plain fetch: a browser has to start, load
	// the page and wait for its scripts.
	renderClient := collect.NewHTTPClient(renderTimeout)
	renderer := collect.NewRenderer(renderClient, cfg.RenderURL)

	registry := collect.NewRegistry(
		collect.NewRSS(client, log),
		collect.NewWebsite(client, renderer, log),
	)

	if err := runCollection(ctx, db, registry, log, enabled); err != nil {
		return err
	}
	if *skipFetch {
		return nil
	}
	return runFullText(ctx, db, collect.NewFullText(client), log, *batchSize)
}

// runCollection fans out over the sources and stores what they produced.
func runCollection(ctx context.Context, db *store.Store, registry *collect.Registry,
	log *slog.Logger, sources []domain.Source) error {

	results := registry.Run(ctx, log, sources)

	totalNew, failed := 0, 0
	for _, result := range results {
		if result.Err != nil {
			// A source being down is expected, not a reason to abandon the run.
			log.Error("source failed", "source", result.Source.Name, "error", result.Err)
			failed++
			continue
		}

		inserted, err := db.SaveRawItems(ctx, result.Items)
		if err != nil {
			return err
		}
		totalNew += inserted
		log.Info("collected", "source", result.Source.Name, "found", len(result.Items), "new", inserted)
	}

	fmt.Printf("collected %d new items from %d sources (%d failed)\n", totalNew, len(sources), failed)
	return nil
}

// runFullText turns unprocessed raw items into articles.
func runFullText(ctx context.Context, db *store.Store, fetcher *collect.FullText,
	log *slog.Logger, batchSize int) error {

	items, err := db.UnprocessedRawItems(ctx, batchSize)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Println("no items waiting to be processed")
		return nil
	}

	processed := make([]int64, 0, len(items))
	created := 0

	for _, item := range items {
		// Stop cleanly on Ctrl-C rather than half-way through an item.
		if ctx.Err() != nil {
			break
		}

		article, err := fetcher.Article(ctx, item)
		if err != nil {
			// The article is still usable with the feed excerpt, so this is a
			// warning: the run continues and stores what it has.
			log.Warn("full text unavailable", "url", item.URL, "error", err)
		}

		_, isNew, err := db.SaveArticle(ctx, article)
		if err != nil {
			return err
		}
		if isNew {
			created++
		}
		processed = append(processed, item.ID)
	}

	if err := db.MarkRawItemsProcessed(ctx, processed); err != nil {
		return err
	}

	total, err := db.CountArticles(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("processed %d items, %d new articles (%d stored in total)\n", len(processed), created, total)
	return nil
}

func enabledOnly(sources []domain.Source) []domain.Source {
	enabled := make([]domain.Source, 0, len(sources))
	for _, s := range sources {
		if s.Enabled {
			enabled = append(enabled, s)
		}
	}
	return enabled
}
