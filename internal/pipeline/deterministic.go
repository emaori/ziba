package pipeline

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/emaori/ziba/internal/config"
	"github.com/emaori/ziba/internal/domain"
)

// Deterministic is an Analyzer that uses no model and no network. The same
// article always produces the same result.
//
// It exists for two reasons: tests can exercise the whole flow without cost or
// flakiness, and the pipeline can be run offline while developing everything
// downstream of it. It is not a good curator — keyword matching is exactly what
// the AI is there to replace — and it never pretends to be.
type Deterministic struct {
	interests config.Interests
}

// NewDeterministic builds the offline analyzer.
func NewDeterministic(interests config.Interests) *Deterministic {
	return &Deterministic{interests: interests}
}

// Assess labels the article with the interest topics whose words appear in it,
// and scores it by the priority of the best match.
func (d *Deterministic) Assess(_ context.Context, a domain.Article) (Assessment, error) {
	haystack := strings.ToLower(a.Title + " " + a.FullText)

	var categories []string
	best := 0
	for _, topic := range d.interests.Topics {
		if !mentions(haystack, topic) {
			continue
		}
		categories = append(categories, topic.Topic)

		// Priority 1 is worth most; each step down costs 15 points.
		best = max(best, 90-(topic.Priority-1)*15)
	}
	if len(categories) == 0 {
		categories = []string{"Uncategorized"}
	}

	// Deterministic jitter keyed on the URL: enough to break ties, never enough
	// to move an article across the threshold on its own.
	h := fnv.New32a()
	h.Write([]byte(a.URL))
	best += int(h.Sum32() % 5)

	return Assessment{
		Categories: categories,
		Entities:   []string{},
		Tone:       "neutral",
		Score:      domain.RelevanceScore(min(best, 100)),
		Reason:     fmt.Sprintf("offline analyzer: matched %s", strings.Join(categories, ", ")),
	}, nil
}

// Summarize returns the opening of the article. It is a placeholder, and says
// so, rather than inventing prose no model wrote.
func (d *Deterministic) Summarize(_ context.Context, a domain.Article, _ Assessment) (string, error) {
	const maxRunes = 400

	text := strings.Join(strings.Fields(a.FullText), " ")
	runes := []rune(text)
	if len(runes) > maxRunes {
		text = strings.TrimSpace(string(runes[:maxRunes])) + "…"
	}
	if text == "" {
		return "", fmt.Errorf("article has no text to summarize")
	}
	return "[offline] " + text, nil
}

// mentions reports whether the article talks about a topic, by the crudest
// possible measure.
func mentions(haystack string, topic config.Interest) bool {
	if strings.Contains(haystack, strings.ToLower(topic.Topic)) {
		return true
	}
	for _, sub := range topic.Subtopics {
		if strings.Contains(haystack, strings.ToLower(sub)) {
			return true
		}
	}
	return false
}
