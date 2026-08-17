package web

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	nethtml "golang.org/x/net/html"

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

type configurableFakeStore struct {
	*fakeStore
	configuration store.Configuration
	saved         store.Configuration
	collectNow    bool
}

func (f *configurableFakeStore) Configuration(context.Context) (store.Configuration, error) {
	return f.configuration, nil
}

func (f *configurableFakeStore) SaveConfiguration(_ context.Context, interests config.Interests, sources []domain.Source) error {
	f.saved = store.Configuration{Configured: true, Interests: interests, Sources: sources, Schedule: f.configuration.Schedule}
	f.configuration = f.saved
	return nil
}

func (f *configurableFakeStore) FinishSetup(ctx context.Context, interests config.Interests, sources []domain.Source, collectNow bool) error {
	f.collectNow = collectNow
	return f.SaveConfiguration(ctx, interests, sources)
}

func (f *configurableFakeStore) SaveSetupInterests(_ context.Context, interests config.Interests) error {
	f.configuration.Interests = interests
	return nil
}

func (f *configurableFakeStore) SaveSetupSources(_ context.Context, interests config.Interests, sources []domain.Source) error {
	f.configuration.Interests = interests
	for i := range sources {
		if sources[i].ID == 0 {
			sources[i].ID = int64(i + 1)
		}
	}
	f.configuration.Sources = sources
	return nil
}

