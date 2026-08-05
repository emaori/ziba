package main

import (
	"context"
	"flag"
	"fmt"
)

// processCmd runs the AI pipeline over articles that have not been analyzed.
func processCmd(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("process", flag.ContinueOnError)
	batchSize := flags.Int("batch", 50, "maximum number of articles to analyze in one run")
	offline := flags.Bool("offline", false, "use the deterministic analyzer: no model, no network, no cost")
	if err := flags.Parse(args); err != nil {
		return err
	}

	mode := analyzerReal
	if *offline {
		mode = analyzerOffline
	}

	s, err := newSetup(ctx, mode)
	if err != nil {
		return err
	}
	defer s.Close()

	analyzed, above, failed, err := s.runner.Analyze(ctx, *batchSize)
	if err != nil {
		return err
	}
	if analyzed == 0 && failed == 0 {
		fmt.Println("no articles waiting to be analyzed")
		return nil
	}

	fmt.Printf("analyzed %d articles, %d above threshold %d (%d failed)\n",
		analyzed, above, s.runner.Threshold(), failed)
	return nil
}
