package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeInterests(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "interests.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write interests file: %v", err)
	}
	return path
}

func TestLoadInterests(t *testing.T) {
	path := writeInterests(t, `
threshold: 70
interests:
  - topic: "AI and machine learning"
    priority: 1
    subtopics: ["LLMs", "AI agents"]
    note: "Practical applications above theory."
  - topic: "Go programming"
    priority: 2
`)

	interests, err := LoadInterests(path)
	if err != nil {
		t.Fatalf("LoadInterests returned error: %v", err)
	}
	if interests.Threshold != 70 {
		t.Errorf("Threshold = %d, want 70", interests.Threshold)
	}
	if len(interests.Topics) != 2 {
		t.Fatalf("got %d topics, want 2", len(interests.Topics))
	}
	if got := interests.Topics[0].Subtopics; len(got) != 2 {
		t.Errorf("Subtopics = %v, want two entries", got)
	}
}

func TestLoadInterestsRejects(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"no interests", "threshold: 60\ninterests: []\n"},
		{"threshold too high", "threshold: 140\ninterests:\n  - topic: X\n    priority: 1\n"},
		{"negative threshold", "threshold: -1\ninterests:\n  - topic: X\n    priority: 1\n"},
		{"missing topic", "threshold: 60\ninterests:\n  - priority: 1\n"},
		{"priority below one", "threshold: 60\ninterests:\n  - topic: X\n    priority: 0\n"},
		{"misspelled key", "threshold: 60\ninterests:\n  - topic: X\n    priority: 1\n    notes: hello\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := LoadInterests(writeInterests(t, tt.content)); err == nil {
				t.Error("LoadInterests returned no error, want one")
			}
		})
	}
}

// Describe is what the model actually reads, so its ordering is behaviour, not
// formatting: the most important interests must come first.
func TestInterestsDescribe(t *testing.T) {
	interests := Interests{
		Topics: []Interest{
			{Topic: "Cooking", Priority: 3},
			{Topic: "AI", Priority: 1, Subtopics: []string{"LLMs"}, Note: "practical only"},
			{Topic: "Go", Priority: 2},
		},
	}

	got := interests.Describe()

	posAI := strings.Index(got, "AI")
	posGo := strings.Index(got, "Go")
	posCooking := strings.Index(got, "Cooking")
	if !(posAI < posGo && posGo < posCooking) {
		t.Errorf("topics are not ordered by priority:\n%s", got)
	}
	if !strings.Contains(got, "LLMs") {
		t.Error("subtopics are missing from the description")
	}
	if !strings.Contains(got, "practical only") {
		t.Error("notes are missing from the description — they carry most of the context")
	}
}
