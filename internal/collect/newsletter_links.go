package collect

import (
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/net/html"

	"github.com/emaori/ziba/internal/domain"
)

// minNewsletterLinkRunes filters out "Read more", "Click here" and the like.
// Twenty is the measured boundary in the fixture corpus between editorial
// headlines and short calls to action.
const minNewsletterLinkRunes = 20

// maxNewsletterLinks caps one email, since a newsletter with two hundred links
// is an advertisement, not a reading list.
const maxNewsletterLinks = 40

// nonEditorialHosts never carry the target article. Mail-provider tracking
// hosts are deliberately excluded because they wrap editorial links too;
// list-management links are recognized by path instead.
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

// videoHosts require a future video collector rather than article extraction.
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

// editorialLinks extracts likely articles and discards navigation, account,
// social, sponsorship and list-management links.
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

// isEssay distinguishes prose from a roundup. Essay links usually begin with a
// lowercase phrase inside a sentence; roundup links are usually headlines. A
// sufficiently long message without links is also prose.
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
