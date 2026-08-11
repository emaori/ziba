package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/emaori/ziba/internal/job"
	"github.com/emaori/ziba/internal/web"
)

// scheduleBatchSize bounds how much one unattended pass handles per stage.
const scheduleBatchSize = 100

// digestCmd builds the selection for a day.
func digestCmd(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("digest", flag.ContinueOnError)
	day := flags.String("date", "", "the day to build, as YYYY-MM-DD (default today)")
	if err := flags.Parse(args); err != nil {
		return err
	}

	date := time.Now()
	if *day != "" {
		parsed, err := time.Parse(time.DateOnly, *day)
		if err != nil {
			return fmt.Errorf("invalid -date: %w", err)
		}
		date = parsed
	}

	s, err := newSetup(ctx, analyzerNone)
	if err != nil {
		return err
	}
	defer s.Close()

	selected, err := s.runner.Digest(ctx, date)
	if err != nil {
		return err
	}

	// Not "at or above the threshold": a source that declares its categories is
	// always selected, whatever it scored.
	fmt.Printf("digest for %s: %d articles (threshold %d, not applied to sources that declare their categories)\n",
		date.Format(time.DateOnly), selected, s.runner.Threshold())
	if selected == 0 {
		fmt.Println("nothing cleared the threshold — lower it in the interests file, or collect more")
	}
	return nil
}

// runCmd performs the whole nightly chain once, then exits. It is what the
// scheduler does on a timer, available to run by hand.
func runCmd(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	batchSize := flags.Int("batch", 100, "maximum number of items to handle per stage")
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

	return s.runner.Daily(ctx, *batchSize)
}

// serveCmd runs the web interface, and — unless told otherwise — the schedule
// alongside it.
func serveCmd(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	// A flag, and deliberately not an environment variable.
	//
	// Where Ziba listens is not deployment configuration: inside a container it
	// is always :8080, which EXPOSE, the healthcheck and the port mapping all
	// assume. What varies between deployments is the mapping, and that belongs
	// to compose. ZIBA_PORT there is the host side of it.
	//
	// This was briefly read from ZIBA_PORT as well, which gave one name two
	// meanings — host port to compose, listen port to the program — in a file
	// both of them read. Setting it then moved the two ends of the mapping in
	// opposite directions. One name, one meaning; the flag covers the only case
	// that needs it, which is running two of these locally at once.
	addr := flags.String("addr", ":8080", "address to listen on")
	noSchedule := flags.Bool("no-schedule", false, "serve only, do not collect or build digests on a timer")
	offline := flags.Bool("offline", false, "schedule with the deterministic analyzer: no model, no network, no cost")
	if err := flags.Parse(args); err != nil {
		return err
	}

	mode := analyzerNone
	if !*noSchedule {
		// Without a key the scheduler still collects, retrieves and selects; it
		// only loses the analysis stage. That is worth having, so a missing key
		// must not stop the server from starting.
		mode = analyzerReal
		if *offline {
			mode = analyzerOffline
		}
	}

	s, err := newSetup(ctx, mode)
	if err != nil && mode == analyzerReal {
		slog.Warn("analysis unavailable, scheduling without it", "error", err)
		s, err = newSetup(ctx, analyzerNone)
	}
	if err != nil {
		return err
	}
	defer s.Close()

	// Bring the schema up to date before serving. In a container there is no
	// opportunity to run migrations by hand, and they are idempotent and
	// lock-protected, so doing it here is both safe and the only sane moment.
	applied, err := s.store.Migrate(ctx)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	for _, name := range applied {
		slog.Info("migration applied", "name", name)
	}

	server, err := web.New(s.store, s.interests)
	if err != nil {
		return fmt.Errorf("build web server: %w", err)
	}

	httpServer := &http.Server{
		Addr:    *addr,
		Handler: server.Handler(),
		// A reader on a slow connection is normal; a client that opens a socket
		// and never speaks is not. These bound the second case.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	var wg sync.WaitGroup
	if !*noSchedule {
		scheduler := job.NewScheduler(s.runner, job.Schedule{
			Every:    s.cfg.CollectEvery,
			DigestAt: s.cfg.DigestAt,
		}, scheduleBatchSize, slog.Default())

		wg.Add(1)
		go func() {
			defer wg.Done()
			scheduler.Run(ctx)
		}()
	}

	errs := make(chan error, 1)
	go func() {
		fmt.Printf("listening on http://localhost%s\n", *addr)
		errs <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-errs:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		// Give in-flight requests a moment to finish rather than cutting them
		// off, and let a scheduled run notice the cancellation.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()

		fmt.Println("\nshutting down")
		err := httpServer.Shutdown(shutdownCtx)
		wg.Wait()
		return err
	}
}
