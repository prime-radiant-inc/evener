package llm

import (
	"strings"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

func TestResponsesRequestFingerprintIgnoresStreamAndContinuationFields(t *testing.T) {
	t.Parallel()
	base := map[string]any{"model": "gpt-5.5", "input": []any{"a"}, "temperature": 0.2}
	streamed := map[string]any{"model": "gpt-5.5", "input": []any{"b"}, "temperature": 0.2, "stream": true, "previous_response_id": "resp_1", "conversation": "conv_1", "store": true}
	a, err := ResponsesRequestFingerprint(ResponsesEndpointFamilyOpenAIPublic, base)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ResponsesRequestFingerprint(ResponsesEndpointFamilyOpenAIPublic, streamed)
	if err != nil {
		t.Fatal(err)
	}
	if a != b || !strings.HasPrefix(a, "cont-req-v3:") {
		t.Fatalf("fingerprints must agree across Complete/Stream and continuation fields: %s vs %s", a, b)
	}
	c, _ := ResponsesRequestFingerprint(ResponsesEndpointFamilyOpenAICodex, streamed)
	d, _ := ResponsesRequestFingerprint(ResponsesEndpointFamilyOpenAICodex, base)
	if c == d {
		t.Fatal("store is part of the fingerprint on the Codex family")
	}
}

// TestResponsesRequestFingerprintSeparatesBodies is the other half of the
// contract: everything outside the excluded set is hashed, so two bodies that
// would reach the model differently cannot share a fingerprint.
func TestResponsesRequestFingerprintSeparatesBodies(t *testing.T) {
	t.Parallel()
	base := map[string]any{"model": "gpt-5.5", "input": []any{"a"}, "temperature": 0.2}
	hotter := map[string]any{"model": "gpt-5.5", "input": []any{"a"}, "temperature": 0.5}
	a, err := ResponsesRequestFingerprint(ResponsesEndpointFamilyOpenAIPublic, base)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ResponsesRequestFingerprint(ResponsesEndpointFamilyOpenAIPublic, hotter)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("a different body must fingerprint differently")
	}
}

func TestResponsesEndpointFamilyFor(t *testing.T) {
	t.Parallel()
	if got := ResponsesEndpointFamilyFor(registry.Resolved{Transport: registry.Transport{Auth: registry.AuthOAuthOpenAICodex}}); got != ResponsesEndpointFamilyOpenAICodex {
		t.Fatalf("codex transport: %s", got)
	}
	if got := ResponsesEndpointFamilyFor(registry.Resolved{Transport: registry.Transport{Auth: registry.AuthBearer}}); got != ResponsesEndpointFamilyOpenAIPublic {
		t.Fatalf("bearer: %s", got)
	}
}

// TestResponsesRequestFingerprintUpgradesV2Anchor pins the cont-req-v3 cut-over: a
// stored v2 anchor cannot match a v3 plan, so the session resends full history and
// re-anchors under v3.
func TestResponsesRequestFingerprintUpgradesV2Anchor(t *testing.T) {
	t.Parallel()
	body := map[string]any{"model": "gpt-5.5", "input": []any{"a"}, "temperature": 0.2}
	plan, err := ResponsesRequestFingerprint(ResponsesEndpointFamilyOpenAIPublic, body)
	if err != nil {
		t.Fatal(err)
	}
	if v2 := "cont-req-v2:" + strings.TrimPrefix(plan, responsesRequestFingerprintPrefix); v2 == plan {
		t.Fatalf("a v2 anchor must not satisfy a v3 plan, both are %q", plan)
	}
}
