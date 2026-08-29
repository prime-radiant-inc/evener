// Package registry is the data-driven provider registry: it turns models.dev,
// a curated overlay, and providers.toml into one fully materialized Resolved
// record per instance/model reference. It imports nothing from llm; llm
// imports it. Design: docs/superpowers/specs/2026-08-28-provider-registry-design.md.
package registry

// Protocol identifiers (spec §3). Exactly one Go package implements each.
const (
	ProtocolOpenAIChat      = "openai-chat"
	ProtocolOpenAIResponses = "openai-responses"
	ProtocolAnthropic       = "anthropic"
	ProtocolGoogle          = "google"
)

// Auth schemes (spec §4).
const (
	AuthBearer           = "bearer"
	AuthOptionalBearer   = "optional-bearer"
	AuthHeader           = "header"
	AuthNone             = "none"
	AuthGCPADC           = "gcp-adc"
	AuthOAuthOpenAICodex = "oauth-openai-codex"
)

// Surfaces (spec §3): the agent-facing vendor family a model was trained for.
const (
	SurfaceOpenAI    = "openai"
	SurfaceAnthropic = "anthropic"
	SurfaceGoogle    = "google"
	SurfaceGeneric   = "generic"
)

// EndpointUnsupported is the Transport endpoint value meaning "this endpoint
// does not exist on this transport" (spec §4, §9.1).
const EndpointUnsupported = "-"

// Provider is a named endpoint definition (spec §4). The same struct is used
// for registry records and for user instances.
type Provider struct {
	ID                string            `json:"id,omitempty"`
	Base              string            `json:"base,omitempty"`
	InheritModels     *bool             `json:"inherit_models,omitempty"`
	Implicit          *bool             `json:"implicit,omitempty"`
	Name              string            `json:"name,omitempty"`
	Doc               string            `json:"doc,omitempty"`
	Protocol          string            `json:"protocol,omitempty"`
	Surface           string            `json:"surface,omitempty"`
	Family            string            `json:"family,omitempty"`
	Transport         Transport         `json:"transport"`
	APIKeyEnv         []string          `json:"api_key_env,omitempty"`
	APIKey            string            `json:"api_key,omitempty"`
	Headers           map[string]string `json:"headers,omitempty"`
	CredentialHeaders map[string]string `json:"credential_headers,omitempty"`
	Caps              Caps              `json:"caps"`
	Models            map[string]Model  `json:"models,omitempty"`
	DefaultModel      string            `json:"default_model,omitempty"`
	CheapModel        string            `json:"cheap_model,omitempty"`
	Hidden            bool              `json:"hidden,omitempty"`

	// notes are converter warnings that ride through to Resolved.Warnings
	// ("protocol unverified"). Unexported: not part of the data schema.
	notes []string
}

