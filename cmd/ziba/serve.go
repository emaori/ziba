package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"time"

	"github.com/emaori/ziba/internal/config"
	"github.com/emaori/ziba/internal/domain"
	"github.com/emaori/ziba/internal/store"
	"github.com/emaori/ziba/internal/web"
)

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

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	interests, err := config.LoadInterests(cfg.InterestsPath)
	if err != nil {
		return err
	}

	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	selected, err := db.GenerateDigest(ctx, date, domain.RelevanceScore(interests.Threshold))
	if err != nil {
		return err
	}

	fmt.Printf("digest for %s: %d articles at or above %d\n",
		date.Format(time.DateOnly), selected, interests.Threshold)
	if selected == 0 {
		fmt.Println("nothing cleared the threshold — lower it in the interests file, or collect more")
	}
	return nil
}

// serveCmd runs the web interface until interrupted.
func serveCmd(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := flags.String("addr", ":8080", "address to listen on")
	if err := flags.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	server, err := web.New(db)
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

	// Serve in the background so the main path can wait on cancellation.
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
		// Give in-flight requests a moment to finish rather than cutting them off.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()

		fmt.Println("\nshutting down")
		return httpServer.Shutdown(shutdownCtx)
	}
}
