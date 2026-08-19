package config

import (
	"strings"
	"testing"
)

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
