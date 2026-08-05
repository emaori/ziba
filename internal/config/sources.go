package config

import (
	"bytes"
	"fmt"
	"os"
	"regexp"

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
}

// WebsiteEntry is the `website:` block of a scraped source.
type WebsiteEntry struct {
	LinkPattern string `yaml:"link_pattern"`
	Render      bool   `yaml:"render"`
	MaxLinks    int    `yaml:"max_links"`
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

	url, err := domain.NormalizeURL(e.URL)
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
