// Package web serves the reading interface: the daily digest, browsing by
// category, the archive, and the article reader.
//
// Pages are rendered on the server with html/template. There is no build step
// and no JavaScript toolchain: one binary serves the whole interface, which is
// the point of a Go monolith on a homeserver.
package web

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed templates/*.html static/*
var assets embed.FS

// templateFuncs are the helpers available inside every template.
var templateFuncs = template.FuncMap{
	"paragraphs": paragraphs,
	"shortDate":  shortDate,
	"join":       strings.Join,
	"add":        func(a, b int) int { return a + b },
}

// paragraphs splits stored article text into paragraphs for the reader.
//
// The text is plain: extraction stripped the markup, which is what makes it
// safe to store and re-render. Returning []string rather than HTML keeps it
// that way — the template escapes each paragraph, so a page can never inject
// markup into the reader.
func paragraphs(text string) []string {
	var out []string
	for _, block := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(block); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func shortDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("2 Jan 2006")
}

// Server holds everything the handlers need.
type Server struct {
	store Store

	// pages holds one fully independent template set per page, keyed by file
	// name. They cannot share a set: every page defines a template called
	// "content", and in a shared set the last one parsed would silently
	// overwrite the rest — every page would then render the same body.
	pages map[string]*template.Template
}

// New parses the templates and wires the routes. Parsing at startup means a
// broken template fails on boot rather than on the first request that hits it.
func New(store Store) (*Server, error) {
	files, err := fs.Glob(assets, "templates/*.html")
	if err != nil {
		return nil, err
	}

	pages := make(map[string]*template.Template, len(files))
	for _, file := range files {
		name := path.Base(file)
		if name == layoutFile {
			continue // the frame, not a page
		}

		set, err := template.New(name).Funcs(templateFuncs).
			ParseFS(assets, "templates/"+layoutFile, file)
		if err != nil {
			return nil, fmt.Errorf("parse template %s: %w", name, err)
		}
		pages[name] = set
	}

	if len(pages) == 0 {
		return nil, fmt.Errorf("no page templates found")
	}
	return &Server{store: store, pages: pages}, nil
}

// layoutFile holds the frame every page is rendered into.
const layoutFile = "layout.html"

// Handler returns the routed handler for the whole interface.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", s.handleDigest)
	mux.HandleFunc("GET /article/{id}", s.handleArticle)
	mux.HandleFunc("GET /category/{name}", s.handleCategory)
	mux.HandleFunc("GET /archive", s.handleArchive)
	mux.Handle("GET /static/", http.FileServerFS(assets))

	return mux
}
