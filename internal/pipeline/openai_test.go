package pipeline

import (
	"fmt"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3"

	"github.com/emaori/ziba/internal/config"
)

// A model name is required. OpenAI renames and retires models faster than a
// constant here could follow, so a stale default would fail at the first real
// article with an error about an unknown model rather than at startup.
func TestNewOpenAIRequiresModels(t *testing.T) {
	tests := []struct {
		name string
		opts OpenAIOptions
		want string
	}{
		{"no key", OpenAIOptions{FastModel: "a", CapableModel: "b"}, "OPENAI_API_KEY"},
		{"no fast model", OpenAIOptions{APIKey: "k", CapableModel: "b"}, "ZIBA_FAST_MODEL"},
		{"no capable model", OpenAIOptions{APIKey: "k", FastModel: "a"}, "ZIBA_CAPABLE_MODEL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewOpenAI(tt.opts)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not name %s", err, tt.want)
			}
		})
	}

	if _, err := NewOpenAI(OpenAIOptions{APIKey: "k", FastModel: "a", CapableModel: "b"}); err != nil {
		t.Errorf("a complete configuration was refused: %v", err)
	}
}

// The o-series and the GPT-5 line reject temperature outright, so sending it
// fails the call rather than being ignored.
//
// The gpt-5 rows were once asserted the other way round, which is the whole
// reason they are worth listing: the configured capable model is a gpt-5, and
// the pipeline demotes a failed summary to a warning, so the mistake would have
// cost every summary in a run without failing one of them.
func TestTemperatureIsOmittedForReasoningModels(t *testing.T) {
	tests := []struct {
		model string
		send  bool
	}{
		{"gpt-4o-mini", true},
		{"gpt-4o", true},
		{"gpt-5.6", false},
		{"gpt-5.6-luna", false},
		{"gpt-5", false},
		{"o4-mini", false},
		{"o3", false},
		{"o1-preview", false},
	}
	for _, tt := range tests {
		value, send := temperature(tt.model)
		if send != tt.send {
			t.Errorf("temperature(%q) sent = %v, want %v", tt.model, send, tt.send)
		}
		if send && value != 0 {
			t.Errorf("temperature(%q) = %v, want 0: scoring should not vary between runs", tt.model, value)
		}
	}
}

// Strict mode refuses several keywords the shared schema uses. They have to be
// dropped for this provider, and the ones that matter have to survive.
func TestForStrictMode(t *testing.T) {
	schema := forStrictMode(assessmentSchema(schemaInterests(), nil))
	properties := schema["properties"].(map[string]any)

	categories := properties["categories"].(map[string]any)
	for _, refused := range unsupportedByStrict {
		if _, present := categories[refused]; present {
			t.Errorf("categories still carries %q, which strict mode refuses", refused)
		}
	}
	score := properties["score"].(map[string]any)
	for _, refused := range unsupportedByStrict {
		if _, present := score[refused]; present {
			t.Errorf("score still carries %q, which strict mode refuses", refused)
		}
	}

	// What must survive: the model still cannot invent a category, add a field,
	// or leave one out.
	if schema["additionalProperties"] != false {
		t.Error("additionalProperties is no longer false")
	}
	if len(schema["required"].([]string)) != len(properties) {
		t.Errorf("required = %v, want every field", schema["required"])
	}
	items := categories["items"].(map[string]any)
	if _, ok := items["enum"].([]string); !ok {
		t.Error("the category enum was stripped; the model could invent one")
	}

	// And the original is untouched, since Anthropic honours those keywords.
	original := assessmentSchema(schemaInterests(), nil)["properties"].(map[string]any)
	if _, present := original["score"].(map[string]any)["maximum"]; !present {
		t.Error("stripping mutated the shared schema instead of copying it")
	}
}

