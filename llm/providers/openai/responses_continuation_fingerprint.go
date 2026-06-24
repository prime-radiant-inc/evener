package openai

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"primeradiant.com/serf/llm"
)

const responsesRequestFingerprintPrefix = "cont-req-v1"

func requestFingerprintForResponsesBody(family llm.ResponsesEndpointFamily, body map[string]any) (string, error) {
	excluded := responsesRequestFingerprintExcludedFields(family)
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

func responsesRequestFingerprintExcludedFields(family llm.ResponsesEndpointFamily) map[string]bool {
	excluded := map[string]bool{
		"input":                true,
		"previous_response_id": true,
		"conversation":         true,
	}
	if family == llm.ResponsesEndpointFamilyOpenAIPublic {
		excluded["store"] = true
	}
	return excluded
}
