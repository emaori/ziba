package config

import (
	"fmt"
	"strconv"
	"strings"

	neturl "net/url"
	"time"

	"github.com/emaori/ziba/internal/domain"
)

// DefaultNewsletterDays is how many days of mail a run reads when the source
// does not say. One day suits a collection interval of a few hours; widen it if
// the schedule is sparser, or to survive a longer outage.
const DefaultNewsletterDays = 1

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

// SourceAddress validates and canonicalizes a source address for web forms.
func SourceAddress(sourceType domain.SourceType, raw string) (string, error) {
	return addressFor(sourceType, raw)
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

// ParseCollectFrom validates the history setting used by web-managed sources.
func ParseCollectFrom(raw string) (domain.CollectFrom, error) {
	return parseCollectFrom(raw)
}
