package llm

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"primeradiant.com/evener/llm/registry"
)

const responsesRequestFingerprintPrefix = "cont-req-v2:"

// ResponsesEndpointFamilyFor is spec §7.6: openai_codex on the Codex
// transport, openai_public everywhere else.
func ResponsesEndpointFamilyFor(res registry.Resolved) ResponsesEndpointFamily {
	if res.Transport.Auth == registry.AuthOAuthOpenAICodex {
		return ResponsesEndpointFamilyOpenAICodex
	}
	return ResponsesEndpointFamilyOpenAIPublic
}

// ResponsesRequestFingerprint hashes a built Responses body minus the
// fields that differ between a continuation request and its full-history
// twin (spec §7.6): input, previous_response_id, conversation, stream, and
// on the public family store. The v2 prefix marks the cut-over: v1 was
// computed from the pre-registry builder.
func ResponsesRequestFingerprint(family ResponsesEndpointFamily, body map[string]any) (string, error) {
	excluded := excludedFingerprintFields(family)
	filtered := make(map[string]any, len(body))
	for k, v := range body {
		if excluded[k] {
			continue
		}
		filtered[k] = v
	}

	b, err := json.Marshal(filtered)
	if err != nil {
		return "", fmt.Errorf("marshal request fingerprint body: %w", err)
	}
	sum := sha256.Sum256(b)
	return responsesRequestFingerprintPrefix + base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// excludedFingerprintFields are the body fields a continuation is allowed to
// vary without changing the fingerprint: the input and its anchors always,
// and store only on the public API (spec §7.6's storage-policy split).
func excludedFingerprintFields(family ResponsesEndpointFamily) map[string]bool {
	excluded := map[string]bool{
		"input":                true,
		"previous_response_id": true,
		"conversation":         true,
		"stream":               true,
	}
	if family == ResponsesEndpointFamilyOpenAIPublic {
		excluded["store"] = true
	}
	return excluded
}
