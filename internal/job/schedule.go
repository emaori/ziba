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
	runner   *Runner
	schedule Schedule
	batch    int
	log      *slog.Logger
}

// NewScheduler builds the scheduler.
func NewScheduler(runner *Runner, schedule Schedule, batch int, log *slog.Logger) *Scheduler {
	return &Scheduler{runner: runner, schedule: schedule, batch: batch, log: log}
}

// Run works until ctx is cancelled.
//
// Every run collects, processes and refreshes the digest. The timer is computed
// from a wall-clock anchor instead of process start, so restarts do not move the
// morning run.
func (s *Scheduler) Run(ctx context.Context) {
	if s.schedule.Every <= 0 {
		s.log.Info("scheduler disabled")
		return
	}

	next := s.schedule.At.NextEvery(time.Now(), s.schedule.Every)
	timer := time.NewTimer(time.Until(next))
	defer timer.Stop()

	s.log.Info("scheduler started",
		"collect_every", s.schedule.Every,
		"collect_at", s.schedule.At,
		"next_run", next.Format(time.RFC3339))

	s.catchUp(ctx)

	for {
		select {
		case <-ctx.Done():
			s.log.Info("scheduler stopped")
			return

		case <-timer.C:
			s.run(ctx, "collection and digest", func(ctx context.Context) error {
				return s.runner.ScheduledCollection(ctx, s.batch)
			})
			next = s.schedule.At.NextEvery(time.Now(), s.schedule.Every)
			timer.Reset(time.Until(next))
		}
	}
}

// catchUp performs one missed occurrence, never a replay of every run while the
// process was down.
func (s *Scheduler) catchUp(ctx context.Context) {
	scheduled := s.schedule.At.PreviousEvery(time.Now(), s.schedule.Every)
	built, err := s.runner.store.HasDigestSince(ctx, scheduled)
	if err != nil {
		s.log.Error("could not check latest digest", "error", err)
		return
	}
	if built {
		return
	}

	s.log.Info("scheduled run was missed, running it now",
		"scheduled_for", scheduled.Format(time.RFC3339))
	s.run(ctx, "collection and digest (catch-up)", func(ctx context.Context) error {
		return s.runner.ScheduledCollection(ctx, s.batch)
	})
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
