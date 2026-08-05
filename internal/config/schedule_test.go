package config

import (
	"testing"
	"time"
)

func TestParseTimeOfDay(t *testing.T) {
	tests := []struct {
		in   string
		want TimeOfDay
	}{
		{"06:30", TimeOfDay{6, 30}},
		{"0:00", TimeOfDay{0, 0}},
		{"23:59", TimeOfDay{23, 59}},
		{" 7:05 ", TimeOfDay{7, 5}},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseTimeOfDay(tt.in)
			if err != nil {
				t.Fatalf("ParseTimeOfDay(%q) returned error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseTimeOfDay(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseTimeOfDayRejects(t *testing.T) {
	for _, in := range []string{"", "0630", "24:00", "12:60", "-1:00", "noon", "12:", ":30", "a:b"} {
		t.Run(in, func(t *testing.T) {
			if got, err := ParseTimeOfDay(in); err == nil {
				t.Errorf("ParseTimeOfDay(%q) = %v, want an error", in, got)
			}
		})
	}
}

func TestTimeOfDayNext(t *testing.T) {
	at := TimeOfDay{Hour: 6, Minute: 30}

	tests := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			name: "earlier the same day",
			now:  time.Date(2026, 8, 5, 4, 0, 0, 0, time.UTC),
			want: time.Date(2026, 8, 5, 6, 30, 0, 0, time.UTC),
		},
		{
			name: "later the same day rolls over",
			now:  time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC),
			want: time.Date(2026, 8, 6, 6, 30, 0, 0, time.UTC),
		},
		{
			// Strictly after, so firing at exactly the scheduled instant
			// schedules tomorrow rather than looping on today.
			name: "exactly now rolls over",
			now:  time.Date(2026, 8, 5, 6, 30, 0, 0, time.UTC),
			want: time.Date(2026, 8, 6, 6, 30, 0, 0, time.UTC),
		},
		{
			name: "across a month boundary",
			now:  time.Date(2026, 8, 31, 23, 0, 0, 0, time.UTC),
			want: time.Date(2026, 9, 1, 6, 30, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := at.Next(tt.now); !got.Equal(tt.want) {
				t.Errorf("Next(%v) = %v, want %v", tt.now, got, tt.want)
			}
		})
	}
}

// The selection is a wall-clock idea: half past six means half past six local,
// whatever the clocks did overnight.
func TestTimeOfDayNextAcrossDaylightSaving(t *testing.T) {
	rome, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		t.Skipf("timezone data unavailable: %v", err)
	}

	at := TimeOfDay{Hour: 6, Minute: 30}
	// The night European clocks go forward in 2026.
	now := time.Date(2026, 3, 29, 1, 0, 0, 0, rome)

	next := at.Next(now)
	if h, m := next.Hour(), next.Minute(); h != 6 || m != 30 {
		t.Errorf("Next gave %02d:%02d local, want 06:30 — the wall clock must win", h, m)
	}
	if !next.After(now) {
		t.Errorf("Next(%v) = %v, want a later instant", now, next)
	}
}

func TestLoadScheduleDefaults(t *testing.T) {
	t.Setenv("ZIBA_DATABASE_URL", "postgres://localhost/ziba")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.CollectEvery != DefaultCollectEvery {
		t.Errorf("CollectEvery = %v, want %v", cfg.CollectEvery, DefaultCollectEvery)
	}
	if got := cfg.DigestAt.String(); got != DefaultDigestAt {
		t.Errorf("DigestAt = %v, want %v", got, DefaultDigestAt)
	}
}

func TestLoadScheduleRejectsBadValues(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{"unparseable interval", "ZIBA_COLLECT_EVERY", "often"},
		// A schedule tighter than a minute would hammer every source and is
		// certainly a typo — "30" meaning seconds rather than minutes.
		{"interval too short", "ZIBA_COLLECT_EVERY", "30s"},
		{"bad time", "ZIBA_DIGEST_AT", "half past six"},
		{"impossible hour", "ZIBA_DIGEST_AT", "25:00"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ZIBA_DATABASE_URL", "postgres://localhost/ziba")
			t.Setenv(tt.key, tt.value)

			if _, err := Load(); err == nil {
				t.Errorf("Load accepted %s=%q, want an error", tt.key, tt.value)
			}
		})
	}
}

// Zero disables the schedule, which is a legitimate choice, not a typo.
func TestLoadScheduleZeroDisables(t *testing.T) {
	t.Setenv("ZIBA_DATABASE_URL", "postgres://localhost/ziba")
	t.Setenv("ZIBA_COLLECT_EVERY", "0")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.CollectEvery != 0 {
		t.Errorf("CollectEvery = %v, want 0", cfg.CollectEvery)
	}
}
