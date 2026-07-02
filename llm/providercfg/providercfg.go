// Package providercfg is the public configuration schema for serf's providers
// (providers.toml) — the leaf instance/type/behavior-tag vocabulary used to
// build an llm Client from configuration (via llm.NewFromProviders) and to
// resolve agent profiles. It imports no other serf package.
package providercfg

type Type string
type APIStyle string

const (
	StyleResponses       APIStyle = "responses"
	StyleChatCompletions APIStyle = "chat-completions"
	StyleAuto            APIStyle = "auto"
)

type InstanceConfig struct {
	Name     string   `toml:"-"`
	Type     Type     `toml:"type"`
	APIStyle APIStyle `toml:"api_style"`
	BaseURL  string   `toml:"base_url"`
	APIKey   string   `toml:"api_key"`
	Quirks   string   `toml:"quirks"`
	// Headers are extra HTTP request headers sent on every call to this
	// instance's endpoint. Valid for ALL instance types — any provider may sit
	// behind a gateway (Portkey, Helicone, a CF worker) that needs headers.
	// Values support the same $ENV/${ENV}/$$ expansion as api_key, resolved at
	// adapter construction (see ResolveHeaderValue). A user-configured header
	// overrides a provider-set default of the same name (e.g. kimi's coding-plan
	// User-Agent) but does not erase it when unset.
	Headers map[string]string `toml:"headers"`
	// Compat holds OpenAI-compatible protocol overrides applied to every model
	// served by this instance. Only valid for openai-compat-family instances
	// (openai+chat-completions, kimi, glm, openrouter, ollama).
	Compat *CompatConfig `toml:"compat"`
	// Models defines or overrides models served by this instance, keyed by the
	// wire model id. Same family restriction as Compat.
	Models map[string]ModelConfig `toml:"models"`
}

