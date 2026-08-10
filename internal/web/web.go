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
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/emaori/ziba/internal/config"
	"github.com/emaori/ziba/internal/domain"
)

//go:embed templates/*.html static/*
var assets embed.FS

// templateFuncs are the helpers available inside every template.
var templateFuncs = template.FuncMap{
	"paragraphs": paragraphs,
	"shortDate":  shortDate,
	"join":       strings.Join,
	"add":        func(a, b int) int { return a + b },

	// Token counts run to seven figures, and an unbroken run of digits cannot
	// be read at a glance — the difference between 1904322 and 190432 is the
	// whole point of the table.
	"thousands": thousands,
	"isoDate":   func(t time.Time) string { return t.Format(time.DateOnly) },

	// pathEscape, not the built-in urlquery, for anything that lands in a URL
	// *path*. urlquery writes a space as "+", which a query string decodes back
	// to a space but a path does not — so "Computer Science" became the literal
	// "Computer+Science" and matched no interest. Paths need "%20".
	"pathEscape": url.PathEscape,

	// dayLink builds an address for the day view, carrying the interest filter
	// along. Written here rather than assembled in the template so that the two
	// halves are escaped the way each needs — and so a link cannot be built
	// with only one of them remembered.
	"dayLink": dayLink,
}

// dayLink addresses one day, keeping the interest filter if there is one.
func dayLink(day time.Time, interest string) string {
	query := url.Values{"date": {day.Format(time.DateOnly)}}
	if interest != "" {
		query.Set("interest", interest)
	}
	return "/day?" + query.Encode()
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

	// interests drive the tab bar and are the only values the interest routes
	// accept. They come from the configuration file rather than from whatever
	// categories happen to be in the database, so the tabs are stable and are
	// the reader's own subjects rather than the model's invention.
	interests []string
	threshold domain.RelevanceScore

	// pages holds one fully independent template set per page, keyed by file
	// name. They cannot share a set: every page defines a template called
	// "content", and in a shared set the last one parsed would silently
	// overwrite the rest — every page would then render the same body.
	pages map[string]*template.Template
}

// New parses the templates and wires the routes. Parsing at startup means a
// broken template fails on boot rather than on the first request that hits it.
func New(store Store, interests config.Interests) (*Server, error) {
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

	names := make([]string, 0, len(interests.Topics))
	for _, topic := range interests.Topics {
		names = append(names, topic.Topic)
	}

	return &Server{
		store:     store,
		interests: names,
		threshold: domain.RelevanceScore(interests.Threshold),
		pages:     pages,
	}, nil
}

// layoutFile holds the frame every page is rendered into.
const layoutFile = "layout.html"

// Handler returns the routed handler for the whole interface.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", s.handleDigest)
	mux.HandleFunc("GET /interest/{name}", s.handleInterest)
	mux.HandleFunc("GET /day", s.handleDay)
	mux.HandleFunc("GET /article/{id}", s.handleArticle)
	mux.HandleFunc("GET /archive", s.handleArchiveAll)
	mux.HandleFunc("GET /stats", s.handleStats)

	// Marking read changes state, so it is a post and never a link: a crawler
	// or a prefetching browser must not be able to empty the reading list.
	mux.HandleFunc("POST /article/{id}/{action}", s.handleArchive)

	mux.Handle("GET /static/", http.FileServerFS(assets))

	return mux
}

// thousands groups a number with thin spaces, so seven figures can be read
// without counting digits. A space rather than a comma or a full stop: the
// reader is Italian and English, and those two disagree about which of them
// means the decimal point.
// thousandsSeparator is a thin space (U+2009). Named rather than written
// inline because it is invisible in source: a test asserting on it with a
// literal would be a string nobody can read or retype correctly.
const thousandsSeparator = "\u2009"

func thousands(n int64) string {
	digits := strconv.FormatInt(n, 10)
	sign := ""
	if strings.HasPrefix(digits, "-") {
		sign, digits = "-", digits[1:]
	}

	var b strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteString(thousandsSeparator)
		}
		b.WriteRune(r)
	}
	return sign + b.String()
}
