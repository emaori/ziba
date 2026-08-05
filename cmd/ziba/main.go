// Command ziba is the single entry point of the Ziba monolith: it collects
// content from the configured sources, processes it, and serves the web UI.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/emaori/ziba/internal/config"
	"github.com/emaori/ziba/internal/store"
)

// version is overridden at build time via -ldflags (see the Makefile).
var version = "dev"

// errUsage is returned when the command line does not name a known subcommand.
// It is a sentinel: main compares against it with errors.Is to decide whether
// to print usage instead of an error message.
var errUsage = errors.New("usage")

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, errUsage) {
			usage()
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "ziba: %v\n", err)
		os.Exit(1)
	}
}

// run holds the real body of the program. Keeping it separate from main means
// every exit path returns an error instead of calling os.Exit, which makes the
// logic testable.
func run(args []string) error {
	if len(args) == 0 {
		return errUsage
	}

	// Ctrl-C and the signal Docker sends on stop cancel the context, so a
	// command in the middle of network or database work can stop cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch args[0] {
	case "version":
		fmt.Println(version)
		return nil
	case "migrate":
		return migrateCmd(ctx)
	case "collect":
		return collectCmd(ctx, args[1:])
	case "process":
		return processCmd(ctx, args[1:])
	default:
		return fmt.Errorf("unknown command %q: %w", args[0], errUsage)
	}
}

// migrateCmd brings the database schema up to date.
func migrateCmd(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	applied, err := db.Migrate(ctx)
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		fmt.Println("schema is up to date")
		return nil
	}
	for _, name := range applied {
		fmt.Printf("applied %s\n", name)
	}
	return nil
}

func usage() {
	fmt.Fprint(os.Stderr, `ziba — personal content aggregator

usage:
  ziba <command> [flags]

commands:
  version   print the build version
  migrate   apply pending database migrations
  collect   read every enabled source and store what is new
  process   run the AI pipeline over articles not yet analyzed
`)
}
