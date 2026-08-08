//go:build trace

// A throwaway diagnostic: fetch one message from the real mailbox and report,
// link by link, what the extraction rules decided and why. Not part of the
// suite — it needs a live mailbox and a subject to look for.
//
//	go test -tags=trace ./internal/collect/ -run TraceNewsletter -v -args -subject "CD#619"
package collect

import (
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/mail"
	"golang.org/x/net/html"

	"github.com/emaori/ziba/internal/domain"
)

var subject = flag.String("subject", "", "substring of the subject to trace")

func TestTraceNewsletter(t *testing.T) {
	if *subject == "" {
		t.Skip("pass -args -subject <text>")
	}

	client, err := imapclient.DialTLS("imap.gmail.com:993", nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()
	if err := client.Login(os.Getenv("ZIBA_IMAP_USER"), os.Getenv("ZIBA_IMAP_PASSWORD")).Wait(); err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, err := client.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		t.Fatalf("select: %v", err)
	}

	criteria := &imap.SearchCriteria{
		Header: []imap.SearchCriteriaHeaderField{{Key: "Subject", Value: *subject}},
	}
	found, err := client.Search(criteria, nil).Wait()
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	nums := found.AllSeqNums()
	if len(nums) == 0 {
		t.Fatalf("no message matching %q", *subject)
	}

	set := imap.SeqSetNum(nums[0])
	messages, err := client.Fetch(set, &imap.FetchOptions{
		Envelope:    true,
		BodySection: []*imap.FetchItemBodySection{{}},
	}).Collect()
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}

	msg := messages[0]
	var body []byte
	for _, section := range msg.BodySection {
		body = section.Bytes
		break
	}

	fmt.Printf("\nSUBJECT: %s\nSENT:    %s\nBYTES:   %d\n\n",
		msg.Envelope.Subject, msg.Envelope.Date, len(body))

	// What the collector itself would keep.
	text, kept, err := readMessage(body)
	if err != nil {
		t.Fatalf("readMessage: %v", err)
	}
	keptSet := map[string]bool{}
	for _, l := range kept {
		keptSet[l.url] = true
	}

	// Now the same walk, but reporting a verdict for every anchor.
	markup := htmlPartOf(t, body)
	doc, err := html.Parse(strings.NewReader(markup))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	seen := map[string]bool{}
	counts := map[string]int{}
	total := 0

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			total++
			verdict, detail := classify(n, seen)
			counts[verdict]++
			fmt.Printf("%-22s %-96s %s\n", verdict, truncate(detail, 96),
				truncate(strings.Join(strings.Fields(textOf(n)), " "), 46))
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	fmt.Printf("\nanchors: %d\n", total)
	for _, k := range []string{"KEPT", "dropped: too short", "dropped: blocked host",
		"dropped: boilerplate", "dropped: duplicate", "dropped: not http"} {
		if counts[k] > 0 {
			fmt.Printf("  %-24s %d\n", k, counts[k])
		}
	}
	fmt.Printf("\ncollector kept %d links; provenance text is %d chars\n", len(kept), len(text))

	if counts["KEPT"] != len(kept) {
		t.Errorf("this trace kept %d but the collector kept %d — the trace is lying",
			counts["KEPT"], len(kept))
	}
}

// classify mirrors editorialLink, but names the reason instead of discarding.
func classify(n *html.Node, seen map[string]bool) (verdict, detail string) {
	href := strings.TrimSpace(attr(n, "href"))
	if href == "" || strings.HasPrefix(href, "#") {
		return "dropped: not http", href
	}
	parsed, err := url.Parse(href)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "dropped: not http", href
	}

	host := strings.ToLower(strings.TrimPrefix(parsed.Hostname(), "www."))
	for _, blocked := range skippedHosts {
		if host == blocked || strings.HasSuffix(host, "."+blocked) ||
			strings.HasPrefix(host+parsed.Path, blocked) {
			return "dropped: blocked host", parsed.String()
		}
	}
	haystack := strings.ToLower(parsed.Path + "?" + parsed.RawQuery)
	for _, word := range nonEditorialPaths {
		if strings.Contains(haystack, word) {
			return "dropped: boilerplate", word + " in " + host + parsed.Path
		}
	}

	text := strings.Join(strings.Fields(textOf(n)), " ")
	if len([]rune(text)) < minNewsletterLinkRunes {
		return "dropped: too short", fmt.Sprintf("%d runes, need %d", len([]rune(text)), minNewsletterLinkRunes)
	}

	normalized, err := domain.NormalizeURL(parsed.String())
	if err != nil {
		return "dropped: not http", err.Error()
	}
	if seen[normalized] {
		return "dropped: duplicate", normalized
	}
	seen[normalized] = true
	return "KEPT", normalized
}

// htmlPartOf decodes the message the way readMessage does. Reading the raw
// bytes instead is wrong: the body is quoted-printable, so every href arrives
// as 3D"https://... and every anchor looks like it has no scheme.
func htmlPartOf(t *testing.T, body []byte) string {
	t.Helper()

	reader, err := mail.CreateReader(strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("read message: %v", err)
	}
	defer reader.Close()

	var markup strings.Builder
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		inline, ok := part.Header.(*mail.InlineHeader)
		if !ok {
			continue
		}
		content, err := io.ReadAll(part.Body)
		if err != nil {
			continue
		}
		if contentType, _, _ := inline.ContentType(); strings.Contains(contentType, "html") {
			markup.Write(content)
		}
	}
	return markup.String()
}

func truncate(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
