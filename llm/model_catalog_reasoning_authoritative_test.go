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
        "openrouter/vendor/silent": {"litellm_provider": "openrouter"},
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
		{"openrouter/vendor/silent", false, false},
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
