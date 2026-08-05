package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Default schedule. Collection runs several times a day because feeds move
// through it — a front page collected once is a front page mostly missed. The
// selection is built once, early, so it is waiting when the reader arrives.
const (
	DefaultCollectEvery = 6 * time.Hour
	DefaultDigestAt     = "06:30"
)

// TimeOfDay is a wall-clock time, without a date.
//
// The daily selection is a wall-clock idea: "ready by half past six" means half
// past six whatever the calendar is doing. Storing it as hour and minute rather
// than as an instant is what makes it survive the clocks changing.
type TimeOfDay struct {
	Hour   int
	Minute int
}

// ParseTimeOfDay reads "HH:MM".
func ParseTimeOfDay(s string) (TimeOfDay, error) {
	hour, minute, found := strings.Cut(strings.TrimSpace(s), ":")
	if !found {
		return TimeOfDay{}, fmt.Errorf("time %q must look like HH:MM", s)
	}

	h, err := strconv.Atoi(strings.TrimSpace(hour))
	if err != nil || h < 0 || h > 23 {
		return TimeOfDay{}, fmt.Errorf("time %q: hour must be between 0 and 23", s)
	}
	m, err := strconv.Atoi(strings.TrimSpace(minute))
	if err != nil || m < 0 || m > 59 {
		return TimeOfDay{}, fmt.Errorf("time %q: minute must be between 0 and 59", s)
	}
	return TimeOfDay{Hour: h, Minute: m}, nil
}

func (t TimeOfDay) String() string { return fmt.Sprintf("%02d:%02d", t.Hour, t.Minute) }

// On returns this time of day on the given day, in that day's own location.
func (t TimeOfDay) On(day time.Time) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), t.Hour, t.Minute, 0, 0, day.Location())
}

// Next returns the first occurrence of this time strictly after now, in now's
// own location.
func (t TimeOfDay) Next(now time.Time) time.Time {
	candidate := t.On(now)
	if !candidate.After(now) {
		candidate = candidate.AddDate(0, 0, 1)
	}
	return candidate
}
