package llm

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Role identifies the author of a message.
type Role string

const (
	// RoleSystem is the "system" message role.
	RoleSystem Role = "system"
	// RoleUser is the "user" message role.
	RoleUser Role = "user"
	// RoleAssistant is the "assistant" message role.
	RoleAssistant Role = "assistant"
	// RoleTool is the "tool" message role, used for tool results.
	RoleTool Role = "tool"
	// RoleDeveloper is the "developer" message role.
	RoleDeveloper Role = "developer"
)

// ContentKind identifies the type of a content part within a message.
type ContentKind string

const (
	// ContentText is a text content part.
	ContentText ContentKind = "text"
	// ContentImage is an image content part.
	ContentImage ContentKind = "image"
	// ContentAudio is an audio content part.
	ContentAudio ContentKind = "audio"
	// ContentDocument is a document content part.
	ContentDocument ContentKind = "document"
	// ContentToolCall is a tool-call content part.
	ContentToolCall ContentKind = "tool_call"
	// ContentToolResult is a tool-result content part.
	ContentToolResult ContentKind = "tool_result"
	// ContentThinking is a thinking (reasoning) content part.
	ContentThinking ContentKind = "thinking"
	// ContentRedThinking is a redacted thinking content part.
	ContentRedThinking ContentKind = "redacted_thinking"
	// ContentWebSearch is a web-search content part.
	ContentWebSearch ContentKind = "web_search"
)

// Message is a single conversation message with a role and one or more content parts.
type Message struct {
	Role       Role          `json:"role"`
	Content    []ContentPart `json:"content"`
	Name       string        `json:"name,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

// Text returns the concatenation of all text content parts in the message.
func (m Message) Text() string {
	var b strings.Builder
	for _, p := range m.Content {
		if p.Kind == ContentText && p.Text != "" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// System returns a system-role message containing the given text.
func System(text string) Message {
	return Message{Role: RoleSystem, Content: []ContentPart{{Kind: ContentText, Text: text}}}
}

// Developer returns a developer-role message containing the given text.
func Developer(text string) Message {
	return Message{Role: RoleDeveloper, Content: []ContentPart{{Kind: ContentText, Text: text}}}
}

// User returns a user-role message containing the given text.
func User(text string) Message {
	return Message{Role: RoleUser, Content: []ContentPart{{Kind: ContentText, Text: text}}}
}

// Assistant returns an assistant-role message containing the given text.
func Assistant(text string) Message {
	return Message{Role: RoleAssistant, Content: []ContentPart{{Kind: ContentText, Text: text}}}
}

// ToolResult returns a tool-role message carrying the result of a tool call
// identified by toolCallID. Set isError to mark the result as an error.
func ToolResult(toolCallID string, content any, isError bool) Message {
	part := ContentPart{
		Kind: ContentToolResult,
		ToolResult: &ToolResultData{
			ToolCallID: toolCallID,
			Content:    content,
			IsError:    isError,
		},
	}
	return Message{Role: RoleTool, ToolCallID: toolCallID, Content: []ContentPart{part}}
}

// ToolResultNamed returns a tool-role message carrying the result of a tool
// call identified by toolCallID, additionally recording the tool's name. Set
// isError to mark the result as an error.
func ToolResultNamed(toolCallID string, name string, content any, isError bool) Message {
	part := ContentPart{
		Kind: ContentToolResult,
		ToolResult: &ToolResultData{
			ToolCallID: toolCallID,
			Name:       name,
			Content:    content,
			IsError:    isError,
		},
	}
	return Message{Role: RoleTool, ToolCallID: toolCallID, Content: []ContentPart{part}}
}

// ContentPart is one part of a message's content. Its Kind selects which of
// the typed payload fields is populated.
type ContentPart struct {
	Kind ContentKind `json:"kind"`

	Text       string          `json:"text,omitempty"`
	Phase      string          `json:"phase,omitempty"` // OpenAI Responses API phase ("commentary", "final_answer")
	Image      *ImageData      `json:"image,omitempty"`
	Audio      *AudioData      `json:"audio,omitempty"`
	Document   *DocumentData   `json:"document,omitempty"`
	ToolCall   *ToolCallData   `json:"tool_call,omitempty"`
	ToolResult *ToolResultData `json:"tool_result,omitempty"`
	Thinking   *ThinkingData   `json:"thinking,omitempty"`
	WebSearch  *WebSearchData  `json:"web_search,omitempty"`
}

// ImageData holds an image content part, supplied either by URL or inline bytes.
type ImageData struct {
	URL       string `json:"url,omitempty"`
	Data      []byte `json:"data,omitempty"` // raw bytes; adapters base64-encode as needed
	MediaType string `json:"media_type,omitempty"`
	Detail    string `json:"detail,omitempty"` // "auto"|"low"|"high"
}

// AudioData holds an audio content part, supplied either by URL or inline bytes.
type AudioData struct {
	URL       string `json:"url,omitempty"`
	Data      []byte `json:"data,omitempty"`
	MediaType string `json:"media_type,omitempty"`
}

// DocumentData holds a document content part, supplied either by URL or inline bytes.
type DocumentData struct {
	URL       string `json:"url,omitempty"`
	Data      []byte `json:"data,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	FileName  string `json:"file_name,omitempty"`
}

