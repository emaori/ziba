package domain

import (
	"fmt"
	"net/url"
	"strings"
)

// trackingParams are query parameters that identify the reader or the campaign
// rather than the content. Two links differing only by these point at the same
// article, so they are dropped before comparing.
var trackingParams = []string{
	"utm_", // utm_source, utm_medium, utm_campaign, utm_term, utm_content
	"fbclid",
	"gclid",
	"gbraid",
	"wbraid",
	"msclkid",
	"mc_cid",
	"mc_eid",
	"igshid",
	"ref_src",
	"ref_url",
	"_hsenc",
	"_hsmi",
}

// NormalizeURL returns the canonical form of a link, used as the identity of an
// Article. The same article reached from a feed, a newsletter and a scraped page
// must produce the same string here, or it will be stored three times.
//
// It lowercases the scheme and host, drops "www.", removes the default port and
// the fragment, strips tracking parameters, sorts what remains, and removes a
// trailing slash. It deliberately does not change the scheme: an http link is
// not assumed to be the same document as its https version.
func NormalizeURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("empty url")
	}

	u, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("parse url %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("url %q: unsupported scheme %q", raw, u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("url %q: missing host", raw)
	}

	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)

	// Hostname() and Port() split the host, dropping the port when it is the
	// default one for the scheme.
	if port := u.Port(); (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		u.Host = u.Hostname()
	}
	u.Host = strings.TrimPrefix(u.Host, "www.")

	// Fragments address a position inside a document, never a different one.
	u.Fragment = ""
	u.RawFragment = ""

	// Encode() sorts by key, so the same parameters always produce the same
	// string regardless of the order they arrived in.
	query := u.Query()
	for key := range query {
		if isTracking(key) {
			query.Del(key)
		}
	}
	u.RawQuery = query.Encode()

	if u.Path != "/" {
		u.Path = strings.TrimSuffix(u.Path, "/")
	}
	// A bare host normalizes without a trailing slash: "https://example.com".
	if u.Path == "/" && u.RawQuery == "" {
		u.Path = ""
	}

	return u.String(), nil
}

func isTracking(key string) bool {
	key = strings.ToLower(key)
	for _, p := range trackingParams {
		if key == p || (strings.HasSuffix(p, "_") && strings.HasPrefix(key, p)) {
			return true
		}
	}
	return false
}
