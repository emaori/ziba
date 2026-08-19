package pipeline

import (
	"context"
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"
	"unicode"

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

	// patterns holds one compiled matcher per topic, covering the topic name
	// and its subtopics. Compiled once here rather than per article.
	patterns [][]*regexp.Regexp
}

// NewDeterministic builds the offline analyzer.
func NewDeterministic(interests config.Interests) *Deterministic {
	patterns := make([][]*regexp.Regexp, len(interests.Topics))
	for i, topic := range interests.Topics {
		terms := append([]string{topic.Topic}, topic.Subtopics...)
		for _, term := range terms {
			if p := termPattern(term); p != nil {
				patterns[i] = append(patterns[i], p)
			}
		}
	}
	return &Deterministic{interests: interests, patterns: patterns}
}

// Assess labels the article with the interest topics whose words appear in it,
// and scores it by the priority of the best match.
func (d *Deterministic) Assess(_ context.Context, a domain.Article, declared []string) (Assessment, error) {
	// Not lowercased: the patterns fold case themselves, and the short-acronym
	// ones deliberately do not. Folding here would make "AI" unmatchable.
	haystack := a.Title + " " + a.FullText

	// A source that states its subject is taken at its word. Keyword matching is
	// exactly what such a source is meant to spare us: it filed a piece about
	// FastEndpoints as uncategorised for never writing ".NET".
	if len(declared) > 0 {
		h := fnv.New32a()
		h.Write([]byte(a.URL))
		return Assessment{
			Categories:     declared,
			Entities:       []string{},
			Tone:           "neutral",
			ContentQuality: deterministicContentQuality(a.FullText),
			ContentReason:  "offline analyzer: quality based only on whether text is present",
			// No opinion to offer on how interesting a piece is: that needs a
			// reader, or a model. A flat middling score, jittered only to break
			// ties, is more honest than a number pretending to be a judgement.
			Score:  domain.RelevanceScore(50 + int(h.Sum32()%5)),
			Reason: "offline analyzer: declared by the source as " + strings.Join(declared, ", "),
		}, nil
	}

	var categories []string
	best := 0
	for i, topic := range d.interests.Topics {
		if !mentions(haystack, d.patterns[i]) {
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
		Categories:     categories,
		Entities:       []string{},
		Tone:           "neutral",
		ContentQuality: deterministicContentQuality(a.FullText),
		ContentReason:  "offline analyzer: quality based only on whether text is present",
		Score:          domain.RelevanceScore(min(best, 100)),
		Reason:         fmt.Sprintf("offline analyzer: matched %s", strings.Join(categories, ", ")),
	}, nil
}

func deterministicContentQuality(text string) domain.ContentQuality {
	if strings.TrimSpace(text) == "" {
		return domain.ContentUnavailable
	}
	return domain.ContentComplete
}

// Summarize returns the opening of the article. It is a placeholder, and says
// so, rather than inventing prose no model wrote.
func (d *Deterministic) Summarize(_ context.Context, a domain.Article, _ Assessment) (string, Usage, error) {
	const maxRunes = 400

	text := strings.Join(strings.Fields(a.FullText), " ")
	runes := []rune(text)
	if len(runes) > maxRunes {
		text = strings.TrimSpace(string(runes[:maxRunes])) + "…"
	}
	if text == "" {
		return "", Usage{}, fmt.Errorf("article has no text to summarize")
	}
	// No tokens, truthfully: an offline run costs nothing, and reporting zero
	// is what makes the statistics page distinguish it from an unanalyzed one.
	return "[offline] " + text, Usage{}, nil
}

// termPattern builds a matcher for one interest term that will not fire inside
// a longer word.
//
// Plain substring matching is unusable for short terms: "AI" appears inside
// "email", "again", "detail" and — in Italian — "mai" and "assai", so a general
// news source came out 82% classified as artificial intelligence. The boundary
// is required only on the sides where the term itself begins or ends with a
// letter or digit, so ".NET" still matches inside "ASP.NET" and "C#" still
// matches at the end of a sentence.
func termPattern(term string) *regexp.Regexp {
	term = strings.TrimSpace(term)
	if term == "" {
		return nil
	}

	// Short acronyms are matched case-sensitively, because in lower case they
	// collide with ordinary words in other languages. "AI" is the example that
	// forced this: "ai" is one of the commonest Italian prepositions, so an
	// Italian newspaper came out with ski reports and cartoonists filed under
	// artificial intelligence. Word boundaries cannot help — it genuinely is a
	// word, just not that one.
	//
	// Longer terms stay case-insensitive: ".net" and "asp.net" are written both
	// ways and are not words in any language.
	fold := `(?i)`
	if isShortAcronym(term) {
		fold = ``
	}

	pattern := fold + regexp.QuoteMeta(term)
	if isAlphanumeric([]rune(term)[0]) {
		pattern = fold + `(^|[^\p{L}\p{N}])` + regexp.QuoteMeta(term)
	}
	if last := []rune(term)[len([]rune(term))-1]; isAlphanumeric(last) {
		pattern += `([^\p{L}\p{N}]|$)`
	}

	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	return compiled
}

func isAlphanumeric(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// mentions reports whether the article talks about a topic. Still a crude
// measure — it is keyword matching, which is what the model is there to replace
// — but at least one that respects word boundaries.
func mentions(haystack string, patterns []*regexp.Regexp) bool {
	for _, p := range patterns {
		if p != nil && p.MatchString(haystack) {
			return true
		}
	}
	return false
}

// isShortAcronym reports whether a term is a brief all-capitals abbreviation —
// "AI", "ML", "UI". These are the terms whose lower-case form is likely to be a
// word in some language, so they are matched exactly as written.
func isShortAcronym(term string) bool {
	runes := []rune(term)
	if len(runes) == 0 || len(runes) > 3 {
		return false
	}
	for _, r := range runes {
		if !unicode.IsUpper(r) {
			return false
		}
	}
	return true
}