// ToolCallData describes a tool call requested by the model.
type ToolCallData struct {
	ID              string          `json:"id"`
	ItemID          string          `json:"item_id,omitempty"`
	Name            string          `json:"name"`
	Arguments       json.RawMessage `json:"arguments,omitempty"`        // raw JSON object
	ParsedArguments map[string]any  `json:"parsed_arguments,omitempty"` // populated by Parse()
	Type            string          `json:"type,omitempty"`             // usually "function"
	// ThoughtSignature carries provider-specific thought-signature state (e.g., Gemini)
	// required to continue tool-calling turns safely.
	ThoughtSignature string `json:"thought_signature,omitempty"`
}

// Parse unmarshals Arguments into ParsedArguments. If Arguments is nil or empty,
// ParsedArguments is set to an empty map.
func (tc *ToolCallData) Parse() error {
	if len(tc.Arguments) == 0 {
		tc.ParsedArguments = map[string]any{}
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(tc.Arguments, &m); err != nil {
		return err
	}
	tc.ParsedArguments = m
	return nil
}

// ToolResultData describes the result of a tool call, linked back to it by ToolCallID.
type ToolResultData struct {
	ToolCallID string          `json:"tool_call_id"`
	Name       string          `json:"name,omitempty"`
	Content    any             `json:"content"`
	IsError    bool            `json:"is_error"`
	DurationMS int64           `json:"duration_ms,omitempty"`
	ToolState  json.RawMessage `json:"tool_state,omitempty"`

	ImageData      []byte `json:"image_data,omitempty"`
	ImageMediaType string `json:"image_media_type,omitempty"`
}

// ThinkingData holds a model reasoning (thinking) content part.
type ThinkingData struct {
	ID               string   `json:"id,omitempty"`
	Text             string   `json:"text"`
	Signature        string   `json:"signature,omitempty"`
	Redacted         bool     `json:"redacted,omitempty"`
	EncryptedContent string   `json:"encrypted_content,omitempty"`
	Summary          []string `json:"summary,omitempty"`
}

// WebSearchData holds a web-search content part, including the query and the raw provider payload.
type WebSearchData struct {
	Query string          `json:"query,omitempty"`
	Raw   json.RawMessage `json:"raw"`
}

// ToolDefinition describes a tool the model may call, including its JSON Schema parameters.
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"` // JSON Schema (root object)
	// Strict controls provider-side strict schema enforcement when supported.
	// Nil means use the adapter default. For OpenAI Responses, explicit false
	// opts the tool out of strict mode instead of allowing the API to
	// normalize the schema into strict mode automatically.
	Strict *bool `json:"strict,omitempty"`
}

