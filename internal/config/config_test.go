package config

import (
	"strings"
	"testing"
)

// Which provider answers is configuration, and a typo must fail at startup
// rather than as an authentication error hours into a run.
func TestParseProvider(t *testing.T) {
	tests := []struct {
		raw     string
		want    Provider
		wantErr bool
	}{
		{"", DefaultProvider, false},
		{"anthropic", ProviderAnthropic, false},
		{"openai", ProviderOpenAI, false},
		{"OpenAI", ProviderOpenAI, false},
		{"  openai  ", ProviderOpenAI, false},
		{"gemini", "", true},
		{"opnai", "", true},
	}
	for _, tt := range tests {
		got, err := ParseProvider(tt.raw)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseProvider(%q) error = %v, want error = %v", tt.raw, err, tt.wantErr)
			continue
		}
		if err == nil && got != tt.want {
			t.Errorf("ParseProvider(%q) = %q, want %q", tt.raw, got, tt.want)
		}
		if err != nil && !strings.Contains(err.Error(), "anthropic") {
			t.Errorf("error %q does not list what is available", err)
		}
	}
}

// The key looked for follows the provider, so an error can name the variable
// that is actually missing rather than the other one.
func TestAPIKeyFollowsTheProvider(t *testing.T) {
	cfg := Config{
		Provider:        ProviderOpenAI,
		AnthropicAPIKey: "anthropic-key",
		OpenAIAPIKey:    "openai-key",
	}
	if key, name := cfg.APIKey(); key != "openai-key" || name != "OPENAI_API_KEY" {
		t.Errorf("got %q from %s, want the OpenAI key", key, name)
	}

	cfg.Provider = ProviderAnthropic
	if key, name := cfg.APIKey(); key != "anthropic-key" || name != "ANTHROPIC_API_KEY" {
		t.Errorf("got %q from %s, want the Anthropic key", key, name)
	}
}

// A misspelled effort must not reach the API, and must not be quietly dropped
// either: dropped, it leaves the run on the provider's default, which is the
// expensive one and the reason the variable exists.
func TestParseEffort(t *testing.T) {
	for _, raw := range []string{"low", "LOW", " medium ", "none", "max"} {
		if _, err := ParseEffort("ZIBA_FAST_EFFORT", raw); err != nil {
			t.Errorf("ParseEffort(%q) was refused: %v", raw, err)
		}
	}
	if got, err := ParseEffort("ZIBA_FAST_EFFORT", ""); err != nil || got != "" {
		t.Errorf(`ParseEffort("") = %q, %v; want "" and no error: empty means the provider decides`, got, err)
	}
	if _, err := ParseEffort("ZIBA_FAST_EFFORT", "lowest"); err == nil {
		t.Error("ParseEffort accepted \"lowest\"; a typo must fail at startup, not per call")
	}
}

// A misspelled switch must not read as "off". Turning on a debugging aid and
// getting silence is the failure this prevents: the afternoon is spent looking
// for the bug in the feature rather than in the spelling.
func TestParseBool(t *testing.T) {
	for _, raw := range []string{"true", "TRUE", " yes ", "1", "on"} {
		got, err := ParseBool("ZIBA_MODEL_JOURNAL", raw)
		if err != nil || !got {
			t.Errorf("ParseBool(%q) = %v, %v; want true", raw, got, err)
		}
	}
	for _, raw := range []string{"", "false", "no", "0", "off"} {
		got, err := ParseBool("ZIBA_MODEL_JOURNAL", raw)
		if err != nil || got {
			t.Errorf("ParseBool(%q) = %v, %v; want false", raw, got, err)
		}
	}
	if _, err := ParseBool("ZIBA_MODEL_JOURNAL", "ture"); err == nil {
		t.Error(`ParseBool accepted "ture"; a typo must fail at startup, not read as off`)
	}
}

func TestParseServiceURL(t *testing.T) {
	tests := []struct {
		raw     string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{"http://ziba-browser:3000/", "http://ziba-browser:3000", false},
		{"https://browser.example", "https://browser.example", false},
		{"ziba-browser:3000", "", true},
		{"ftp://browser.example", "", true},
		{"http://user:secret@browser.example", "", true},
		{"http://browser.example?target=x", "", true},
	}
	for _, tt := range tests {
		got, err := parseServiceURL("ZIBA_BROWSER_URL", tt.raw)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("parseServiceURL(%q) = %q, %v; want %q, error=%v", tt.raw, got, err, tt.want, tt.wantErr)
		}
	}
}
