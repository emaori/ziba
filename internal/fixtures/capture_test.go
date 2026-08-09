//go:build capture

// Refreshing the corpus. Not part of any suite: it reaches the real mailbox and
// the real network, and it writes into the repository.
//
//	make capture
//
// It scrubs everything on the way in and refuses to write a file that still
// looks identifying, so a mistake in the rules stops the capture rather than
// committing somebody's subscription token.
package fixtures

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// mailWanted maps a fixture name to a distinctive part of a subject. Each was
// chosen because it pins a bug this project actually had.
var mailWanted = map[string]string{
	"cd619":       "CD#619",                 // 16 anchors, 7 kept
	"alphasignal": "Agent Plugins Standard", // 36 anchors, 6 duplicates, a letter-spaced title
	"aspnetcore":  "ASP.NET Core News",      // the provider-wrapper bug: 0 links became 12
	"pd688":       "PD#688",                 // a 24-character headline on the threshold
	"fowler":      "Martin Fowler",          // an essay, not a list of links
}

// webWanted are the addresses to fetch. Each is stored under the name Web
// gives it, which is also the name the transport looks for.
var webWanted = []string{
	"https://dotnetketchup.com/rss",
	"https://dotnetketchup.com/?year=2026&week=32",
	"https://hnrss.org/frontpage",
	"https://spectrum.ieee.org/feeds/feed.rss",
}

func TestCaptureFixtures(t *testing.T) {
	root := "testdata"
	scrubber := NewScrubber()

	captureMail(t, root, scrubber)
	captureWeb(t, root, scrubber)
}

// write scrubs, checks, and only then puts a file in the corpus.
func write(t *testing.T, path string, body []byte, scrubber *Scrubber) {
	t.Helper()

	clean := scrubber.Text(string(body))
	if leaks := Leaks(clean); len(leaks) != 0 {
		t.Errorf("REFUSING to write %s: it still contains %v", path, leaks)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("make %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(clean), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	t.Logf("wrote %s (%d bytes)", path, len(clean))
}

// Nothing is rehomed. A document is stored under the name its own address maps
// to, so its links resolve inside the corpus untouched — and a newsletter's
// links are what the extraction rules judge, so rewriting their hosts would
// change which ones are dropped.

func captureMail(t *testing.T, root string, scrubber *Scrubber) {
	user, password := os.Getenv("ZIBA_IMAP_USER"), os.Getenv("ZIBA_IMAP_PASSWORD")
	if user == "" || password == "" {
		t.Skip("no mailbox credentials; skipping the mail half of the capture")
	}

	client, err := imapclient.DialTLS("imap.gmail.com:993", nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()
	if err := client.Login(user, password).Wait(); err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, err := client.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		t.Fatalf("select: %v", err)
	}

	for name, subject := range mailWanted {
		found, err := client.Search(&imap.SearchCriteria{
			Header: []imap.SearchCriteriaHeaderField{{Key: "Subject", Value: subject}},
		}, nil).Wait()
		if err != nil {
			t.Errorf("%s: search: %v", name, err)
			continue
		}
		nums := found.AllSeqNums()
		if len(nums) == 0 {
			t.Errorf("%s: no message matching %q is left in the mailbox", name, subject)
			continue
		}

		var set imap.SeqSet
		set.AddNum(nums[0])
		messages, err := client.Fetch(set, &imap.FetchOptions{
			BodySection: []*imap.FetchItemBodySection{{}},
		}).Collect()
		if err != nil {
			t.Errorf("%s: fetch: %v", name, err)
			continue
		}
		for _, section := range messages[0].BodySection {
			write(t, filepath.Join(root, "mail", name+".eml"), section.Bytes, scrubber)
			break
		}
	}
}

func captureWeb(t *testing.T, root string, scrubber *Scrubber) {
	client := &http.Client{Timeout: 30 * time.Second}

	for _, address := range webWanted {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, address, nil)
		if err != nil {
			t.Errorf("%s: %v", address, err)
			continue
		}
		req.Header.Set("User-Agent", "Ziba/1.0 (personal content aggregator)")

		resp, err := client.Do(req)
		if err != nil {
			t.Errorf("%s: %v", address, err)
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if readErr != nil {
			t.Errorf("%s: %v", address, readErr)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s said %s", address, resp.Status)
			continue
		}

		// Stored under the same mapping the transport reads, so a document's own
		// links resolve inside the corpus without rewriting the document.
		write(t, filepath.Join(root, Web(address)), body, scrubber)
		time.Sleep(time.Second) // the same politeness the collector shows
	}

	// A recorded redirect and a recorded refusal: both are behaviours the
	// pipeline has to get right and neither can be captured as a page.
	write(t, filepath.Join(root, Web("https://tracker.example/c")),
		[]byte(redirectPrefix+"https://publisher.example/article/real-one"), scrubber)
	write(t, filepath.Join(root, Web("https://tracker.example/paywalled")),
		[]byte(redirectPrefix+"https://paywalled.example/article"), scrubber)
	write(t, filepath.Join(root, Web("https://publisher.example/article/real-one")),
		[]byte(`<!doctype html><html><head><title>The article behind the tracker</title></head>`+
			`<body><article><p>Enough words here to count as a paragraph of prose.</p>`+
			`<p>And a second one, so the extraction has something to do.</p></article></body></html>`), scrubber)

	fmt.Println("capture finished")
}
