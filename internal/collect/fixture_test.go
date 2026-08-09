package collect

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/emaori/ziba/internal/domain"
	"github.com/emaori/ziba/internal/fixtures"
)

// These run against the frozen corpus, so they can assert exact numbers where
// the live suite can only say "something arrived". Every case here is a bug
// this project had: the counts are the ones measured when it was fixed.
//
// A number that changes is not automatically a failure — a rule may improve —
// but it must be a deliberate change, which is the point of writing it down.

func mailFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := fixtures.Read("mail/" + name)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return body
}

// fixtureMessage wraps a captured .eml as the buffer the collector receives
// from the server, so the corpus exercises the whole path and not just the
// parser.
func fixtureMessage(raw []byte) *imapclient.FetchMessageBuffer {
	subject := "Fixture"
	for _, line := range strings.Split(string(raw), "\n") {
		if after, found := strings.CutPrefix(line, "Subject: "); found {
			subject = strings.TrimSpace(after)
			break
		}
	}
	return &imapclient.FetchMessageBuffer{
		Envelope: &imap.Envelope{
			Subject:   subject,
			Date:      time.Date(2026, 8, 8, 18, 0, 0, 0, time.UTC),
			MessageID: "<" + subject + "@fixture.test>",
			From:      []imap.Address{{Name: "Publisher", Mailbox: "sender", Host: "fixture.test"}},
		},
		BodySection: []imapclient.FetchBodySectionBuffer{{Bytes: raw}},
	}
}

func TestNewslettersFromTheCorpus(t *testing.T) {
	tests := []struct {
		file    string
		anchors int    // every <a> in the message
		kept    int    // what survives the editorial rules
		essay   bool   // whether the message is itself the article
		pins    string // the bug this fixture exists for
	}{
		{
			file: "cd619.eml", anchors: 16, kept: 7,
			pins: "seven short anchors and two boilerplate paths dropped",
		},
		{
			file: "alphasignal.eml", anchors: 36, kept: 10,
			pins: "every story linked twice; six duplicates collapse",
		},
		{
			file: "aspnetcore.eml", anchors: 24, kept: 13,
			pins: "the provider wraps every link; blocking the domain lost the whole issue",
		},
		{
			file: "pd688.eml", anchors: 16, kept: 7,
			pins: "the lead article is 24 characters and must clear the threshold",
		},
		{
			file: "fowler.eml", anchors: 30, kept: 11, essay: true,
			pins: "an essay: its links are citations, so it becomes the article itself",
		},
	}

	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			t.Log(tt.pins)
			body := mailFixture(t, tt.file)

			text, links, err := readMessage(body)
			if err != nil {
				t.Fatalf("readMessage: %v", err)
			}
			if got := len(links); got != tt.kept {
				for _, l := range links {
					t.Logf("kept %s — %q", l.url, l.text)
				}
				t.Errorf("kept %d links, want %d", got, tt.kept)
			}
			if got := isEssay(text, links); got != tt.essay {
				t.Errorf("isEssay = %v, want %v", got, tt.essay)
			}

			// And the whole message, through the collector.
			items, err := new(Newsletter).itemsFromMessage(
				domain.Source{ID: 1, Name: "Newsletters", Type: domain.SourceTypeNewsletter},
				fixtureMessage(body))
			if err != nil {
				t.Fatalf("itemsFromMessage: %v", err)
			}

			if tt.essay {
				if len(items) != 1 || items[0].Kind != domain.ItemKindArticle {
					t.Errorf("an essay produced %d items, want one article", len(items))
				}
				return
			}
			if got := len(items); got != tt.kept+1 {
				t.Errorf("produced %d items, want %d links plus one provenance", got, tt.kept)
			}
			if items[0].Kind != domain.ItemKindProvenance {
				t.Errorf("first item is %q, want provenance", items[0].Kind)
			}
		})
	}
}

// The lead article of PD#688 is named in its own subject and was dropped for
// being one character under the old threshold. It must be kept.
func TestTheCorpusKeepsTheHeadlineThatWasLost(t *testing.T) {
	_, links, err := readMessage(mailFixture(t, "pd688.eml"))
	if err != nil {
		t.Fatalf("readMessage: %v", err)
	}
	for _, link := range links {
		if strings.Contains(link.text, "How big is a Git commit") {
			return
		}
	}
	for _, l := range links {
		t.Logf("kept %q", l.text)
	}
	t.Error(`"How big is a Git commit?" was dropped again: it is 24 characters`)
}

