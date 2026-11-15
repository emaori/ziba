package domain

import "testing"

// An essay sent by email has no external original: its address is the synthetic
// identifier of the message, which no browser can open. The reader uses this to
// decide whether to offer the link at all.
func TestHasOriginal(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://example.com/a", true},
		{"http://example.com/a", true},
		{"imap://Newsletters/82206B41-C003", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := (Article{URL: tt.url}).HasOriginal(); got != tt.want {
			t.Errorf("HasOriginal(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}
