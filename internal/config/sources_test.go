package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/emaori/ziba/internal/domain"
)

// writeSources puts a sources file in a temporary directory and returns its
// path. t.TempDir is cleaned up automatically when the test ends.
func writeSources(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sources.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write sources file: %v", err)
	}
	return path
}

func TestLoadSources(t *testing.T) {
	path := writeSources(t, `
sources:
  - name: "Il Post"
    type: rss
    url: "https://www.ilpost.it/feed/"
  - name: "Paused feed"
    type: rss
    url: "https://example.com/feed"
    enabled: false
`)

	sources, err := LoadSources(path)
	if err != nil {
		t.Fatalf("LoadSources returned error: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("got %d sources, want 2", len(sources))
	}

	first := sources[0]
	if first.Name != "Il Post" {
		t.Errorf("Name = %q, want %q", first.Name, "Il Post")
	}
	if first.Type != domain.SourceTypeRSS {
		t.Errorf("Type = %q, want %q", first.Type, domain.SourceTypeRSS)
	}
	// Normalized at load time, so what reaches the database is canonical.
	if want := "https://ilpost.it/feed"; first.URL != want {
		t.Errorf("URL = %q, want %q", first.URL, want)
	}
	// Absent means enabled: a source is written down in order to be read.
	if !first.Enabled {
		t.Error("Enabled = false, want true when the key is absent")
	}
	if sources[1].Enabled {
		t.Error("Enabled = true, want false when set explicitly")
	}
}

func TestLoadSourcesRejects(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"no sources", "sources: []\n"},
		{"missing name", "sources:\n  - type: rss\n    url: \"https://example.com/feed\"\n"},
		{"unknown type", "sources:\n  - name: X\n    type: carrier-pigeon\n    url: \"https://example.com/feed\"\n"},
		{"invalid url", "sources:\n  - name: X\n    type: rss\n    url: \"not a url\"\n"},
		{"misspelled key", "sources:\n  - name: X\n    type: rss\n    urls: \"https://example.com/feed\"\n"},
		{"duplicate source", `
sources:
  - name: "One"
    type: rss
    url: "https://example.com/feed"
  - name: "Same feed again"
    type: rss
    url: "https://www.example.com/feed/"
`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := LoadSources(writeSources(t, tt.content)); err == nil {
				t.Error("LoadSources returned no error, want one")
			}
		})
	}
}

func TestLoadSourcesMissingFile(t *testing.T) {
	if _, err := LoadSources(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Error("LoadSources returned no error for a missing file, want one")
	}
}
