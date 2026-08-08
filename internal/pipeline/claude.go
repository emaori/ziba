package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/emaori/ziba/internal/config"
	"github.com/emaori/ziba/internal/domain"
)

// Model defaults. Assessment is a mechanical judgement made on every collected
// article, so it runs on the fast model; summarization only ever sees articles
// above threshold, so it can afford the capable one. That split is what keeps a
// daily run cheap.
//
// These are aliases rather than dated snapshot ids: same model, and they do not
// need editing when a new snapshot ships.
const (
	DefaultFastModel    = "claude-haiku-4-5"
	DefaultCapableModel = "claude-sonnet-5"
)

// maxArticleRunes bounds how much of an article is sent. Beyond this a longer
// prompt buys nothing: the opening of a piece already says what it is about,
// and the cost grows with every rune.
const maxArticleRunes = 12000

// Claude implements Analyzer against the Claude API.
type Claude struct {
	client       anthropic.Client
	fastModel    string
	capableModel string
	interests    config.Interests
}

// ClaudeOptions configures the analyzer. Empty model names fall back to the
// defaults above.
type ClaudeOptions struct {
	APIKey       string
	FastModel    string
	CapableModel string
	Interests    config.Interests
}

// NewClaude builds the analyzer.
func NewClaude(opts ClaudeOptions) (*Claude, error) {
	if opts.APIKey == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY is not set")
	}

	return &Claude{
		client:       anthropic.NewClient(option.WithAPIKey(opts.APIKey)),
		fastModel:    orDefault(opts.FastModel, DefaultFastModel),
		capableModel: orDefault(opts.CapableModel, DefaultCapableModel),
		interests:    opts.Interests,
	}, nil
}

// assessmentSchema is the shape the model must answer in. Declaring it means
// the response is parseable by construction, instead of being prose that has to
// be coaxed into JSON.
//
// The field order matters: the model fills the structure in order, so naming
// the subject before rating it means the score is reached with the subject
// already established rather than in the same breath.
//
// Categories are restricted to the configured interests rather than left free.
// They are what the interface files articles under, so a model inventing
// "Artificial intelligence" one day and "AI and machine learning" the next would
// scatter one subject across several headings and leave the reader's own
// interests unrepresented. An article that fits none of them returns an empty
// list, which is a legitimate answer.
func assessmentSchema(interests config.Interests) map[string]any {
	names := make([]string, 0, len(interests.Topics))
	for _, topic := range interests.Topics {
		names = append(names, topic.Topic)
	}

	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"categories": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string", "enum": names},
				"minItems":    0,
				"maxItems":    3,
				"description": "Which of the reader's interests this article belongs to. Choose only those it genuinely is about; an empty list is correct for an article that fits none.",
			},
			"entities": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"maxItems":    8,
				"description": "People, organizations, products or places the article is actually about.",
			},
			"tone": map[string]any{
				"type":        "string",
				"enum":        []string{"news", "analysis", "opinion", "tutorial", "announcement", "interview", "review"},
				"description": "What kind of piece this is.",
			},
			"score": map[string]any{
				"type":        "integer",
				"minimum":     0,
				"maximum":     100,
				"description": "How relevant this article is to the reader's interests.",
			},
			"reason": map[string]any{
				"type":        "string",
				"description": "One sentence explaining the score, naming the interest it matches or misses.",
			},
		},
		"required":             []string{"categories", "entities", "tone", "score", "reason"},
		"additionalProperties": false,
	}
}

// Assess implements Assessor: one call that identifies the article and rates
// it. Sending the article text once instead of twice is where most of the
// running cost of Ziba was saved.
func (c *Claude) Assess(ctx context.Context, a domain.Article) (Assessment, error) {
	system := fmt.Sprintf(`You classify and rate articles for one specific reader.

First identify what the article is about, factually and without judging its
quality. Then rate how relevant it is to this reader.

The reader's interests, most important first:

%s

Rate from 0 to 100. Reward depth and usefulness to these interests; do not
reward an article merely for mentioning a keyword. An article outside every
interest scores low even when it is excellent in general.

Answer only with the requested structure.`, c.interests.Describe())

	var result struct {
		Categories []string `json:"categories"`
		Entities   []string `json:"entities"`
		Tone       string   `json:"tone"`
		Score      int      `json:"score"`
		Reason     string   `json:"reason"`
	}
	if err := c.ask(ctx, c.fastModel, 1024, system, articlePrompt(a), assessmentSchema(c.interests), &result); err != nil {
		return Assessment{}, err
	}

	// The schema constrains this, but the database has a check constraint and a
	// rejected insert is a worse failure than a clamped score.
	score := min(max(result.Score, 0), 100)

	return Assessment{
		Categories: result.Categories,
		Entities:   result.Entities,
		Tone:       result.Tone,
		Score:      domain.RelevanceScore(score),
		Reason:     result.Reason,
	}, nil
}

// Summarize implements Summarizer. This is the only stage that runs on the
// capable model, and only for articles above threshold.
func (c *Claude) Summarize(ctx context.Context, a domain.Article, _ Assessment) (string, error) {
	system := fmt.Sprintf(`You write short summaries for one specific reader.

The reader's interests, most important first:

%s

Write 3 to 4 sentences saying what the article reports and why it matters to
this reader. Be concrete: name the finding, the number, the decision. Do not
open with "This article" and do not recommend reading it — the reader decides
that. Answer with the summary alone.`, c.interests.Describe())

	message, err := c.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     c.capableModel,
		MaxTokens: 512,
		System:    []anthropic.TextBlockParam{{Text: system}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(articlePrompt(a))),
		},
	})
	if err != nil {
		return "", fmt.Errorf("summarize: %w", err)
	}

	summary := strings.TrimSpace(textOf(message))
	if summary == "" {
		return "", fmt.Errorf("summarize: model returned no text")
	}
	return summary, nil
}

// ask sends one request constrained to a JSON schema and decodes the answer
// into out.
func (c *Claude) ask(ctx context.Context, model string, maxTokens int64,
	system, prompt string, schema map[string]any, out any) error {

	message, err := c.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     model,
		MaxTokens: maxTokens,
		System:    []anthropic.TextBlockParam{{Text: system}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
		OutputConfig: anthropic.OutputConfigParam{
			Format: anthropic.JSONOutputFormatParam{Schema: schema},
		},
	})
	if err != nil {
		return fmt.Errorf("call %s: %w", model, err)
	}

	text := textOf(message)
	if text == "" {
		return fmt.Errorf("call %s: model returned no text", model)
	}
	if err := json.Unmarshal([]byte(text), out); err != nil {
		return fmt.Errorf("call %s: decode response: %w", model, err)
	}
	return nil
}

// textOf joins the text blocks of a response, ignoring any other block type.
func textOf(m *anthropic.Message) string {
	var b strings.Builder
	for _, block := range m.Content {
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}

// articlePrompt renders the article for the model, truncated to a sane length.
func articlePrompt(a domain.Article) string {
	text := a.FullText
	if runes := []rune(text); len(runes) > maxArticleRunes {
		text = string(runes[:maxArticleRunes]) + "\n[truncated]"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Title: %s\n", a.Title)
	if a.Author != "" {
		fmt.Fprintf(&b, "Author: %s\n", a.Author)
	}
	fmt.Fprintf(&b, "URL: %s\n\n%s", a.URL, text)
	return b.String()
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
