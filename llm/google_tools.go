package llm

import (
	"encoding/json"
	"strings"
)

// GoogleEffectiveToolsKinds classifies an effective Google Interactions tools
// value by marshaling to JSON and reading each element's "type" field so
// preflight and request assembly match the exact wire JSON.
func GoogleEffectiveToolsKinds(raw any) (hasFunction, hasGoogleSearch bool) {
	if raw == nil {
		return false, false
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return false, false
	}
	var items []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(b, &items) != nil {
		return false, false
	}
	for _, item := range items {
		switch strings.ToLower(strings.TrimSpace(item.Type)) {
		case "function":
			hasFunction = true
		case "google_search":
			hasGoogleSearch = true
		}
	}
	return hasFunction, hasGoogleSearch
}
