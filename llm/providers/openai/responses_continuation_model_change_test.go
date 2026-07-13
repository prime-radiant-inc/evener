package openai

// Task 6 / spec N4 test (i): an OpenAI Responses continuation anchor is keyed on
// the request fingerprint, which hashes the model. A mid-session model change
// therefore changes the fingerprint, so the stored anchor cannot be reused and
// the request falls back to full-history replay.

import (
	"testing"

	"primeradiant.com/serf/llm"
)

func TestResponsesRequestFingerprint_ModelChangeInvalidatesAnchor(t *testing.T) {
	t.Parallel()
	family := llm.ResponsesEndpointFamilyOpenAIPublic
	base := map[string]any{
		"model":       "gpt-5.4",
		"input":       []any{map[string]any{"role": "user"}},
		"temperature": 0.5,
	}
	fpA, err := requestFingerprintForResponsesBody(family, base)
	if err != nil {
		t.Fatalf("fingerprint A: %v", err)
	}

	switched := map[string]any{
		"model":       "gpt-5.6",
		"input":       []any{map[string]any{"role": "user"}},
		"temperature": 0.5,
	}
	fpB, err := requestFingerprintForResponsesBody(family, switched)
	if err != nil {
		t.Fatalf("fingerprint B: %v", err)
	}

	if fpA == fpB {
		t.Fatalf("model change did not change the continuation fingerprint (%q); a switched model would wrongly reuse the anchor", fpA)
	}
}
