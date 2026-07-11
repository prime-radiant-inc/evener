package openai

import (
	"encoding/json"
	"testing"

	"primeradiant.com/serf/llm"
)

// buildCodexBodyForTest runs the real Responses request builder on a
// codex-backend adapter and round-trips the result through JSON so assertions
// see exactly the wire shape.
func buildCodexBodyForTest(t *testing.T, req llm.Request) map[string]any {
	t.Helper()
	a := &Adapter{ChatGPTAccountID: "acct_test"}
	body, err := a.buildRequestBody(req)
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	var round map[string]any
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	return round
}

// The ChatGPT codex backend has no bare "gpt-5.6" slug (it 400s with "not
// supported when using Codex with a ChatGPT account"); the codex CLI always
// sends a full variant slug. Map bare gpt-5.6 to the default variant on the
// wire, codex backend only.
func TestGPT56_CodexBackendMapsBareSlugToSol(t *testing.T) {
	body := buildCodexBodyForTest(t, llm.Request{
		Model:    "gpt-5.6",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi"}}}},
	})
	if body["model"] != "gpt-5.6-sol" {
		t.Errorf("model = %#v, want \"gpt-5.6-sol\" on the codex backend", body["model"])
	}

	// Explicit variant slugs pass through untouched.
	body = buildCodexBodyForTest(t, llm.Request{
		Model:    "gpt-5.6-terra",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi"}}}},
	})
	if body["model"] != "gpt-5.6-terra" {
		t.Errorf("model = %#v, want \"gpt-5.6-terra\"", body["model"])
	}
}

// Platform-API (api-key) requests keep the caller's slug: the public API
// serves bare gpt-5.6 directly.
func TestGPT56_PlatformKeepsBareSlug(t *testing.T) {
	body := buildBodyForTest(t, llm.Request{
		Model:    "gpt-5.6",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi"}}}},
	})
	if body["model"] != "gpt-5.6" {
		t.Errorf("model = %#v, want \"gpt-5.6\" on the platform API", body["model"])
	}
}
