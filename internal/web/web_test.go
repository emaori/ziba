package web

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/emaori/ziba/internal/config"
	"github.com/emaori/ziba/internal/domain"
	"github.com/emaori/ziba/internal/store"
)

// fakeStore serves fixed data, so the handlers can be exercised without a
// database. This is what declaring the Store interface in this package buys.
type fakeStore struct {
	digest   domain.Digest
	article  domain.Article
	articles []domain.Article
	missing  bool

	archivedCalls []bool
}

func (f *fakeStore) LatestDigest(context.Context) (domain.Digest, error) {
	return f.digest, nil
}

func (f *fakeStore) Article(_ context.Context, id int64) (domain.Article, error) {
	if f.missing {
		return domain.Article{}, pgx.ErrNoRows
	}
	return f.article, nil
}

func (f *fakeStore) SetArchived(_ context.Context, id int64, archived bool) error {
	if f.missing {
		return pgx.ErrNoRows
	}
	f.archivedCalls = append(f.archivedCalls, archived)
	return nil
}

func (f *fakeStore) ArticlesByInterest(context.Context, string, domain.RelevanceScore, int, int) ([]domain.Article, error) {
	return f.articles, nil
}

func (f *fakeStore) ArticlesOnDay(context.Context, string, time.Time, []string) ([]domain.Article, error) {
	return f.articles, nil
}

func (f *fakeStore) DayNavigation(context.Context, string, time.Time, []string) (store.DayNavigation, error) {
	return store.DayNavigation{
		First: date(2026, 7, 1), Last: date(2026, 8, 8),
		Prev: date(2026, 8, 6), Next: date(2026, 8, 8),
	}, nil
}

func (f *fakeStore) Archive(context.Context, int, int, []string) ([]domain.Article, error) {
	return f.articles, nil
}

func date(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func testInterests() config.Interests {
	return config.Interests{
		Threshold: 60,
		Topics: []config.Interest{
			{Topic: "AI", Priority: 1},
			{Topic: "Robotics", Priority: 2},
		},
	}
}

func newTestServer(t *testing.T, s Store) http.Handler {
	t.Helper()
	server, err := New(s, testInterests())
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	return server.Handler()
}

func get(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Code, rec.Body.String()
}

func sampleArticle() domain.Article {
	return domain.Article{
		ID:          42,
		SourceName:  "IEEE Spectrum",
		Title:       "A robot walks into a bar",
		URL:         "https://spectrum.ieee.org/robot",
		Author:      "Jane Writer",
		PublishedAt: time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC),
		Categories:  []string{"Robotics"},
		Summary:     "A summary of the piece.",
		Score:       82,
		ScoreReason: "matches robotics",
		FullText:    "First paragraph.\nSecond paragraph.\nThird paragraph.",
	}
}

