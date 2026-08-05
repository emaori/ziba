package domain

import "testing"

func TestNormalizeURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"already canonical", "https://ilpost.it/2026/08/05/story", "https://ilpost.it/2026/08/05/story"},
		{"uppercase scheme and host", "HTTPS://Www.IlPost.IT/story", "https://ilpost.it/story"},
		{"default port removed", "https://ilpost.it:443/story", "https://ilpost.it/story"},
		{"other port kept", "http://ilpost.it:8080/story", "http://ilpost.it:8080/story"},
		{"fragment removed", "https://ilpost.it/story#section-2", "https://ilpost.it/story"},
		{"trailing slash removed", "https://ilpost.it/story/", "https://ilpost.it/story"},
		{"bare host", "https://ilpost.it/", "https://ilpost.it"},
		{"tracking removed", "https://ilpost.it/story?utm_source=news&utm_campaign=x", "https://ilpost.it/story"},
		{"click id removed", "https://ilpost.it/story?fbclid=abc", "https://ilpost.it/story"},
		{"meaningful query kept", "https://ilpost.it/story?page=2", "https://ilpost.it/story?page=2"},
		{"query order normalized", "https://ilpost.it/s?b=2&a=1", "https://ilpost.it/s?a=1&b=2"},
		{"mixed query", "https://ilpost.it/s?utm_medium=rss&id=7", "https://ilpost.it/s?id=7"},
		{"whitespace trimmed", "  https://ilpost.it/story  ", "https://ilpost.it/story"},
		{"scheme not upgraded", "http://ilpost.it/story", "http://ilpost.it/story"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeURL(tt.in)
			if err != nil {
				t.Fatalf("NormalizeURL(%q) returned error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("NormalizeURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNormalizeURLRejects(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"blank", "   "},
		{"relative", "/story"},
		{"unsupported scheme", "mailto:someone@example.com"},
		{"no host", "https://"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := NormalizeURL(tt.in); err == nil {
				t.Errorf("NormalizeURL(%q) = %q, want an error", tt.in, got)
			}
		})
	}
}

// The same article arriving from three different places must collapse to one
// identity — this is what keeps it from being stored three times.
func TestNormalizeURLCollapsesDuplicates(t *testing.T) {
	variants := []string{
		"https://www.ilpost.it/2026/08/05/story/",
		"https://ilpost.it/2026/08/05/story?utm_source=newsletter&utm_medium=email",
		"HTTPS://ilpost.it:443/2026/08/05/story#top",
	}

	want, err := NormalizeURL(variants[0])
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, v := range variants[1:] {
		got, err := NormalizeURL(v)
		if err != nil {
			t.Fatalf("NormalizeURL(%q) returned error: %v", v, err)
		}
		if got != want {
			t.Errorf("NormalizeURL(%q) = %q, want %q", v, got, want)
		}
	}
}