// "Set LogLevel of Blazor" is 22 characters and was lost to the old threshold
// of 25. It is here by name because the count alone would not say which article
// came back, and this is the one the change was for.
func TestTheCorpusKeepsTheShortHeadline(t *testing.T) {
	_, links, err := readMessage(mailFixture(t, "aspnetcore.eml"))
	if err != nil {
		t.Fatalf("readMessage: %v", err)
	}
	for _, link := range links {
		if link.text == "Set LogLevel of Blazor" {
			return
		}
	}
	t.Error(`"Set LogLevel of Blazor" was dropped: it is 22 characters`)
}

// The whole ASP.NET issue was lost when its provider's domain was blocked.
func TestTheCorpusKeepsTheProviderWrappedIssue(t *testing.T) {
	_, links, err := readMessage(mailFixture(t, "aspnetcore.eml"))
	if err != nil {
		t.Fatalf("readMessage: %v", err)
	}
	if len(links) == 0 {
		t.Fatal("the issue yielded nothing: the provider's domain is being blocked again")
	}
	for _, link := range links {
		if !strings.Contains(link.url, "list-manage.com") {
			continue
		}
		return // at least one provider-wrapped link survived, which is the point
	}
	t.Error("no provider-wrapped link survived")
}

// A roundup feed publishes issues, and an issue is opened for its links.
func TestRoundupFeedFromTheCorpus(t *testing.T) {
	ctx := context.Background()
	client := fixtures.Client()
	src := domain.Source{
		ID: 2, Name: ".NET Ketchup", Type: domain.SourceTypeRSS,
		URL: "https://dotnetketchup.com/rss", Roundup: true,
	}

	items, err := NewRSS(client, testLogger()).Collect(ctx, src)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(items) < 200 {
		t.Errorf("the feed yielded %d entries, want the whole backlog", len(items))
	}
	for _, item := range items {
		if item.Kind != domain.ItemKindRoundup {
			t.Fatalf("%s is %q, want every entry marked as an issue", item.URL, item.Kind)
		}
	}

	// One issue, opened.
	links, err := NewRoundup(client).Links(ctx, domain.RawItem{
		SourceID: src.ID, Kind: domain.ItemKindRoundup,
		URL: "https://dotnetketchup.com/?year=2026&week=32",
	})
	if err != nil {
		t.Fatalf("Links: %v", err)
	}
	// Eight, as this issue stood when it was captured. The live page said seven
	// a day earlier — the publisher added a piece — which is the whole argument
	// for freezing it: the number now describes the fixture, not the world, and
	// it changes only when somebody changes the rules.
	if len(links) != 8 {
		for _, l := range links {
			t.Logf("kept %s — %q", l.URL, l.Title)
		}
		t.Errorf("the issue yielded %d links, want 8", len(links))
	}
	for _, link := range links {
		if strings.Contains(link.URL, "youtube") || strings.Contains(link.URL, "youtu.be") {
			t.Errorf("a video was kept: %s", link.URL)
		}
	}
}

// A tracker resolves to the article's own address, and keeps it even when the
// page behind it refuses to be read.
func TestTrackersFromTheCorpus(t *testing.T) {
	ctx := context.Background()
	full := NewFullText(fixtures.Client())

	got, err := full.Article(ctx, domain.RawItem{
		URL: "https://tracker.example/c", Title: "Behind a tracker",
	})
	if err != nil {
		t.Fatalf("Article: %v", err)
	}
	if strings.Contains(got.URL, "tracker.example") {
		t.Errorf("URL = %q, want the address the tracker led to", got.URL)
	}
	if got.FullText == "" {
		t.Error("no text was retrieved from behind the tracker")
	}

	// The same, but the destination refuses us.
	refused, err := full.Article(ctx, domain.RawItem{
		URL: "https://tracker.example/paywalled", Title: "Behind a paywall",
	})
	if err == nil {
		t.Fatal("expected an error for a refused page")
	}
	if strings.Contains(refused.URL, "tracker.example") {
		t.Errorf("URL = %q: the landing address was discarded when the page refused", refused.URL)
	}
}
