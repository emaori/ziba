package config

import (
	"fmt"
	"slices"
	"strings"
)

// Interests describes what is worth reading.
type Interests struct {
	// Threshold is the relevance score an article must reach to be summarized
	// and to appear in the latest digest. Below it the article is still stored
	// and browsable: the AI curates, it does not censor.
	Threshold int

	Topics []Interest
}

// Interest is one topic, with as much context as the reader cares to give. The
// notes matter: they are what turns a keyword list into a description of taste.
type Interest struct {
	Topic     string
	Priority  int
	Subtopics []string
	Note      string
}

// ValidateInterests applies the rules used by web-managed configuration.
func ValidateInterests(interests Interests) error {
	if interests.Threshold < 0 || interests.Threshold > 100 {
		return fmt.Errorf("threshold must be between 0 and 100, got %d", interests.Threshold)
	}
	if len(interests.Topics) == 0 {
		return fmt.Errorf("define at least one interest")
	}

	for i, topic := range interests.Topics {
		if strings.TrimSpace(topic.Topic) == "" {
			return fmt.Errorf("interests[%d]: topic is required", i)
		}
		if topic.Priority < 1 {
			return fmt.Errorf("interests[%d] (%q): priority must be 1 or greater",
				i, topic.Topic)
		}
	}
	return nil
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
