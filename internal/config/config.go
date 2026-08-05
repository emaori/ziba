// Package config loads runtime configuration. Secrets come from the
// environment; everything else will come from hand-edited YAML files.
package config

import (
	"fmt"
	"os"
	"time"
)

// DefaultSourcesPath is where the hand-edited source list lives unless
// ZIBA_SOURCES_FILE says otherwise.
const DefaultSourcesPath = "config/sources.yaml"

// Config is the configuration the whole program needs to start.
type Config struct {
	// DatabaseURL is a PostgreSQL connection string, e.g.
	// postgres://user:password@host:5432/ziba?sslmode=disable
	DatabaseURL string

	// SourcesPath points at the YAML list of configured sources.
	SourcesPath string

	// InterestsPath points at the YAML description of what is worth reading.
	InterestsPath string

	// AnthropicAPIKey authenticates the AI pipeline. Empty is not an error at
	// load time: collection and migration do not need it.
	AnthropicAPIKey string

	// FastModel handles extraction and scoring, CapableModel the summaries.
	// Empty means the pipeline's own defaults.
	FastModel    string
	CapableModel string

	// RenderURL points at the browser sidecar. Empty disables rendering, which
	// only matters for sources that ask for it.
	RenderURL string

	// CollectEvery is how often the unattended schedule collects and analyzes.
	CollectEvery time.Duration

	// DigestAt is when it builds the day's selection.
	DigestAt TimeOfDay
}

// Load reads the configuration from the environment.
func Load() (Config, error) {
	cfg := Config{
		DatabaseURL:     os.Getenv("ZIBA_DATABASE_URL"),
		SourcesPath:     envOr("ZIBA_SOURCES_FILE", DefaultSourcesPath),
		InterestsPath:   envOr("ZIBA_INTERESTS_FILE", DefaultInterestsPath),
		AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
		FastModel:       os.Getenv("ZIBA_FAST_MODEL"),
		CapableModel:    os.Getenv("ZIBA_CAPABLE_MODEL"),
		RenderURL:       os.Getenv("ZIBA_RENDER_URL"),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("ZIBA_DATABASE_URL is not set")
	}

	// The schedule is parsed here rather than where it is used, so a mistyped
	// value fails at startup instead of at half past six some morning.
	cfg.CollectEvery = DefaultCollectEvery
	if raw := os.Getenv("ZIBA_COLLECT_EVERY"); raw != "" {
		every, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("ZIBA_COLLECT_EVERY: %w", err)
		}
		if every > 0 && every < time.Minute {
			return Config{}, fmt.Errorf("ZIBA_COLLECT_EVERY: %s is too often; use a minute or more", every)
		}
		cfg.CollectEvery = every
	}

	digestAt, err := ParseTimeOfDay(envOr("ZIBA_DIGEST_AT", DefaultDigestAt))
	if err != nil {
		return Config{}, fmt.Errorf("ZIBA_DIGEST_AT: %w", err)
	}
	cfg.DigestAt = digestAt

	return cfg, nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
