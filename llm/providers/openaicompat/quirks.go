package openaicompat

import "strings"

// ProviderQuirks configures per-provider behavioral overrides for OpenAI-compatible
// APIs that deviate from the standard Chat Completions contract.
type ProviderQuirks struct {
	LockTemperature      bool
	LockTopP             bool
	LockFrequencyPenalty bool
	LockPresencePenalty  bool
	ToolChoiceAutoOnly   bool
	MaxStopSequences     int
	StripEmptyContent    bool
	NoJSONSchema         bool
	FinishReasonMap      map[string]string
	TranslateMaxToXHigh  bool // OpenRouter vocab: our "max" → their "xhigh"
}

func (q ProviderQuirks) mapFinishReason(raw string) string {
	if q.FinishReasonMap == nil {
		return raw
	}
	if mapped, ok := q.FinishReasonMap[raw]; ok {
		return mapped
	}
	return raw
}

// QuirksPreset returns a ProviderQuirks configuration for a known provider name.
func QuirksPreset(name string) ProviderQuirks {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "kimi-k2.5", "kimi", "moonshot":
		return ProviderQuirks{
			LockTemperature:      true,
			LockTopP:             true,
			LockFrequencyPenalty: true,
			LockPresencePenalty:  true,
			ToolChoiceAutoOnly:   true,
			NoJSONSchema:         true,
		}
	case "glm-5", "glm-5-turbo", "glm", "zhipu":
		return ProviderQuirks{
			StripEmptyContent:  true,
			ToolChoiceAutoOnly: true,
			MaxStopSequences:   1,
			NoJSONSchema:       true,
			FinishReasonMap: map[string]string{
				"sensitive":     "content_filter",
				"network_error": "error",
			},
		}
	case "openrouter":
		return ProviderQuirks{
			TranslateMaxToXHigh: true,
		}
	default:
		return ProviderQuirks{}
	}
}
