package collect

import (
	"net/url"
	"strings"

	"golang.org/x/net/html"

	"github.com/emaori/ziba/internal/domain"
)

// minNewsletterLinkRunes filters out "Read more", "Click here" and the like.
// An editorial link in a newsletter carries a headline or a real blurb.
const minNewsletterLinkRunes = 25

// maxNewsletterLinks caps one email, since a newsletter with two hundred links
// is an advertisement, not a reading list.
const maxNewsletterLinks = 40

// nonEditorialHosts never carry the article a newsletter is pointing at. Social
// buttons and list-management links appear in almost every newsletter sent.
var nonEditorialHosts = []string{
	"twitter.com", "x.com", "facebook.com", "instagram.com", "linkedin.com",
	"threads.net", "bsky.app", "mastodon.social", "reddit.com", "pinterest.com",
	"whatsapp.com", "telegram.me", "t.me",
	"list-manage.com", "mailchi.mp", "campaign-archive.com",
	"sendgrid.net", "mailgun.org", "constantcontact.com",
	"apple.com/app-store", "play.google.com",
}

// videoHosts carry real editorial content, but none of it is text. Following
// one yields a page of player markup and a description, which is not an article
// however it is scored.
//
// This is a temporary exclusion, not a judgement: video is a planned source with
// its own collector, and when that exists these links should be collected as
// video rather than dropped.
var videoHosts = []string{
	"youtube.com", "youtu.be", "vimeo.com", "twitch.tv", "dailymotion.com",
}

// skippedHosts is the two lists above as one, built once rather than joined on
// every link tested.
var skippedHosts = append(append([]string{}, nonEditorialHosts...), videoHosts...)

// nonEditorialPaths mark list management and boilerplate rather than content.
var nonEditorialPaths = []string{
	"unsubscribe", "optout", "opt-out", "opt_out", "preferences", "manage-subscription",
	"manage_subscription", "subscription", "webversion", "view-in-browser",
	"viewinbrowser", "email-preferences", "privacy", "terms", "imprint",
	"advertise", "sponsor", "donate", "forward-to-friend", "profile-center",
}

// extractedLink is one candidate article found in a newsletter.
type extractedLink struct {
	url  string
	text string
}

// editorialLinks pulls the article links out of a newsletter's markup.
//
// A newsletter is mostly not article links: it is social buttons, an
// unsubscribe footer, a "view in browser" header, sponsor slots and tracking
// pixels. This keeps what reads like an article and drops the rest.
//
// The filtering is deliberately rule-based rather than model-driven. The
// functional documentation calls for the AI to do this, and it eventually
// should — a model would judge a sponsored post that looks editorial far better
// than a host list can. But rules are free, deterministic and testable, which
// makes them the right first implementation and a sound fallback for when the
// model is unavailable.
func editorialLinks(markup string) []extractedLink {
	doc, err := html.Parse(strings.NewReader(markup))
	if err != nil {
		return nil
	}

	seen := make(map[string]bool)
	var links []extractedLink

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if len(links) >= maxNewsletterLinks {
			return
		}
		if n.Type == html.ElementNode && n.Data == "a" {
			if link, ok := editorialLink(n, seen); ok {
				links = append(links, link)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	return links
}

func editorialLink(n *html.Node, seen map[string]bool) (extractedLink, bool) {
	href := strings.TrimSpace(attr(n, "href"))
	if href == "" || strings.HasPrefix(href, "#") {
		return extractedLink{}, false
	}

	parsed, err := url.Parse(href)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return extractedLink{}, false
	}
	if isNonEditorial(parsed) {
		return extractedLink{}, false
	}

	text := strings.Join(strings.Fields(textOf(n)), " ")
	if len([]rune(text)) < minNewsletterLinkRunes {
		return extractedLink{}, false
	}

	// Normalizing here is what strips the campaign parameters newsletters are
	// full of, and it is why the same article linked from two newsletters
	// collapses to one entry downstream.
	normalized, err := domain.NormalizeURL(parsed.String())
	if err != nil || seen[normalized] {
		return extractedLink{}, false
	}
	seen[normalized] = true

	return extractedLink{url: normalized, text: text}, true
}

func isNonEditorial(u *url.URL) bool {
	host := strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
	for _, blocked := range skippedHosts {
		if host == blocked || strings.HasSuffix(host, "."+blocked) ||
			strings.HasPrefix(host+u.Path, blocked) {
			return true
		}
	}

	haystack := strings.ToLower(u.Path + "?" + u.RawQuery)
	for _, word := range nonEditorialPaths {
		if strings.Contains(haystack, word) {
			return true
		}
	}
	return false
}
