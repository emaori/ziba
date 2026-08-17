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

func TestTimeOfDayAnchoredInterval(t *testing.T) {
	at := TimeOfDay{Hour: 4}
	every := 6 * time.Hour

	tests := []struct {
		now          time.Time
		wantPrevious time.Time
		wantNext     time.Time
	}{
		{
			time.Date(2026, 8, 5, 3, 0, 0, 0, time.UTC),
			time.Date(2026, 8, 4, 22, 0, 0, 0, time.UTC),
			time.Date(2026, 8, 5, 4, 0, 0, 0, time.UTC),
		},
		{
			time.Date(2026, 8, 5, 13, 0, 0, 0, time.UTC),
			time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC),
			time.Date(2026, 8, 5, 16, 0, 0, 0, time.UTC),
		},
		{
			time.Date(2026, 8, 5, 22, 0, 0, 0, time.UTC),
			time.Date(2026, 8, 5, 22, 0, 0, 0, time.UTC),
			time.Date(2026, 8, 6, 4, 0, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		if got := at.PreviousEvery(tt.now, every); !got.Equal(tt.wantPrevious) {
			t.Errorf("PreviousEvery(%v) = %v, want %v", tt.now, got, tt.wantPrevious)
		}
		if got := at.NextEvery(tt.now, every); !got.Equal(tt.wantNext) {
			t.Errorf("NextEvery(%v) = %v, want %v", tt.now, got, tt.wantNext)
		}
	}
}

func TestParseCollectionScheduleDefaults(t *testing.T) {
	cfg, err := ParseCollectionSchedule("", "")
	if err != nil {
		t.Fatalf("ParseCollectionSchedule returned error: %v", err)
	}
	if cfg.Every != DefaultCollectEvery {
		t.Errorf("Every = %v, want %v", cfg.Every, DefaultCollectEvery)
	}
	if got := cfg.At.String(); got != DefaultCollectAt {
		t.Errorf("At = %v, want %v", got, DefaultCollectAt)
	}
}

func TestParseCollectionScheduleRejectsBadValues(t *testing.T) {
	tests := []struct {
		name  string
		every string
		at    string
	}{
		{"unparseable interval", "often", "04:00"},
		// A schedule tighter than a minute would hammer every source and is
		// certainly a typo — "30" meaning seconds rather than minutes.
		{"interval too short", "30s", "04:00"},
		{"bad time", "6h", "half past six"},
		{"impossible hour", "6h", "25:00"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseCollectionSchedule(tt.every, tt.at); err == nil {
				t.Errorf("ParseCollectionSchedule accepted every=%q at=%q, want an error", tt.every, tt.at)
			}
		})
	}
}

func TestLoadLegacyDigestAtAsScheduleAnchor(t *testing.T) {
	t.Setenv("ZIBA_DATABASE_URL", "postgres://localhost/ziba")
	t.Setenv("ZIBA_DIGEST_AT", "05:30")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := cfg.LegacyCollectAt; got != "05:30" {
		t.Errorf("LegacyCollectAt = %v, want legacy value 05:30", got)
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
	if cfg.LegacyCollectEvery != "0" {
		t.Errorf("LegacyCollectEvery = %v, want raw value 0", cfg.LegacyCollectEvery)
	}
}
