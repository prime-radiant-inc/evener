package openai

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

const responsesRequestFingerprintPrefix = "cont-req-v1"

func requestFingerprintForResponsesBody(body map[string]any) (string, error) {
	filtered := make(map[string]any, len(body))
	for k, v := range body {
		switch k {
		case "previous_response_id", "conversation", "store":
			continue
		default:
			filtered[k] = v
		}
	}

	b, err := json.Marshal(filtered)
	if err != nil {
		return "", fmt.Errorf("marshal request fingerprint body: %w", err)
	}
	sum := sha256.Sum256(b)
	return responsesRequestFingerprintPrefix + ":" + base64.RawURLEncoding.EncodeToString(sum[:]), nil
}