// Each page must render its own body. Every page template defines a template
// called "content", so parsing them into one shared set would make the last one
// win and serve the same body everywhere — which is exactly what happened
// before the template sets were separated.
func TestEachPageRendersItsOwnContent(t *testing.T) {
	article := sampleArticle()
	handler := newTestServer(t, &fakeStore{
		digest:   domain.Digest{Date: time.Now(), Articles: []domain.Article{article}},
		article:  article,
		articles: []domain.Article{article},
	})

	tests := []struct {
		name   string
		path   string
		expect string // markup unique to that page
	}{
		{"home is the day's selection", "/", `class="card"`},
		{"article reader", "/article/42", `class="reader"`},
		{"interest", "/interest/Robotics", `class="card"`},
		{"day", "/day", `class="daypick"`},
		{"archive", "/archive", `class="pager"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, body := get(t, handler, tt.path)
			if code != http.StatusOK {
				t.Fatalf("status = %d, want 200", code)
			}
			if !strings.Contains(body, tt.expect) {
				t.Errorf("page does not contain %q — wrong template rendered?", tt.expect)
			}
			// The frame is shared, so every page must carry it.
			if !strings.Contains(body, `class="masthead"`) {
				t.Error("page is missing the layout")
			}
		})
	}
}

// The reader turns stored text into paragraphs rather than one wall of text.
func TestReaderRendersParagraphs(t *testing.T) {
	article := sampleArticle()
	handler := newTestServer(t, &fakeStore{article: article})

	code, body := get(t, handler, "/article/42")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if got := strings.Count(body, "<p>"); got < 3 {
		t.Errorf("got %d body paragraphs, want at least 3", got)
	}
	if !strings.Contains(body, "Second paragraph.") {
		t.Error("article text is missing")
	}
}

// Stored text is plain, and the reader must escape it. A page that could inject
// markup into the reader would be a hole straight through to every reader.
func TestReaderEscapesArticleText(t *testing.T) {
	article := sampleArticle()
	article.FullText = `<script>alert("xss")</script>`
	article.Title = `<img src=x onerror=alert(1)>`

	handler := newTestServer(t, &fakeStore{article: article})

	_, body := get(t, handler, "/article/42")

	// The test is whether a tag survives, not whether the words do: once the
	// angle brackets are escaped, "onerror=" is inert text on the page.
	//
	// Matched against what was injected rather than against "<script" alone,
	// because the layout legitimately loads one script of its own and a blanket
	// check would fail on that instead of on a real hole.
	for _, tag := range []string{"<script>alert", "<img"} {
		if strings.Contains(body, tag) {
			t.Errorf("%q was rendered as markup, not escaped", tag)
		}
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("expected the article text to appear escaped")
	}
}

func TestMissingArticleIs404(t *testing.T) {
	handler := newTestServer(t, &fakeStore{missing: true})

	if code, _ := get(t, handler, "/article/42"); code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", code)
	}
	if code, _ := get(t, handler, "/article/not-a-number"); code != http.StatusNotFound {
		t.Errorf("status = %d for a non-numeric id, want 404", code)
	}
}

// An empty digest is a normal state — the day's collection may not have cleared
// the threshold — so it must render, not fail.
func TestEmptyDigestRenders(t *testing.T) {
	handler := newTestServer(t, &fakeStore{})

	code, body := get(t, handler, "/")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if !strings.Contains(body, "ziba digest") {
		t.Error("empty digest page does not explain how to generate one")
	}
}

func TestParagraphs(t *testing.T) {
	got := paragraphs("First.\n\n  Second.  \n\nThird.\n")
	want := []string{"First.", "Second.", "Third."}

	if len(got) != len(want) {
		t.Fatalf("got %d paragraphs %q, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("paragraph %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func post(t *testing.T, h http.Handler, path, referer string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Marking read must be a post, not a link: a crawler or a prefetching browser
// following links must not be able to empty the reading list.
func TestArchivingIsNotReachableByGet(t *testing.T) {
	handler := newTestServer(t, &fakeStore{article: sampleArticle()})

	if code, _ := get(t, handler, "/article/42/archive"); code == http.StatusOK {
		t.Error("a GET marked an article read; it must only answer POST")
	}
}

func TestArchiveAndUnarchive(t *testing.T) {
	store := &fakeStore{article: sampleArticle()}
	handler := newTestServer(t, store)

	rec := post(t, handler, "/article/42/archive", "http://example.com/interest/Robotics")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	// Back to the page the button was pressed on, so the list closes over the
	// gap rather than throwing the reader to the front page.
	if got := rec.Header().Get("Location"); got != "/interest/Robotics" {
		t.Errorf("Location = %q, want the page it came from", got)
	}

	post(t, handler, "/article/42/unarchive", "http://example.com/day?date=2026-08-08")

	if len(store.archivedCalls) != 2 || !store.archivedCalls[0] || store.archivedCalls[1] {
		t.Errorf("archived calls = %v, want [true false]", store.archivedCalls)
	}
}

// The return address comes from the referer, so it must be checked. Following
// an arbitrary one would let another site bounce a visitor onward through us.
func TestArchiveWillNotRedirectOffSite(t *testing.T) {
	handler := newTestServer(t, &fakeStore{article: sampleArticle()})

	for _, referer := range []string{
		"https://elsewhere.example/phishing",
		"//elsewhere.example/phishing",
		"",
	} {
		rec := post(t, handler, "/article/42/archive", referer)
		if got := rec.Header().Get("Location"); got != "/" {
			t.Errorf("referer %q redirected to %q, want /", referer, got)
		}
	}
}

// The button has to be on both screens the reader might press it from.
func TestArchiveButtonAppearsOnCardsAndReader(t *testing.T) {
	article := sampleArticle()
	handler := newTestServer(t, &fakeStore{
		article:  article,
		articles: []domain.Article{article},
		digest:   domain.Digest{Date: time.Now(), Articles: []domain.Article{article}},
	})

	for _, path := range []string{"/", "/interest/Robotics", "/article/42"} {
		_, body := get(t, handler, path)
		if !strings.Contains(body, `action="/article/42/archive"`) {
			t.Errorf("%s has no mark-read control", path)
		}
	}
}

// An article already read offers the way back, not the way out again.
func TestReadArticleOffersUnarchive(t *testing.T) {
	article := sampleArticle()
	article.ArchivedAt = time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	handler := newTestServer(t, &fakeStore{article: article})

	_, body := get(t, handler, "/article/42")
	if !strings.Contains(body, `action="/article/42/unarchive"`) {
		t.Error("a read article does not offer a way to un-read it")
	}
}

// Interest routes accept only configured interests, so a stale or invented
// heading cannot render an empty page that looks real.
func TestUnknownInterestIs404(t *testing.T) {
	handler := newTestServer(t, &fakeStore{})

	if code, _ := get(t, handler, "/interest/Basket%20weaving"); code != http.StatusNotFound {
		t.Errorf("status = %d for an unconfigured interest, want 404", code)
	}
	if code, _ := get(t, handler, "/day?date=not-a-date"); code != http.StatusNotFound {
		t.Errorf("status = %d for a bad date, want 404", code)
	}
}

// Follow the links the templates actually emit, rather than asserting a path
// typed by hand.
//
// This is the test that was missing. The tab links were built with the query
// escaper, which writes a space as "+" — correct inside a query string, wrong
// in a path, where it stays a literal plus. Every interest whose name contains
// a space led to a 404, while a hand-written "%20" in the tests passed happily.
func TestEmittedLinksAreFollowable(t *testing.T) {
	article := sampleArticle()
	store := &fakeStore{
		article:  article,
		articles: []domain.Article{article},
		digest:   domain.Digest{Date: time.Now(), Articles: []domain.Article{article}},
	}

	// An interest whose name contains a space is the whole point.
	server, err := New(store, config.Interests{
		Threshold: 60,
		Topics: []config.Interest{
			{Topic: "Computer Science", Priority: 1},
			{Topic: ".NET", Priority: 2},
			{Topic: "Italian and International news", Priority: 3},
		},
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	handler := server.Handler()

	href := regexp.MustCompile(`href="(/(?:interest|day|article|digest|archive)[^"]*)"`)

	for _, from := range []string{"/", "/day", "/archive"} {
		code, body := get(t, handler, from)
		if code != http.StatusOK {
			t.Fatalf("%s: status = %d", from, code)
		}

		matches := href.FindAllStringSubmatch(body, -1)
		if len(matches) == 0 {
			t.Errorf("%s emitted no internal links to check", from)
		}

		for _, m := range matches {
			link := html.UnescapeString(m[1])
			if code, _ := get(t, handler, link); code != http.StatusOK {
				t.Errorf("%s links to %q, which answers %d", from, link, code)
			}
		}
	}
}

