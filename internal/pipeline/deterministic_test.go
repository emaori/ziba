package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/emaori/ziba/internal/config"
	"github.com/emaori/ziba/internal/domain"
)

// Short interest names are the hard case. "AI" inside "email" or the Italian
// "mai" made a general-news source come out 82% artificial intelligence on the
// first real run.
func TestTermPatternRespectsWordBoundaries(t *testing.T) {
	tests := []struct {
		term string
		text string
		want bool
	}{
		{"AI", "advances in AI research", true},
		{"AI", "AI. Full stop.", true},
		{"AI", "AI-powered tooling", true},
		{"AI", "send me an email about it", false},
		{"AI", "non l'ho mai visto", false},
		{"AI", "assai interessante", false},
		{"AI", "the detail was fair", false},
		{"AI", "again and again", false},
		// "ai" is an ordinary Italian preposition; only the acronym counts.
		{"AI", "una guida ai musei di Roma", false},
		{"AI", "si sono accordati ai prezzi", false},
		{"AI", "AI research in Italy", true},

		{".NET", "building with asp.net core", true},
		{".NET", "the .net runtime", true},
		{".NET", "networking basics", false},
		{".NET", "on the internet", false},

		{"C#", "written in c#", true},
		{"C#", "c# 14 features", true},
		{"ML", "ml pipelines", false},
		{"ML", "ML pipelines", true},

		{"Go programming", "go programming is fun", true},
		{"Go programming", "ongo programming", false},
	}

	for _, tt := range tests {
		t.Run(tt.term+" in "+tt.text, func(t *testing.T) {
			pattern := termPattern(tt.term)
			if pattern == nil {
				t.Fatalf("termPattern(%q) returned nil", tt.term)
			}
			if got := pattern.MatchString(tt.text); got != tt.want {
				t.Errorf("%q in %q = %v, want %v (pattern %s)", tt.term, tt.text, got, tt.want, pattern)
			}
		})
	}
}

// The end-to-end version of the same bug: an Italian news article that mentions
// no technology at all must not be filed under AI.
func TestDeterministicDoesNotMisfileGeneralNews(t *testing.T) {
	interests := config.Interests{
		Threshold: 60,
		Topics: []config.Interest{
			{Topic: "AI", Priority: 1, Subtopics: []string{"LLMs", "machine learning"}},
			{Topic: ".NET", Priority: 2, Subtopics: []string{"C#"}},
		},
	}
	analyzer := NewDeterministic(interests)

	italianNews := domain.Article{
		URL:   "https://ilpost.it/2026/08/08/consiglio-comunale",
		Title: "Il consiglio comunale ha approvato il bilancio",
		FullText: "Non era mai successo che il dibattito durasse assai a lungo. " +
			"I consiglieri hanno discusso per ore, e alla fine hanno votato.",
	}

	got, err := analyzer.Assess(context.Background(), italianNews, nil)
	if err != nil {
		t.Fatalf("Assess returned error: %v", err)
	}

	for _, category := range got.Categories {
		if category != "Uncategorized" {
			t.Errorf("Italian council news was filed under %q — substring matching is back", category)
		}
	}
	if got.Score >= 60 {
		t.Errorf("Score = %d for unrelated news, want below the threshold", got.Score)
	}
}

// A source that states its subject is taken at its word: no keyword matching,
// and the categories are the declared ones whatever the text says.
func TestDeterministicHonoursDeclaredCategories(t *testing.T) {
	analyzer := NewDeterministic(config.Interests{
		Threshold: 60,
		Topics: []config.Interest{
			{Topic: "AI", Priority: 1},
			{Topic: ".NET", Priority: 2, Subtopics: []string{"C#"}},
		},
	})

	// Deliberately a piece that mentions none of the configured terms — the
	// case that sent a FastEndpoints article to Uncategorized.
	article := domain.Article{
		URL:      "https://example.com/validation",
		Title:    "Validation in FastEndpoints: pipelines and pitfalls",
		FullText: "You inherit Validator<TRequest>, add a couple of RuleFor lines, and get your 400 response.",
	}

	got, err := analyzer.Assess(context.Background(), article, []string{".NET"})
	if err != nil {
		t.Fatalf("Assess returned error: %v", err)
	}
	if len(got.Categories) != 1 || got.Categories[0] != ".NET" {
		t.Errorf("Categories = %v, want the declared [.NET]", got.Categories)
	}
	if got.Score == 0 {
		t.Error("Score = 0 for a declared source; it should still be ordered")
	}
	if !strings.Contains(got.Reason, "declared") {
		t.Errorf("Reason = %q, want it to say the source declared this", got.Reason)
	}

	// Without the declaration the same article falls through the keyword rules.
	fallback, _ := analyzer.Assess(context.Background(), article, nil)
	if len(fallback.Categories) != 1 || fallback.Categories[0] != "Uncategorized" {
		t.Errorf("Categories = %v without a declaration, want Uncategorized", fallback.Categories)
	}
}
