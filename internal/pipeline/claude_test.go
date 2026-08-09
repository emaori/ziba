package pipeline

import (
	"strings"
	"testing"

	"github.com/emaori/ziba/internal/config"
)

func schemaInterests() config.Interests {
	return config.Interests{
		Threshold: 60,
		Topics:    []config.Interest{{Topic: "AI", Priority: 1}, {Topic: ".NET", Priority: 2}},
	}
}

// The request the model answers differs by whether the source states its
// subject, and the difference is the point of declaring: a question that has no
// business being asked is not asked at all.
func TestAssessmentSchema(t *testing.T) {
	inferred := assessmentSchema(schemaInterests(), nil)
	declared := assessmentSchema(schemaInterests(), []string{".NET"})

	properties := func(s map[string]any) map[string]any {
		return s["properties"].(map[string]any)
	}

	if _, asked := properties(inferred)["categories"]; !asked {
		t.Error("the inferred schema does not ask for categories")
	}
	if _, asked := properties(declared)["categories"]; asked {
		t.Error("the declared schema still asks for categories, which are already known")
	}

	// Required must agree with the properties, or the model is asked for a
	// field the schema forbids.
	for name, schema := range map[string]map[string]any{"inferred": inferred, "declared": declared} {
		props := properties(schema)
		for _, field := range schema["required"].([]string) {
			if _, present := props[field]; !present {
				t.Errorf("%s schema requires %q but does not define it", name, field)
			}
		}
	}

	// And the score means different things, which the description has to say
	// because it is all the model is told about the field.
	inferredScore := properties(inferred)["score"].(map[string]any)["description"].(string)
	declaredScore := properties(declared)["score"].(map[string]any)["description"].(string)

	if !strings.Contains(inferredScore, "relevant") {
		t.Errorf("inferred score description = %q, want it to be about relevance", inferredScore)
	}
	if !strings.Contains(declaredScore, "interesting") {
		t.Errorf("declared score description = %q, want it to be about interest", declaredScore)
	}
}

// The categories the model is allowed to choose are the reader's own, so the
// tab bar stays stable and is never the model's invention.
func TestAssessmentSchemaConstrainsCategoriesToTheInterests(t *testing.T) {
	schema := assessmentSchema(schemaInterests(), nil)
	categories := schema["properties"].(map[string]any)["categories"].(map[string]any)
	items := categories["items"].(map[string]any)

	got, ok := items["enum"].([]string)
	if !ok {
		t.Fatalf("categories items have no enum: %v", items)
	}
	if len(got) != 2 || got[0] != "AI" || got[1] != ".NET" {
		t.Errorf("enum = %v, want the configured interests", got)
	}
}

// No provider carries a default model name. A name in code is a claim about
// what exists, made once and never revisited, and a stale one fails at the
// first real article rather than at startup.
func TestNewClaudeRequiresModels(t *testing.T) {
	tests := []struct {
		name string
		opts ClaudeOptions
		want string
	}{
		{"no key", ClaudeOptions{FastModel: "a", CapableModel: "b"}, "ANTHROPIC_API_KEY"},
		{"no fast model", ClaudeOptions{APIKey: "k", CapableModel: "b"}, "ZIBA_FAST_MODEL"},
		{"no capable model", ClaudeOptions{APIKey: "k", FastModel: "a"}, "ZIBA_CAPABLE_MODEL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewClaude(tt.opts)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not name %s", err, tt.want)
			}
		})
	}

	if _, err := NewClaude(ClaudeOptions{APIKey: "k", FastModel: "a", CapableModel: "b"}); err != nil {
		t.Errorf("a complete configuration was refused: %v", err)
	}
}