// Both providers must answer the same question, or comparing them measures the
// prompts rather than the models.
func TestBothProvidersShareTheirPrompts(t *testing.T) {
	interests := schemaInterests()

	inferred := assessSystemPrompt(interests, nil)
	declared := assessSystemPrompt(interests, []string{".NET"})

	if !strings.Contains(inferred, "what the piece is actually about") {
		t.Error("the score bands are missing from the inferred prompt")
	}
	if !strings.Contains(declared, "what the piece is actually about") {
		t.Error("the score bands are missing from the declared prompt")
	}
	if !strings.Contains(inferred, "multiple of 5") {
		t.Error("the rounding instruction is missing")
	}
	if !strings.Contains(inferred, `"score":70`) {
		t.Error("the worked example is missing")
	}
	// The declared prompt asks a different question and must not offer the
	// reader's interests as something to judge against.
	if strings.Contains(declared, "Do not question whether the subject is relevant") == false {
		t.Error("the declared prompt no longer says the subject is settled")
	}
}

// The effort is the only setting that meaningfully moves the bill, because
// reasoning tokens are billed as output and output costs six times input. The
// rules that matter: it goes only to models that reason, it is left out when
// unset so the provider chooses, and it is never sent alongside a temperature —
// no model accepts both, so sending both would fail every call.
func TestReasoningEffortGoesOnlyToModelsThatReason(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		effort     config.ReasoningEffort
		wantEffort string
		wantTemp   bool
	}{
		{"a reasoning model takes the effort", "gpt-5.6-luna", config.EffortLow, "low", false},
		{"unset leaves the choice to the provider", "gpt-5.6-terra", "", "", false},
		{"a model with a temperature is not sent an effort", "gpt-4o-mini", config.EffortLow, "", true},
		{"the o-series reasons too", "o4-mini", config.EffortMinimal, "minimal", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var params openai.ChatCompletionNewParams
			tune(&params, tt.model, tt.effort)

			if got := string(params.ReasoningEffort); got != tt.wantEffort {
				t.Errorf("reasoning_effort = %q, want %q", got, tt.wantEffort)
			}
			if got := params.Temperature.Valid(); got != tt.wantTemp {
				t.Errorf("temperature sent = %v, want %v", got, tt.wantTemp)
			}
		})
	}
}

// The category limit has to be stated in words, not only in the schema.
//
// maxItems is one of the keywords OpenAI's strict mode refuses, so it is
// stripped before the request is sent and the model is told nothing. The first
// real call returned four categories for an article that was squarely about
// one. Anthropic honours the schema, so without this the same article would be
// filed under a different number of interests depending on which company
// answered — the divergence that sharing the prompts exists to prevent.
func TestTheCategoryLimitSurvivesStrictMode(t *testing.T) {
	interests := schemaInterests()

	prompt := assessSystemPrompt(interests, nil)
	if !strings.Contains(prompt, fmt.Sprintf("at most %d", maxCategories)) {
		t.Errorf("the assessment prompt never states the limit of %d categories", maxCategories)
	}

	// And in the one place a description survives stripping.
	stripped := forStrictMode(assessmentSchema(interests, nil))
	categories := stripped["properties"].(map[string]any)["categories"].(map[string]any)
	if _, present := categories["maxItems"]; present {
		t.Fatal("maxItems survived; strict mode would refuse the call")
	}
	description, _ := categories["description"].(string)
	if !strings.Contains(description, fmt.Sprintf("at most %d", maxCategories)) {
		t.Errorf("the categories description does not state the limit: %q", description)
	}

	// Anthropic still gets the enforceable version.
	full := assessmentSchema(interests, nil)["properties"].(map[string]any)["categories"].(map[string]any)
	if full["maxItems"] != maxCategories {
		t.Errorf("maxItems = %v, want %d", full["maxItems"], maxCategories)
	}

	// A declared source is not asked the question at all, so the limit is
	// meaningless there and must not be claimed.
	if strings.Contains(assessSystemPrompt(interests, []string{".NET"}), "at most") {
		t.Error("the declared prompt talks about choosing categories it never asks for")
	}
}
