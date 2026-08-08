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
      days: 3
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
	if opts.LookBackDays != 3 {
		t.Errorf("LookBackDays = %d, want 3", opts.LookBackDays)
	}
	// A mailbox address is not a web address and must survive intact.
	if want := "imaps://imap.example.com:993/"; sources[0].URL != want {
		t.Errorf("URL = %q, want %q", sources[0].URL, want)
	}
}

// A mailbox is read like a feed: a window of recent days, never the read flag.
// One day suits a collection interval of a few hours.
func TestNewsletterDefaultsToOneDay(t *testing.T) {
	t.Setenv("ZIBA_TEST_IMAP_USER", "reader")
	t.Setenv("ZIBA_TEST_IMAP_PASSWORD", "secret")

	sources, err := LoadSources(writeSources(t, `
sources:
  - name: "Newsletters"
    type: newsletter
    url: "imaps://imap.example.com"
    newsletter:
      username_env: ZIBA_TEST_IMAP_USER
      password_env: ZIBA_TEST_IMAP_PASSWORD
`))
	if err != nil {
		t.Fatalf("LoadSources returned error: %v", err)
	}
	if got := sources[0].Newsletter.LookBackDays; got != DefaultNewsletterDays {
		t.Errorf("LookBackDays = %d, want %d", got, DefaultNewsletterDays)
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
		{"negative days", "sources:\n  - name: N\n    type: newsletter\n    url: \"imaps://imap.example.com\"\n" +
			"    newsletter:\n      username_env: ZIBA_TEST_IMAP_USER\n      password_env: ZIBA_TEST_IMAP_PASSWORD\n      days: -1\n"},
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

func TestLoadRoundupSource(t *testing.T) {
	sources, err := LoadSources(writeSources(t, `
sources:
  - name: "Weekly digest"
    type: rss
    url: "https://example.com/rss"
    roundup: true
  - name: "Ordinary feed"
    type: rss
    url: "https://example.com/feed"
`))
	if err != nil {
		t.Fatalf("LoadSources returned error: %v", err)
	}
	if !sources[0].Roundup {
		t.Error("Roundup = false for a source marked roundup: true")
	}
	// The default matters more than the flag: every other feed must keep
	// behaving as it did.
	if sources[1].Roundup {
		t.Error("Roundup = true for a source that never mentioned it")
	}
}

func TestLoadSourcesRejectsRoundupOnMailbox(t *testing.T) {
	t.Setenv("TEST_IMAP_USER", "reader")
	t.Setenv("TEST_IMAP_PASS", "secret")

	_, err := LoadSources(writeSources(t, `
sources:
  - name: "Mailbox"
    type: newsletter
    url: "imaps://mail.example.com"
    roundup: true
    newsletter:
      username_env: TEST_IMAP_USER
      password_env: TEST_IMAP_PASS
`))
	if err == nil {
		t.Fatal("LoadSources accepted roundup on a newsletter, want it rejected")
	}
	if !strings.Contains(err.Error(), "roundup applies to type rss") {
		t.Errorf("error %q does not explain that roundup is for feeds", err)
	}
}

func TestLoadSourcesMissingFile(t *testing.T) {
	if _, err := LoadSources(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Error("LoadSources returned no error for a missing file, want one")
	}
}