// ToolChoice controls whether and which tool the model is allowed or required to call.
type ToolChoice struct {
	Mode string `json:"mode"` // "auto", "none", "required"
	Name string `json:"name,omitempty"`
}

// ResponseFormat constrains the format of the model's response (plain text, JSON, or a JSON schema).
type ResponseFormat struct {
	Type       string         `json:"type"`                  // "text", "json", "json_schema"
	JSONSchema map[string]any `json:"json_schema,omitempty"` // when type=json_schema
	Strict     bool           `json:"strict,omitempty"`
}

// Request is a provider-agnostic chat-completion request.
type Request struct {
	Model    string    `json:"model"`
	Provider string    `json:"provider,omitempty"`
	Messages []Message `json:"messages"`

	Tools      []ToolDefinition `json:"tools,omitempty"`
	ToolChoice *ToolChoice      `json:"tool_choice,omitempty"`

	ResponseFormat       *ResponseFormat   `json:"response_format,omitempty"`
	Temperature          *float64          `json:"temperature,omitempty"`
	TopP                 *float64          `json:"top_p,omitempty"`
	MaxTokens            *int              `json:"max_tokens,omitempty"`
	StopSequences        []string          `json:"stop_sequences,omitempty"`
	ReasoningEffort      *string           `json:"reasoning_effort,omitempty"` // low|medium|high|none
	Metadata             map[string]string `json:"metadata,omitempty"`
	ClientMetadata       map[string]string `json:"client_metadata,omitempty"`
	Include              []string          `json:"include,omitempty"`
	PromptCacheKey       string            `json:"prompt_cache_key,omitempty"`
	PreviousResponseID   string            `json:"previous_response_id,omitempty"`
	ConversationID       string            `json:"conversation_id,omitempty"`
	ServiceTier          string            `json:"service_tier,omitempty"`
	SafetyIdentifier     string            `json:"safety_identifier,omitempty"`
	PromptCacheRetention string            `json:"prompt_cache_retention,omitempty"`
	Truncation           string            `json:"truncation,omitempty"`
	MaxToolCalls         *int              `json:"max_tool_calls,omitempty"`
	Background           *bool             `json:"background,omitempty"`
	Store                *bool             `json:"store,omitempty"`
	SessionID            string            `json:"session_id,omitempty"`
	ThreadID             string            `json:"thread_id,omitempty"`

	ProviderOptions map[string]any `json:"provider_options,omitempty"`

	WebSearch bool `json:"web_search,omitempty"`

	HistoryMode                    HistoryMode           `json:"-"`
	Continuation                   *ContinuationMetadata `json:"-"`
	FullHistoryFallbackMessages    []Message             `json:"-"`
	InputTokensEstimate            int                   `json:"-"`
	FullHistoryInputTokensEstimate int                   `json:"-"`
	ContinuationDiagnostic         string                `json:"-"`

	AdapterTimeout *AdapterTimeout `json:"adapter_timeout,omitempty"`
}

// FinishReason holds a canonical finish reason alongside the raw provider value.
type FinishReason struct {
	Reason string `json:"reason"`
	Raw    string `json:"raw,omitempty"`
}

// Canonical finish reason values per spec section 3.8.
const (
	// FinishReasonStop indicates the model stopped naturally or hit a stop sequence.
	FinishReasonStop = "stop"
	// FinishReasonLength indicates the model stopped at the max-token limit.
	FinishReasonLength = "length"
	// FinishReasonToolCalls indicates the model stopped to make tool calls.
	FinishReasonToolCalls = "tool_calls"
	// FinishReasonContentFilter indicates generation was halted by a content filter.
	FinishReasonContentFilter = "content_filter"
	// FinishReasonError indicates generation ended due to an error.
	FinishReasonError = "error"
	// FinishReasonOther indicates a finish reason that maps to none of the canonical values.
	FinishReasonOther = "other"
	// FinishReasonPauseTurn indicates the turn was paused and may be continued.
	FinishReasonPauseTurn = "pause_turn"
)

