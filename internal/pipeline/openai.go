package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"

	"github.com/emaori/ziba/internal/config"
	"github.com/emaori/ziba/internal/domain"
)

// OpenAI implements Analyzer against the OpenAI API.
//
// It is a second provider rather than a replacement. The pipeline talks to an
// Analyzer, so which company answers is a configuration choice — and the point
// of having two is that the running cost of this project is entirely a function
// of who is asked, so being able to compare on real articles is worth more than
// an estimate.
//
// The prompts and the response schema are deliberately the same as the Claude
// provider's. If they differed, a comparison between the two would be measuring
// the prompts rather than the models.
type OpenAI struct {
	client       openai.Client
	fastModel    string
	capableModel string
	interests    config.Interests
}

// OpenAIOptions configures the analyzer.
//
// Both model names are required, as they are for every provider: see
// ClaudeOptions for why there are no defaults anywhere. An unknown name is then
// the API's error to report rather than this repository's to guess at.
type OpenAIOptions struct {
	APIKey       string
	FastModel    string
	CapableModel string
	Interests    config.Interests
}

// reasoningModel matches the o-series — o1, o3, o4-mini and their kin.
//
// They matter here for one practical reason: they reject the temperature
// parameter outright, so sending it fails the call rather than being ignored.
// A run configured with o4-mini would otherwise die on its first article with
// an error about an unsupported parameter.
var reasoningModel = regexp.MustCompile(`^o\d`)

// temperature returns the sampling temperature to send, and whether to send one
// at all.
//
// Zero, for everything that accepts it. Scoring is a judgement that should not
// change between two runs over the same article, and the small variation a
// higher temperature buys is worth nothing when the number's only job is to
// order a page.
func temperature(model string) (float64, bool) {
	if reasoningModel.MatchString(model) {
		return 0, false
	}
	return 0, true
}

// unsupportedByStrict are schema keywords OpenAI's strict mode refuses.
//
// The shared schema uses all four to say what a good answer looks like — at
// most three categories, a score between 0 and 100. Sent as they are, the call
// is rejected outright. They are dropped for this provider rather than removed
// from the schema, because Anthropic honours them and they are worth keeping
// there.
//
// What survives is what matters most: additionalProperties is false, every
// field is required, and the categories are an enum of the reader's own
// interests, so the model still cannot invent one. The score's range is left to
// the clamp in Assess, which is why that clamp exists.
var unsupportedByStrict = []string{"minItems", "maxItems", "minimum", "maximum"}

// forStrictMode copies a schema without the keywords strict mode refuses.
func forStrictMode(schema map[string]any) map[string]any {
	out := make(map[string]any, len(schema))
	for key, value := range schema {
		if slices.Contains(unsupportedByStrict, key) {
			continue
		}
		switch typed := value.(type) {
		case map[string]any:
			out[key] = forStrictMode(typed)
		default:
			out[key] = value
		}
	}
	return out
}

// NewOpenAI builds the analyzer.
func NewOpenAI(opts OpenAIOptions) (*OpenAI, error) {
	if opts.APIKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY is not set")
	}
	if opts.FastModel == "" || opts.CapableModel == "" {
		return nil, fmt.Errorf(
			"ZIBA_FAST_MODEL and ZIBA_CAPABLE_MODEL must both name an OpenAI model: " +
				"there is no default, because a stale one fails at the first call")
	}

	return &OpenAI{
		client:       openai.NewClient(option.WithAPIKey(opts.APIKey)),
		fastModel:    opts.FastModel,
		capableModel: opts.CapableModel,
		interests:    opts.Interests,
	}, nil
}

// Assess implements Assessor: one call that identifies the article and rates
// it. The instructions are the Claude provider's, word for word, so that the
// two can be compared.
func (o *OpenAI) Assess(ctx context.Context, a domain.Article, declared []string) (Assessment, error) {
	system := assessSystemPrompt(o.interests, declared)

	var result struct {
		Categories []string `json:"categories"`
		Entities   []string `json:"entities"`
		Tone       string   `json:"tone"`
		Score      int      `json:"score"`
		Reason     string   `json:"reason"`
	}
	if err := o.ask(ctx, o.fastModel, system, articlePrompt(a),
		"assessment", forStrictMode(assessmentSchema(o.interests, declared)), &result); err != nil {
		return Assessment{}, err
	}

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

// Summarize implements Summarizer, on the more capable model.
func (o *OpenAI) Summarize(ctx context.Context, a domain.Article, _ Assessment) (string, error) {
	if strings.TrimSpace(a.FullText) == "" {
		return "", fmt.Errorf("article has no text to summarize")
	}

	params := openai.ChatCompletionNewParams{
		Model: o.capableModel,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(summarySystemPrompt(o.interests)),
			openai.UserMessage(articlePrompt(a)),
		},
	}
	if value, send := temperature(o.capableModel); send {
		params.Temperature = openai.Float(value)
	}

	completion, err := o.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return "", fmt.Errorf("call %s: %w", o.capableModel, err)
	}
	if len(completion.Choices) == 0 {
		return "", fmt.Errorf("call %s: no reply", o.capableModel)
	}

	summary := strings.TrimSpace(completion.Choices[0].Message.Content)
	if summary == "" {
		return "", fmt.Errorf("call %s: empty summary", o.capableModel)
	}
	return summary, nil
}

// ask makes one structured-output call and decodes the reply.
//
// The schema is sent with strict validation, which is what makes the reply
// safe to unmarshal without checking every field: the categories can only be
// the reader's own interests, and the score can only be an integer in range.
func (o *OpenAI) ask(ctx context.Context, model, system, prompt, schemaName string,
	schema map[string]any, out any) error {

	params := openai.ChatCompletionNewParams{
		Model: model,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(system),
			openai.UserMessage(prompt),
		},
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{
				JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{
					Name:   schemaName,
					Schema: schema,
					Strict: openai.Bool(true),
				},
			},
		},
	}
	if value, send := temperature(model); send {
		params.Temperature = openai.Float(value)
	}

	completion, err := o.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return fmt.Errorf("call %s: %w", model, err)
	}
	if len(completion.Choices) == 0 {
		return fmt.Errorf("call %s: no reply", model)
	}

	text := completion.Choices[0].Message.Content
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("call %s: model returned no text", model)
	}
	if err := json.Unmarshal([]byte(text), out); err != nil {
		return fmt.Errorf("call %s: decode response: %w", model, err)
	}
	return nil
}
