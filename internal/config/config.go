// Package config loads runtime configuration. Secrets come from the
// environment; everything else will come from hand-edited YAML files.
package config

import (
	"fmt"
	"os"
	"strings"
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

	// Provider names which company answers the AI pipeline. Either is a
	// complete implementation; the choice is about cost and judgement, which is
	// why it is configuration and not a rewrite.
	Provider Provider

	// AnthropicAPIKey and OpenAIAPIKey authenticate the pipeline. Empty is not
	// an error at load time: collection and migration do not need either.
	AnthropicAPIKey string
	OpenAIAPIKey    string

	// FastModel handles extraction and scoring, CapableModel the summaries.
	// Empty means the provider's own defaults, where it has any — OpenAI
	// deliberately has none, since its model names change faster than a
	// constant here can follow.
	FastModel    string
	CapableModel string

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
		OpenAIAPIKey:    os.Getenv("OPENAI_API_KEY"),
		FastModel:       os.Getenv("ZIBA_FAST_MODEL"),
		CapableModel:    os.Getenv("ZIBA_CAPABLE_MODEL"),
	}

	provider, err := ParseProvider(os.Getenv("ZIBA_AI_PROVIDER"))
	if err != nil {
		return Config{}, err
	}
	cfg.Provider = provider
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

// Provider names an AI provider.
type Provider string

const (
	ProviderAnthropic Provider = "anthropic"
	ProviderOpenAI    Provider = "openai"
)

// DefaultProvider is used when none is named. Anthropic, because it is the one
// the models and the cost estimates in the documentation were written against.
const DefaultProvider = ProviderAnthropic

// ParseProvider reads ZIBA_AI_PROVIDER. An unknown name is refused at startup
// rather than at the first article, and the message lists what is available —
// a typo here would otherwise surface as an authentication failure hours later.
func ParseProvider(raw string) (Provider, error) {
	switch value := Provider(strings.ToLower(strings.TrimSpace(raw))); value {
	case "":
		return DefaultProvider, nil
	case ProviderAnthropic, ProviderOpenAI:
		return value, nil
	default:
		return "", fmt.Errorf("ZIBA_AI_PROVIDER is %q; it must be %q or %q",
			raw, ProviderAnthropic, ProviderOpenAI)
	}
}

// APIKey returns the key for the configured provider, and the name of the
// variable it comes from, so an error can say which one to set.
func (c Config) APIKey() (key, variable string) {
	if c.Provider == ProviderOpenAI {
		return c.OpenAIAPIKey, "OPENAI_API_KEY"
	}
	return c.AnthropicAPIKey, "ANTHROPIC_API_KEY"
}
