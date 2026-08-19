package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"

	"github.com/emaori/ziba/internal/config"
	"github.com/emaori/ziba/internal/domain"
)

// OpenAI implements Analyzer against the OpenAI API, using the same prompts and
// response schema as the Claude provider.
type OpenAI struct {
	client        openai.Client
	fastModel     string
	capableModel  string
	fastEffort    config.ReasoningEffort
	capableEffort config.ReasoningEffort
	interests     config.Interests
}

// OpenAIOptions configures the analyzer. Both model names are required; there
// are no defaults that can become stale.
type OpenAIOptions struct {
	APIKey       string
	FastModel    string
	CapableModel string
	Interests    config.Interests

	// FastEffort and CapableEffort are optional: empty leaves the choice to the
	// provider. Unlike the model names there is a sane thing to do without
	// them, so they are not required.
	FastEffort    config.ReasoningEffort
	CapableEffort config.ReasoningEffort

	// HTTPClient replaces the default one. The debug journal uses it to record
	// every exchange; nil means the SDK's own.
	HTTPClient *http.Client
}

// reasoningModel matches model families that reject the temperature parameter.
var reasoningModel = regexp.MustCompile(`^(o\d|gpt-5)`)

// temperature returns zero for deterministic scoring, unless the model rejects
// the parameter entirely.
func temperature(model string) (float64, bool) {
	if reasoningModel.MatchString(model) {
		return 0, false
	}
	return 0, true
}

// tune applies either temperature or reasoning effort, which are mutually
// exclusive for the supported model families.
func tune(params *openai.ChatCompletionNewParams, model string, effort config.ReasoningEffort) {
	if value, send := temperature(model); send {
		params.Temperature = openai.Float(value)
		return
	}
	if effort != "" {
		params.ReasoningEffort = shared.ReasoningEffort(effort)
	}
}

// unsupportedByStrict are schema keywords OpenAI strict mode rejects. They stay
// in the shared schema because Anthropic supports them.
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

	options := []option.RequestOption{option.WithAPIKey(opts.APIKey)}
	if opts.HTTPClient != nil {
		options = append(options, option.WithHTTPClient(opts.HTTPClient))
	}

	return &OpenAI{
		client:        openai.NewClient(options...),
		fastModel:     opts.FastModel,
		capableModel:  opts.CapableModel,
		fastEffort:    opts.FastEffort,
		capableEffort: opts.CapableEffort,
		interests:     opts.Interests,
	}, nil
}

// Assess implements Assessor: one call that identifies the article and rates
// it. The instructions are the Claude provider's, word for word, so that the
// two can be compared.
func (o *OpenAI) Assess(ctx context.Context, a domain.Article, declared []string) (Assessment, error) {
	system := assessSystemPrompt(o.interests, declared)

	var result struct {
		Categories     []string              `json:"categories"`
		Entities       []string              `json:"entities"`
		Tone           string                `json:"tone"`
		ContentQuality domain.ContentQuality `json:"content_quality"`
		ContentReason  string                `json:"content_reason"`
		Score          int                   `json:"score"`
		Reason         string                `json:"reason"`
	}
	used, err := o.ask(ctx, o.fastModel, o.fastEffort, system, articlePrompt(a),
		"assessment", forStrictMode(assessmentSchema(o.interests, declared)), &result)
	if err != nil {
		return Assessment{}, err
	}

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

// Summarize implements Summarizer, on the more capable model.
func (o *OpenAI) Summarize(ctx context.Context, a domain.Article, as Assessment) (string, Usage, error) {
	if strings.TrimSpace(a.FullText) == "" {
		return "", Usage{}, fmt.Errorf("article has no text to summarize")
	}

	params := openai.ChatCompletionNewParams{
		Model: o.capableModel,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(summarySystemPrompt(o.interests,
				as.ContentQuality == domain.ContentLimited || as.ContentQuality == domain.ContentMismatched)),
			openai.UserMessage(summaryArticlePrompt(a, as.ContentQuality)),
		},
	}
	tune(&params, o.capableModel, o.capableEffort)

	completion, err := o.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return "", Usage{}, fmt.Errorf("call %s: %w", o.capableModel, err)
	}
	// Reported even when the reply turns out to be unusable: a call that
	// produced nothing was still charged for, and a bill that only counts
	// successes is the wrong bill.
	used := usageOf(completion)
	if len(completion.Choices) == 0 {
		return "", used, fmt.Errorf("call %s: no reply", o.capableModel)
	}

	summary := strings.TrimSpace(completion.Choices[0].Message.Content)
	if summary == "" {
		return "", used, fmt.Errorf("call %s: empty summary", o.capableModel)
	}
	return summary, used, nil
}

// usageOf reads what the provider says the call cost. Reasoning tokens are
// already inside completion tokens, so they are not added again.
func usageOf(completion *openai.ChatCompletion) Usage {
	return Usage{
		Input:  int(completion.Usage.PromptTokens),
		Output: int(completion.Usage.CompletionTokens),
	}
}

// ask makes one structured-output call and decodes the reply.
//
// The schema is sent with strict validation, which is what makes the reply
// safe to unmarshal without checking every field: the categories can only be
// the reader's own interests, and the score can only be an integer in range.
func (o *OpenAI) ask(ctx context.Context, model string, effort config.ReasoningEffort,
	system, prompt, schemaName string, schema map[string]any, out any) (Usage, error) {

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
	tune(&params, model, effort)

	completion, err := o.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return Usage{}, fmt.Errorf("call %s: %w", model, err)
	}
	used := usageOf(completion)
	if len(completion.Choices) == 0 {
		return used, fmt.Errorf("call %s: no reply", model)
	}

	text := completion.Choices[0].Message.Content
	if strings.TrimSpace(text) == "" {
		return used, fmt.Errorf("call %s: model returned no text", model)
	}
	if err := json.Unmarshal([]byte(text), out); err != nil {
		return used, fmt.Errorf("call %s: decode response: %w", model, err)
	}
	return used, nil
}
