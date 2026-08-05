// Package config loads runtime configuration. Secrets come from the
// environment; everything else will come from hand-edited YAML files.
package config

import (
	"fmt"
	"os"
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
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("ZIBA_DATABASE_URL is not set")
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
