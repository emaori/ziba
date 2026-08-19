package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/emaori/ziba/internal/config"
	"github.com/emaori/ziba/internal/domain"
	"github.com/emaori/ziba/internal/linkwarden"
	"github.com/emaori/ziba/internal/store"
)

// listPageSize bounds how much of an interest or the archive one page shows.
const listPageSize = 50

type readingStore interface {
	LatestDigest(ctx context.Context) (domain.Digest, error)
	Article(ctx context.Context, id int64) (domain.Article, error)
	SetArchived(ctx context.Context, id int64, archived bool) error
	ArticlesByInterest(ctx context.Context, interest string, threshold domain.RelevanceScore, limit, offset int) ([]domain.Article, error)
	ArticlesOnDay(ctx context.Context, interest string, day time.Time, interests []string) ([]domain.Article, error)
	DayNavigation(ctx context.Context, interest string, day time.Time, interests []string) (store.DayNavigation, error)
	Archive(ctx context.Context, limit, offset int, interests []string) ([]domain.Article, error)
}

type feedbackStore interface {
	SetScoreFeedback(ctx context.Context, id int64, feedback domain.ScoreFeedback) error
	ScoreFeedbackSummary(ctx context.Context) (store.ScoreFeedbackSummary, error)
	ResetPersonalizedScoring(ctx context.Context) error
}

type statisticsStore interface {
	TalliesBySource(ctx context.Context) ([]store.Tally, error)
	TalliesByDay(ctx context.Context, limit int) ([]store.DayTally, error)
	Articles(ctx context.Context, interests []string, threshold int) (store.ArticleStats, error)

	Tokens(ctx context.Context) (store.TokenTally, error)
	TokensByInterest(ctx context.Context, interests []string) ([]store.TokenTally, error)
	TokensByDay(ctx context.Context, limit int) ([]store.TokenTally, error)
	Backlogs(ctx context.Context) ([]store.Backlog, error)
}

// Store is the constructor contract for the web application. Handlers use the
// narrower capabilities above; keeping their composition here preserves the
// existing public API while the package is split gradually.
type Store interface {
	readingStore
	feedbackStore
	statisticsStore
}

type configurationStore interface {
	Configuration(ctx context.Context) (store.Configuration, error)
	SaveSetupInterests(ctx context.Context, interests config.Interests) error
	SaveSetupSources(ctx context.Context, interests config.Interests, sources []domain.Source) error
	DeleteSetupSource(ctx context.Context, id int64) error
	SaveSchedule(ctx context.Context, schedule config.CollectionSchedule) error
	FinishSetup(ctx context.Context, interests config.Interests, sources []domain.Source, collectNow bool) error
	SaveConfiguration(ctx context.Context, interests config.Interests, sources []domain.Source) error
	SaveLinkwarden(ctx context.Context, configuration linkwarden.Configuration) error
}

type collectionStateStore interface {
	CollectionState(ctx context.Context) (running bool, completed uint64, err error)
}

func (s *Server) handleScoreFeedback(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if !s.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	feedback := domain.ScoreFeedback(r.Form.Get("feedback"))
	if feedback != "" && feedback != domain.FeedbackHigher && feedback != domain.FeedbackLower {
		http.Error(w, "invalid feedback", http.StatusBadRequest)
		return
	}
	if err := s.feedback.SetScoreFeedback(r.Context(), id, feedback); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		s.fail(w, r, err)
		return
	}
	if r.Header.Get(asyncHeader) != "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, backTo(r), http.StatusSeeOther)
}

