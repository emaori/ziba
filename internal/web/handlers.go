package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/emaori/ziba/internal/domain"
	"github.com/emaori/ziba/internal/store"
)

// archivePageSize bounds how much of the archive one page shows.
const archivePageSize = 50

// Store is what the web package needs from persistence. Declaring the interface
// here, where it is consumed, keeps the handlers testable with a fake and
// documents exactly how much of the database this package can reach.
type Store interface {
	LatestDigest(ctx context.Context) (domain.Digest, error)
	Article(ctx context.Context, id int64) (domain.Article, error)
	Categories(ctx context.Context) ([]store.Category, error)
	ArticlesByCategory(ctx context.Context, category string, limit int) ([]domain.Article, error)
	Archive(ctx context.Context, limit, offset int) ([]domain.Article, error)
}

// page is the data every template receives. Categories appear in the navigation
// on every screen, so they are loaded for every page.
type page struct {
	Title      string
	Categories []store.Category

	Digest   domain.Digest
	Article  domain.Article
	Articles []domain.Article
	Category string
	Offset   int
	PageSize int
}

func (s *Server) handleDigest(w http.ResponseWriter, r *http.Request) {
	digest, err := s.store.LatestDigest(r.Context())
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.fail(w, r, err)
		return
	}
	// No digest generated yet is an empty page, not an error — the template
	// explains how to generate one.

	s.render(w, r, "digest.html", &page{Title: "Today", Digest: digest})
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

func (s *Server) handleCategory(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	articles, err := s.store.ArticlesByCategory(r.Context(), name, archivePageSize)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	s.render(w, r, "list.html", &page{Title: name, Category: name, Articles: articles})
}

func (s *Server) handleArchive(w http.ResponseWriter, r *http.Request) {
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}

	articles, err := s.store.Archive(r.Context(), archivePageSize, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	s.render(w, r, "list.html", &page{
		Title:    "Archive",
		Articles: articles,
		Offset:   offset,
		PageSize: archivePageSize,
	})
}

// render loads the navigation and writes the page.
func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data *page) {
	categories, err := s.store.Categories(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Categories = categories

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

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "path", r.URL.Path, "error", err)
	http.Error(w, "something went wrong", http.StatusInternalServerError)
}
