package config

import (
	"bytes"
	"fmt"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultInterestsPath is where the hand-edited interests live unless
// ZIBA_INTERESTS_FILE says otherwise.
const DefaultInterestsPath = "config/interests.yaml"

// Interests describes what is worth reading. It is hand-edited and changes
// rarely, which is why it is a file and not a screen.
type Interests struct {
	// Threshold is the relevance score an article must reach to be summarized
	// and to appear in the daily digest. Below it the article is still stored
	// and browsable: the AI curates, it does not censor.
	Threshold int `yaml:"threshold"`

	Topics []Interest `yaml:"interests"`
}

// Interest is one topic, with as much context as the reader cares to give. The
// notes matter: they are what turns a keyword list into a description of taste.
type Interest struct {
	Topic     string   `yaml:"topic"`
	Priority  int      `yaml:"priority"`
	Subtopics []string `yaml:"subtopics"`
	Note      string   `yaml:"note"`
}

// LoadInterests reads and validates the interests file.
func LoadInterests(path string) (Interests, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Interests{}, fmt.Errorf("read interests file: %w", err)
	}

	var interests Interests
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&interests); err != nil {
		return Interests{}, fmt.Errorf("parse interests file %s: %w", path, err)
	}

	if interests.Threshold < 0 || interests.Threshold > 100 {
		return Interests{}, fmt.Errorf("interests file %s: threshold must be between 0 and 100, got %d",
			path, interests.Threshold)
	}
	if len(interests.Topics) == 0 {
		return Interests{}, fmt.Errorf("interests file %s defines no interests", path)
	}

	for i, topic := range interests.Topics {
		if strings.TrimSpace(topic.Topic) == "" {
			return Interests{}, fmt.Errorf("interests[%d]: topic is required", i)
		}
		if topic.Priority < 1 {
			return Interests{}, fmt.Errorf("interests[%d] (%q): priority must be 1 or greater",
				i, topic.Topic)
		}
	}
	return interests, nil
}

// Describe renders the interests as the text handed to the model. Keeping the
// rendering here, next to the structure, is what keeps the two from drifting
// apart when a field is added.
//
// Topics are listed by priority so the most important ones are read first.
func (in Interests) Describe() string {
	topics := slices.Clone(in.Topics)
	slices.SortStableFunc(topics, func(a, b Interest) int { return a.Priority - b.Priority })

	var b strings.Builder
	for _, t := range topics {
		fmt.Fprintf(&b, "- %s (priority %d)\n", t.Topic, t.Priority)
		if len(t.Subtopics) > 0 {
			fmt.Fprintf(&b, "  specifically: %s\n", strings.Join(t.Subtopics, ", "))
		}
		if t.Note != "" {
			fmt.Fprintf(&b, "  note: %s\n", t.Note)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
