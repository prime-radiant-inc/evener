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

	// ThinkingFormat selects the reasoning wire shape; see
	// providercfg.CompatConfig.ThinkingFormat for the vocabulary. Empty means
	// the OpenAI default (top-level reasoning_effort).
	ThinkingFormat string
	// SupportsReasoningEffort gates the reasoning_effort field where a format
	// treats it as optional; nil defers to the format default (openai,
	// deepseek, together: true; zai: false).
	SupportsReasoningEffort *bool
	// MaxTokensField names the output-cap field; empty means "max_tokens".
	MaxTokensField string
	// ToolStream sends z.ai's tool_stream:true when tools are present.
	ToolStream bool
	// SendStoreFalse opts out of server-side retention on providers that
	// accept OpenAI's store parameter.
	SendStoreFalse bool
	// UseDeveloperRole sends the system prompt under the "developer" role.
	UseDeveloperRole bool
	// OmitStreamUsage drops stream_options:{include_usage:true} for providers
	// that reject the field.
	OmitStreamUsage bool
	// RequireToolResultName adds a name field to tool-role messages.
	RequireToolResultName bool
	// RequireAssistantAfterToolResult injects an empty assistant message
	// between a tool result and a following user message.
	RequireAssistantAfterToolResult bool
	// ThinkingAsText replays assistant thinking as plain text content instead
	// of a reasoning_content field.
	ThinkingAsText bool
	// EmptyReasoningContentOnAssistant adds reasoning_content:"" to replayed
	// assistant messages that carry none (DeepSeek-style strict validators).
	EmptyReasoningContentOnAssistant bool
	// CacheControlFormat: "anthropic" applies Anthropic cache_control markers
	// for gateways that forward them.
	CacheControlFormat string
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
