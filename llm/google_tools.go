package llm

import (
	"encoding/json"
	"strings"
)

// GoogleEffectiveToolsKinds classifies an effective Google Interactions tools
// value (typed slice, generic JSON slice, json.RawMessage, or arbitrary JSON
// slice) into whether it contains any function declaration and/or google_search.
func GoogleEffectiveToolsKinds(raw any) (hasFunction, hasGoogleSearch bool) {
	if raw == nil {
		return false, false
	}
	classifyType := func(t string) {
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "function":
			hasFunction = true
		case "google_search":
			hasGoogleSearch = true
		}
	}
	switch x := raw.(type) {
	case []map[string]any:
		for _, item := range x {
			if s, ok := item["type"].(string); ok {
				classifyType(s)
			}
		}
		return hasFunction, hasGoogleSearch
	case []any:
		for _, elem := range x {
			switch m := elem.(type) {
			case map[string]any:
				if s, ok := m["type"].(string); ok {
					classifyType(s)
				}
			case map[string]string:
				classifyType(m["type"])
			default:
				b, err := json.Marshal(elem)
				if err != nil {
					continue
				}
				var obj struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(b, &obj) == nil {
					classifyType(obj.Type)
				}
			}
		}
		return hasFunction, hasGoogleSearch
	case json.RawMessage:
		var items []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(x, &items) == nil {
			for _, item := range items {
				classifyType(item.Type)
			}
		}
		return hasFunction, hasGoogleSearch
	case []byte:
		var items []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(x, &items) == nil {
			for _, item := range items {
				classifyType(item.Type)
			}
		}
		return hasFunction, hasGoogleSearch
	default:
		b, err := json.Marshal(raw)
		if err != nil {
			return false, false
		}
		var items []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(b, &items) == nil {
			for _, item := range items {
				classifyType(item.Type)
			}
		}
		return hasFunction, hasGoogleSearch
	}
}
