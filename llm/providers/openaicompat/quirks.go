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

	// ToolChoiceAutoUnderReasoning downgrades a FORCING tool_choice
	// ("required"/named) to "auto" only while the request carries active
	// reasoning controls. Anthropic-routed OpenRouter models silently return
	// no reasoning under forced tool use (the direct Anthropic API 400s on
	// the same combo; that guard lives in the anthropic adapter) —
	// live-bisected 2026-07-02: dropping tool_choice restored reasoning.
	ToolChoiceAutoUnderReasoning bool

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
	// SupportsLongCacheRetention emits prompt_cache_key +
	// prompt_cache_retention:"24h" (and, with CacheControlFormat "anthropic",
	// ttl:"1h" on the ephemeral markers). The cache key derives from
	// req.PromptCacheKey, else "serf-session-"+req.SessionID — the same
	// convention agent.Session uses on the openai path, so both agree.
	SupportsLongCacheRetention bool
	// SendSessionAffinityHeaders sends per-request session-affinity headers
	// (session_id, x-client-request-id, x-session-affinity) when the request
	// carries a session id.
	SendSessionAffinityHeaders bool
	// SupportsStrictMode, when explicitly true, adds strict:false inside every
	// tool definition's function object. nil/false emits no strict field.
	SupportsStrictMode *bool
	// ChatTemplateKwargs is emitted verbatim as the request's
	// chat_template_kwargs object for the "chat-template" thinking format when an
	// effort is set.
	ChatTemplateKwargs map[string]any
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
			// z.ai's dialect: thinking:{"type":"enabled","clear_thinking":false}.
			ThinkingFormat: "zai",
		}
	case "openrouter":
		return ProviderQuirks{
			TranslateMaxToXHigh:          true,
			ToolChoiceAutoUnderReasoning: true,
			// OpenRouter's canonical reasoning control is the
			// {"reasoning":{"effort":...}} object. Live-verified 2026-07-02:
			// behaves identically to top-level reasoning_effort on OpenAI AND
			// Anthropic routed models (effort→budget translation happens
			// either way), and accepts the full serf vocabulary incl.
			// xhigh/minimal — the canonical form also carries future knobs
			// (exclude, max_tokens) top-level reasoning_effort cannot.
			ThinkingFormat: "openrouter",
		}
	default:
		return ProviderQuirks{}
	}
}
