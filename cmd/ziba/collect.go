package main

import (
	"context"
	"flag"
	"fmt"
)

// collectCmd reads every enabled source, stores what is new, and turns the new
// raw items into articles by retrieving their full text.
func collectCmd(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("collect", flag.ContinueOnError)
	batchSize := flags.Int("batch", 100, "maximum number of raw items to turn into articles in one run")
	skipFetch := flags.Bool("no-fetch", false, "only collect raw items, do not retrieve full text")
	if err := flags.Parse(args); err != nil {
		return err
	}

	s, err := newSetup(ctx, analyzerNone)
	if err != nil {
		return err
	}
	defer s.Close()

	collected, err := s.runner.Collect(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("collected %d new items from %d sources (%d failed)\n",
		collected.New, collected.Sources, collected.Failed)

	if *skipFetch {
		return nil
	}

	// Before the full text, because opening one issue of a link digest is what
	// produces the items the next stage retrieves.
	opened, queued, err := s.runner.Expand(ctx, *batchSize)
	if err != nil {
		return err
	}
	if opened > 0 {
		fmt.Printf("opened %d roundups, queued %d articles\n", opened, queued)
	}

	processed, created, err := s.runner.Hydrate(ctx, *batchSize)
	if err != nil {
		return err
	}
	if processed == 0 {
		fmt.Println("no items waiting to be processed")
		return nil
	}

	total, err := s.store.CountArticles(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("processed %d items, %d new articles (%d stored in total)\n", processed, created, total)
	return nil
}
