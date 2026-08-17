package web

import (
	"github.com/emaori/ziba/internal/config"
	"github.com/emaori/ziba/internal/store"
)

type interestPreset struct {
	ID, Label string
	Value     config.Interest
}
type sourcePreset struct {
	ID, Label string
	Value     store.SourceInput
}

var interestPresets = []interestPreset{
	{"ai", "AI", config.Interest{Topic: "AI", Priority: 1, Subtopics: []string{"LLMs", "AI agents", "machine learning", "ML infrastructure"}, Note: "Practical applications and how things actually work. Less interested in funding rounds and industry gossip."}},
	{"computer-science", "Computer Science", config.Interest{Topic: "Computer Science", Priority: 1, Subtopics: []string{"algorithms", "distributed systems", "programming languages", "research"}, Note: "Ideas with substance, including academic work when it is readable."}},
}

var sourcePresets = []sourcePreset{
	{"ieee-spectrum", "IEEE Spectrum", store.SourceInput{Name: "IEEE Spectrum", Type: "rss", URL: "https://spectrum.ieee.org/feeds/feed.rss", Enabled: true}},
	{"hacker-news", "Hacker News — front page", store.SourceInput{Name: "Hacker News — front page", Type: "rss", URL: "https://hnrss.org/frontpage", Enabled: true}},
}
