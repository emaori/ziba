package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/emaori/ziba/internal/config"
	"github.com/emaori/ziba/internal/domain"
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

// ClaudeOptions configures the analyzer. Both model names are required; there
// are no defaults that can become stale.
type ClaudeOptions struct {
	APIKey       string
	FastModel    string
	CapableModel string
	Interests    config.Interests

	// HTTPClient replaces the default one. The debug journal uses it to record
	// every exchange; nil means the SDK's own.
	HTTPClient *http.Client
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
		client:       anthropic.NewClient(clientOptions(opts)...),
		fastModel:    opts.FastModel,
		capableModel: opts.CapableModel,
		interests:    opts.Interests,
	}, nil
}

// assessmentSchema constrains categories to configured interests. When a source
// declares its categories, the field is omitted and those values are assigned
// after decoding.
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
				"type":     "array",
				"items":    map[string]any{"type": "string", "enum": names},
				"minItems": 0,
				"maxItems": maxCategories,
				// The limit is repeated in words because maxItems does not
				// survive OpenAI's strict mode. A description does.
				"description": fmt.Sprintf("Which of the reader's interests this article belongs to, at most %d. "+
					"Choose only those it genuinely is about; an empty list is correct for an article that fits none.",
					maxCategories),
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
			"content_quality": map[string]any{
				"type":        "string",
				"enum":        []string{"complete", "limited", "mismatched", "unavailable"},
				"description": "Whether the retrieved body is trustworthy and sufficient for summarization.",
			},
			"content_reason": map[string]any{
				"type":        "string",
				"description": "One internal sentence explaining the content-quality judgement.",
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
		"required":             []string{"categories", "entities", "tone", "content_quality", "content_reason", "score", "reason"},
		"additionalProperties": false,
	}

	if len(declared) > 0 {
		properties := schema["properties"].(map[string]any)
		delete(properties, "categories")
		schema["required"] = []string{"entities", "tone", "content_quality", "content_reason", "score", "reason"}
	}
	return schema
}

// Assess implements Assessor: one call that identifies the article and rates
// it. Sending the article text once instead of twice is where most of the
// running cost of Ziba was saved.
func (c *Claude) Assess(ctx context.Context, a domain.Article, declared []string) (Assessment, error) {
	system := assessSystemPrompt(c.interests, declared)

	var result struct {
		Categories     []string              `json:"categories"`
		Entities       []string              `json:"entities"`
		Tone           string                `json:"tone"`
		ContentQuality domain.ContentQuality `json:"content_quality"`
		ContentReason  string                `json:"content_reason"`
		Score          int                   `json:"score"`
		Reason         string                `json:"reason"`
	}
	used, err := c.ask(ctx, c.fastModel, 1024, system, articlePrompt(a),
		assessmentSchema(c.interests, declared), &result)
	if err != nil {
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
		Categories:     result.Categories,
		Entities:       result.Entities,
		Tone:           result.Tone,
		ContentQuality: result.ContentQuality,
		ContentReason:  result.ContentReason,
		Score:          domain.RelevanceScore(score),
		Reason:         result.Reason,
		Usage:          used,
	}, nil
}

// Summarize implements Summarizer. This is the only stage that runs on the
// capable model, and only for articles above threshold.
func (c *Claude) Summarize(ctx context.Context, a domain.Article, as Assessment) (string, Usage, error) {
	limited := as.ContentQuality == domain.ContentLimited || as.ContentQuality == domain.ContentMismatched
	system := summarySystemPrompt(c.interests, limited)

	message, err := c.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     c.capableModel,
		MaxTokens: 512,
		System:    []anthropic.TextBlockParam{{Text: system}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(summaryArticlePrompt(a, as.ContentQuality))),
		},
	})
	if err != nil {
		return "", Usage{}, fmt.Errorf("summarize: %w", err)
	}

	// Reported even when the reply is unusable: the call was charged for.
	used := usageOfMessage(message)
	summary := strings.TrimSpace(textOf(message))
	if summary == "" {
		return "", used, fmt.Errorf("summarize: model returned no text")
	}
	return summary, used, nil
}

// usageOfMessage reads what the provider says the call cost.
func usageOfMessage(m *anthropic.Message) Usage {
	return Usage{Input: int(m.Usage.InputTokens), Output: int(m.Usage.OutputTokens)}
}

// ask sends one request constrained to a JSON schema and decodes the answer
// into out.
func (c *Claude) ask(ctx context.Context, model string, maxTokens int64,
	system, prompt string, schema map[string]any, out any) (Usage, error) {

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
		return Usage{}, fmt.Errorf("call %s: %w", model, err)
	}
	used := usageOfMessage(message)

	text := textOf(message)
	if text == "" {
		return used, fmt.Errorf("call %s: model returned no text", model)
	}
	if err := json.Unmarshal([]byte(text), out); err != nil {
		return used, fmt.Errorf("call %s: decode response: %w", model, err)
	}
	return used, nil
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

// summaryArticlePrompt excludes a body that assessment identified as belonging
// to some other page. The title and ordinary metadata remain useful for a
// cautious overview; sending known-bad prose would invite the same failure a
// second time.
func summaryArticlePrompt(a domain.Article, quality domain.ContentQuality) string {
	if quality != domain.ContentMismatched && quality != domain.ContentUnavailable {
		return articlePrompt(a)
	}
	copy := a
	copy.FullText = ""
	return articlePrompt(copy)
}

// clientOptions builds the SDK options, adding the journal's client when there
// is one.
func clientOptions(opts ClaudeOptions) []option.RequestOption {
	options := []option.RequestOption{option.WithAPIKey(opts.APIKey)}
	if opts.HTTPClient != nil {
		options = append(options, option.WithHTTPClient(opts.HTTPClient))
	}
	return options
}
