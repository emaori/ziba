package job

import (
	"context"
	"log/slog"
	"time"

	"github.com/emaori/ziba/internal/config"
)

// Schedule says how often the work runs.
type Schedule struct {
	// Every is how often to collect and analyze. Zero disables it.
	Every time.Duration

	// DigestAt is when to build the day's selection.
	DigestAt config.TimeOfDay
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
// Collection and the daily selection are on separate clocks on purpose. Feeds
// move through the day and a front page collected once is a front page mostly
// missed, while the selection is a morning thing — it should be waiting when
// the reader arrives, not rebuilt under them as they read.
func (s *Scheduler) Run(ctx context.Context) {
	if s.schedule.Every <= 0 {
		s.log.Info("scheduler disabled")
		return
	}

	collectTicker := time.NewTicker(s.schedule.Every)
	defer collectTicker.Stop()

	digestTimer := time.NewTimer(time.Until(s.schedule.DigestAt.Next(time.Now())))
	defer digestTimer.Stop()

	s.log.Info("scheduler started",
		"collect_every", s.schedule.Every,
		"digest_at", s.schedule.DigestAt,
		"next_digest", s.schedule.DigestAt.Next(time.Now()).Format(time.RFC3339))

	s.catchUp(ctx)

	for {
		select {
		case <-ctx.Done():
			s.log.Info("scheduler stopped")
			return

		case <-collectTicker.C:
			s.run(ctx, "collection", func(ctx context.Context) error {
				if _, err := s.runner.Collect(ctx); err != nil {
					return err
				}
				if _, _, err := s.runner.Hydrate(ctx, s.batch); err != nil {
					return err
				}
				if s.runner.pipeline == nil {
					return nil
				}
				_, _, _, err := s.runner.Analyze(ctx, s.batch)
				return err
			})

		case <-digestTimer.C:
			// Rearm before the work, not after: a run that takes twenty minutes
			// must not push tomorrow's selection twenty minutes later, and one
			// that fails must not stop the schedule altogether.
			digestTimer.Reset(time.Until(s.schedule.DigestAt.Next(time.Now())))

			s.run(ctx, "digest", func(ctx context.Context) error {
				selected, err := s.runner.Digest(ctx, time.Now())
				if err == nil {
					s.log.Info("digest built", "articles", selected)
				}
				return err
			})
		}
	}
}

// catchUp builds today's selection if the appointed time has already passed and
// nothing was built.
//
// Without this, a process that was down at half past six skips that day
// entirely and waits for tomorrow — the one failure mode a reader would
// actually notice. It checks rather than simply rebuilding, so an ordinary
// restart later in the day leaves a selection already being read alone.
func (s *Scheduler) catchUp(ctx context.Context) {
	now := time.Now()
	scheduled := s.schedule.DigestAt.On(now)
	if scheduled.After(now) {
		return // today's time has not come round yet
	}

	built, err := s.runner.store.HasDigest(ctx, now)
	if err != nil {
		s.log.Error("could not check today's digest", "error", err)
		return
	}
	if built {
		return
	}

	s.log.Info("today's digest was missed, building it now",
		"scheduled_for", scheduled.Format(time.RFC3339))
	s.run(ctx, "digest (catch-up)", func(ctx context.Context) error {
		selected, err := s.runner.Digest(ctx, now)
		if err == nil {
			s.log.Info("digest built", "articles", selected)
		}
		return err
	})
}

// run performs one scheduled task, logging rather than propagating failure:
// nothing is watching, and a failed night must not end the schedule.
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