// Model is one row under a provider (spec §4).
type Model struct {
	ID        string            `json:"id,omitempty"`
	WireID    string            `json:"wire_id,omitempty"`
	AliasOf   string            `json:"alias_of,omitempty"`
	Family    string            `json:"family,omitempty"`
	Protocol  string            `json:"protocol,omitempty"`
	Transport *Transport        `json:"transport,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Surface   string            `json:"surface,omitempty"`
	Caps      Caps              `json:"caps"`
	Status    string            `json:"status,omitempty"`
	Hidden    bool              `json:"hidden,omitempty"`
}

// Transport says how to reach an endpoint (spec §4).
type Transport struct {
	Preset              string            `json:"preset,omitempty"`
	Auth                string            `json:"auth,omitempty"`
	AuthHeader          string            `json:"auth_header,omitempty"`
	BaseURL             string            `json:"base_url,omitempty"`
	HostRule            string            `json:"host_rule,omitempty"`
	Endpoint            string            `json:"endpoint,omitempty"`
	StreamEndpoint      string            `json:"stream_endpoint,omitempty"`
	ModelsEndpoint      string            `json:"models_endpoint,omitempty"`
	CountTokensEndpoint string            `json:"count_tokens_endpoint,omitempty"`
	Vars                map[string]string `json:"vars,omitempty"`
	VarsEnv             map[string]string `json:"vars_env,omitempty"`
	Body                map[string]any    `json:"body,omitempty"`
}

// Caps is the flat capability record shared by every protocol (spec §4.1).
// Pointer fields distinguish "unset" from false/0 at every layer.
type Caps struct {
	// Model facts. This block plus Surface and Family is what an alias row
	// inherits (spec §4.2).
	ContextWindow     *int     `toml:"context_window" json:"context_window,omitempty"`
	MaxOutputTokens   *int     `toml:"max_output_tokens" json:"max_output_tokens,omitempty"`
	Tools             *bool    `toml:"tools" json:"tools,omitempty"`
	StructuredOutput  *bool    `toml:"structured_output" json:"structured_output,omitempty"`
	Sampling          *bool    `toml:"sampling" json:"sampling,omitempty"`
	Reasoning         *bool    `toml:"reasoning" json:"reasoning,omitempty"`
	ReasoningControls []string `toml:"reasoning_controls" json:"reasoning_controls,omitempty"`
	EffortValues      []string `toml:"effort_values" json:"effort_values,omitempty"`
	InputModalities   []string `toml:"input_modalities" json:"input_modalities,omitempty"`
	KnowledgeCutoff   *string  `toml:"knowledge_cutoff" json:"knowledge_cutoff,omitempty"`
	Cost              *Cost    `toml:"cost" json:"cost,omitempty"`

	// Optional wire fields: JSON path → send (spec §8.2). Key-wise merge.
	Fields map[string]bool `toml:"fields" json:"fields,omitempty"`

	// Structural request shaping.
	MaxTokensField     *string           `toml:"max_tokens_field" json:"max_tokens_field,omitempty"`
	ThinkingFormat     *string           `toml:"thinking_format" json:"thinking_format,omitempty"`
	ThinkingShape      *string           `toml:"thinking_shape" json:"thinking_shape,omitempty"`
	ThinkingDisplay    *string           `toml:"thinking_display" json:"thinking_display,omitempty"`
	ThinkingAlwaysOn   *bool             `toml:"thinking_always_on" json:"thinking_always_on,omitempty"`
	ReasoningField     *string           `toml:"reasoning_field" json:"reasoning_field,omitempty"`
	ReasoningSummary   *string           `toml:"reasoning_summary" json:"reasoning_summary,omitempty"`
	ChatTemplateKwargs map[string]any    `toml:"chat_template_kwargs" json:"chat_template_kwargs,omitempty"`
	FinishReasonMap    map[string]string `toml:"finish_reason_map" json:"finish_reason_map,omitempty"`
	CacheControl       *string           `toml:"cache_control" json:"cache_control,omitempty"`
	CacheTTL           *string           `toml:"cache_ttl" json:"cache_ttl,omitempty"`
	StrictTools        *bool             `toml:"strict_tools" json:"strict_tools,omitempty"`
	ToolChoiceForcing  *bool             `toml:"tool_choice_forcing" json:"tool_choice_forcing,omitempty"`
	MaxStopSequences   *int              `toml:"max_stop_sequences" json:"max_stop_sequences,omitempty"`
	ImageDetail        *string           `toml:"image_detail" json:"image_detail,omitempty"`
	ResponsesLite      *bool             `toml:"responses_lite" json:"responses_lite,omitempty"`

	// Message transforms (openai-chat).
	AssistantAfterToolResult *bool `toml:"assistant_after_tool_result" json:"assistant_after_tool_result,omitempty"`
	ThinkingAsText           *bool `toml:"thinking_as_text" json:"thinking_as_text,omitempty"`
	EmptyReasoningContent    *bool `toml:"empty_reasoning_content" json:"empty_reasoning_content,omitempty"`
	StripEmptyContent        *bool `toml:"strip_empty_content" json:"strip_empty_content,omitempty"`
	ToolResultName           *bool `toml:"tool_result_name" json:"tool_result_name,omitempty"`
	ToolStream               *bool `toml:"tool_stream" json:"tool_stream,omitempty"`
	SessionAffinityHeaders   *bool `toml:"session_affinity_headers" json:"session_affinity_headers,omitempty"`

	// Protocol features.
	MultimodalToolResults *bool `toml:"multimodal_tool_results" json:"multimodal_tool_results,omitempty"`
	WebSearch             *bool `toml:"web_search" json:"web_search,omitempty"`
}

// Cost is $ per million tokens (spec §4.1).
type Cost struct {
	Input      float64    `toml:"input" json:"input,omitempty"`
	Output     float64    `toml:"output" json:"output,omitempty"`
	CacheRead  float64    `toml:"cache_read" json:"cache_read,omitempty"`
	CacheWrite float64    `toml:"cache_write" json:"cache_write,omitempty"`
	Tiers      []CostTier `toml:"tiers" json:"tiers,omitempty"`
}

// CostTier is a context-size pricing tier (spec §4.1).
type CostTier struct {
	InputTokensAbove int     `toml:"input_tokens_above" json:"input_tokens_above,omitempty"`
	Input            float64 `toml:"input" json:"input,omitempty"`
	Output           float64 `toml:"output" json:"output,omitempty"`
	CacheRead        float64 `toml:"cache_read" json:"cache_read,omitempty"`
	CacheWrite       float64 `toml:"cache_write" json:"cache_write,omitempty"`
}

// Credential is what resolution found for an instance (spec §4, §10). Value
// is never logged; the continuation scope HMACs it (§7.6).
type Credential struct {
	Value  string `json:"value,omitempty"`
	Source string `json:"source,omitempty"` // api_key | credential_headers | store | env:<VAR> | oauth | adc | none
}

// Resolved is the fully materialized record adapters consume (spec §4.4).
type Resolved struct {
	Instance   string            `json:"instance,omitempty"`
	ProviderID string            `json:"provider_id,omitempty"`
	Protocol   string            `json:"protocol,omitempty"`
	Surface    string            `json:"surface,omitempty"`
	Transport  Transport         `json:"transport"`
	ModelID    string            `json:"model_id,omitempty"`
	WireID     string            `json:"wire_id,omitempty"`
	Model      Model             `json:"model"`
	Caps       Caps              `json:"caps"`
	Headers    map[string]string `json:"headers,omitempty"`
	Credential Credential        `json:"-"`
	Provenance map[string]string `json:"provenance,omitempty"`
	Warnings   []string          `json:"warnings,omitempty"`
}

// Ref names an instance/model pair (spec §7).
type Ref struct {
	Instance string `json:"instance,omitempty"`
	Model    string `json:"model,omitempty"`
}

// String renders the reference in instance/model form.
func (r Ref) String() string { return r.Instance + "/" + r.Model }