// NormalizeFinishReason maps a provider-specific finish reason to a canonical value.
// The raw value is always preserved in .Raw.
func NormalizeFinishReason(provider, raw string) FinishReason {
	if raw == "" {
		return FinishReason{Reason: FinishReasonStop}
	}
	reason := normalizeFinish(provider, raw)
	return FinishReason{Reason: reason, Raw: raw}
}

func normalizeFinish(provider, raw string) string {
	switch provider {
	case "anthropic":
		switch raw {
		case "end_turn", "stop_sequence":
			return FinishReasonStop
		case "max_tokens":
			return FinishReasonLength
		case "tool_use":
			return FinishReasonToolCalls
		case "pause_turn":
			return FinishReasonPauseTurn
		}
	case "google":
		switch raw {
		case "STOP":
			return FinishReasonStop
		case "MAX_TOKENS":
			return FinishReasonLength
		case "SAFETY", "RECITATION":
			return FinishReasonContentFilter
		}
	default:
		// OpenAI already uses canonical values.
		switch raw {
		case "stop", "length", "tool_calls", "content_filter":
			return raw
		}
	}
	return FinishReasonOther
}

// Usage is the normalized token-usage report returned by every adapter.
//
// InputTokens invariant: "new input tokens billed at the full input rate,"
// i.e. tokens that are neither cache reads nor cache-creation writes.
// Adapters are responsible for subtracting cached token counts from their
// raw provider response so this field has the same meaning regardless of
// provider. Callers never need to know which provider reported a call in
// order to compute cost.
//
// CacheReadTokens counts input tokens served from the provider's prompt
// cache. CacheWriteTokens counts input tokens written to the 5-minute
// TTL cache (Anthropic only — OpenAI has no separate write cost).
// CacheWrite1hTokens counts tokens written to the 1-hour TTL cache; only
// populated when the caller opts into extended cache via the Anthropic
// beta header. All three are nil when the provider doesn't report them.
//
// ReasoningTokens is nil unless the provider natively reports a count.
// When set, callers should treat it as metadata only — it is NOT added
// to OutputTokens for billing, because every supported provider already
// bills thinking tokens via OutputTokens at the output rate.
//
// ReasoningTokensEstimated is populated when the adapter can derive a
// rough thinking-token count from response content (e.g. char count of
// thinking blocks divided by 4), used only when the provider doesn't
// supply a native count. Informational signal only; never enters the
// billing path. Display it separately from ReasoningTokens with a clear
// "estimated" marker.
//
// TotalTokens = InputTokens + OutputTokens + optional cache reads/writes.
type Usage struct {
	InputTokens              int  `json:"input_tokens"`
	OutputTokens             int  `json:"output_tokens"`
	TotalTokens              int  `json:"total_tokens"`
	ReasoningTokens          *int `json:"reasoning_tokens,omitempty"`
	ReasoningTokensEstimated *int `json:"reasoning_tokens_estimated,omitempty"`
	CacheReadTokens          *int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens         *int `json:"cache_write_tokens,omitempty"`
	CacheWrite1hTokens       *int `json:"cache_write_1h_tokens,omitempty"`

	Raw map[string]any `json:"raw,omitempty"`
}

