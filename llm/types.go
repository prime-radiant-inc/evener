package llm

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
	RoleDeveloper Role = "developer"
)

type ContentKind string

const (
	ContentText        ContentKind = "text"
	ContentImage       ContentKind = "image"
	ContentAudio       ContentKind = "audio"
	ContentDocument    ContentKind = "document"
	ContentToolCall    ContentKind = "tool_call"
	ContentToolResult  ContentKind = "tool_result"
	ContentThinking    ContentKind = "thinking"
	ContentRedThinking ContentKind = "redacted_thinking"
	ContentWebSearch   ContentKind = "web_search"
)

type Message struct {
	Role       Role          `json:"role"`
	Content    []ContentPart `json:"content"`
	Name       string        `json:"name,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

func (m Message) Text() string {
	var b strings.Builder
	for _, p := range m.Content {
		if p.Kind == ContentText && p.Text != "" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func System(text string) Message {
	return Message{Role: RoleSystem, Content: []ContentPart{{Kind: ContentText, Text: text}}}
}
func Developer(text string) Message {
	return Message{Role: RoleDeveloper, Content: []ContentPart{{Kind: ContentText, Text: text}}}
}
func User(text string) Message {
	return Message{Role: RoleUser, Content: []ContentPart{{Kind: ContentText, Text: text}}}
}
func Assistant(text string) Message {
	return Message{Role: RoleAssistant, Content: []ContentPart{{Kind: ContentText, Text: text}}}
}

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

type ImageData struct {
	URL       string `json:"url,omitempty"`
	Data      []byte `json:"data,omitempty"` // raw bytes; adapters base64-encode as needed
	MediaType string `json:"media_type,omitempty"`
	Detail    string `json:"detail,omitempty"` // "auto"|"low"|"high"
}

type AudioData struct {
	URL       string `json:"url,omitempty"`
	Data      []byte `json:"data,omitempty"`
	MediaType string `json:"media_type,omitempty"`
}

type DocumentData struct {
	URL       string `json:"url,omitempty"`
	Data      []byte `json:"data,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	FileName  string `json:"file_name,omitempty"`
}

type ToolCallData struct {
	ID              string          `json:"id"`
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

type ToolResultData struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name,omitempty"`
	Content    any    `json:"content"`
	IsError    bool   `json:"is_error"`
	DurationMS int64  `json:"duration_ms,omitempty"`

	ImageData      []byte `json:"image_data,omitempty"`
	ImageMediaType string `json:"image_media_type,omitempty"`
}

type ThinkingData struct {
	Text      string `json:"text"`
	Signature string `json:"signature,omitempty"`
	Redacted  bool   `json:"redacted,omitempty"`
}

type WebSearchData struct {
	Query string          `json:"query,omitempty"`
	Raw   json.RawMessage `json:"raw"`
}

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

type ToolChoice struct {
	Mode string `json:"mode"` // "auto", "none", "required"
	Name string `json:"name,omitempty"`
}

type ResponseFormat struct {
	Type       string         `json:"type"`                  // "text", "json", "json_schema"
	JSONSchema map[string]any `json:"json_schema,omitempty"` // when type=json_schema
	Strict     bool           `json:"strict,omitempty"`
}

type Request struct {
	Model    string    `json:"model"`
	Provider string    `json:"provider,omitempty"`
	Messages []Message `json:"messages"`

	Tools      []ToolDefinition `json:"tools,omitempty"`
	ToolChoice *ToolChoice      `json:"tool_choice,omitempty"`

	ResponseFormat  *ResponseFormat   `json:"response_format,omitempty"`
	Temperature     *float64          `json:"temperature,omitempty"`
	TopP            *float64          `json:"top_p,omitempty"`
	MaxTokens       *int              `json:"max_tokens,omitempty"`
	StopSequences   []string          `json:"stop_sequences,omitempty"`
	ReasoningEffort *string           `json:"reasoning_effort,omitempty"` // low|medium|high|none
	Metadata        map[string]string `json:"metadata,omitempty"`
	ClientMetadata  map[string]string `json:"client_metadata,omitempty"`
	Include         []string          `json:"include,omitempty"`
	PromptCacheKey  string            `json:"prompt_cache_key,omitempty"`
	SessionID       string            `json:"session_id,omitempty"`
	ThreadID        string            `json:"thread_id,omitempty"`

	ProviderOptions map[string]any `json:"provider_options,omitempty"`

	WebSearch bool `json:"web_search,omitempty"`

	AdapterTimeout *AdapterTimeout `json:"adapter_timeout,omitempty"`
}

type FinishReason struct {
	Reason string `json:"reason"`
	Raw    string `json:"raw,omitempty"`
}

// Canonical finish reason values per spec section 3.8.
const (
	FinishReasonStop          = "stop"
	FinishReasonLength        = "length"
	FinishReasonToolCalls     = "tool_calls"
	FinishReasonContentFilter = "content_filter"
	FinishReasonError         = "error"
	FinishReasonOther         = "other"
	FinishReasonPauseTurn     = "pause_turn"
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

type Warning struct {
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

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

func (r Response) Text() string { return r.Message.Text() }

func (r Response) ToolCalls() []ToolCallData {
	var calls []ToolCallData
	for _, p := range r.Message.Content {
		if p.Kind == ContentToolCall && p.ToolCall != nil {
			calls = append(calls, *p.ToolCall)
		}
	}
	return calls
}

func (r Response) ReasoningText() string {
	var b strings.Builder
	for _, p := range r.Message.Content {
		if p.Kind == ContentThinking && p.Thinking != nil && p.Thinking.Text != "" {
			b.WriteString(p.Thinking.Text)
		}
	}
	return b.String()
}

func (req Request) Validate() error {
	if strings.TrimSpace(req.Model) == "" {
		return fmt.Errorf("request.model is required")
	}
	if len(req.Messages) == 0 {
		return fmt.Errorf("request.messages is required")
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
	case "low":
		return 1024
	case "medium":
		return 8192
	case "high":
		return 32768
	case "max":
		return 131072
	default:
		return 0
	}
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
