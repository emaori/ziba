package collect

import (
	"strings"
	"testing"
)

// A realistic newsletter: two articles buried in social buttons, list
// management, a sponsor slot and a duplicate.
const newsletterMarkup = `<html><body>
<p><a href="https://example.com/newsletter/view-in-browser">View this email in your browser</a></p>
<h2><a href="https://spectrum.ieee.org/quantum-milestone?utm_source=newsletter&utm_campaign=weekly">Quantum error correction hits a milestone researchers called unlikely</a></h2>
<h2><a href="https://go.dev/blog/generics-next-steps">What comes next for generics in the Go language</a></h2>
<p><a href="https://twitter.com/example">Follow us on Twitter for more updates like this</a></p>
<p><a href="https://www.facebook.com/example">Find our page on Facebook and say hello to us</a></p>
<p><a href="https://example.com/sponsor/acme-advertise-here">A message from our sponsor, Acme Corporation</a></p>
<p><a href="https://example.com/x">Read more</a></p>
<p><a href="https://list-manage.com/unsubscribe?id=123">Unsubscribe from this newsletter at any time</a></p>
<p><a href="https://spectrum.ieee.org/quantum-milestone?utm_source=footer">Quantum error correction hits a milestone researchers called unlikely</a></p>
<p><a href="mailto:editor@example.com">Write to the editor with your thoughts</a></p>
</body></html>`

func TestEditorialLinks(t *testing.T) {
	links := editorialLinks(newsletterMarkup)

	if len(links) != 2 {
		var got []string
		for _, l := range links {
			got = append(got, l.url)
		}
		t.Fatalf("got %d links, want 2:\n  %s", len(links), strings.Join(got, "\n  "))
	}

	// Tracking parameters are stripped on the way in, which is also why the
	// duplicate at the foot of the email collapses onto the first one.
	if want := "https://spectrum.ieee.org/quantum-milestone"; links[0].url != want {
		t.Errorf("first link = %q, want %q", links[0].url, want)
	}
	if !strings.Contains(links[0].text, "Quantum error correction") {
		t.Errorf("link text = %q, want the headline", links[0].text)
	}
	if want := "https://go.dev/blog/generics-next-steps"; links[1].url != want {
		t.Errorf("second link = %q, want %q", links[1].url, want)
	}
}

func TestEditorialLinksRejects(t *testing.T) {
	tests := []struct {
		name string
		html string
	}{
		{"social button", `<a href="https://twitter.com/x">Follow us on Twitter for updates</a>`},
		{"social subdomain", `<a href="https://mobile.twitter.com/x">Follow us on Twitter for updates</a>`},
		{"unsubscribe", `<a href="https://e.example.com/unsubscribe?u=1">Unsubscribe from these emails</a>`},
		{"preferences", `<a href="https://e.example.com/preferences">Update your email preferences here</a>`},
		{"view in browser", `<a href="https://e.example.com/webversion">View this email in your browser</a>`},
		{"sponsor", `<a href="https://e.example.com/sponsor/acme">A word from our lovely sponsor</a>`},
		{"privacy", `<a href="https://e.example.com/privacy">Read our privacy policy in full</a>`},
		{"short text", `<a href="https://example.com/article">Read more</a>`},
		{"mailto", `<a href="mailto:e@example.com">Write to the editor with your thoughts</a>`},
		{"anchor", `<a href="#top">Jump back to the top of this newsletter</a>`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if links := editorialLinks("<html><body>" + tt.html + "</body></html>"); len(links) != 0 {
				t.Errorf("got %d links, want none: %q", len(links), links[0].url)
			}
		})
	}
}

func TestEditorialLinksCap(t *testing.T) {
	var b strings.Builder
	b.WriteString("<html><body>")
	for i := range 200 {
		b.WriteString(`<a href="https://example.com/article-`)
		b.WriteString(strings.Repeat("x", 1))
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString(string(rune('a' + i/26)))
		b.WriteString(`">A headline long enough to look like a real article</a>`)
	}
	b.WriteString("</body></html>")

	if links := editorialLinks(b.String()); len(links) > maxNewsletterLinks {
		t.Errorf("got %d links, want at most %d", len(links), maxNewsletterLinks)
	}
}