// Add returns the field-wise sum of u and v. Optional pointer fields are summed
// via addOptInt, and the result's Raw is reset to an empty map.
func (u Usage) Add(v Usage) Usage {
	out := Usage{
		InputTokens:  u.InputTokens + v.InputTokens,
		OutputTokens: u.OutputTokens + v.OutputTokens,
		TotalTokens:  u.TotalTokens + v.TotalTokens,
		Raw:          map[string]any{},
	}
	out.ReasoningTokens = addOptInt(u.ReasoningTokens, v.ReasoningTokens)
	out.ReasoningTokensEstimated = addOptInt(u.ReasoningTokensEstimated, v.ReasoningTokensEstimated)
	out.CacheReadTokens = addOptInt(u.CacheReadTokens, v.CacheReadTokens)
	out.CacheWriteTokens = addOptInt(u.CacheWriteTokens, v.CacheWriteTokens)
	out.CacheWrite1hTokens = addOptInt(u.CacheWrite1hTokens, v.CacheWrite1hTokens)
	return out
}

func addOptInt(a, b *int) *int {
	if a == nil && b == nil {
		return nil
	}
	av := 0
	bv := 0
	if a != nil {
		av = *a
	}
	if b != nil {
		bv = *b
	}
	sum := av + bv
	return &sum
}

// Warning is a non-fatal advisory message returned alongside a response.
type Warning struct {
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

// RateLimitInfo reports provider rate-limit state parsed from response headers.
type RateLimitInfo struct {
	RequestsRemaining *int       `json:"requests_remaining,omitempty"`
	RequestsLimit     *int       `json:"requests_limit,omitempty"`
	TokensRemaining   *int       `json:"tokens_remaining,omitempty"`
	TokensLimit       *int       `json:"tokens_limit,omitempty"`
	ResetAt           *time.Time `json:"reset_at,omitempty"`
}

// AdapterTimeout defines granular timeout configuration for adapter-level HTTP operations.
type AdapterTimeout struct {
	Connect    time.Duration `json:"connect"`     // time to establish HTTP connection (default: 10s)
	Request    time.Duration `json:"request"`     // time for entire request/response cycle (default: 120s)
	StreamRead time.Duration `json:"stream_read"` // max time between consecutive stream events (default: 30s)
}

// DefaultAdapterTimeout returns the spec-recommended defaults.
func DefaultAdapterTimeout() AdapterTimeout {
	return AdapterTimeout{
		Connect:    10 * time.Second,
		Request:    120 * time.Second,
		StreamRead: 30 * time.Second,
	}
}

// Response is a provider-agnostic chat-completion response.
type Response struct {
	ID        string         `json:"id"`
	Model     string         `json:"model"`
	Provider  string         `json:"provider"`
	Message   Message        `json:"message"`
	Finish    FinishReason   `json:"finish_reason"`
	Usage     Usage          `json:"usage"`
	Raw       map[string]any `json:"raw,omitempty"`
	Warnings  []Warning      `json:"warnings,omitempty"`
	RateLimit *RateLimitInfo `json:"rate_limit,omitempty"`

	// Raw HTTP bodies for debugging. Populated only when SERF_LOG_RAW_HTTP=1.
	// Excluded from JSON serialization to avoid bloating api.jsonl.
	RawRequestBody  string `json:"-"`
	RawResponseBody string `json:"-"`
}

// Text returns the concatenated text content of the response message.
func (r Response) Text() string { return r.Message.Text() }

// ToolCalls returns the tool calls present in the response message.
func (r Response) ToolCalls() []ToolCallData {
	var calls []ToolCallData
	for _, p := range r.Message.Content {
		if p.Kind == ContentToolCall && p.ToolCall != nil {
			calls = append(calls, *p.ToolCall)
		}
	}
	return calls
}

// ReasoningText returns the response's thinking content, distinct thinking blocks
// joined by a blank line — matching the section break the streaming path emits
// between blocks, so the assembled/reloaded reasoning reads the same as the live
// view.
func (r Response) ReasoningText() string {
	var b strings.Builder
	for _, p := range r.Message.Content {
		if p.Kind == ContentThinking && p.Thinking != nil && p.Thinking.Text != "" {
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(p.Thinking.Text)
		}
	}
	return b.String()
}

// Validate checks that the request has a model and messages and that every tool
// has a valid name and parameter schema, returning the first error found.
func (req Request) Validate() error {
	if strings.TrimSpace(req.Model) == "" {
		return errors.New("request.model is required")
	}
	if len(req.Messages) == 0 {
		return errors.New("request.messages is required")
	}
	for _, t := range req.Tools {
		if err := ValidateToolName(t.Name); err != nil {
			return err
		}
		if err := validateToolParameters(t.Parameters); err != nil {
			return err
		}
	}
	return nil
}

// ReasoningBudget converts a reasoning effort level to a token budget.
// Returns 0 for unrecognized values.
func ReasoningBudget(effort string) int {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "minimal":
		return 512
	case "low":
		return 1024
	case "medium":
		return 8192
	case "high":
		return 32768
	case "xhigh", "max":
		return 131072
	default:
		return 0
	}
}

