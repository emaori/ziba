package collect

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/emaori/ziba/internal/domain"
)

// message builds the buffer the collector receives from the server.
func message(subject, sender, html string) *imapclient.FetchMessageBuffer {
	raw := "From: " + sender + " <writer@example.com>\r\n" +
		"Subject: " + subject + "\r\n" +
		"Message-ID: <" + subject + "@example.com>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n\r\n" + html

	return &imapclient.FetchMessageBuffer{
		Envelope: &imap.Envelope{
			Subject:   subject,
			Date:      time.Date(2026, 8, 8, 18, 0, 0, 0, time.UTC),
			MessageID: "<" + subject + "@example.com>",
			From:      []imap.Address{{Name: sender, Mailbox: "writer", Host: "example.com"}},
		},
		BodySection: []imapclient.FetchBodySectionBuffer{{Bytes: []byte(raw)}},
	}
}

var mailbox = domain.Source{ID: 3, Name: "Newsletters", Type: domain.SourceTypeNewsletter}

// A roundup email yields its links, plus the email itself as provenance which
// is never shown.
func TestItemsFromRoundupMessage(t *testing.T) {
	html := `<html><body>
	  <h2><a href="https://spectrum.ieee.org/quantum-milestone">Quantum error correction hits a milestone</a></h2>
	  <h2><a href="https://devblogs.microsoft.com/dotnet/eleven">What's new for C# in .NET 11</a></h2>
	  <p><a href="https://example.com/unsubscribe">Unsubscribe from this list</a></p>
	</body></html>`

	items, err := new(Newsletter).itemsFromMessage(mailbox, message("Weekly roundup", "Editor", html))
	if err != nil {
		t.Fatalf("itemsFromMessage: %v", err)
	}
	if len(items) != 3 {
		for _, i := range items {
			t.Logf("%s %s — %q", i.Kind, i.URL, i.Title)
		}
		t.Fatalf("got %d items, want 3 (provenance plus two links)", len(items))
	}

	if items[0].Kind != domain.ItemKindProvenance {
		t.Errorf("first item is %q, want provenance", items[0].Kind)
	}
	if !strings.HasPrefix(items[0].URL, "imap://") {
		t.Errorf("provenance address = %q, want a synthetic imap one", items[0].URL)
	}
	for _, link := range items[1:] {
		if link.Kind != domain.ItemKindArticle {
			t.Errorf("%s is %q, want an article", link.URL, link.Kind)
		}
	}
}

// An essay is the article. Its links are citations inside its own sentences and
// must not be collected: doing so turned one piece of writing into nine entries
// titled with fragments of it, while the writing itself was never shown.
func TestItemsFromEssayMessage(t *testing.T) {
	prose := strings.Repeat(
		"There has been a fair bit of publicity about this, and it is worth saying why. ", 30)
	html := `<html><body><p>` + prose + `
	  and Anthropic <a href="https://anthropic.com/news/incidents">discovered three incidents where models had gained access</a>,
	  which is <a href="https://en.wikipedia.org/wiki/Irrational_exuberance">irrational exuberance</a> of a kind,
	  with <a href="https://nytimes.com/oracle">lots of details on Oracle's investments in AI</a>.
	</p></body></html>`

	items, err := new(Newsletter).itemsFromMessage(mailbox, message("Fragments: August 4", "Martin Fowler", html))
	if err != nil {
		t.Fatalf("itemsFromMessage: %v", err)
	}

	if len(items) != 1 {
		for _, i := range items {
			t.Logf("%s %s — %q", i.Kind, i.URL, i.Title)
		}
		t.Fatalf("got %d items, want 1: the essay itself and none of its citations", len(items))
	}

	essay := items[0]
	if essay.Kind != domain.ItemKindArticle {
		t.Errorf("kind = %q, want an article", essay.Kind)
	}
	if essay.Title != "Fragments: August 4" {
		t.Errorf("title = %q, want the subject", essay.Title)
	}
	if !strings.Contains(essay.Text, "worth saying why") {
		t.Error("the essay's own text was not kept")
	}
	// No external original: the reader renders it inline.
	if (domain.Article{URL: essay.URL}).HasOriginal() {
		t.Errorf("URL = %q, which the reader would offer to open", essay.URL)
	}
}
