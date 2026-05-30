package main

import "testing"

func TestAbbreviateModel(t *testing.T) {
	tests := []struct {
		id   string
		want string
	}{
		// Known provider prefix — stripped generically.
		{"openai/gpt-5", "gpt-5"},
		// Custom instance name — same behaviour; instance stripped, model preserved.
		{"work/gpt-5", "gpt-5"},
		// Meta-provider: only the first segment is stripped.
		{"openrouter/anthropic/claude-opus", "anthropic/claude-opus"},
		// Date suffix stripped after instance prefix.
		{"openai/gpt-5-20260101", "gpt-5"},
		// Custom instance + date suffix.
		{"work/gpt-5-20260101", "gpt-5"},
		// No slash — bare model, returned unchanged.
		{"bare-model", "bare-model"},
		// Empty string.
		{"", ""},
	}
	for _, tc := range tests {
		got := abbreviateModel(tc.id)
		if got != tc.want {
			t.Errorf("abbreviateModel(%q) = %q, want %q", tc.id, got, tc.want)
		}
	}
}
