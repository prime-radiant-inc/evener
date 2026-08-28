package llm

import (
	"strings"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

func TestResponsesRequestFingerprintIgnoresStreamAndContinuationFields(t *testing.T) {
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
	if a != b || !strings.HasPrefix(a, "cont-req-v2:") {
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
	if got := ResponsesEndpointFamilyFor(registry.Resolved{Transport: registry.Transport{Auth: registry.AuthOAuthOpenAICodex}}); got != ResponsesEndpointFamilyOpenAICodex {
		t.Fatalf("codex transport: %s", got)
	}
	if got := ResponsesEndpointFamilyFor(registry.Resolved{Transport: registry.Transport{Auth: registry.AuthBearer}}); got != ResponsesEndpointFamilyOpenAIPublic {
		t.Fatalf("bearer: %s", got)
	}
}
