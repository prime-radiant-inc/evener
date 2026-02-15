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

type Usage struct {
	InputTokens      int  `json:"input_tokens"`
	OutputTokens     int  `json:"output_tokens"`
	TotalTokens      int  `json:"total_tokens"`
	ReasoningTokens  *int `json:"reasoning_tokens,omitempty"`
	CacheReadTokens  *int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens *int `json:"cache_write_tokens,omitempty"`

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
	out.CacheReadTokens = addOptInt(u.CacheReadTokens, v.CacheReadTokens)
	out.CacheWriteTokens = addOptInt(u.CacheWriteTokens, v.CacheWriteTokens)
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
