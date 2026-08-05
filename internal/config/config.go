// Package config loads runtime configuration. Secrets come from the
// environment; everything else will come from hand-edited YAML files.
package config

import (
	"fmt"
	"os"
)

// Config is the configuration the whole program needs to start.
type Config struct {
	// DatabaseURL is a PostgreSQL connection string, e.g.
	// postgres://user:password@host:5432/ziba?sslmode=disable
	DatabaseURL string
}

// Load reads the configuration from the environment.
func Load() (Config, error) {
	cfg := Config{
		DatabaseURL: os.Getenv("ZIBA_DATABASE_URL"),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("ZIBA_DATABASE_URL is not set")
	}
	return cfg, nil
}
