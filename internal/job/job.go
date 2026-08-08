// Package job holds the work Ziba does on a schedule: collecting from every
// source, turning what was collected into articles, analyzing them, and
// building the day's selection.
//
// It exists so that the commands and the scheduler run exactly the same code.
// A nightly run that differs from what you get by typing the command is a bug
// waiting for a quiet night to happen.
package job

import (
	"context"
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
		p = pipeline.New(opts.Analyzer, interests.Threshold, log)
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
	issues, err := r.store.UnexpandedRoundups(ctx, batch)
	if err != nil {
		return 0, 0, err
	}
	if len(issues) == 0 {
		return 0, 0, nil
	}

	done := make([]int64, 0, len(issues))
	for _, issue := range issues {
		if ctx.Err() != nil {
			break
		}

		links, err := r.roundup.Links(ctx, issue)
		if err != nil {
			// Leave it unprocessed: unlike a missing article body, there is no
			// partial result worth keeping, and the next run should try again.
			r.log.Warn("roundup unavailable", "url", issue.URL, "error", err)
			continue
		}

		inserted, err := r.store.SaveRawItems(ctx, links)
		if err != nil {
			return len(done), queued, err
		}

		r.log.Info("roundup expanded", "issue", issue.Title,
			"links", len(links), "new", inserted)
		queued += inserted
		done = append(done, issue.ID)
	}

	if err := r.store.MarkRawItemsProcessed(ctx, done); err != nil {
		return len(done), queued, err
	}
	return len(done), queued, nil
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

	done := make([]int64, 0, len(items))
	for _, item := range items {
		if ctx.Err() != nil {
			break
		}

		article, err := r.fullText.Article(ctx, item)
		if err != nil {
			// The article is still usable with the feed excerpt, so this is a
			// warning: the run continues and stores what it has.
			r.log.Warn("full text unavailable", "url", item.URL, "error", err)
		}

		_, isNew, err := r.store.SaveArticle(ctx, article)
		if err != nil {
			return len(done), created, err
		}
		if isNew {
			created++
		}
		done = append(done, item.ID)
	}

	if err := r.store.MarkRawItemsProcessed(ctx, done); err != nil {
		return len(done), created, err
	}
	return len(done), created, nil
}

// Analyze runs the AI pipeline over articles not yet analyzed.
func (r *Runner) Analyze(ctx context.Context, batch int) (analyzed, aboveThreshold, failed int, err error) {
	if r.pipeline == nil {
		return 0, 0, 0, fmt.Errorf("no analyzer configured")
	}

	articles, err := r.store.UnanalyzedArticles(ctx, batch)
	if err != nil {
		return 0, 0, 0, err
	}
	if len(articles) == 0 {
		return 0, 0, 0, nil
	}

	var (
		mu      sync.Mutex
		results []domain.Article
	)

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(maxParallelAnalyses)

	for _, article := range articles {
		group.Go(func() error {
			result, err := r.pipeline.Analyze(groupCtx, article)

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				// A failure costs that article, not the run: it stays
				// unanalyzed and is picked up next time.
				r.log.Error("analysis failed", "url", article.URL, "error", err)
				failed++
				return nil
			}
			results = append(results, result)
			return nil
		})
	}
	_ = group.Wait()

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

// Digest builds and stores the selection for a day.
func (r *Runner) Digest(ctx context.Context, date time.Time) (int, error) {
	return r.store.GenerateDigest(ctx, date, r.threshold, r.interests)
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
	collected, err := r.Collect(ctx)
	if err != nil {
		return fmt.Errorf("collect: %w", err)
	}
	r.log.Info("collection finished",
		"sources", collected.Sources, "new", collected.New, "failed", collected.Failed)

	opened, queued, err := r.Expand(ctx, batch)
	if err != nil {
		return fmt.Errorf("expand roundups: %w", err)
	}
	if opened > 0 {
		r.log.Info("roundups finished", "issues", opened, "articles_queued", queued)
	}

	processed, created, err := r.Hydrate(ctx, batch)
	if err != nil {
		return fmt.Errorf("retrieve full text: %w", err)
	}
	r.log.Info("full text finished", "processed", processed, "articles", created)

	if r.pipeline != nil {
		analyzed, above, failed, err := r.Analyze(ctx, batch)
		if err != nil {
			return fmt.Errorf("analyze: %w", err)
		}
		r.log.Info("analysis finished", "analyzed", analyzed, "above_threshold", above, "failed", failed)
	} else {
		r.log.Warn("no analyzer configured, skipping analysis")
	}

	selected, err := r.Digest(ctx, time.Now())
	if err != nil {
		return fmt.Errorf("build digest: %w", err)
	}
	r.log.Info("digest finished", "articles", selected, "threshold", r.threshold)

	return nil
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
