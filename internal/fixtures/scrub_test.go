package fixtures

import (
	"strings"
	"testing"
)

// The scrubber runs once, on capture, and whatever it misses is committed. So
// its own test is the one that matters most in this package.
func TestScrubRemovesEverythingIdentifying(t *testing.T) {
	raw := `From: Emanuele <emanuele@origgi.com>
To: ziba.ns.emaori@gmail.com
Subject: CD#619

<a href="https://csharpdigest.net/links/22896/17f5691a-7c5e-47b7-9c38-85666e15eb10/email">An article</a>
<a href="https://app.alphasignal.ai/c?cid=48b16c953435c2a7&uid=7wgWEUsFQJukcvqY">Another</a>
<a href="https://news.us7.list-manage.com/unsubscribe?u=66fdd2122e968863d381a26e1&e=847feaba8a">Unsubscribe</a>
Reply to writer@somepublisher.com if you like.
Encoded in the body: emanuele=40origgi.com`

	got := NewScrubber().Text(raw)

	if leaks := Leaks(got); len(leaks) != 0 {
		t.Errorf("scrubbed text still leaks %v", leaks)
	}
	for _, secret := range []string{
		"emanuele@origgi.com", "origgi", "ziba.ns.emaori",
		"17f5691a-7c5e-47b7-9c38-85666e15eb10", // the subscriber uuid
		"48b16c953435c2a7", "7wgWEUsFQJukcvqY", // alphasignal identifiers
		"847feaba8a",                // the recipient parameter
		"66fdd2122e968863d381a26e1", // the list identifier
		"writer@somepublisher.com",  // somebody else's address
		"emanuele=40origgi.com",     // the quoted-printable form
	} {
		if strings.Contains(got, secret) {
			t.Errorf("%q survived scrubbing", secret)
		}
	}

	// The shape has to survive, or the fixtures stop testing anything.
	for _, kept := range []string{
		"csharpdigest.net/links/22896/", "app.alphasignal.ai/c?cid=",
		"list-manage.com/unsubscribe", "Subject: CD#619",
	} {
		if !strings.Contains(got, kept) {
			t.Errorf("scrubbing destroyed %q, which the tests rely on", kept)
		}
	}
}

// Two distinct trackers must stay distinct: "the same article is linked twice
// under different trackers" is a case the corpus exists to pin.
func TestScrubKeepsDistinctTokensDistinct(t *testing.T) {
	s := NewScrubber()
	got := s.Text(`
		<a href="https://x.test/c?lid=cD3PNZoqUuUpd0qs">one</a>
		<a href="https://x.test/c?lid=Zlv1kQ2mAb9xYtRe">two</a>
		<a href="https://x.test/c?lid=cD3PNZoqUuUpd0qs">one again</a>`)

	first := strings.Count(got, "lid=t0001")
	if first != 2 {
		t.Errorf("the repeated tracker was replaced %d times consistently, want 2", first)
	}
	if !strings.Contains(got, "lid=t0002") {
		t.Errorf("the second tracker collapsed into the first:\n%s", got)
	}
}

// The corpus is committed, so the check that matters is on the files as they
// stand, not only on the rules that produced them. This is the test that would
// have caught the first capture, which let a display name, a bare domain and a
// line-wrapped uuid through.
func TestTheCorpusIsClean(t *testing.T) {
	for _, dir := range []string{"mail", "web"} {
		names, err := List(dir)
		if err != nil {
			t.Fatalf("list %s: %v", dir, err)
		}
		if len(names) == 0 {
			t.Errorf("%s holds no fixtures", dir)
		}

		for _, name := range names {
			body, err := Read(name)
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			if leaks := Leaks(string(body)); len(leaks) != 0 {
				t.Errorf("%s leaks %v", name, leaks)
			}
		}
	}
}

// Quoted-printable is how mail actually arrives, and it hides things from a
// naive rule: a value may be split across a soft line break, and the separator
// in a query string may itself be encoded.
func TestScrubSeesThroughQuotedPrintable(t *testing.T) {
	raw := "Subscriber 17f5691a-7c5e-47b7-9c38-8=\r\n5666e15eb10 and e=3D847feaba8a\r\n" +
		"header.from=origgi.com\r\nFrom: Emanuele <somebody@elsewhere.example>\r\n" +
		"split name: eman=\r\nuele@origgi.com"

	got := NewScrubber().Text(raw)

	for _, secret := range []string{"17f5691a", "5666e15eb10", "847feaba8a", "origgi", "Emanuele", "eman=\r\nuele"} {
		if strings.Contains(got, secret) {
			t.Errorf("%q survived:\n%s", secret, got)
		}
	}
	if leaks := Leaks(got); len(leaks) != 0 {
		t.Errorf("still leaking %v", leaks)
	}
}

// A subscriber identifier is not always a query parameter. One hid in a path
// segment, in base62 rather than hex, and slipped past two rules that were each
// looking somewhere else.
func TestScrubCatchesIdentifiersInPaths(t *testing.T) {
	raw := `<a href="https://app.alphasignal.ai/unsubscribe/u/7wgWEUsFQJukcvqY?cid=3Dabc">unsubscribe</a>`

	got := NewScrubber().Text(raw)
	if strings.Contains(got, "7wgWEUsFQJukcvqY") {
		t.Errorf("the path identifier survived: %s", got)
	}
	// The word the link filter matches on has to survive; the segments after it
	// are the token and may be replaced freely.
	if !strings.Contains(got, "/unsubscribe/") {
		t.Errorf("the path shape was destroyed: %s", got)
	}
	if !strings.Contains(got, "app.alphasignal.ai") {
		t.Errorf("the host was destroyed: %s", got)
	}
	if leaks := Leaks(got); len(leaks) != 0 {
		t.Errorf("still leaking %v", leaks)
	}
}
