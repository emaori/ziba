// Package job holds the work Ziba does on a schedule: collecting from every
// source, turning what was collected into articles, analyzing them, and
// refreshing the digest.
//
// It exists so that the commands and the scheduler run exactly the same code.
// A nightly run that differs from what you get by typing the command is a bug
// waiting for a quiet night to happen.
package job

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/emaori/ziba/internal/collect"
	"github.com/emaori/ziba/internal/config"
	"github.com/emaori/ziba/internal/domain"
	"github.com/emaori/ziba/internal/pipeline"
	"github.com/emaori/ziba/internal/store"
)

const (
	// httpTimeout bounds every outbound request. Generous enough for a slow
	// site, short enough that one bad source does not stall the run.
	httpTimeout = 30 * time.Second

	// maxParallelAnalyses caps concurrent model calls. Small on purpose: this
	// is a personal tool with no deadline, and a burst of requests only invites
	// rate limiting.
	maxParallelAnalyses = 4

	// maxRetries is how many scheduled attempts follow the initial failure.
	maxRetries = 3
)

// Runner performs the work. Build one with New and reuse it.
type Runner struct {
	store     *store.Store
	registry  *collect.Registry
	fullText  *collect.FullText
	roundup   *collect.Roundup
	pipeline  *pipeline.Pipeline
	threshold domain.RelevanceScore
	interests []string
	sources   []domain.Source
	log       *slog.Logger
}

// Options carries what the runner cannot work out for itself.
type Options struct {
	// Analyzer may be nil, in which case analysis is unavailable and Process
	// stops after retrieving full text. That is the state without an API key,
	// and it is a usable one: collecting and reading still work.
	Analyzer pipeline.Analyzer
}

// New wires a runner from configuration. It does not touch the network or the
// database; failures here are configuration mistakes.
func New(cfg config.Config, sources []domain.Source, interests config.Interests,
	db *store.Store, log *slog.Logger, opts Options) *Runner {

	client := collect.NewHTTPClient(httpTimeout, collect.PolitenessInterval)

	var p *pipeline.Pipeline
	if opts.Analyzer != nil {
		p = pipeline.NewPersonalized(opts.Analyzer, interests.Threshold, log, db)
	}

	return &Runner{
		store: db,
		registry: collect.NewRegistry(
			collect.NewRSS(client, log),
			collect.NewNewsletter(log),
		),
		fullText:  collect.NewFullText(client),
		roundup:   collect.NewRoundup(client),
		pipeline:  p,
		threshold: domain.RelevanceScore(interests.Threshold),
		interests: interestNames(interests),
		sources:   sources,
		log:       log,
	}
}

// CollectResult reports what one collection pass found.
type CollectResult struct {
	Sources int
	Found   int
	New     int
	Failed  int

	// TooOld counts items discarded for predating their source's cutoff. It is
	// reported because a feed offering 277 entries and yielding 2 looks broken
	// unless the run says why.
	TooOld int
}

// Collect reads every enabled source and stores what is new.
func (r *Runner) Collect(ctx context.Context) (CollectResult, error) {
	synced, err := r.store.SyncSources(ctx, r.sources)
	if err != nil {
		return CollectResult{}, err
	}

	enabled := make([]domain.Source, 0, len(synced))
	for _, s := range synced {
		if s.Enabled {
			enabled = append(enabled, s)
		}
	}
	if len(enabled) == 0 {
		return CollectResult{}, fmt.Errorf("no enabled sources configured")
	}

	result := CollectResult{Sources: len(enabled)}
	for _, res := range r.registry.Run(ctx, r.log, enabled) {
		if res.Err != nil {
			// A source being down is expected, not a reason to abandon the run.
			r.log.Error("source failed", "source", res.Source.Name, "error", res.Err)
			result.Failed++
			continue
		}
		recent, tooOld := withinCutoff(res.Source, res.Items)

		inserted, err := r.store.SaveRawItems(ctx, recent)
		if err != nil {
			return result, err
		}
		result.Found += len(res.Items)
		result.New += inserted
		result.TooOld += tooOld

		r.log.Info("collected", "source", res.Source.Name,
			"found", len(res.Items), "new", inserted, "too_old", tooOld)
	}
	return result, nil
}

// Expand opens the collected issues of link digests and queues the articles
// they point at.
//
// It sits between collection and full text because it produces work for the
// stage after it: one issue becomes ten raw items, which the next Hydrate then
// turns into articles. An issue that yields nothing is still marked done —
// a week with no links worth keeping is a normal week, not a failure to retry.
func (r *Runner) Expand(ctx context.Context, batch int) (opened, queued int, err error) {
	opened, queued, _, err = r.expandBatch(ctx, batch, time.Now())
	return opened, queued, err
}

