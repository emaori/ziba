package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Default schedule. The anchor keeps the morning run predictable across
// restarts; each run also refreshes the digest.
const (
	DefaultCollectEvery = 6 * time.Hour
	DefaultCollectAt    = "04:00"
)

// CollectionSchedule controls the unattended collection and digest cycle.
// PostgreSQL owns the live value; legacy environment values are imported once.
type CollectionSchedule struct {
	Every time.Duration
	At    TimeOfDay
}

// ParseCollectionSchedule validates the values accepted by Settings and by the
// one-time environment compatibility import. Empty values use the defaults.
func ParseCollectionSchedule(everyRaw, atRaw string) (CollectionSchedule, error) {
	if strings.TrimSpace(everyRaw) == "" {
		everyRaw = DefaultCollectEvery.String()
	}
	every, err := time.ParseDuration(strings.TrimSpace(everyRaw))
	if err != nil {
		return CollectionSchedule{}, fmt.Errorf("collection interval: %w", err)
	}
	if every < 0 || (every > 0 && every < time.Minute) {
		return CollectionSchedule{}, fmt.Errorf("collection interval %s is invalid; use 0 or one minute or more", every)
	}
	if strings.TrimSpace(atRaw) == "" {
		atRaw = DefaultCollectAt
	}
	at, err := ParseTimeOfDay(atRaw)
	if err != nil {
		return CollectionSchedule{}, fmt.Errorf("collection start: %w", err)
	}
	return CollectionSchedule{Every: every, At: at}, nil
}

// TimeOfDay is a wall-clock time, without a date.
//
// The schedule anchor is a wall-clock idea. Storing it as hour and minute makes
// the morning run stay put when clocks change.
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

// NextEvery returns the next occurrence of an interval anchored at this local
// time each day.
func (t TimeOfDay) NextEvery(now time.Time, every time.Duration) time.Time {
	if every <= 0 {
		return time.Time{}
	}
	anchor := t.On(now)
	for candidate, nextDay := anchor, anchor.AddDate(0, 0, 1); candidate.Before(nextDay); candidate = candidate.Add(every) {
		if candidate.After(now) {
			return candidate
		}
	}
	return anchor.AddDate(0, 0, 1)
}

// PreviousEvery returns the latest anchored occurrence at or before now.
func (t TimeOfDay) PreviousEvery(now time.Time, every time.Duration) time.Time {
	if every <= 0 {
		return time.Time{}
	}
	day := now
	if now.Before(t.On(now)) {
		day = now.AddDate(0, 0, -1)
	}
	anchor, nextDay := t.On(day), t.On(day).AddDate(0, 0, 1)
	previous := anchor
	for candidate := anchor.Add(every); candidate.Before(nextDay) && !candidate.After(now); candidate = candidate.Add(every) {
		previous = candidate
	}
	return previous
}