// The day view is reached by a date picker. The regression it replaced was a
// sideways-scrolling strip of days, so what matters is that the picker is a
// real date input, bounded by what the archive actually holds, and that moving
// around does not quietly drop the interest filter.
func TestDayPicker(t *testing.T) {
	handler := newTestServer(t, &fakeStore{articles: []domain.Article{sampleArticle()}})

	_, body := get(t, handler, "/day?date=2026-08-07&interest=AI")

	for _, want := range []string{
		`type="date"`,
		`name="date"`,
		`value="2026-08-07"`,
		// Bounded by the span that holds something, so the calendar cannot
		// offer a year of empty days.
		`min="2026-07-01"`,
		`max="2026-08-08"`,
		// The filter has to survive submitting the form.
		`type="hidden" name="interest" value="AI"`,
		// The arrows step to the nearest populated day, carrying the interest.
		`/day?date=2026-08-06&amp;interest=AI`,
		`/day?date=2026-08-08&amp;interest=AI`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("day page does not contain %q", want)
		}
	}

	// The strip is gone, not merely restyled.
	if strings.Contains(body, `class="days"`) {
		t.Error("the old day strip is still being rendered")
	}
}

// With no interest chosen the links must not invent one.
func TestDayPickerWithoutInterest(t *testing.T) {
	handler := newTestServer(t, &fakeStore{articles: []domain.Article{sampleArticle()}})

	_, body := get(t, handler, "/day")

	if strings.Contains(body, "interest=") {
		t.Error("day page carries an interest filter that was never asked for")
	}
	if !strings.Contains(body, `/day?date=2026-08-06`) {
		t.Error("day page has no link to the previous populated day")
	}
}

