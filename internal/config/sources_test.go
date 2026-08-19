package config

import (
	"testing"
	"time"

	"github.com/emaori/ziba/internal/domain"
)

func TestSourceAddress(t *testing.T) {
	tests := []struct {
		name       string
		sourceType domain.SourceType
		raw        string
		want       string
		wantErr    bool
	}{
		{"rss", domain.SourceTypeRSS, "https://www.example.com/feed/", "https://example.com/feed", false},
		{"mailbox", domain.SourceTypeNewsletter, "imaps://mail.example.com:993", "imaps://mail.example.com:993", false},
		{"mailbox credentials", domain.SourceTypeNewsletter, "imaps://user:pass@mail.example.com", "", true},
		{"mailbox web scheme", domain.SourceTypeNewsletter, "https://mail.example.com", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SourceAddress(tt.sourceType, tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("SourceAddress() error = %v, want error %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("SourceAddress() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseCollectFrom(t *testing.T) {
	day, err := ParseCollectFrom("7d")
	if err != nil || day.Grace != 7*24*time.Hour {
		t.Fatalf("ParseCollectFrom(7d) = %+v, %v", day, err)
	}
	all, err := ParseCollectFrom("all")
	if err != nil || !all.All {
		t.Fatalf("ParseCollectFrom(all) = %+v, %v", all, err)
	}
	if _, err := ParseCollectFrom("never"); err == nil {
		t.Fatal("ParseCollectFrom accepted an invalid value")
	}
}