// A Mailchimp newsletter puts every link on the same tracking domain: the
// articles, the sponsor, the preferences page and the unsubscribe footer. The
// domain was once blocked outright, which silently discarded whole issues, so
// this pins the shape that broke it.
func TestEditorialLinksThroughAMailProvider(t *testing.T) {
	markup := `<html><body>
<a href="https://us7.campaign-archive.com/?e=847f&u=66fd&id=a8c4">View this email in your browser</a>
<a href="https://us.list-manage.com/yTLi4WaeY2n?e=847f">Getting Started with NLog in ASP.NET Core</a>
<a href="https://us.list-manage.com/PdyMO1p5SAX?e=847f">ASP.NET Core Integration Testing: WebApplicationFactory</a>
<a href="https://news.us7.list-manage.com/profile?u=66fd&id=25e5&e=847f">update your preferences</a>
<a href="https://news.us7.list-manage.com/unsubscribe?u=66fd&id=25e5&e=847f">unsubscribe from this list</a>
</body></html>`

	links := editorialLinks(markup)

	if len(links) != 2 {
		for _, l := range links {
			t.Logf("kept %s — %q", l.url, l.text)
		}
		t.Fatalf("kept %d links, want the 2 articles", len(links))
	}
	for _, l := range links {
		if !strings.Contains(l.text, "ASP.NET Core") {
			t.Errorf("kept %q, which is not one of the articles", l.text)
		}
	}
}

// The provider's own pages — the email as a web page, the preferences screen —
// must still be dropped even though the domain is no longer blocked.
func TestEditorialLinksStillDropsListManagement(t *testing.T) {
	for _, href := range []string{
		"https://us7.campaign-archive.com/?u=66fd",
		"https://news.us7.list-manage.com/profile?u=66fd",
		"https://news.us7.list-manage.com/unsubscribe?u=66fd",
		"https://example.mailchi.mp/newsletter/issue-12",
	} {
		markup := `<a href="` + href + `">A perfectly long and editorial-looking headline</a>`
		if links := editorialLinks(markup); len(links) != 0 {
			t.Errorf("%s was kept, want it dropped", href)
		}
	}
}

// The length rule sits between real headlines and navigation, and the gap is
// narrow. These are measured anchor texts from real newsletters: the kept ones
// were being lost, the dropped ones are what the rule exists for.
func TestEditorialLinksLengthBoundary(t *testing.T) {
	tests := []struct {
		text string
		keep bool
	}{
		{"How big is a Git commit?", true}, // 24 — the issue's own lead story
		{"Set LogLevel of Blazor", true},   // 22
		{"irrational exuberance", true},    // 21
		{"partner with us →", false},       // 19 — the longest noise measured
		{"Programming Digest", false},      // 18 — a sister publication
		{"Read more", false},               // 9
	}

	for _, tt := range tests {
		markup := `<a href="https://example.com/an-article">` + tt.text + `</a>`
		got := len(editorialLinks(markup)) == 1
		if got != tt.keep {
			t.Errorf("%q (%d runes): kept = %v, want %v",
				tt.text, len([]rune(tt.text)), got, tt.keep)
		}
	}
}

// Telling an essay from a list of links. The numbers here are the measured
// shapes of the five real newsletters this was built against.
func TestIsEssay(t *testing.T) {
	long := strings.Repeat("Prose about software, at length. ", 60) // ~2000 runes

	links := func(texts ...string) []extractedLink {
		out := make([]extractedLink, 0, len(texts))
		for i, s := range texts {
			out = append(out, extractedLink{url: "https://example.com/" + string(rune('a'+i)), text: s})
		}
		return out
	}

	tests := []struct {
		name  string
		text  string
		links []extractedLink
		want  bool
	}{
		{
			// Martin Fowler's Bliki: links are phrases inside his sentences.
			name: "citations inside prose",
			text: long,
			links: links(
				"discovered three incidents where models had gained access",
				"the Normalization of Deviance in AI",
				"lots of details on Oracle's investments in AI",
				"crash in South Korean memory stocks",
				"irrational exuberance",
				"Simon Wilison concluded",
				"Mr Musk told our editor-in-chief"),
			want: true,
		},
		{
			// A roundup: every link is a headline, and headlines are capitalised.
			name: "headlines",
			text: long,
			links: links(
				"An architectural view of ML.NET",
				"Your Agent Needs Its Own Computer",
				"BulkSynchronize in EF Core",
				"How to prevent duplicate API requests in .NET"),
			want: false,
		},
		{
			// AlphaSignal numbers and bullets its list; neither is lowercase.
			name:  "decorated headlines",
			text:  long,
			links: links("▸ OpenAI launches Agent Plugins", "1. Mistral releases a 3B model"),
			want:  false,
		},
		{
			// The case the requirements first described: prose with no links.
			name: "prose with no links at all",
			text: long, want: true,
		},
		{
			// An account notice is short and linkless, and is not reading material.
			name: "a short notice is not an essay",
			text: "Someone signed in to your account from a new device.",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isEssay(tt.text, tt.links); got != tt.want {
				t.Errorf("isEssay = %v, want %v", got, tt.want)
			}
		})
	}
}