// A day the picker offers but nothing was published on must say so, and offer
// the way back rather than a dead end.
func TestEmptyDayPointsSomewhere(t *testing.T) {
	handler := newTestServer(t, &fakeStore{})

	code, body := get(t, handler, "/day?date=2026-08-07")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if !strings.Contains(body, "Nothing published on this day") {
		t.Error("an empty day does not say it is empty")
	}
	if !strings.Contains(body, `/day?date=2026-08-06`) {
		t.Error("an empty day offers no way to a day that has something")
	}
}

// Marking read from the page's own script must not answer with a redirect: the
// reload is the whole thing being avoided.
func TestArchiveAsyncGetsNoRedirect(t *testing.T) {
	store := &fakeStore{}
	handler := newTestServer(t, store)

	req := httptest.NewRequest(http.MethodPost, "/article/42/archive", nil)
	req.Header.Set("X-Ziba-Async", "1")
	req.Header.Set("Referer", "http://example.com/")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
	if location := rec.Header().Get("Location"); location != "" {
		t.Errorf("async post was redirected to %q, want no redirect", location)
	}
	if len(store.archivedCalls) != 1 || !store.archivedCalls[0] {
		t.Errorf("archivedCalls = %v, want one archive", store.archivedCalls)
	}
}

// The script is an enhancement, so the plain form post must keep redirecting.
func TestArchiveWithoutScriptStillRedirects(t *testing.T) {
	handler := newTestServer(t, &fakeStore{})

	rec := post(t, handler, "/article/42/archive", "http://example.com/interest/AI")

	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", rec.Code)
	}
	if want := "/interest/AI"; rec.Header().Get("Location") != want {
		t.Errorf("Location = %q, want %q", rec.Header().Get("Location"), want)
	}
}

// The script swaps the button between its two states, and takes both labels
// from the markup rather than keeping its own copy. If the template stops
// emitting them the enhancement silently blanks the button.
func TestArchiveButtonCarriesBothLabels(t *testing.T) {
	article := sampleArticle()
	handler := newTestServer(t, &fakeStore{
		article:  article,
		articles: []domain.Article{article},
	})

	for _, path := range []string{"/interest/Robotics", "/article/42"} {
		_, body := get(t, handler, path)
		for _, want := range []string{
			`data-alt-label="↩ Unread"`,
			`data-alt-title="Put this back in the reading list"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: button is missing %s", path, want)
			}
		}
	}

	// And the other way round for an article already read.
	read := sampleArticle()
	read.ArchivedAt = time.Now()
	handler = newTestServer(t, &fakeStore{article: read})

	_, body := get(t, handler, "/article/42")
	if !strings.Contains(body, `data-alt-label="✓ Mark read"`) {
		t.Error("an archived article's button does not offer the way back")
	}
}

// The enhancement is loaded on every page, and is the only script.
func TestScriptIsLoadedAndDeferred(t *testing.T) {
	handler := newTestServer(t, &fakeStore{article: sampleArticle()})

	_, body := get(t, handler, "/article/42")

	if !strings.Contains(body, `<script src="/static/app.js" defer></script>`) {
		t.Error("app.js is not loaded, or not deferred")
	}
	// Inline script would defeat the point of having one file, and is the thing
	// that quietly accumulates.
	if strings.Count(body, "<script") != 1 {
		t.Error("page carries more than one script")
	}
}
