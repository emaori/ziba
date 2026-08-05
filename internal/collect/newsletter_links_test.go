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
