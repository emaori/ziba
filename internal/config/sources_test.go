package config

import (
	"os"
	"path/filepath"
	"strings"
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

func TestLoadNewsletterSource(t *testing.T) {
	t.Setenv("ZIBA_TEST_IMAP_USER", "reader")
	t.Setenv("ZIBA_TEST_IMAP_PASSWORD", "secret")

	path := writeSources(t, `
sources:
  - name: "Newsletters"
    type: newsletter
    url: "imaps://imap.example.com:993/"
    newsletter:
      folder: "Newsletters"
      username_env: ZIBA_TEST_IMAP_USER
      password_env: ZIBA_TEST_IMAP_PASSWORD
      max_messages: 20
`)

	sources, err := LoadSources(path)
	if err != nil {
		t.Fatalf("LoadSources returned error: %v", err)
	}
	if len(sources) != 1 || sources[0].Newsletter == nil {
		t.Fatal("newsletter options were not loaded")
	}

	opts := sources[0].Newsletter
	if opts.Folder != "Newsletters" {
		t.Errorf("Folder = %q, want %q", opts.Folder, "Newsletters")
	}
	// Reading unseen messages only is what keeps a nightly run cheap on a
	// mailbox with years of history, so it must be the default.
	if !opts.UnreadOnly {
		t.Error("UnreadOnly = false, want true by default")
	}
	// A mailbox address is not a web address and must survive intact.
	if want := "imaps://imap.example.com:993/"; sources[0].URL != want {
		t.Errorf("URL = %q, want %q", sources[0].URL, want)
	}
}

func TestLoadNewsletterSourceRejects(t *testing.T) {
	t.Setenv("ZIBA_TEST_IMAP_USER", "reader")
	t.Setenv("ZIBA_TEST_IMAP_PASSWORD", "secret")

	const block = `
    newsletter:
      username_env: ZIBA_TEST_IMAP_USER
      password_env: ZIBA_TEST_IMAP_PASSWORD
`
	tests := []struct {
		name    string
		content string
	}{
		{"web address", "sources:\n  - name: N\n    type: newsletter\n    url: \"https://example.com\"" + block},
		{"credentials in the address", "sources:\n  - name: N\n    type: newsletter\n    url: \"imaps://user:pass@imap.example.com\"" + block},
		{"no host", "sources:\n  - name: N\n    type: newsletter\n    url: \"imaps://\"" + block},
		{"missing block", "sources:\n  - name: N\n    type: newsletter\n    url: \"imaps://imap.example.com\"\n"},
		{"unset variable", "sources:\n  - name: N\n    type: newsletter\n    url: \"imaps://imap.example.com\"\n" +
			"    newsletter:\n      username_env: ZIBA_TEST_NOT_SET\n      password_env: ZIBA_TEST_IMAP_PASSWORD\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := LoadSources(writeSources(t, tt.content)); err == nil {
				t.Error("LoadSources returned no error, want one")
			}
		})
	}
}

// Scraping was removed, and someone will eventually copy an old configuration
// forward. The refusal has to explain itself rather than say "unknown type".
func TestLoadSourcesRejectsRetiredWebsiteType(t *testing.T) {
	_, err := LoadSources(writeSources(t, `
sources:
  - name: "Some site"
    type: website
    url: "https://example.com/news"
`))
	if err == nil {
		t.Fatal("LoadSources accepted a website source, want it rejected")
	}
	for _, want := range []string{"scraping was removed", "RSS", "newsletter"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestLoadSourcesMissingFile(t *testing.T) {
	if _, err := LoadSources(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Error("LoadSources returned no error for a missing file, want one")
	}
}