func (r *Runner) expandBatch(ctx context.Context, batch int, before time.Time) (opened, queued, failed int, err error) {
	issues, err := r.store.UnexpandedRoundupsBefore(ctx, batch, before)
	if err != nil {
		return 0, 0, 0, err
	}
	if len(issues) == 0 {
		return 0, 0, 0, nil
	}

	done := make(map[domain.Outcome][]int64)
	for _, issue := range issues {
		if ctx.Err() != nil {
			break
		}

		links, err := r.roundup.Links(ctx, issue)
		if err != nil {
			r.log.Warn("roundup unavailable", "url", issue.URL, "error", err)
			if recordErr := r.store.RecordRawItemFailure(ctx, issue.ID, err.Error(), maxRetries+1); recordErr != nil {
				return opened, queued, failed, recordErr
			}
			failed++
			continue
		}

		inserted, err := r.store.SaveRawItems(ctx, links)
		if err != nil {
			return len(done[domain.OutcomeExpanded]), queued, failed, err
		}

		r.log.Info("roundup expanded", "issue", issue.Title,
			"links", len(links), "new", inserted)
		queued += inserted
		done[domain.OutcomeExpanded] = append(done[domain.OutcomeExpanded], issue.ID)
	}

	opened = len(done[domain.OutcomeExpanded])
	if err := r.store.MarkRawItemsProcessed(ctx, done); err != nil {
		return opened, queued, failed, err
	}
	return opened, queued, failed, nil
}

// Hydrate turns collected items into articles by retrieving their full text.
func (r *Runner) Hydrate(ctx context.Context, batch int) (processed, created int, err error) {
	items, err := r.store.UnprocessedRawItems(ctx, batch)
	if err != nil {
		return 0, 0, err
	}
	if len(items) == 0 {
		return 0, 0, nil
	}

	done := make(map[domain.Outcome][]int64)
	finish := func(outcome domain.Outcome, id int64) {
		done[outcome] = append(done[outcome], id)
		processed++
	}

	for _, item := range items {
		if ctx.Err() != nil {
			break
		}

		article, err := r.fullText.Article(ctx, item)
		if errors.Is(err, collect.ErrNotArticle) {
			// A link that leads somewhere we do not store — a video, most
			// often, reached through a newsletter's redirect. Mark it done and
			// move on: there is nothing to keep and nothing to retry.
			r.log.Info("skipping link", "url", item.URL, "reason", err)
			finish(domain.OutcomeSkipped, item.ID)
			continue
		}
		if err != nil {
			// The article is still usable with the feed excerpt, so this is a
			// warning: the run continues and stores what it has.
			r.log.Warn("full text unavailable", "url", item.URL, "error", err)
		}

		_, isNew, err := r.store.SaveArticle(ctx, article)
		if err != nil {
			return processed, created, err
		}
		if isNew {
			created++
			finish(domain.OutcomeStored, item.ID)
		} else {
			// The same address was already stored, usually because another
			// source published the same link first.
			finish(domain.OutcomeDuplicate, item.ID)
		}
	}

	if err := r.store.MarkRawItemsProcessed(ctx, done); err != nil {
		return processed, created, err
	}
	return processed, created, nil
}

// Analyze runs the AI pipeline over articles not yet analyzed.
func (r *Runner) Analyze(ctx context.Context, batch int) (analyzed, aboveThreshold, failed int, err error) {
	return r.analyzeBatch(ctx, batch, time.Now())
}

func (r *Runner) analyzeBatch(ctx context.Context, batch int, before time.Time) (analyzed, aboveThreshold, failed int, err error) {
	if r.pipeline == nil {
		return 0, 0, 0, fmt.Errorf("no analyzer configured")
	}

	articles, err := r.store.UnanalyzedArticlesBefore(ctx, batch, before)
	if err != nil {
		return 0, 0, 0, err
	}
	if len(articles) == 0 {
		return 0, 0, 0, nil
	}

	// Which sources state their own subject. Read once for the batch rather
	// than per article: it is a handful of rows and does not change mid-run.
	declared, err := r.store.DeclaredCategories(ctx)
	if err != nil {
		return 0, 0, 0, err
	}

	var (
		mu         sync.Mutex
		results    []domain.Article
		recordErrs []error
	)

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(maxParallelAnalyses)

	for _, article := range articles {
		group.Go(func() error {
			result, err := r.pipeline.Analyze(groupCtx, article, declared[article.SourceID])
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				r.log.Error("analysis failed", "url", article.URL, "error", err)
				recordErr := r.store.RecordAnalysisFailure(ctx, article.ID, err.Error(), maxRetries+1)
				mu.Lock()
				defer mu.Unlock()
				failed++
				if recordErr != nil {
					recordErrs = append(recordErrs, recordErr)
				}
				return nil
			}
			mu.Lock()
			defer mu.Unlock()
			results = append(results, result)
			return nil
		})
	}
	groupErr := group.Wait()
	if ctx.Err() != nil {
		return 0, 0, failed, ctx.Err()
	}
	if groupErr != nil {
		return 0, 0, failed, groupErr
	}
	if len(recordErrs) > 0 {
		return 0, 0, failed, errors.Join(recordErrs...)
	}

	for _, article := range results {
		if err := r.store.SaveAnalysis(ctx, article); err != nil {
			return analyzed, aboveThreshold, failed, err
		}
		analyzed++
		if article.Score >= r.threshold {
			aboveThreshold++
		}
	}
	return analyzed, aboveThreshold, failed, nil
}

