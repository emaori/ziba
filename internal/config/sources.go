package config

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"

	neturl "net/url"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/emaori/ziba/internal/domain"
)

// SourcesFile is the shape of sources.yaml, the hand-edited list of what Ziba
// reads. Adding a source must stay as easy as adding four lines here.
type SourcesFile struct {
	Sources []SourceEntry `yaml:"sources"`
}

// SourceEntry is one configured source.
type SourceEntry struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"`
	URL  string `yaml:"url"`

	// Enabled defaults to true: a source is written down in order to be read.
	// The pointer distinguishes "absent" from "explicitly false", which a plain
	// bool cannot do.
	Enabled *bool `yaml:"enabled"`

	// CollectFrom bounds how much history this source contributes on first
	// contact: a duration such as "7d" or "48h", an absolute date such as
	// "2026-01-01", or "all" to accept whatever the source offers. Absent means
	// the default grace.
	CollectFrom string `yaml:"collect_from"`

	// Roundup marks a feed that publishes issues of a link digest rather than
	// articles: each entry points at a page listing other people's writing.
	// Ziba then reads that page for its links instead of storing it. Applies to
	// RSS only.
	Roundup bool `yaml:"roundup"`

	// Categories declares what this source is about, instead of having it
	// inferred. Each must name a configured interest. Articles from a source
	// with categories are always shown, and their score rates how interesting
	// they are rather than how well they match.
	Categories []string `yaml:"categories"`

	// Newsletter applies to mailboxes only.
	Newsletter *NewsletterEntry `yaml:"newsletter"`
}

// DefaultNewsletterDays is how many days of mail a run reads when the source
// does not say. One day suits a collection interval of a few hours; widen it if
// the schedule is sparser, or to survive a longer outage.
const DefaultNewsletterDays = 1

// NewsletterEntry is the `newsletter:` block of a mailbox source.
type NewsletterEntry struct {
	Folder      string `yaml:"folder"`
	UsernameEnv string `yaml:"username_env"`
	PasswordEnv string `yaml:"password_env"`
	Days        int    `yaml:"days"`
	MaxMessages int    `yaml:"max_messages"`
}

// LoadSources reads and validates the sources file.
//
// Interests are needed to validate a source's declared categories: a name that
// matches no interest would silently never be shown, which is the opposite of
// what declaring it is for.
func LoadSources(path string, interests Interests) ([]domain.Source, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read sources file: %w", err)
	}

	var file SourcesFile
	// KnownFields makes a typo in a key an error instead of a silently ignored
	// line — the failure mode of hand-edited configuration.
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("parse sources file %s: %w", path, err)
	}

	if len(file.Sources) == 0 {
		return nil, fmt.Errorf("sources file %s defines no sources", path)
	}

	sources := make([]domain.Source, 0, len(file.Sources))
	seen := make(map[string]string, len(file.Sources))

	known := make(map[string]bool, len(interests.Topics))
	for _, topic := range interests.Topics {
		known[topic.Topic] = true
	}

	for i, entry := range file.Sources {
		src, err := entry.toDomain(known)
		if err != nil {
			// Both the position and the name, because one of the two is always
			// the one the user can find quickly.
			return nil, fmt.Errorf("sources[%d] (%q): %w", i, entry.Name, err)
		}

		key := string(src.Type) + " " + src.URL
		if other, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("sources[%d] (%q): same type and url as %q", i, entry.Name, other)
		}
		seen[key] = entry.Name

		sources = append(sources, src)
	}
	return sources, nil
}

// addressFor validates and canonicalizes a source's address according to what
// kind of thing it names.
func addressFor(sourceType domain.SourceType, raw string) (string, error) {
	if sourceType != domain.SourceTypeNewsletter {
		return domain.NormalizeURL(raw)
	}

	parsed, err := neturl.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("parse mailbox address %q: %w", raw, err)
	}
	switch parsed.Scheme {
	case "imaps", "imap":
	default:
		return "", fmt.Errorf("mailbox address %q must use imaps:// or imap://", raw)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("mailbox address %q has no host", raw)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("mailbox address %q must not contain credentials — "+
			"name environment variables with username_env and password_env instead", raw)
	}
	return parsed.String(), nil
}

