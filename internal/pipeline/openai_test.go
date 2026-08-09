package pipeline

import (
	"strings"
	"testing"
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

// The o-series rejects temperature outright, so sending it would fail the call
// rather than be ignored — which is what a run configured with o4-mini would
// hit on its first article.
func TestTemperatureIsOmittedForReasoningModels(t *testing.T) {
	tests := []struct {
		model string
		send  bool
	}{
		{"gpt-4o-mini", true},
		{"gpt-5.6", true},
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
	if len(schema["required"].([]string)) != 5 {
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