// ScheduledCollection collects once and drains every currently eligible queue
// in bounded chunks. Failed rows wait for the next run, so they cannot loop
// within the same drain.
func (r *Runner) ScheduledCollection(ctx context.Context, batch int) error {
	return r.runAll(ctx, batch)
}

// runAll is the one complete workflow used by unattended and manual runs.
// batch is a chunk size, not a total limit: Drain keeps taking chunks until
// everything eligible at the start of the run has been handled.
func (r *Runner) runAll(ctx context.Context, batch int) error {
	collected, err := r.Collect(ctx)
	if err != nil {
		return fmt.Errorf("collect: %w", err)
	}
	r.log.Info("collection finished",
		"sources", collected.Sources, "new", collected.New, "failed", collected.Failed)
	if err := r.Drain(ctx, batch); err != nil {
		return err
	}
	selected, err := r.Digest(ctx, time.Now())
	if err != nil {
		return fmt.Errorf("build digest: %w", err)
	}
	r.log.Info("digest refreshed", "articles", selected, "window", 24*time.Hour)
	return nil
}

// Drain processes every currently eligible item, keeping batch as the chunk
// size rather than a total limit.
func (r *Runner) Drain(ctx context.Context, batch int) error {
	started := time.Now()
	for {
		opened, queued, failed, err := r.expandBatch(ctx, batch, started)
		if err != nil {
			return fmt.Errorf("expand roundups: %w", err)
		}
		if opened+failed == 0 {
			break
		}
		r.log.Info("roundup chunk finished", "issues", opened,
			"articles_queued", queued, "failed", failed)
	}

	for {
		processed, created, err := r.Hydrate(ctx, batch)
		if err != nil {
			return fmt.Errorf("retrieve full text: %w", err)
		}
		if processed == 0 {
			break
		}
		r.log.Info("full text chunk finished", "processed", processed, "articles", created)
	}

	if r.pipeline == nil {
		r.log.Warn("no analyzer configured, skipping analysis")
		return nil
	}
	for {
		analyzed, above, failed, err := r.analyzeBatch(ctx, batch, started)
		if err != nil {
			return fmt.Errorf("analyze: %w", err)
		}
		if analyzed+failed == 0 {
			break
		}
		r.log.Info("analysis chunk finished", "analyzed", analyzed,
			"above_threshold", above, "failed", failed)
	}
	return nil
}

// Digest builds and stores the selection for the 24 hours ending at end.
func (r *Runner) Digest(ctx context.Context, end time.Time) (int, error) {
	return r.store.GenerateDigest(ctx, end, r.threshold, r.interests)
}

// interestNames flattens the configured interests to the plain list the queries
// filter on.
func interestNames(interests config.Interests) []string {
	names := make([]string, 0, len(interests.Topics))
	for _, topic := range interests.Topics {
		names = append(names, topic.Topic)
	}
	return names
}

// Threshold reports the score an article must reach to be selected.
func (r *Runner) Threshold() domain.RelevanceScore { return r.threshold }

// Daily is the whole nightly chain: collect, retrieve, analyze, select.
//
// Each stage logs its own result and the chain stops at the first real failure.
// Analysis being unavailable is not a real failure — without an API key the
// rest of the chain is still worth running, and the selection can be built from
// whatever scores already exist.
func (r *Runner) Daily(ctx context.Context, batch int) error {
	return r.runAll(ctx, batch)
}

// withinCutoff drops items published before their source's cutoff.
//
// This is what stops a newly added feed from arriving with years of backlog.
// It runs here rather than inside each collector so that every source type is
// covered by one rule, and so the collectors stay unaware of it.
func withinCutoff(src domain.Source, items []domain.RawItem) (recent []domain.RawItem, tooOld int) {
	if _, filtering := src.CollectFrom.Cutoff(src.CreatedAt); !filtering {
		return items, 0
	}

	recent = make([]domain.RawItem, 0, len(items))
	for _, item := range items {
		// Provenance items carry the date of the email that produced them and
		// are the record of where a link came from; dropping one would orphan
		// links that were themselves accepted.
		if item.Kind == domain.ItemKindProvenance ||
			src.CollectFrom.Accepts(src.CreatedAt, item.PublishedAt) {
			recent = append(recent, item)
			continue
		}
		tooOld++
	}
	return recent, tooOld
}
