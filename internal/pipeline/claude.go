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

// ClaudeOptions configures the analyzer.
//
// Both model names are required, as they are for every provider. There was once
// a default pair here and it was removed on purpose: a model name written into
// the code is a claim about what exists, made at the moment the line was typed
// and never revisited. Model families are renamed, superseded and retired, and
// a default that has gone stale fails at the first real article with an error
// about an unknown model — long after the run looked configured. Naming them in
// the environment puts the claim next to the key it is used with, where someone
// will see it.
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
	if opts.FastModel == "" || opts.CapableModel == "" {
		return nil, fmt.Errorf(
			"ZIBA_FAST_MODEL and ZIBA_CAPABLE_MODEL must both name a Claude model: " +
				"there is no default, because a stale one fails at the first call")
	}

	return &Claude{
		client:       anthropic.NewClient(option.WithAPIKey(opts.APIKey)),
		fastModel:    opts.FastModel,
		capableModel: opts.CapableModel,
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
// assessmentSchema builds the structure the model must answer with.
//
// When the source has declared its categories there is nothing to choose, so
// the field is left out of the schema entirely rather than asked for and
// discarded — a question not asked cannot be answered wrongly, and the model
// spends its attention on the score instead.
func assessmentSchema(interests config.Interests, declared []string) map[string]any {
	names := make([]string, 0, len(interests.Topics))
	for _, topic := range interests.Topics {
		names = append(names, topic.Topic)
	}

	scoreDescription := "How relevant this article is to the reader's interests."
	reasonDescription := "One sentence explaining the score, naming the interest it matches or misses."
	if len(declared) > 0 {
		scoreDescription = "How interesting and worth reading this article is."
		reasonDescription = "One sentence explaining the score, naming what makes the piece worth reading or not."
	}

	schema := map[string]any{
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
				"description": scoreDescription,
			},
			"reason": map[string]any{
				"type":        "string",
				"description": reasonDescription,
			},
		},
		"required":             []string{"categories", "entities", "tone", "score", "reason"},
		"additionalProperties": false,
	}

	if len(declared) > 0 {
		properties := schema["properties"].(map[string]any)
		delete(properties, "categories")
		schema["required"] = []string{"entities", "tone", "score", "reason"}
	}
	return schema
}

// Assess implements Assessor: one call that identifies the article and rates
// it. Sending the article text once instead of twice is where most of the
// running cost of Ziba was saved.
func (c *Claude) Assess(ctx context.Context, a domain.Article, declared []string) (Assessment, error) {
	system := assessSystemPrompt(c.interests, declared)

	var result struct {
		Categories []string `json:"categories"`
		Entities   []string `json:"entities"`
		Tone       string   `json:"tone"`
		Score      int      `json:"score"`
		Reason     string   `json:"reason"`
	}
	if err := c.ask(ctx, c.fastModel, 1024, system, articlePrompt(a),
		assessmentSchema(c.interests, declared), &result); err != nil {
		return Assessment{}, err
	}

	// The source said what this is about, so that is what it is about.
	if len(declared) > 0 {
		result.Categories = declared
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
	system := summarySystemPrompt(c.interests)

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
