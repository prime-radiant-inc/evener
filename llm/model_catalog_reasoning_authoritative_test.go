package llm

import "testing"

// supports_reasoning is knowledge when the source states it explicitly or the
// entry is a bare curated key (litellm's silence there means non-reasoning).
// A silent provider-prefixed mirror key leaves the answer unknown, so a
// sparse openrouter/ or ollama/ entry cannot mark a real reasoning model
// non-reasoning, while an explicit false on a mirror (azure audio models)
// still counts.
func TestParseLiteLLMCatalog_ReasoningAuthoritative(t *testing.T) {
	cat, err := parseLiteLLMCatalog([]byte(`{
        "bare-reasoner":            {"litellm_provider": "openai", "supports_reasoning": true},
        "bare-silent":              {"litellm_provider": "openai"},
        "bare-effort-flags":        {"litellm_provider": "openai", "supports_xhigh_reasoning_effort": true},
        "openrouter/vendor/silent": {"litellm_provider": "openrouter"},
        "openrouter/vendor/flags":  {"litellm_provider": "openrouter", "supports_xhigh_reasoning_effort": true},
        "openrouter/vendor/denied": {"litellm_provider": "openrouter", "supports_reasoning": false, "supports_xhigh_reasoning_effort": true},
        "openrouter/vendor/nope":   {"litellm_provider": "openrouter", "supports_reasoning": false}
    }`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cases := []struct {
		id                string
		wantAuthoritative bool
		wantSupports      bool
	}{
		{"bare-reasoner", true, true},
		{"bare-silent", true, false},
		// Effort flags imply a reasoning model when supports_reasoning is
		// absent (gpt-5-search-api's shape) — on bare and mirror keys alike —
		// and an explicit false wins over them (perplexity mirrors).
		{"bare-effort-flags", true, true},
		{"openrouter/vendor/silent", false, false},
		{"openrouter/vendor/flags", true, true},
		{"openrouter/vendor/denied", true, false},
		{"openrouter/vendor/nope", true, false},
	}
	for _, tc := range cases {
		mi := cat.GetModelInfo(tc.id)
		if mi == nil {
			t.Fatalf("%s not found", tc.id)
		}
		if mi.ReasoningAuthoritative != tc.wantAuthoritative || mi.SupportsReasoning != tc.wantSupports {
			t.Errorf("%s authoritative/supports = %v/%v, want %v/%v",
				tc.id, mi.ReasoningAuthoritative, mi.SupportsReasoning, tc.wantAuthoritative, tc.wantSupports)
		}
	}
}

// OpenRouter spells Anthropic versions with dots (claude-opus-4.6) where the
// overrides file keys on dashes; the family mapping must bridge that, or the
// adaptive-Claude default effort is lost on the OpenRouter path and those
// sessions run at medium where the direct path runs at high.
func TestEmbeddedCatalog_DottedClaudeMirrorsInheritFamilyOverlay(t *testing.T) {
	cat := EmbeddedModelCatalog()
	if cat == nil {
		t.Fatal("embedded catalog nil")
	}
	for _, id := range []string{
		"openrouter/anthropic/claude-opus-4.6",
		"openrouter/anthropic/claude-sonnet-4.6",
	} {
		mi := cat.GetModelInfo(id)
		if mi == nil {
			t.Fatalf("%s not found in embedded catalog", id)
		}
		if mi.DefaultReasoningEffort != "high" {
			t.Errorf("%s DefaultReasoningEffort = %q, want high from the dashed family overlay", id, mi.DefaultReasoningEffort)
		}
	}
}