// CompatConfig is the user-configurable slice of OpenAI-compatible protocol
// behavior. Every field is optional; nil/empty means "inherit" — the quirks
// preset (and each format's own default) rules. Instance-level compat overlays
// the preset; per-model compat overlays the instance, field by field.
type CompatConfig struct {
	// ThinkingFormat selects how reasoning is requested on the wire:
	// "openai" (reasoning_effort, the default), "zai" (thinking:{type} object),
	// "deepseek" (thinking:{type} + reasoning_effort), "openrouter"
	// (reasoning:{effort}), "together" (reasoning:{enabled}), "qwen"
	// (enable_thinking), "qwen-chat-template"
	// (chat_template_kwargs:{enable_thinking,preserve_thinking}), "chat-template"
	// (chat_template_kwargs = ChatTemplateKwargs verbatim), or "string-thinking"
	// (thinking:"<effort>").
	ThinkingFormat string `toml:"thinking_format"`
	// SupportsStrictMode, when EXPLICITLY true, adds strict:false inside every
	// tool definition's "function" object. Default nil/false emits no strict
	// field. This deliberately diverges from Pi, whose default always sends
	// strict:false — flipping the wire shape of every existing serf request is
	// not worth the risk, so serf opts in per instance instead.
	SupportsStrictMode *bool `toml:"supports_strict_mode"`
	// ChatTemplateKwargs is emitted verbatim as the request's
	// chat_template_kwargs object when thinking_format = "chat-template" and an
	// effort is set. Overlay replaces wholesale (like FinishReasonMap). serf
	// deliberately skips Pi's per-value $var indirection (YAGNI).
	ChatTemplateKwargs map[string]any `toml:"chat_template_kwargs"`
	// SupportsReasoningEffort gates emitting the reasoning_effort field for
	// formats that treat it as optional. nil defers to the format's default
	// (openai/deepseek/together: true; zai: false).
	SupportsReasoningEffort *bool `toml:"supports_reasoning_effort"`
	// MaxTokensField names the output-cap field: "max_tokens" (default) or
	// "max_completion_tokens".
	MaxTokensField string `toml:"max_tokens_field"`
	// ToolStream sends z.ai's tool_stream:true when tools are present, enabling
	// incremental tool-call argument streaming.
	ToolStream *bool `toml:"tool_stream"`
	// SupportsStore marks providers that accept OpenAI's store parameter;
	// serf then sends store:false to opt out of server-side retention.
	SupportsStore *bool `toml:"supports_store"`
	// SupportsDeveloperRole sends the system prompt under the "developer" role.
	SupportsDeveloperRole *bool `toml:"supports_developer_role"`
	// SupportsUsageInStreaming gates stream_options:{include_usage:true};
	// nil/true sends it, false omits it for providers that reject the field.
	SupportsUsageInStreaming *bool `toml:"supports_usage_in_streaming"`
	// RequiresToolResultName adds a name field to tool-role messages.
	RequiresToolResultName *bool `toml:"requires_tool_result_name"`
	// RequiresAssistantAfterToolResult injects an empty assistant message
	// between a tool result and a following user message.
	RequiresAssistantAfterToolResult *bool `toml:"requires_assistant_after_tool_result"`
	// RequiresThinkingAsText replays assistant thinking as plain text content
	// instead of a reasoning_content field.
	RequiresThinkingAsText *bool `toml:"requires_thinking_as_text"`
	// RequiresReasoningContentOnAssistant adds reasoning_content:"" to replayed
	// assistant messages that carry none (DeepSeek-style strict validators).
	RequiresReasoningContentOnAssistant *bool `toml:"requires_reasoning_content_on_assistant"`
	// CacheControlFormat: "anthropic" applies Anthropic cache_control markers
	// (system prompt, last tool, last message) for gateways that forward them.
	CacheControlFormat string `toml:"cache_control_format"`
	// The remaining knobs mirror the built-in quirks presets.
	LockTemperature      *bool             `toml:"lock_temperature"`
	LockTopP             *bool             `toml:"lock_top_p"`
	LockFrequencyPenalty *bool             `toml:"lock_frequency_penalty"`
	LockPresencePenalty  *bool             `toml:"lock_presence_penalty"`
	ToolChoiceAutoOnly   *bool             `toml:"tool_choice_auto_only"`
	MaxStopSequences     *int              `toml:"max_stop_sequences"`
	StripEmptyContent    *bool             `toml:"strip_empty_content"`
	NoJSONSchema         *bool             `toml:"no_json_schema"`
	FinishReasonMap      map[string]string `toml:"finish_reason_map"`
	TranslateMaxToXHigh  *bool             `toml:"translate_max_to_xhigh"`
}

// ModelConfig defines or overrides one model served by an instance. It both
// overlays the embedded model catalog (context window, output cap, reasoning
// capability) and carries per-model wire behavior to the adapter.
type ModelConfig struct {
	ContextWindow   int   `toml:"context_window"`
	MaxOutputTokens int   `toml:"max_output_tokens"`
	Reasoning       *bool `toml:"reasoning"`
	// ThinkingLevels maps serf effort levels (minimal/low/medium/high/xhigh;
	// "max" is accepted as an alias of xhigh) to the wire string the provider
	// wants. When present it is the complete authority: levels absent from the
	// map are unsupported and get clamped away.
	ThinkingLevels map[string]string `toml:"thinking_levels"`
	Compat         *CompatConfig     `toml:"compat"`
}

type Config struct {
	Default   string
	Instances []InstanceConfig
}

// BehaviorTag is the internal behavior identity every provider-conditional
// behavior keys on. It equals the type for all types except openai, which
// splits by apiStyle.
func BehaviorTag(typ, style string) string {
	if typ == "openai" && style == string(StyleChatCompletions) {
		return "openai-compatible"
	}
	return typ
}

// NameToTag maps each instance's name to its behavior tag.
func NameToTag(cfg Config) map[string]string {
	m := make(map[string]string, len(cfg.Instances))
	for _, in := range cfg.Instances {
		m[in.Name] = BehaviorTag(string(in.Type), string(in.APIStyle))
	}
	return m
}
