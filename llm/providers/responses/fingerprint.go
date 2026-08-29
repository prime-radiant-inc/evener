package responses

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

const responsesRequestFingerprintPrefix = "cont-req-v1"

// RequestFingerprint hashes a built Responses body into a continuation
// fingerprint (spec §13 "Continuation"): stable across Complete and Stream,
// and across everything a continuation may vary (previous_response_id,
// conversation, stream, and — on the public API — store), so the planner
// can tell "same logical request" from "different request" without those
// fields leaking in.
func RequestFingerprint(family llm.ResponsesEndpointFamily, body map[string]any) (string, error) {
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
	return responsesRequestFingerprintPrefix + ":" + base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// excludedFingerprintFields are the body fields a continuation is allowed to
// vary without changing the fingerprint: the input and its anchors always,
// and store only on the public API (spec §13's storage-policy split).
func excludedFingerprintFields(family llm.ResponsesEndpointFamily) map[string]bool {
	excluded := map[string]bool{
		"input":                true,
		"previous_response_id": true,
		"conversation":         true,
		"stream":               true,
	}
	if family == llm.ResponsesEndpointFamilyOpenAIPublic {
		excluded["store"] = true
	}
	return excluded
}

// EndpointFamily reports which Responses endpoint family the instance's
// transport talks to (spec §7.6): the Codex transport's backend, or the
// public API everything else shares.
func EndpointFamily(res registry.Resolved) llm.ResponsesEndpointFamily {
	if res.Transport.Auth == registry.AuthOAuthOpenAICodex {
		return llm.ResponsesEndpointFamilyOpenAICodex
	}
	return llm.ResponsesEndpointFamilyOpenAIPublic
}
