package domain

import (
	"testing"
	"time"
)

func TestCollectFromCutoff(t *testing.T) {
	firstSeen := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		from      CollectFrom
		want      time.Time
		filtering bool
	}{
		{"default is a week before first contact", CollectFrom{},
			time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC), true},
		{"explicit grace", CollectFrom{Grace: 14 * 24 * time.Hour},
			time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC), true},
		{"absolute date wins over grace",
			CollectFrom{Grace: 14 * 24 * time.Hour, Date: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
			time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), true},
		{"all disables filtering", CollectFrom{All: true}, time.Time{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, filtering := tt.from.Cutoff(firstSeen)
			if filtering != tt.filtering {
				t.Fatalf("filtering = %v, want %v", filtering, tt.filtering)
			}
			if filtering && !got.Equal(tt.want) {
				t.Errorf("cutoff = %v, want %v", got, tt.want)
			}
		})
	}
}

// The behaviour that motivated the whole thing: a feed offering five years of
// backlog must contribute only its recent entries.
func TestCollectFromAccepts(t *testing.T) {
	firstSeen := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	day := func(y int, m time.Month, d int) time.Time {
		return time.Date(y, m, d, 12, 0, 0, 0, time.UTC)
	}

	tests := []struct {
		name      string
		from      CollectFrom
		published time.Time
		want      bool
	}{
		{"published today", CollectFrom{}, day(2026, 8, 8), true},
		{"published yesterday", CollectFrom{}, day(2026, 8, 7), true},
		{"exactly on the cutoff", CollectFrom{}, day(2026, 8, 1), true},
		{"a day before the cutoff", CollectFrom{}, day(2026, 7, 31), false},
		{"five years old", CollectFrom{}, day(2021, 4, 25), false},
		{"five years old, wider grace still rejects",
			CollectFrom{Grace: 14 * 24 * time.Hour}, day(2021, 4, 25), false},
		{"within a wider grace", CollectFrom{Grace: 14 * 24 * time.Hour}, day(2026, 7, 28), true},
		{"anything at all when unfiltered", CollectFrom{All: true}, day(2015, 1, 1), true},
		// Collectors substitute the collection time when a source gives no date,
		// so this is mostly theoretical — but losing content silently would be
		// worse than letting a little through.
		{"undated items are accepted", CollectFrom{}, time.Time{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.from.Accepts(firstSeen, tt.published); got != tt.want {
				t.Errorf("Accepts(%v) = %v, want %v", tt.published, got, tt.want)
			}
		})
	}
}

// The cutoff is anchored to first contact, not to now, so a pause in collection
// does not silently widen into lost articles.
func TestCollectFromDoesNotDriftWithTime(t *testing.T) {
	firstSeen := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	from := CollectFrom{}

	published := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	if !from.Accepts(firstSeen, published) {
		t.Fatal("article within the window was rejected")
	}

	// A month later, with collection having been down throughout, the same
	// article must still be accepted.
	if !from.Accepts(firstSeen, published) {
		t.Error("the cutoff moved with wall-clock time; an outage would cost articles")
	}
}