func (e SourceEntry) toDomain(knownInterests map[string]bool) (domain.Source, error) {
	if e.Name == "" {
		return domain.Source{}, fmt.Errorf("name is required")
	}

	sourceType := domain.SourceType(e.Type)
	switch sourceType {
	case domain.SourceTypeRSS, domain.SourceTypeNewsletter, domain.SourceTypePDF:
	case domain.SourceTypeWebsite:
		// Worth saying why, because this used to work and someone will copy an
		// old configuration forward.
		return domain.Source{}, fmt.Errorf(
			"scraping was removed: it needed a bespoke selector per site and broke " +
				"whenever one was redesigned. Look for an RSS feed or a newsletter instead")
	default:
		return domain.Source{}, fmt.Errorf("unknown type %q", e.Type)
	}

	// Only web sources have web addresses. A mailbox is named by an IMAP
	// address, which the web normalizer would rightly reject.
	url, err := addressFor(sourceType, e.URL)
	if err != nil {
		return domain.Source{}, err
	}

	enabled := true
	if e.Enabled != nil {
		enabled = *e.Enabled
	}

	collectFrom, err := parseCollectFrom(e.CollectFrom)
	if err != nil {
		return domain.Source{}, fmt.Errorf("collect_from: %w", err)
	}

	for _, category := range e.Categories {
		if !knownInterests[category] {
			return domain.Source{}, fmt.Errorf(
				"category %q is not one of the configured interests — "+
					"an article filed under it would never be shown", category)
		}
	}

	if e.Roundup && sourceType != domain.SourceTypeRSS {
		return domain.Source{}, fmt.Errorf("roundup applies to type rss, not %q — "+
			"a newsletter is already read for its links", e.Type)
	}

	source := domain.Source{
		Name:        e.Name,
		Type:        sourceType,
		URL:         url,
		Enabled:     enabled,
		Roundup:     e.Roundup,
		Categories:  e.Categories,
		CollectFrom: collectFrom,
	}

	if e.Newsletter != nil {
		if sourceType != domain.SourceTypeNewsletter {
			return domain.Source{}, fmt.Errorf("a newsletter block only applies to type newsletter, not %q", e.Type)
		}
		if e.Newsletter.UsernameEnv == "" || e.Newsletter.PasswordEnv == "" {
			return domain.Source{}, fmt.Errorf("username_env and password_env are required")
		}
		// Credentials are named, never written down. Catching an empty variable
		// here means the run fails at startup with the variable's name, rather
		// than as an authentication error from the server.
		for _, name := range []string{e.Newsletter.UsernameEnv, e.Newsletter.PasswordEnv} {
			if os.Getenv(name) == "" {
				return domain.Source{}, fmt.Errorf("environment variable %s is empty", name)
			}
		}

		days := e.Newsletter.Days
		if days == 0 {
			days = DefaultNewsletterDays
		}
		if days < 0 {
			return domain.Source{}, fmt.Errorf("days cannot be negative")
		}
		folder := e.Newsletter.Folder
		if folder == "" {
			folder = "INBOX"
		}

		source.Newsletter = &domain.NewsletterOptions{
			Folder:       folder,
			UsernameEnv:  e.Newsletter.UsernameEnv,
			PasswordEnv:  e.Newsletter.PasswordEnv,
			LookBackDays: days,
			MaxMessages:  e.Newsletter.MaxMessages,
		}
	}

	if sourceType == domain.SourceTypeNewsletter && e.Newsletter == nil {
		return domain.Source{}, fmt.Errorf("a newsletter source needs a newsletter block")
	}

	return source, nil
}

// parseCollectFrom reads the `collect_from` setting.
//
// Accepted: empty (the default grace), "all", a date as YYYY-MM-DD, or a
// duration. Durations allow a "d" suffix for days, which the standard parser
// does not — "7d" is how a person writes a week, and requiring "168h" would be
// a small daily cruelty.
func parseCollectFrom(raw string) (domain.CollectFrom, error) {
	value := strings.TrimSpace(raw)
	switch {
	case value == "":
		return domain.CollectFrom{}, nil
	case strings.EqualFold(value, "all"):
		return domain.CollectFrom{All: true}, nil
	}

	if date, err := time.Parse(time.DateOnly, value); err == nil {
		if date.After(time.Now()) {
			return domain.CollectFrom{}, fmt.Errorf("%q is in the future, so nothing would ever be collected", value)
		}
		return domain.CollectFrom{Date: date}, nil
	}

	if days, found := strings.CutSuffix(value, "d"); found {
		n, err := strconv.Atoi(days)
		if err == nil && n > 0 {
			return domain.CollectFrom{Grace: time.Duration(n) * 24 * time.Hour}, nil
		}
	}

	grace, err := time.ParseDuration(value)
	if err != nil || grace <= 0 {
		return domain.CollectFrom{}, fmt.Errorf(
			"%q is not a duration such as \"7d\" or \"48h\", a date such as \"2026-01-01\", or \"all\"", value)
	}
	return domain.CollectFrom{Grace: grace}, nil
}
