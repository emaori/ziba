package collect

import (
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/net/html"

	"github.com/emaori/ziba/internal/domain"
)

// minNewsletterLinkRunes filters out "Read more", "Click here" and the like.
// An editorial link in a newsletter carries a headline or a real blurb.
//
// Twenty, lowered from twenty-five, which was costing real articles. The issue
// titled "How big is a Git commit?" lost that very article — twenty-four
// characters, its own headline, named in the subject line — and "Set LogLevel
// of Blazor" went the same way at twenty-two.
//
// The number comes from measuring every rejected anchor across five real
// newsletters rather than from taste. Nothing genuine fell below twenty-one:
// the longest noise was "partner with us" and the sister-publication links at
// eighteen and nineteen. The gap is narrow, so this will admit the occasional
// short call to action — a fair trade against losing an issue's lead story,
// and a judgement a model would make properly.
const minNewsletterLinkRunes = 20

// maxNewsletterLinks caps one email, since a newsletter with two hundred links
// is an advertisement, not a reading list.
const maxNewsletterLinks = 40

// nonEditorialHosts never carry the article a newsletter is pointing at. Social
// buttons and list-management links appear in almost every newsletter sent.
//
// Mail providers are deliberately absent, and it took a real newsletter to see
// why. Mailchimp, SendGrid and their kind do not host a separate domain for
// unsubscribe links: they wrap *every* link in the same tracking domain, the
// articles included. Blocking the domain therefore discarded an entire issue —
// the ASP.NET Core newsletter yielded nothing at all, twelve articles dropped
// in one go. They are treated as the click trackers they are, followed to
// wherever they lead, and list management is recognised by the path below
// instead.
var nonEditorialHosts = []string{
	"twitter.com", "x.com", "facebook.com", "instagram.com", "linkedin.com",
	"threads.net", "bsky.app", "mastodon.social", "reddit.com", "pinterest.com",
	"whatsapp.com", "telegram.me", "t.me",

	// Not trackers: these host the email itself, as a web page.
	"mailchi.mp", "campaign-archive.com",

	"apple.com/app-store", "play.google.com",

	// Account and security mail is not a newsletter, but it arrives in the same
	// mailbox and its links are all "review this activity" and "change your
	// password". Collecting those is at best noise.
	"accounts.google.com", "myaccount.google.com", "account.live.com",
	"appleid.apple.com",
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
	"advertise", "sponsor", "donate", "forward-to-friend",

	// "profile", not "profile-center": Mailchimp's preferences page is plain
	// /profile, and it is reached on the same domain as the articles now that
	// the domain is no longer blocked outright.
	"profile",
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

// minEssayRunes is how much prose a message needs before it can be an essay
// rather than a list. Below it, a message with few links is simply a short
// notice — an account alert, a receipt — and not something to read.
const minEssayRunes = 1500

// isEssay reports whether a message is a piece of writing rather than a list of
// other people's articles.
//
// The two are told apart by the shape of the link text, which turned out to be
// the only signal that separates them cleanly. A roundup links headlines, and a
// headline is capitalised. An essay links phrases inside its own sentences —
// "discovered three incidents where models had gained access", "lots of details
// on Oracle's investments" — and those start lowercase because they start
// mid-sentence.
//
// Measured on real mail: a Martin Fowler post had seven of eleven links
// starting lowercase, while four roundups had none at all between them, the one
// exception being a bare address. Sender would have been the obvious signal and
// is not available — several publications arrive through the same forwarder or
// digest service. The ratio of text to links does not separate them either: the
// essay sits at 780 characters per link and one roundup at 570.
//
// A message with no links at all and real text is an essay too. That is the
// case the requirements first described: prose sent by email, which under the
// old rule produced no article because there was nothing to extract.
func isEssay(text string, links []extractedLink) bool {
	if len([]rune(text)) < minEssayRunes {
		return false
	}
	if len(links) == 0 {
		return true
	}

	lowercase := 0
	for _, link := range links {
		for _, r := range link.text {
			if unicode.IsLower(r) {
				lowercase++
			}
			// Only the first rune decides; punctuation and digits count as
			// neither, so a bulleted or numbered list is not an essay.
			break
		}
	}
	return lowercase*2 > len(links)
}
