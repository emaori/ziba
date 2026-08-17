package job

import (
	"context"
	"log/slog"
	"time"

	"github.com/emaori/ziba/internal/config"
)

// Schedule says how often the work runs and where its wall-clock cycle starts.
type Schedule struct {
	// Every is how often to collect, analyze and refresh the digest. Zero disables it.
	Every time.Duration

	// At anchors the interval to a local wall-clock time.
	At config.TimeOfDay
}

// Scheduler runs a Runner unattended.
type Scheduler struct {
	work      func(context.Context, int) error
	hasDigest func(context.Context, time.Time) (bool, error)
	schedule  func(context.Context) (Schedule, error)
	requested func(context.Context) (bool, error)
	batch     int
	log       *slog.Logger
}

// NewScheduler builds the scheduler.
func NewScheduler(runner *Runner, schedule Schedule, batch int, log *slog.Logger) *Scheduler {
	return NewSchedulerFunc(func(ctx context.Context, batch int) error {
		return runner.ScheduledCollection(ctx, batch)
	}, runner.store.HasDigestSince, schedule, batch, log)
}

// NewSchedulerFunc builds a scheduler whose runner is resolved for each run.
// This lets web-managed configuration take effect without restarting Ziba.
func NewSchedulerFunc(work func(context.Context, int) error,
	hasDigest func(context.Context, time.Time) (bool, error),
	schedule Schedule, batch int, log *slog.Logger) *Scheduler {
	return NewDynamicSchedulerFunc(work, hasDigest, func(context.Context) (Schedule, error) {
		return schedule, nil
	}, nil, batch, log)
}

// NewDynamicSchedulerFunc builds a scheduler that periodically reloads its
// timing. This lets Settings changes take effect without restarting Ziba.
func NewDynamicSchedulerFunc(work func(context.Context, int) error,
	hasDigest func(context.Context, time.Time) (bool, error),
	schedule func(context.Context) (Schedule, error), requested func(context.Context) (bool, error),
	batch int, log *slog.Logger) *Scheduler {
	return &Scheduler{work: work, hasDigest: hasDigest, schedule: schedule, requested: requested, batch: batch, log: log}
}

// Run works until ctx is cancelled.
//
// Every run collects, processes and refreshes the digest. The timer is computed
// from a wall-clock anchor instead of process start, so restarts do not move the
// morning run.
func (s *Scheduler) Run(ctx context.Context) {
	const refresh = 30 * time.Second
	var previous Schedule
	var lastOccurrence time.Time
	for {
		if s.requested != nil {
			requested, err := s.requested(ctx)
			if err != nil {
				s.log.Error("could not check immediate collection request", "error", err)
			} else if requested {
				s.run(ctx, "first collection and digest", func(ctx context.Context) error { return s.work(ctx, s.batch) })
			}
		}
		schedule, err := s.schedule(ctx)
		if err != nil {
			s.log.Error("could not read collection schedule", "error", err)
		} else {
			if schedule != previous {
				if schedule.Every <= 0 {
					s.log.Info("scheduler disabled")
				} else {
					s.log.Info("scheduler updated", "collect_every", schedule.Every, "collect_at", schedule.At,
						"next_run", schedule.At.NextEvery(time.Now(), schedule.Every).Format(time.RFC3339))
				}
				previous = schedule
			}
			if schedule.Every > 0 {
				occurrence := schedule.At.PreviousEvery(time.Now(), schedule.Every)
				if occurrence.After(lastOccurrence) {
					s.catchUp(ctx, occurrence)
					lastOccurrence = occurrence
				}
			}
		}
		timer := time.NewTimer(refresh)
		select {
		case <-ctx.Done():
			timer.Stop()
			s.log.Info("scheduler stopped")
			return
		case <-timer.C:
		}
	}
}

// catchUp performs one missed occurrence, never a replay of every run while the
// process was down.
func (s *Scheduler) catchUp(ctx context.Context, scheduled time.Time) {
	built, err := s.hasDigest(ctx, scheduled)
	if err != nil {
		s.log.Error("could not check latest digest", "error", err)
		return
	}
	if built {
		return
	}

	s.log.Info("scheduled run was missed, running it now",
		"scheduled_for", scheduled.Format(time.RFC3339))
	s.run(ctx, "collection and digest (catch-up)", func(ctx context.Context) error { return s.work(ctx, s.batch) })
}

// run performs one scheduled task, logging rather than propagating failure:
// nothing is watching, and a failed run must not end the schedule.
func (s *Scheduler) run(ctx context.Context, name string, work func(context.Context) error) {
	started := time.Now()
	s.log.Info("scheduled run starting", "task", name)

	if err := work(ctx); err != nil {
		s.log.Error("scheduled run failed", "task", name, "error", err,
			"elapsed", time.Since(started).Round(time.Second))
		return
	}
	s.log.Info("scheduled run finished", "task", name,
		"elapsed", time.Since(started).Round(time.Second))
}