// effortRank orders reasoning-effort levels from least to most. "xhigh" and
// "max" are the same top tier under different vendor names (OpenRouter says
// "xhigh"; serf and Anthropic say "max"), so they share a rank.
var effortRank = map[string]int{
	"minimal": 1,
	"low":     2,
	"medium":  3,
	"high":    4,
	"xhigh":   5,
	"max":     5,
}

// NormalizeReasoningEffort lowercases and trims a reasoning-effort value and maps
// the "disable" aliases (none/null/off/false/0) to "" (no effort). It does not
// validate the level — unknown non-empty values pass through lowercased. This is
// the single place the disable-aliases are defined, shared by the CLI resolver
// and the runtime setter so they cannot drift.
func NormalizeReasoningEffort(s string) string {
	v := strings.ToLower(strings.TrimSpace(s))
	switch v {
	case "none", "null", "off", "false", "0":
		return ""
	default:
		return v
	}
}

// ReasoningEffortRank returns the ordinal rank of a reasoning-effort level
// (minimal=1 … xhigh=max=5), or 0 for empty/unknown values. Use it instead of a
// local hierarchy so effort comparisons (e.g. a "floor at high" side-channel)
// honor the full vocabulary and cannot drift as levels are added.
func ReasoningEffortRank(effort string) int {
	return effortRank[strings.ToLower(strings.TrimSpace(effort))]
}

// ClampReasoningEffort clamps a requested effort to the levels a model supports.
// A request above the model's top supported level is lowered to that level; a
// request below the model's lowest is raised to it. Empty, "none", unknown
// vocabulary, or an empty supported set pass through unchanged so the provider
// can decide. This is the single guard that keeps loop-detector escalation, the
// --reasoning-effort flag, and the UI selector from sending a level a model
// rejects (e.g. "xhigh" to a model that only supports minimal/low/medium/high).
func ClampReasoningEffort(requested string, supportedLevels []string) string {
	req := strings.ToLower(strings.TrimSpace(requested))
	if req == "" || req == "none" || len(supportedLevels) == 0 {
		return requested
	}
	reqRank, ok := effortRank[req]
	if !ok {
		return requested
	}
	best, bestRank := "", -1
	lowest, lowestRank := "", 1<<30
	for _, lvl := range supportedLevels {
		l := strings.ToLower(strings.TrimSpace(lvl))
		r, ok := effortRank[l]
		if !ok {
			continue
		}
		if l == req {
			return l
		}
		if r < lowestRank {
			lowest, lowestRank = l, r
		}
		if r <= reqRank && r > bestRank {
			best, bestRank = l, r
		}
	}
	if best != "" {
		return best
	}
	if lowest != "" {
		return lowest
	}
	return requested
}

// IntFromAny extracts an integer from a JSON-decoded value.
// Handles json.Number, float64, and int; returns 0 for other types.
func IntFromAny(v any) int {
	switch x := v.(type) {
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	case float64:
		return int(x)
	case int:
		return x
	default:
		return 0
	}
}
