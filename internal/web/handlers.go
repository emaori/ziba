package web

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/emaori/ziba/internal/config"
	"github.com/emaori/ziba/internal/domain"
	"github.com/emaori/ziba/internal/store"
)

// listPageSize bounds how much of an interest or the archive one page shows.
const listPageSize = 50

// Store is what the web package needs from persistence. Declaring the interface
// here, where it is consumed, keeps the handlers testable with a fake and
// documents exactly how much of the database this package can reach.
type Store interface {
	LatestDigest(ctx context.Context) (domain.Digest, error)
	Article(ctx context.Context, id int64) (domain.Article, error)
	SetArchived(ctx context.Context, id int64, archived bool) error

	ArticlesByInterest(ctx context.Context, interest string, threshold domain.RelevanceScore, limit, offset int) ([]domain.Article, error)
	ArticlesOnDay(ctx context.Context, interest string, day time.Time, interests []string) ([]domain.Article, error)
	DayNavigation(ctx context.Context, interest string, day time.Time, interests []string) (store.DayNavigation, error)

	Archive(ctx context.Context, limit, offset int, interests []string) ([]domain.Article, error)

	TalliesBySource(ctx context.Context) ([]store.Tally, error)
	TalliesByDay(ctx context.Context, limit int) ([]store.DayTally, error)
	Articles(ctx context.Context, interests []string, threshold int) (store.ArticleStats, error)

	Tokens(ctx context.Context) (store.TokenTally, error)
	TokensByInterest(ctx context.Context, interests []string) ([]store.TokenTally, error)
	TokensByDay(ctx context.Context, limit int) ([]store.TokenTally, error)
	Backlogs(ctx context.Context) ([]store.Backlog, error)
}

type configurationStore interface {
	Configuration(ctx context.Context) (store.Configuration, error)
	SaveSetupInterests(ctx context.Context, interests config.Interests) error
	SaveSetupSources(ctx context.Context, interests config.Interests, sources []domain.Source) error
	SaveConfiguration(ctx context.Context, interests config.Interests, sources []domain.Source) error
}

// page is the data every template receives. The interests appear in the tab bar
// on every screen, so they travel with every page.
type page struct {
	Title     string
	Interests []string
	Active    string // which tab is highlighted, if any

	Digest   domain.Digest
	Article  domain.Article
	Articles []domain.Article
	Nav      store.DayNavigation
	Day      time.Time
	Interest string
	BySource []store.Tally
	ByDay    []store.DayTally
	Library  store.ArticleStats
	Unknown  int

	Tokens           store.TokenTally
	TokensByInterest []store.TokenTally
	TokensByDay      []store.TokenTally
	Backlogs         []store.Backlog
	Offset           int
	PageSize         int
	Threshold        domain.RelevanceScore
	Configured       bool
	Settings         store.Configuration
	Source           store.SourceInput
	InterestForm     config.Interest
	InterestIndex    int
	EditingInterest  bool
	EditingSource    bool
	Error            string
	SetupMode        bool
	SettingsSection  string
	FormAction       string
	CancelURL        string
	InterestPresets  []interestPreset
	SourcePresets    []sourcePreset
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

	articles, err := s.store.ArticlesByInterest(r.Context(), interest, threshold, listPageSize, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	s.render(w, r, "interest.html", &page{
		Title: interest, Active: interest, Interest: interest,
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

	articles, err := s.store.ArticlesOnDay(r.Context(), interest, day, interests)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	nav, err := s.store.DayNavigation(r.Context(), interest, day, interests)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	s.render(w, r, "day.html", &page{
		Title: "By day", Active: interest, Interest: interest,
		Articles: articles, Nav: nav, Day: day,
	})
}

func (s *Server) handleDigest(w http.ResponseWriter, r *http.Request) {
	digest, err := s.store.LatestDigest(r.Context())
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, "digest.html", &page{Title: "Last 24 hours", Digest: digest})
}

func (s *Server) handleArticle(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	article, err := s.store.Article(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}

	s.render(w, r, "article.html", &page{Title: article.Title, Article: article})
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
	if action != "archive" && action != "unarchive" {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf_token")), []byte(s.csrfToken)) != 1 {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}

	archived := action == "archive"
	if err := s.store.SetArchived(r.Context(), id, archived); err != nil {
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

	articles, err := s.store.Archive(r.Context(), listPageSize, offset, interests)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	s.render(w, r, "list.html", &page{
		Title: "Everything", Articles: articles, Offset: offset, PageSize: listPageSize,
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
	bySource, err := s.store.TalliesBySource(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	byDay, err := s.store.TalliesByDay(r.Context(), statsDays)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	library, err := s.store.Articles(r.Context(), interests, int(threshold))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	tokens, err := s.store.Tokens(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	tokensByInterest, err := s.store.TokensByInterest(r.Context(), interests)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	tokensByDay, err := s.store.TokensByDay(r.Context(), statsDays)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	backlogs, err := s.store.Backlogs(r.Context())
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

	s.render(w, r, "stats.html", &page{
		Title: "Statistics", BySource: bySource, ByDay: byDay,
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
func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data *page) {
	interests, threshold, err := s.currentValues(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Interests = interests
	data.Threshold = threshold

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