func (f *configurableFakeStore) DeleteSetupSource(_ context.Context, id int64) error {
	for i := range f.configuration.Sources {
		if f.configuration.Sources[i].ID == id {
			f.configuration.Sources = append(f.configuration.Sources[:i], f.configuration.Sources[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("setup source not found")
}

func (f *configurableFakeStore) SaveSchedule(_ context.Context, schedule config.CollectionSchedule) error {
	f.configuration.Schedule = schedule
	return nil
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

func (f *fakeStore) Tokens(context.Context) (store.TokenTally, error) {
	return store.TokenTally{Articles: 40, Input: 1904322, Output: 20480}, nil
}

func (f *fakeStore) TokensByInterest(_ context.Context, _ []string) ([]store.TokenTally, error) {
	return []store.TokenTally{
		{Label: "AI", Articles: 25, Input: 1200000, Output: 12000},
		{Label: "Computer Science", Articles: 15, Input: 704322, Output: 8480},
	}, nil
}

func (f *fakeStore) TokensByDay(_ context.Context, _ int) ([]store.TokenTally, error) {
	return []store.TokenTally{
		{Day: date(2026, 8, 10), Articles: 40, Input: 1904322, Output: 20480},
	}, nil
}

func (f *fakeStore) Backlogs(context.Context) ([]store.Backlog, error) {
	return []store.Backlog{
		{Stage: "Roundup expansion", Pending: 1},
		{Stage: "Full-text retrieval", Pending: 3, Oldest: time.Now().Add(-2 * time.Hour)},
		{Stage: "Analysis", Pending: 4, Failed: 1, Oldest: time.Now().Add(-3 * 24 * time.Hour)},
	}, nil
}

func (f *fakeStore) TalliesBySource(context.Context) ([]store.Tally, error) {
	return []store.Tally{{
		Source: "IEEE Spectrum", Links: 30, Stored: 24, Duplicate: 4, Skipped: 2,
	}}, nil
}

func (f *fakeStore) TalliesByDay(context.Context, int) ([]store.DayTally, error) {
	return []store.DayTally{{
		Tally: store.Tally{Day: date(2026, 8, 8), Links: 12, Stored: 11, Duplicate: 1, Provenance: 3},
		Sources: []store.Tally{
			{Source: "Il Post", Links: 9, Stored: 9},
			{Source: "Newsletters", Links: 3, Stored: 2, Duplicate: 1, Provenance: 3},
		},
	}}, nil
}

func (f *fakeStore) Articles(context.Context, []string, int) (store.ArticleStats, error) {
	return store.ArticleStats{
		Total: 443, Analyzed: 440, Shown: 300, Hidden: 140, NoText: 9,
		Archived: 5, AboveScore: 210,
	}, nil
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
	server, err := newServer(s, testInterests(), testCSRFToken)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	return server.Handler()
}

const testCSRFToken = "test-csrf-token"

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
		{"home is the latest selection", "/", `class="card"`},
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

// An empty digest is a normal state — recent collection may not have cleared
// the threshold — so it must render, not fail.
func TestEmptyDigestRenders(t *testing.T) {
	handler := newTestServer(t, &fakeStore{})

	code, body := get(t, handler, "/")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if !strings.Contains(body, "ziba run") {
		t.Error("empty digest page does not explain how to generate one")
	}
}

func TestFirstRunIsGatedBySetupWizard(t *testing.T) {
	db := &configurableFakeStore{fakeStore: &fakeStore{}, configuration: store.Configuration{Schedule: config.CollectionSchedule{Every: 6 * time.Hour, At: config.TimeOfDay{Hour: 4}}}}
	server, err := newServer(db, config.Interests{}, "setup-token")
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	handler := server.Handler()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/setup/interests" {
		t.Fatalf("home response = %d %q, want setup redirect", rec.Code, rec.Header().Get("Location"))
	}
	req = httptest.NewRequest(http.MethodGet, "/setup/interests", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, `href="/stats"`) || strings.Contains(body, `href="/archive"`) {
		t.Fatal("normal navigation is visible during setup")
	}
	if !strings.Contains(body, "Or start with") {
		t.Fatal("setup does not offer preconfigured interests")
	}
	req = httptest.NewRequest(http.MethodGet, "/setup/interest/new", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	body = rec.Body.String()
	if !strings.Contains(body, "Separate subtopics with commas") {
		t.Fatal("setup does not explain the subtopic format")
	}

	form := url.Values{
		"csrf_token": {"setup-token"}, "topic": {"AI"}, "priority": {"1"},
		"subtopics": {"agents, models"}, "note": {"Practical work"},
	}
	req = httptest.NewRequest(http.MethodPost, "/setup/interest/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/setup/interests" {
		t.Fatalf("interest step = %d %q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	form = url.Values{"csrf_token": {"setup-token"}, "name": {"Example"}, "type": {"rss"}, "url": {"https://example.com/feed"}, "enabled": {"on"}, "collect_from": {"7d"}}
	req = httptest.NewRequest(http.MethodPost, "/setup/source/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/setup/sources" {
		t.Fatalf("add source = %d %q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	form = url.Values{"csrf_token": {"setup-token"}}
	req = httptest.NewRequest(http.MethodPost, "/setup/sources", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/setup/schedule" {
		t.Fatalf("source step = %d %q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/setup/schedule", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	body = rec.Body.String()
	if !strings.Contains(body, `name="collect_every_amount" min="0" step="1" value="6"`) || !strings.Contains(body, `value="hours" selected`) || !strings.Contains(body, `value="04:00"`) || !strings.Contains(body, `name="collect_now" checked`) {
		t.Fatalf("schedule defaults were not proposed: %s", body)
	}
	form = url.Values{"csrf_token": {"setup-token"}, "collect_every_amount": {"6"}, "collect_every_unit": {"hours"}, "collect_at": {"04:00"}, "collect_now": {"on"}}
	req = httptest.NewRequest(http.MethodPost, "/setup/schedule", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("schedule step = %d %q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if !db.saved.Configured || len(db.saved.Sources) != 1 || db.saved.Interests.Topics[0].Topic != "AI" {
		t.Errorf("saved configuration = %+v", db.saved)
	}
	if !db.collectNow {
		t.Error("setup did not request the default first collection")
	}
}

func TestWizardAddsPreconfiguredSources(t *testing.T) {
	db := &configurableFakeStore{fakeStore: &fakeStore{}, configuration: store.Configuration{
		Interests: config.Interests{Threshold: 60, Topics: []config.Interest{{Topic: "AI", Priority: 1}}},
	}}
	server, err := newServer(db, config.Interests{}, "token")
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	for _, id := range []string{"ieee-spectrum", "hacker-news"} {
		form := url.Values{"csrf_token": {"token"}}
		req := httptest.NewRequest(http.MethodPost, "/setup/sources/preset/"+id, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
	}
	if len(db.configuration.Sources) != 2 {
		t.Fatalf("saved %d sources, want 2", len(db.configuration.Sources))
	}
}

func TestWizardCanRemoveDraftSource(t *testing.T) {
	db := &configurableFakeStore{fakeStore: &fakeStore{}, configuration: store.Configuration{
		Interests: config.Interests{Threshold: 60, Topics: []config.Interest{{Topic: "AI", Priority: 1}}},
		Sources:   []domain.Source{{ID: 7, Name: "Draft feed", Type: domain.SourceTypeRSS, URL: "https://example.com/feed", Enabled: true}},
	}}
	server, err := newServer(db, config.Interests{}, "token")
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	handler := server.Handler()
	code, body := get(t, handler, "/setup/source/7/remove")
	if code != http.StatusOK || !strings.Contains(body, "Remove “Draft feed”?") {
		t.Fatalf("confirmation = %d %q", code, body)
	}
	form := url.Values{"csrf_token": {"token"}}
	req := httptest.NewRequest(http.MethodPost, "/setup/source/7/remove", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || len(db.configuration.Sources) != 0 {
		t.Fatalf("remove response = %d, sources = %d", rec.Code, len(db.configuration.Sources))
	}
}

func TestNewsletterCredentialsAreNotRendered(t *testing.T) {
	db := &configurableFakeStore{fakeStore: &fakeStore{}, configuration: store.Configuration{
		Configured: true,
		Interests:  config.Interests{Threshold: 60, Topics: []config.Interest{{Topic: "AI", Priority: 1}}},
		Sources: []domain.Source{{ID: 1, Name: "Mail", Type: domain.SourceTypeNewsletter,
			URL: "imaps://mail.example/INBOX", Enabled: true,
			Newsletter: &domain.NewsletterOptions{Folder: "INBOX", Username: "private-user", Password: "private-password", LookBackDays: 1}}},
	}}
	server, err := newServer(db, db.configuration.Interests, "token")
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/settings/source/1", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, body)
	}
	if strings.Contains(body, "private-user") || strings.Contains(body, "private-password") {
		t.Fatal("stored newsletter credentials were rendered")
	}
}

func TestScheduleSettingsSaveToConfiguration(t *testing.T) {
	db := &configurableFakeStore{fakeStore: &fakeStore{}, configuration: store.Configuration{
		Configured: true,
		Interests:  config.Interests{Threshold: 60, Topics: []config.Interest{{Topic: "AI", Priority: 1}}},
		Schedule:   config.CollectionSchedule{Every: 6 * time.Hour, At: config.TimeOfDay{Hour: 4}},
	}}
	server, err := newServer(db, db.configuration.Interests, "token")
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	code, body := get(t, server.Handler(), "/settings/schedule")
	if code != http.StatusOK || !strings.Contains(body, "Enter 0 to stop scheduled collection") {
		t.Fatalf("schedule page = %d body=%s", code, body)
	}
	form := url.Values{"csrf_token": {"token"}, "collect_every_amount": {"8"}, "collect_every_unit": {"hours"}, "collect_at": {"05:15"}}
	req := httptest.NewRequest(http.MethodPost, "/settings/schedule", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || db.configuration.Schedule.Every != 8*time.Hour || db.configuration.Schedule.At.String() != "05:15" {
		t.Fatalf("saved schedule = %+v, status = %d", db.configuration.Schedule, rec.Code)
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
	form := url.Values{"csrf_token": {testCSRFToken}}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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

func TestArchiveRequiresCSRFToken(t *testing.T) {
	store := &fakeStore{article: sampleArticle()}
	handler := newTestServer(t, store)

	for _, form := range []url.Values{
		{},
		{"csrf_token": {"wrong-token"}},
	} {
		req := httptest.NewRequest(http.MethodPost, "/article/42/archive",
			strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("token %q: status = %d, want 403", form.Get("csrf_token"), rec.Code)
		}
	}
	if len(store.archivedCalls) != 0 {
		t.Errorf("invalid CSRF requests changed state: %v", store.archivedCalls)
	}
}

func TestArchiveFormCarriesCSRFToken(t *testing.T) {
	handler := newTestServer(t, &fakeStore{article: sampleArticle()})

	_, body := get(t, handler, "/article/42")
	want := `name="csrf_token" value="` + testCSRFToken + `"`
	if !strings.Contains(body, want) {
		t.Error("archive form does not carry the CSRF token")
	}
}

func TestArchiveRejectsUnknownAction(t *testing.T) {
	store := &fakeStore{article: sampleArticle()}
	handler := newTestServer(t, store)

	rec := post(t, handler, "/article/42/delete", "http://example.com/")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if len(store.archivedCalls) != 0 {
		t.Errorf("unknown action changed state: %v", store.archivedCalls)
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

	form := url.Values{"csrf_token": {testCSRFToken}}
	req := httptest.NewRequest(http.MethodPost, "/article/42/archive", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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

// The reader repeats both controls at the head of the page, so a long article
// need not be scrolled to its end to be marked read or opened at source.
func TestReaderRepeatsActionsAtTop(t *testing.T) {
	article := sampleArticle()
	handler := newTestServer(t, &fakeStore{article: article})

	_, body := get(t, handler, "/article/42")

	if got := strings.Count(body, `action="/article/42/archive"`); got != 2 {
		t.Errorf("mark-read control appears %d times, want 2 (head and foot)", got)
	}
	if got := strings.Count(body, `Open original`); got != 2 {
		t.Errorf("open-original link appears %d times, want 2 (head and foot)", got)
	}

	// Above the body, or it is not the point. The first copy must come before
	// the article text starts.
	if strings.Index(body, `action="/article/42/archive"`) > strings.Index(body, `class="body"`) {
		t.Error("the first mark-read control is below the article text")
	}
}

// The mark-read control must really be inside the row of buttons.
//
// Checking the markup as a string is not enough, and missing that cost three
// attempts at "aligning" a control that was never in the row: the form used to
// sit inside a <p>, which cannot contain one. A browser closes the paragraph
// and reparents the form next to it, so the source read correctly while the
// page rendered with the button on its own line below. Parsing here reproduces
// exactly that, because x/net/html implements the same algorithm.
func TestActionsRowActuallyContainsTheForm(t *testing.T) {
	article := sampleArticle()
	handler := newTestServer(t, &fakeStore{
		article:  article,
		articles: []domain.Article{article},
		digest:   domain.Digest{Date: time.Now(), Articles: []domain.Article{article}},
	})

	for _, path := range []string{"/", "/interest/Robotics", "/article/42"} {
		_, body := get(t, handler, path)

		doc, err := nethtml.Parse(strings.NewReader(body))
		if err != nil {
			t.Fatalf("%s: parsing the page failed: %v", path, err)
		}

		forms := 0
		var walk func(*nethtml.Node, bool)
		walk = func(n *nethtml.Node, inActions bool) {
			if n.Type == nethtml.ElementNode {
				if n.Data == "form" && hasClass(n, "inline") {
					forms++
					if !inActions {
						t.Errorf("%s: the mark-read form is not inside .actions "+
							"— an invalid nesting the browser will rearrange", path)
					}
				}
				if hasClass(n, "actions") {
					inActions = true
				}
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c, inActions)
			}
		}
		walk(doc, false)

		if forms == 0 {
			t.Errorf("%s: found no mark-read form at all", path)
		}
	}
}

func hasClass(n *nethtml.Node, want string) bool {
	for _, a := range n.Attr {
		if a.Key == "class" {
			for _, c := range strings.Fields(a.Val) {
				if c == want {
					return true
				}
			}
		}
	}
	return false
}

// Marking read is last in its row and pushed to the right edge, on every page
// that shows it. It is a different kind of act from the ways of opening an
// article, and sits apart from them. This is a choice, not an accident of
// template order, so both halves of it are asserted.
func TestMarkReadSitsWhereItShould(t *testing.T) {
	article := sampleArticle()
	handler := newTestServer(t, &fakeStore{
		article:  article,
		articles: []domain.Article{article},
		digest:   domain.Digest{Date: time.Now(), Articles: []domain.Article{article}},
	})

	for _, path := range []string{"/", "/interest/Robotics", "/article/42"} {
		_, body := get(t, handler, path)

		form := strings.Index(body, `<form class="inline"`)
		link := strings.Index(body, `Open original`)
		if form < 0 || link < 0 {
			t.Fatalf("%s: expected both controls, got form=%d link=%d", path, form, link)
		}
		if form < link {
			t.Errorf("%s: mark-read comes before the open-original link, want it last", path)
		}
	}

	// Last in the markup only puts it at the right edge because of this rule.
	css, err := assets.ReadFile("static/style.css")
	if err != nil {
		t.Fatalf("reading the stylesheet: %v", err)
	}
	if !strings.Contains(string(css), ".actions form.inline { margin-left: auto; }") {
		t.Error("the rule pushing the control to the right edge is gone")
	}
}

// An article whose page could not be fetched — paywalled, or refusing the
// request — must say so rather than showing an empty reading column.
func TestReaderExplainsMissingText(t *testing.T) {
	article := sampleArticle()
	article.FullText = ""
	handler := newTestServer(t, &fakeStore{article: article})

	_, body := get(t, handler, "/article/42")

	if !strings.Contains(body, "could not be retrieved") {
		t.Error("an article with no text renders a blank column with no explanation")
	}
	// And an article that does have text must not be told it has none.
	handler = newTestServer(t, &fakeStore{article: sampleArticle()})
	if _, body := get(t, handler, "/article/42"); strings.Contains(body, "could not be retrieved") {
		t.Error("an article with text claims its text is missing")
	}
}

// The statistics page must show the figures it was given, and must reach the
// day view from a day's row: the numbers are only useful if the articles behind
// them are one click away.
func TestStatsPage(t *testing.T) {
	handler := newTestServer(t, &fakeStore{})

	code, body := get(t, handler, "/stats")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}

	for _, want := range []string{
		"443",           // articles stored
		"140",           // hidden for matching no interest
		"IEEE Spectrum", // the by-source row
		"Processing queues",
		"Full-text retrieval",
		"Analysis",
		"/day?date=2026-08-08", // the by-day row links to that day
	} {
		if !strings.Contains(body, want) {
			t.Errorf("statistics page is missing %q", want)
		}
	}

	// Collected is a derived figure and easy to get wrong: 30 links, no
	// provenance, no roundups.
	if !strings.Contains(body, ">30<") {
		t.Error("the by-source row does not show its collected total")
	}
	// And on the day row: 12 links plus 3 provenance.
	if !strings.Contains(body, ">15<") {
		t.Error("the by-day row does not add provenance into its collected total")
	}

	// Each day is broken down by the sources that made it up.
	byDay := body[strings.Index(body, "By day collected"):]
	for _, want := range []string{"Il Post", "Newsletters", `class="day-source"`} {
		if !strings.Contains(byDay, want) {
			t.Errorf("the by-day table is missing %q", want)
		}
	}
}

// The token figures are the reason the page is worth opening now: they are the
// only place the running cost of this project is visible.
func TestStatsPageShowsTokens(t *testing.T) {
	handler := newTestServer(t, &fakeStore{})

	code, body := get(t, handler, "/stats")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}

	// Expectations are written with ordinary spaces and converted, because the
	// separator is a thin space: a literal one is invisible in source, and an
	// expectation nobody can read or retype correctly is worse than no test.
	grouped := func(s string) string { return strings.ReplaceAll(s, " ", thousandsSeparator) }

	tokens := body[strings.Index(body, ">Tokens<"):]
	for _, want := range []string{
		grouped("1 924 802"), // the overall total: 1904322 in + 20480 out
		grouped("1 904 322"), // input alone, because it is priced separately
		grouped("20 480"),    // and output
		grouped("48 120"),    // per article: 1924802 / 40
		"Computer Science",
		grouped("704 322"), // the interest row
	} {
		if !strings.Contains(tokens, want) {
			t.Errorf("the token section is missing %q", want)
		}
	}

	// An interest row leads to the articles behind it, as the day rows do.
	if !strings.Contains(tokens, `href="/interest/Computer%20Science"`) {
		t.Error("an interest row does not link to its articles")
	}
	// The by-day figures are labelled by analysis, not collection: the same page
	// already has two tables grouped by collection and confusing them would make
	// a backfill look like a month of spending.
	if !strings.Contains(tokens, "By day analyzed") {
		t.Error("the token day table does not say which day it means")
	}
}

// Seven-figure counts have to be readable at a glance, and the separator must
// not turn into a decimal point for one of the reader's two languages.
func TestThousands(t *testing.T) {
	for input, want := range map[int64]string{
		0: "0", 7: "7", 999: "999", 1000: "1 000",
		20480: "20 480", 1904322: "1 904 322", -4310: "-4 310",
	} {
		want = strings.ReplaceAll(want, " ", thousandsSeparator)
		if got := thousands(input); got != want {
			t.Errorf("thousands(%d) = %q, want %q", input, got, want)
		}
	}
}

// The page is reachable from every other one, or it will not be looked at.
func TestStatsIsInTheNav(t *testing.T) {
	handler := newTestServer(t, &fakeStore{})
	if _, body := get(t, handler, "/"); !strings.Contains(body, `href="/stats"`) {
		t.Error("the masthead does not link to the statistics page")
	}
}
