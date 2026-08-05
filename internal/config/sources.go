package config

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"

	neturl "net/url"

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

	// Website applies to scraped sites only.
	Website *WebsiteEntry `yaml:"website"`

	// Newsletter applies to mailboxes only.
	Newsletter *NewsletterEntry `yaml:"newsletter"`
}

// WebsiteEntry is the `website:` block of a scraped source.
type WebsiteEntry struct {
	LinkPattern string `yaml:"link_pattern"`
	Render      bool   `yaml:"render"`
	MaxLinks    int    `yaml:"max_links"`
}

// NewsletterEntry is the `newsletter:` block of a mailbox source.
type NewsletterEntry struct {
	Folder      string `yaml:"folder"`
	UsernameEnv string `yaml:"username_env"`
	PasswordEnv string `yaml:"password_env"`
	UnreadOnly  *bool  `yaml:"unread_only"`
	MaxMessages int    `yaml:"max_messages"`
}

// LoadSources reads and validates the sources file.
func LoadSources(path string) ([]domain.Source, error) {
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

	for i, entry := range file.Sources {
		src, err := entry.toDomain()
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

func (e SourceEntry) toDomain() (domain.Source, error) {
	if e.Name == "" {
		return domain.Source{}, fmt.Errorf("name is required")
	}

	sourceType := domain.SourceType(e.Type)
	switch sourceType {
	case domain.SourceTypeRSS, domain.SourceTypeWebsite, domain.SourceTypeNewsletter, domain.SourceTypePDF:
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

	source := domain.Source{
		Name:    e.Name,
		Type:    sourceType,
		URL:     url,
		Enabled: enabled,
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

		unreadOnly := true
		if e.Newsletter.UnreadOnly != nil {
			unreadOnly = *e.Newsletter.UnreadOnly
		}
		folder := e.Newsletter.Folder
		if folder == "" {
			folder = "INBOX"
		}

		source.Newsletter = &domain.NewsletterOptions{
			Folder:      folder,
			UsernameEnv: e.Newsletter.UsernameEnv,
			PasswordEnv: e.Newsletter.PasswordEnv,
			UnreadOnly:  unreadOnly,
			MaxMessages: e.Newsletter.MaxMessages,
		}
	}

	if sourceType == domain.SourceTypeNewsletter && e.Newsletter == nil {
		return domain.Source{}, fmt.Errorf("a newsletter source needs a newsletter block")
	}

	if e.Website != nil {
		if sourceType != domain.SourceTypeWebsite {
			return domain.Source{}, fmt.Errorf("a website block only applies to type website, not %q", e.Type)
		}
		// Compiling here means a bad expression is caught when the file is
		// loaded, not hours later when the scheduler runs.
		if e.Website.LinkPattern != "" {
			if _, err := regexp.Compile(e.Website.LinkPattern); err != nil {
				return domain.Source{}, fmt.Errorf("link_pattern: %w", err)
			}
		}
		if e.Website.MaxLinks < 0 {
			return domain.Source{}, fmt.Errorf("max_links cannot be negative")
		}
		source.Website = &domain.WebsiteOptions{
			LinkPattern: e.Website.LinkPattern,
			Render:      e.Website.Render,
			MaxLinks:    e.Website.MaxLinks,
		}
	}

	return source, nil
}
