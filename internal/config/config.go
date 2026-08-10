// Package config loads runtime configuration. Secrets come from the
// environment; everything else will come from hand-edited YAML files.
package config

import (
	"fmt"
	"os"
	"slices"
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

	// FastEffort and CapableEffort are how hard each model is asked to think.
	// Empty takes the provider's default. They are ignored by models that have
	// no reasoning setting, which is not an error: the same configuration has
	// to work when the models change.
	FastEffort    ReasoningEffort
	CapableEffort ReasoningEffort

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

	if cfg.FastEffort, err = ParseEffort("ZIBA_FAST_EFFORT", os.Getenv("ZIBA_FAST_EFFORT")); err != nil {
		return Config{}, err
	}
	if cfg.CapableEffort, err = ParseEffort("ZIBA_CAPABLE_EFFORT", os.Getenv("ZIBA_CAPABLE_EFFORT")); err != nil {
		return Config{}, err
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

// ReasoningEffort is how hard a reasoning model is asked to think before it
// answers. It replaces temperature on the models that have it, and it is the
// only knob that meaningfully moves the bill: reasoning tokens are charged as
// output, and output costs six times input on every model in the GPT-5 line.
//
// Empty means send nothing and take the provider's own default, which is
// model-dependent and, on the models documented so far, medium. That is a
// deliberate default rather than an omission — unlike a model name, which
// cannot be guessed and so has none, "whatever the provider thinks" is a
// coherent answer here. It is also the expensive one, which is why both roles
// are worth setting explicitly.
type ReasoningEffort string

// The values the API accepts, cheapest first. None skips reasoning entirely.
const (
	EffortNone    ReasoningEffort = "none"
	EffortMinimal ReasoningEffort = "minimal"
	EffortLow     ReasoningEffort = "low"
	EffortMedium  ReasoningEffort = "medium"
	EffortHigh    ReasoningEffort = "high"
	EffortXHigh   ReasoningEffort = "xhigh"
	EffortMax     ReasoningEffort = "max"
)

var validEfforts = []ReasoningEffort{
	EffortNone, EffortMinimal, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax,
}

// ParseEffort reads one of the effort variables. A misspelling is refused at
// startup: sent to the API it would fail every call, and sent nowhere it would
// silently leave the run on the provider's default and the expensive setting.
func ParseEffort(variable, raw string) (ReasoningEffort, error) {
	value := ReasoningEffort(strings.ToLower(strings.TrimSpace(raw)))
	if value == "" {
		return "", nil
	}
	if slices.Contains(validEfforts, value) {
		return value, nil
	}

	names := make([]string, 0, len(validEfforts))
	for _, effort := range validEfforts {
		names = append(names, string(effort))
	}
	return "", fmt.Errorf("%s is %q; it must be one of %s, or empty for the provider's default",
		variable, raw, strings.Join(names, ", "))
}

// APIKey returns the key for the configured provider, and the name of the
// variable it comes from, so an error can say which one to set.
func (c Config) APIKey() (key, variable string) {
	if c.Provider == ProviderOpenAI {
		return c.OpenAIAPIKey, "OPENAI_API_KEY"
	}
	return c.AnthropicAPIKey, "ANTHROPIC_API_KEY"
}
