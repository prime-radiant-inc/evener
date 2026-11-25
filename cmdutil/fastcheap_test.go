package cmdutil

import "testing"

func TestCrossProviderFastCheapRef(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		raw            string
		activeProvider string
		wantProvider   string
		wantModel      string
		wantOK         bool
	}{
		{name: "empty", raw: "", activeProvider: "openai", wantOK: false},
		{name: "whitespace", raw: "   \t ", activeProvider: "openai", wantOK: false},
		{name: "bare model", raw: "gpt-5-mini", activeProvider: "openai", wantOK: false},
		{name: "bare disable token", raw: "off", activeProvider: "openai", wantOK: false},
		{name: "trailing slash", raw: "anthropic/", activeProvider: "openai", wantOK: false},
		{name: "leading slash", raw: "/claude-haiku", activeProvider: "openai", wantOK: false},
		{name: "same provider", raw: "openai/gpt-5-mini", activeProvider: "openai", wantOK: false},
		{name: "same provider case-insensitive", raw: "OpenAI/gpt-5-mini", activeProvider: "openai", wantOK: false},
		{name: "cross provider", raw: "anthropic/claude-haiku-4-5-20251001", activeProvider: "openai", wantProvider: "anthropic", wantModel: "claude-haiku-4-5-20251001", wantOK: true},
		{name: "cross provider trimmed", raw: " anthropic/claude-haiku-4-5 ", activeProvider: " openai ", wantProvider: "anthropic", wantModel: "claude-haiku-4-5", wantOK: true},
		{name: "cross provider with slash in model", raw: "openrouter/anthropic/claude-haiku-4-5", activeProvider: "openai", wantProvider: "openrouter", wantModel: "anthropic/claude-haiku-4-5", wantOK: true},
		{name: "empty active provider", raw: "anthropic/claude-haiku-4-5", activeProvider: "", wantProvider: "anthropic", wantModel: "claude-haiku-4-5", wantOK: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			prov, model, ok := CrossProviderFastCheapRef(tc.raw, tc.activeProvider)
			if ok != tc.wantOK || prov != tc.wantProvider || model != tc.wantModel {
				t.Fatalf("CrossProviderFastCheapRef(%q, %q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.raw, tc.activeProvider, prov, model, ok, tc.wantProvider, tc.wantModel, tc.wantOK)
			}
		})
	}
}
