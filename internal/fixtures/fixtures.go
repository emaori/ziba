// Package fixtures holds a frozen corpus of real inputs — feeds, article
// pages and newsletters — and the machinery to serve them to a test.
//
// It exists because the live integration suite cannot assert anything exact.
// Real sources publish different articles every hour, so the most a test
// against them can say is "something arrived and it parsed". Every bug worth
// remembering in this project was found by reading one particular message: a
// newsletter that yielded nothing because its provider wrapped every link, an
// issue that dropped the article named in its own subject, a digest that turned
// one essay into nine citations. Those messages scroll out of a mailbox within
// days. Frozen here, they keep their bugs fixed.
//
// The corpus is captured from the real sources by the tagged test alongside
// this file, and scrubbed on the way in — see Scrub.
package fixtures

import (
	"embed"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed testdata
var corpus embed.FS

// Read returns one fixture by its path within the corpus, such as
// "mail/cd619.eml".
func Read(name string) ([]byte, error) {
	data, err := corpus.ReadFile("testdata/" + name)
	if err != nil {
		return nil, fmt.Errorf("fixture %s: %w", name, err)
	}
	return data, nil
}

// List returns the fixture paths under a directory, such as "mail".
func List(dir string) ([]string, error) {
	entries, err := fs.ReadDir(corpus, "testdata/"+dir)
	if err != nil {
		return nil, fmt.Errorf("fixture directory %s: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, dir+"/"+entry.Name())
		}
	}
	return names, nil
}

// Web maps a web address to its path in the corpus: the host and path, with
// the query dropped and separators flattened, so that one file is one address.
//
// The query is dropped deliberately. A newsletter's tracker carries a different
// query for every recipient and every send, and the corpus should hold one copy
// of a page rather than one per campaign.
func Web(url string) string {
	trimmed := strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
	if query := strings.IndexByte(trimmed, '?'); query >= 0 {
		trimmed = trimmed[:query]
	}
	host, path, _ := strings.Cut(trimmed, "/")
	path = strings.Trim(path, "/")
	if path == "" {
		path = "index"
	}
	return "web/" + host + "_" + strings.ReplaceAll(path, "/", "_")
}

// Client returns an http.Client that answers from the corpus instead of the
// network, and fails loudly on anything it does not hold.
//
// A transport rather than a test server: the recorded addresses stay exactly as
// captured, so a feed's own links resolve without rewriting the feed, and a
// test reads the same addresses a person would. Nothing here can reach the
// network, so there is no need to restrict which hosts it will answer for — a
// miss is an error naming the file it looked for.
func Client() *http.Client {
	return &http.Client{Transport: transport{}}
}

type transport struct{}

func (transport) RoundTrip(req *http.Request) (*http.Response, error) {
	name := Web(req.URL.String())

	body, err := Read(name)
	if err != nil {
		// A captured refusal is a real answer: the corpus records those too.
		if status, ok := refusals[name]; ok {
			return response(req, status, nil), nil
		}
		return nil, fmt.Errorf("no fixture for %s (looked for %s)", req.URL, name)
	}

	// A recorded redirect is a file holding only its destination.
	if target, found := strings.CutPrefix(string(body), redirectPrefix); found {
		resp := response(req, http.StatusFound, nil)
		resp.Header.Set("Location", strings.TrimSpace(target))
		return resp, nil
	}
	return response(req, http.StatusOK, body), nil
}

func response(req *http.Request, status int, body []byte) *http.Response {
	resp := &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": {"text/html; charset=utf-8"}},
		Body:       http.NoBody,
		Request:    req,
	}
	if body != nil {
		resp.Body = io.NopCloser(strings.NewReader(string(body)))
		if strings.Contains(req.URL.Path, "rss") || strings.Contains(req.URL.Path, "feed") {
			resp.Header.Set("Content-Type", "application/rss+xml")
		}
	}
	return resp
}

// redirectPrefix marks a fixture that stands for a redirect rather than a page.
const redirectPrefix = "ziba-fixture-redirect: "

// refusals are the addresses the corpus records as refusing us. Keeping them is
// the point: a paywall, and a tracker leading to one, are behaviours the
// pipeline has to get right and neither can be captured as a page.
var refusals = map[string]int{
	"web/paywalled.example_article": http.StatusForbidden,
}