// handleInterest lists one interest's unread articles, most relevant first.
func (s *Server) handleInterest(w http.ResponseWriter, r *http.Request) {
	interests, threshold, err := s.currentValues(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	interest := r.PathValue("name")
	if !knownInterest(interests, interest) {
		http.NotFound(w, r)
		return
	}

	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	offset = max(offset, 0)

	articles, err := s.reading.ArticlesByInterest(r.Context(), interest, threshold, listPageSize, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	s.render(w, r, "interest.html", &readingPage{
		layoutData: layoutData{Title: interest, Active: interest}, Interest: interest,
		Articles: articles, Offset: offset, PageSize: listPageSize,
	})
}

// handleDay shows everything for one day — read, unread, and below threshold.
// This is the view that hides nothing.
func (s *Server) handleDay(w http.ResponseWriter, r *http.Request) {
	interests, _, err := s.currentValues(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	interest := r.URL.Query().Get("interest")
	if interest != "" && !knownInterest(interests, interest) {
		http.NotFound(w, r)
		return
	}

	day := time.Now()
	if raw := r.URL.Query().Get("date"); raw != "" {
		parsed, err := time.Parse(time.DateOnly, raw)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		day = parsed
	}

	articles, err := s.reading.ArticlesOnDay(r.Context(), interest, day, interests)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	nav, err := s.reading.DayNavigation(r.Context(), interest, day, interests)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	s.render(w, r, "day.html", &readingPage{
		layoutData: layoutData{Title: "By day", Active: interest}, Interest: interest,
		Articles: articles, Nav: nav, Day: day,
	})
}

func (s *Server) handleDigest(w http.ResponseWriter, r *http.Request) {
	digest, err := s.reading.LatestDigest(r.Context())
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, "digest.html", &readingPage{layoutData: layoutData{Title: "Last 24 hours"}, Digest: digest})
}

func (s *Server) handleArticle(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	article, err := s.reading.Article(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}

	cfg, configErr := s.currentConfiguration(r)
	if configErr != nil {
		s.fail(w, r, configErr)
		return
	}
	s.render(w, r, "article.html", &readingPage{layoutData: layoutData{Title: article.Title, LinkwardenEnabled: cfg.Linkwarden.Enabled}, Article: article})
}

// handleArchive marks an article read, or puts it back.
//
// It answers a form post and redirects, rather than rendering. That is the
// post-redirect-get pattern, and it is what stops the browser's back button and
// refresh from silently repeating the action.
//
// Unless the post came from the page's own script, which asks for no reply at
// all: reloading to change one button threw the reader back to the top of the
// page. The redirect remains the answer to a plain form post, so the button
// still works with the script absent.
func (s *Server) handleArchive(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	action := r.PathValue("action")
	if action == "" {
		action = path.Base(r.URL.Path)
	}
	if action != "archive" && action != "unarchive" {
		http.NotFound(w, r)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if mediaType == "multipart/form-data" {
		err = r.ParseMultipartForm(32 << 10)
	} else {
		err = r.ParseForm()
	}
	if err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf_token")), []byte(s.csrfToken)) != 1 {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}

	archived := action == "archive"
	if err := s.reading.SetArchived(r.Context(), id, archived); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		s.fail(w, r, err)
		return
	}

	if r.Header.Get(asyncHeader) != "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, backTo(r), http.StatusSeeOther)
}

// asyncHeader marks a post made by app.js rather than by submitting a form.
// Only a same-origin script can set it — a cross-site form post cannot add a
// header — so honouring it does not widen what a hostile page can do here.
const asyncHeader = "X-Ziba-Async"

// backTo works out where to send the reader after they press a button.
//
// The referer is the page they were on. Only a same-site address is honoured:
// following an arbitrary one would turn this into an open redirect, letting
// another site bounce a visitor onward through us.
func backTo(r *http.Request) string {
	referer, err := url.Parse(r.Referer())
	if err != nil || referer.Path == "" {
		return "/"
	}
	if referer.Host != "" && referer.Host != r.Host {
		return "/"
	}

	back := referer.Path
	if referer.RawQuery != "" {
		back += "?" + referer.RawQuery
	}
	return back
}

func (s *Server) handleArchiveAll(w http.ResponseWriter, r *http.Request) {
	interests, _, err := s.currentValues(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	offset = max(offset, 0)

	articles, err := s.reading.Archive(r.Context(), listPageSize, offset, interests)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	s.render(w, r, "list.html", &readingPage{
		layoutData: layoutData{Title: "Everything"}, Articles: articles, Offset: offset, PageSize: listPageSize,
	})
}

// statsDays is how far back the day-by-day table reaches. A month is enough to
// see a pattern and short enough to read without scrolling.
const statsDays = 30

// handleStats shows what collection has actually been doing.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	interests, threshold, err := s.currentValues(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	bySource, err := s.statistics.TalliesBySource(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	byDay, err := s.statistics.TalliesByDay(r.Context(), statsDays)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	library, err := s.statistics.Articles(r.Context(), interests, int(threshold))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	tokens, err := s.statistics.Tokens(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	tokensByInterest, err := s.statistics.TokensByInterest(r.Context(), interests)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	tokensByDay, err := s.statistics.TokensByDay(r.Context(), statsDays)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	backlogs, err := s.statistics.Backlogs(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// Rows that finished before outcomes were recorded. Worth naming rather
	// than folding into another column, so the totals stay honest.
	unknown := 0
	for _, tally := range bySource {
		unknown += tally.Unknown
	}

	s.render(w, r, "stats.html", &statisticsPage{
		layoutData: layoutData{Title: "Statistics"}, BySource: bySource, ByDay: byDay,
		Library: library, Unknown: unknown,
		Tokens: tokens, TokensByInterest: tokensByInterest, TokensByDay: tokensByDay,
		Backlogs: backlogs,
	})
}

func knownInterest(interests []string, name string) bool {
	for _, i := range interests {
		if i == name {
			return true
		}
	}
	return false
}

// render writes the page. The tab bar is the same on every screen, so it is
// filled in here rather than in each handler.
func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data templateData) {
	interests, threshold, err := s.currentValues(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	layout := data.layout()
	layout.Interests = interests
	layout.Threshold = threshold
	if !layout.SetupMode {
		cfg, cfgErr := s.currentConfiguration(r)
		if cfgErr != nil {
			s.fail(w, r, cfgErr)
			return
		}
		layout.ScheduleDisabled = cfg.Schedule.Every <= 0
		layout.LinkwardenEnabled = cfg.Linkwarden.Enabled
		if layout.ReturnTo == "" {
			layout.ReturnTo = r.URL.RequestURI()
		}
		if !layout.ScheduleDisabled {
			layout.NextCollection = cfg.Schedule.At.NextEvery(time.Now(), cfg.Schedule.Every)
		}
		if s.collectionState != nil {
			layout.CollectionRunning, layout.CollectionCompleted, cfgErr = s.collectionState.CollectionState(r.Context())
			if cfgErr != nil {
				s.fail(w, r, cfgErr)
				return
			}
		}
	}

	set, ok := s.pages[name]
	if !ok {
		s.fail(w, r, errors.New("no such template: "+name))
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// "layout" is the frame; each page supplies the "content" it wraps.
	if err := set.ExecuteTemplate(w, "layout", data); err != nil {
		// Too late for a status code: the response is already partly written.
		slog.Error("render failed", "template", name, "error", err)
	}
}

func (s *Server) handleCollectionStatus(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.currentConfiguration(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	running, completed := false, uint64(0)
	if s.collectionState != nil {
		running, completed, err = s.collectionState.CollectionState(r.Context())
		if err != nil {
			s.fail(w, r, err)
			return
		}
	}
	var next time.Time
	if cfg.Schedule.Every > 0 {
		next = cfg.Schedule.At.NextEvery(time.Now(), cfg.Schedule.Every)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Running   bool      `json:"running"`
		Completed uint64    `json:"completed"`
		Disabled  bool      `json:"disabled"`
		Next      time.Time `json:"next"`
		NextLabel string    `json:"next_label"`
	}{running, completed, cfg.Schedule.Every <= 0, next, nextCollectionLabel(next)})
}

func nextCollectionLabel(next time.Time) string {
	if next.IsZero() {
		return ""
	}
	now := time.Now()
	day := "on " + next.Format("2 Jan")
	if next.YearDay() == now.YearDay() && next.Year() == now.Year() {
		day = "today"
	} else if tomorrow := now.AddDate(0, 0, 1); next.YearDay() == tomorrow.YearDay() && next.Year() == tomorrow.Year() {
		day = "tomorrow"
	}
	return fmt.Sprintf("Next collection %s at %s", day, next.Format("15:04"))
}

func (s *Server) currentValues(r *http.Request) ([]string, domain.RelevanceScore, error) {
	cfg, err := s.currentConfiguration(r)
	if err != nil {
		return nil, 0, err
	}
	names := make([]string, 0, len(cfg.Interests.Topics))
	for _, interest := range cfg.Interests.Topics {
		names = append(names, interest.Topic)
	}
	return names, domain.RelevanceScore(cfg.Interests.Threshold), nil
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "path", r.URL.Path, "error", err)
	http.Error(w, "something went wrong", http.StatusInternalServerError)
}
