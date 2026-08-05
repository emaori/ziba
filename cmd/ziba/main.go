// Command ziba is the single entry point of the Ziba monolith: it collects
// content from the configured sources, processes it, and serves the web UI.
package main

import (
	"errors"
	"fmt"
	"os"
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

	switch args[0] {
	case "version":
		fmt.Println(version)
		return nil
	default:
		return fmt.Errorf("unknown command %q: %w", args[0], errUsage)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `ziba — personal content aggregator

usage:
  ziba <command> [flags]

commands:
  version   print the build version
`)
}
