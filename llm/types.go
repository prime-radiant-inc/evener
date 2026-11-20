package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"primeradiant.com/evener/llm/effort"
	"primeradiant.com/evener/llm/registry"
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

	// Machinery marks a text part as session-manufactured machinery — a
	// notification for the model the user never typed — rather than
	// user-facing prose. Producers set it at construction; consumers that
	// build user-facing text (apptranscript.UserFacingText) rely on it
	// exclusively instead of matching the part's text shape.
	Machinery bool `json:"machinery,omitempty"`
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
	// RawArguments preserves the model's original argument bytes when they
	// were not valid JSON and Arguments was replaced with the replay-safe {}
	// form. It is a diagnostic record — the durable transcript keeps what the
	// model actually sent — and never feeds provider requests.
	RawArguments string `json:"raw_arguments,omitempty"`
	// ArgumentsSpelling keeps the model's own bytes when compacted Arguments differ.
	ArgumentsSpelling Spelling `json:"arguments_spelling,omitempty"`
	// ThoughtSignature carries provider-specific thought-signature state (e.g., Gemini)
	// required to continue tool-calling turns safely.
	ThoughtSignature string `json:"thought_signature,omitempty"`
}

// Spelling is a tool call's recorded bytes; a non-string value decodes to empty, not an error.
type Spelling string

// UnmarshalJSON leaves the field empty for a value that is not a JSON string.
func (s *Spelling) UnmarshalJSON(raw []byte) error { _ = json.Unmarshal(raw, (*string)(s)); return nil }

// NormalizeToolArguments canonicalizes as session_tools.go and keeps the model's bytes; empty stays empty, not "null".
func NormalizeToolArguments(raw []byte) (json.RawMessage, Spelling) {
	canonical, err := json.Marshal(json.RawMessage(raw)) // An error means raw is not JSON.
	if len(raw) == 0 || err != nil || bytes.Equal(canonical, raw) {
		return append(json.RawMessage(nil), raw...), ""
	}
	return canonical, Spelling(raw)
}

// WireArguments returns the spelling while it canonicalizes to Arguments, else Arguments.
func (tc *ToolCallData) WireArguments() string {
	if tc == nil {
		return ""
	}
	if s := tc.ArgumentsSpelling; s != "" { // Arguments holds canonical bytes.
		if canonical, _ := NormalizeToolArguments([]byte(s)); bytes.Equal(canonical, tc.Arguments) {
			return string(s)
		}
	}
	return string(tc.Arguments)
}

// SentArguments returns the argument bytes the model actually sent: the
// preserved raw bytes for a call whose arguments were not valid JSON
// (Arguments holds the replay-safe {} placeholder there), else the recorded
// arguments. Byte-faithful in both branches; callers wanting tidy edges trim
// presentation-side.
func (tc *ToolCallData) SentArguments() string {
	if tc.RawArguments != "" {
		return tc.RawArguments
	}
	return string(tc.Arguments)
}

// SentArgumentsBytes returns the argument bytes the model actually sent as a
// []byte, avoiding the string copy SentArguments performs in the common case.
// Same precedence and byte-faithful content as SentArguments: RawArguments
// wins when set (copying the string), else the recorded Arguments are returned
// directly (no copy). Callers that immediately feed the result to a []byte
// consumer — json.RawMessage, a hash, json.Unmarshal — use this to elide the
// []byte(SentArguments()) round trip; callers that need a string use
// SentArguments. The returned slice aliases tc.Arguments when RawArguments is
// empty, so callers must not mutate it.
func (tc *ToolCallData) SentArgumentsBytes() []byte {
	if tc.RawArguments != "" {
		return []byte(tc.RawArguments)
	}
	return tc.Arguments
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
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name,omitempty"`
	Content    any    `json:"content"`
	IsError    bool   `json:"is_error"`
	// PrevalOnly is true when IsError came from a pre-dispatch rejection (an
	// unknown tool name, or arguments that still failed schema validation
	// after repair) rather than the tool's own execution never having run at
	// all. Persisted so a reload sees the same distinction a live session
	// did. Meaningless when IsError is false.
	PrevalOnly bool            `json:"preval_only,omitempty"`
	DurationMS int64           `json:"duration_ms,omitempty"`
	ToolState  json.RawMessage `json:"tool_state,omitempty"`

	ImageData      []byte `json:"image_data,omitempty"`
	ImageMediaType string `json:"image_media_type,omitempty"`
}

// ThinkingData holds a model reasoning (thinking) content part.
type ThinkingData struct {
	ID   string `json:"id,omitempty"`
	Text string `json:"text"`
	// Signature is provider-scoped replay metadata: Anthropic stores its
	// cryptographic thinking signature; OpenAI-compatible providers store the
	// wire field the reasoning arrived on (see OpenAICompatReasoningFields).
	// Providers must guard against replaying a foreign provider's value.
	Signature        string   `json:"signature,omitempty"`
	Redacted         bool     `json:"redacted,omitempty"`
	EncryptedContent string   `json:"encrypted_content,omitempty"`
	Summary          []string `json:"summary,omitempty"`
	// GoogleInteraction marks a Gemini Interactions thought step. A step that
	// carries only a Signature is replay metadata with nothing to display.
	GoogleInteraction bool `json:"google_interaction,omitempty"`
	// Chat Completions scalar replay when Text includes structured reasoning.
	ScalarReasoningField string `json:"scalar_reasoning_field,omitempty"`
	ScalarReasoningText  string `json:"scalar_reasoning_text,omitempty"`

	// Responses native parts retain their order, type and exact text beside the projections.
	ResponsesContentParts []ResponsesReasoningPart `json:"responses_content_parts,omitempty"`
	ResponsesSummaryParts []ResponsesReasoningPart `json:"responses_summary_parts,omitempty"`
	// ResponsesIncomplete marks a reasoning item the provider cut off.
	ResponsesIncomplete bool `json:"responses_incomplete,omitempty"`
}

// ResponsesReasoningPart is one typed text part of a Responses reasoning item.
type ResponsesReasoningPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ReplayableResponsesPlaintext reports a finished Responses item with native
// typed parts. A bare Text projection is display-only and never replays.
func (t *ThinkingData) ReplayableResponsesPlaintext() bool {
	return t != nil && !t.ResponsesIncomplete && !t.Redacted && t.EncryptedContent == "" &&
		(len(t.ResponsesContentParts) > 0 || len(t.ResponsesSummaryParts) > 0)
}

// ChatReplayText selects the text for the Chat Completions replay shape.
func (t *ThinkingData) ChatReplayText(scalar bool) string {
	if scalar && t.ScalarReasoningText != "" && IsOpenAICompatReasoningField(t.ScalarReasoningField) {
		return t.ScalarReasoningText
	}
	return t.Text
}

// openAICompatReasoningFields is the canonical ordered list of wire fields
// OpenAI-compatible providers use for assistant reasoning. Order matters:
// parsers take the first non-empty variant per chunk.
var openAICompatReasoningFields = []string{"reasoning_content", "reasoning", "reasoning_text"}

// OpenAICompatReasoningFields returns the wire field names OpenAI-compatible
// providers use for assistant reasoning. ThinkingData.Signature carries one of
// these when thinking arrived from such a provider, so replay can route it
// back to the same field.
func OpenAICompatReasoningFields() []string {
	return append([]string(nil), openAICompatReasoningFields...)
}

// IsOpenAICompatReasoningField reports whether sig names one of the
// OpenAI-compatible reasoning wire fields. Providers whose thinking signatures
// are cryptographic (Anthropic) use this to treat such a value as "no
// signature" instead of replaying it as a signature the API would reject.
func IsOpenAICompatReasoningField(sig string) bool {
	return slices.Contains(openAICompatReasoningFields, sig)
}

// IsOpenAICompatEncryptedReasoning reports whether s is the JSON array of
// {"type":"reasoning.encrypted", ...} items the openai-compat adapter stores
// in ThinkingData.EncryptedContent for OpenRouter-style encrypted reasoning.
// Providers whose EncryptedContent is an opaque blob (OpenAI Responses) use
// this to skip replaying a foreign provider's value on a cross-provider
// transcript.
func IsOpenAICompatEncryptedReasoning(s string) bool {
	if len(s) == 0 || s[0] != '[' {
		return false
	}
	var items []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(s), &items); err != nil || len(items) == 0 {
		return false
	}
	for _, it := range items {
		// The array carries every continuation-bearing detail item —
		// encrypted blocks and signature-bearing reasoning.text — so any
		// "reasoning."-typed item marks the value as ours. An OpenAI
		// Responses opaque blob is not a JSON array of typed items at all.
		if !strings.HasPrefix(it.Type, "reasoning.") {
			return false
		}
	}
	return true
}

// OpenAICompatSignatureReasoningNeedsText reports whether the opaque
// reasoning_details array carries a signature-bearing reasoning.text item
// whose text field is empty. The chat builder fills that field from the part's
// ThinkingData.Text, so the text is signed continuation state, not display
// reasoning, and must survive a replay trim. A part whose array has no such
// item carries display text only.
func OpenAICompatSignatureReasoningNeedsText(s string) bool {
	if !IsOpenAICompatEncryptedReasoning(s) {
		return false
	}
	var items []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(s), &items) != nil {
		return false
	}
	for _, it := range items {
		if it.Type == "reasoning.text" && it.Text == "" {
			return true
		}
	}
	return false
}

// TrimThinkingForReplay applies a target's thinking-replay policy to one
// content part. When dropPlain is true, plain reasoning is dropped; an
// encrypted reasoning_details array is kept because the chat adapter
// round-trips it, with its display text cleared unless a signature-bearing
// reasoning.text item needs that text merged into it. The returned part is the
// input unchanged when nothing applies, and dropped reports whether the part
// must be omitted.
func TrimThinkingForReplay(p ContentPart, dropPlain bool) (trimmed ContentPart, dropped bool) {
	if p.Kind != ContentThinking && p.Kind != ContentRedThinking {
		return p, false
	}
	if !dropPlain {
		return p, false
	}
	if p.Kind == ContentRedThinking || p.Thinking == nil {
		return p, true
	}
	if !IsOpenAICompatEncryptedReasoning(p.Thinking.EncryptedContent) {
		return p, true
	}
	if p.Thinking.Text == "" || OpenAICompatSignatureReasoningNeedsText(p.Thinking.EncryptedContent) {
		return p, false
	}
	display := *p.Thinking
	display.Text = ""
	p.Thinking = &display
	return p, false
}

// TrimsThinkingForReplay reports whether TrimThinkingForReplayMessage would
// change msg under the same dropPlain policy. It is the one test both the trim
// and a caller deciding whether to work on a message list apply, so a caller
// that skips unchanged messages and the trim that would rewrite them cannot
// disagree: a rule added to the trim is a rule added here, because there is
// only one scan.
func TrimsThinkingForReplay(msg Message, dropPlain bool) bool {
	if !dropPlain {
		return false
	}
	for _, p := range msg.Content {
		if p.Kind != ContentThinking && p.Kind != ContentRedThinking {
			continue
		}
		if np, dropped := TrimThinkingForReplay(p, true); dropped || np.Thinking != p.Thinking {
			return true
		}
	}
	return false
}

// TrimThinkingForReplayMessage maps TrimThinkingForReplay over a message,
// returning the input unchanged when nothing has to change.
func TrimThinkingForReplayMessage(msg Message, dropPlain bool) Message {
	if !TrimsThinkingForReplay(msg, dropPlain) {
		return msg
	}
	out := make([]ContentPart, 0, len(msg.Content))
	for _, p := range msg.Content {
		np, dropped := TrimThinkingForReplay(p, true)
		if dropped {
			continue
		}
		out = append(out, np)
	}
	msg.Content = out
	return msg
}

// StripOpaqueThinkingForCrossProvider returns the thinking part an
// openai-responses target may take from a foreign turn: the blob and a
// cryptographic signature come off. It answers ReplayableResponsesPlaintext,
// the rule reasoningItem applies, and dropped reports the part must go.
func StripOpaqueThinkingForCrossProvider(p ContentPart) (ContentPart, bool) {
	if p.Kind != ContentThinking || p.Thinking == nil || p.Thinking.Redacted {
		return p, true
	}
	kept := *p.Thinking
	kept.EncryptedContent = ""
	if !IsOpenAICompatReasoningField(kept.Signature) {
		kept.Signature = ""
	}
	if !kept.ReplayableResponsesPlaintext() {
		return p, true
	}
	if kept.EncryptedContent == p.Thinking.EncryptedContent && kept.Signature == p.Thinking.Signature {
		return p, false
	}
	p.Thinking = &kept
	return p, false
}

// WebSearchSource is one citation source of a web-search content part.
type WebSearchSource struct {
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`
}

// WebSearchData holds a web-search content part, including the query, its
// citation sources, and the raw provider payload.
type WebSearchData struct {
	Query   string            `json:"query,omitempty"`
	Sources []WebSearchSource `json:"sources,omitempty"`
	Raw     json.RawMessage   `json:"raw"`
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

	// declaredNoParameters records that the caller declared this tool with no
	// parameters at all, as distinct from declaring the open empty object.
	// prepareTools fills Parameters with the open schema either way, because
	// that is the schema the adapters put on the wire and the budget has to
	// price it. The distinction still decides strict projection: only a tool
	// that declares no arguments can be sent in strict mode, which admits
	// exactly the empty argument set. An open object accepts arbitrary keys and
	// cannot be closed without changing what the caller declared.
	//
	// The field is not authoritative on its own: DeclaresNoParameters also
	// requires Parameters to still be the schema preparation synthesized.
	// Middleware can replace a tool's parameters after preparation, and a flag
	// that outlived its value would have the builder discard the replacement and
	// send an empty object schema in its place.
	declaredNoParameters bool
}

// DeclaresNoParameters reports whether the caller declared this tool without a
// parameter schema, as opposed to declaring an open one, and whether that is
// still what Parameters holds.
//
// An adapter reads this instead of testing Parameters for nil, because a
// prepared definition carries the open schema there: the wire and the token
// budget both need it present, and the declaration still has to be recoverable
// for the strict decision.
//
// The declaration only describes the value preparation put there. A replacement
// schema arrives through request middleware, and it has an advertised meaning of
// its own: discarding it for a synthesized empty object would send the provider a
// schema the caller never wrote. Such a definition is therefore not a
// no-parameters declaration any more, and goes through ordinary emission and
// eligibility.
func (t ToolDefinition) DeclaresNoParameters() bool {
	return t.declaredNoParameters && isEmptyObjectSchema(t.Parameters)
}

// isEmptyObjectSchema reports whether schema is exactly the open empty object the
// parameterless fallback synthesizes. It is spelled out rather than compared by
// length so a schema that happens to be empty is not mistaken for it: a
// replacement carrying no keys is still the caller's own value.
func isEmptyObjectSchema(schema map[string]any) bool {
	if len(schema) != 2 {
		return false
	}
	if typ, _ := schema["type"].(string); typ != "object" {
		return false
	}
	props, ok := schema["properties"].(map[string]any)
	return ok && len(props) == 0
}

// WithNoParametersDeclared returns t marked as declaring no parameter schema,
// filling nil Parameters with defaultToolParameters so local validation and
// open-schema wire pricing see the empty object schema while strict-capable
// adapters can still distinguish a parameterless declaration from an explicit
// open schema. prepareTools applies this to every tool declared with nil
// Parameters.
func (t ToolDefinition) WithNoParametersDeclared() ToolDefinition {
	if t.Parameters == nil {
		t.Parameters = defaultToolParameters()
	}
	t.declaredNoParameters = true
	return t
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
	ReasoningEffort      *string           `json:"reasoning_effort,omitempty"` // minimal|low|medium|high|xhigh|max|none
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
	InputTokensEstimate            int                   `json:"-"`
	FullHistoryInputTokensEstimate int                   `json:"-"`
	ContinuationDiagnostic         string                `json:"-"`
	// ToolsJSONBytes is the serialized byte length of Tools, stamped by a
	// caller that measured the list once with ToolsJSONBytes. The local
	// estimator bills the tool list by that length and re-marshals only when
	// this is zero, so a stamped request prices its tools without
	// serializing them (#6406). Zero means unstamped. A stamped request must
	// treat Tools and every element's Parameters as immutable: the stamp
	// describes the bytes at stamping time, so a schema edited in place would
	// drift the estimate silently. Rebuild the list and re-measure instead.
	ToolsJSONBytes int `json:"-"`

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
		case "refusal":
			// Claude 5+ models stop with "refusal" (plus a stop_details object)
			// when they decline to continue; the closest canonical value is
			// content_filter, with the raw value preserved alongside.
			return FinishReasonContentFilter
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

// WarningCodeImageColorUnmanaged marks successful image recovery without color management.
const WarningCodeImageColorUnmanaged = "image_color_unmanaged"

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
	Connect    time.Duration `json:"connect"`     // time to establish the network connection (default: 10s)
	Request    time.Duration `json:"request"`     // optional whole-attempt deadline, including body lifetime (default: disabled)
	StreamRead time.Duration `json:"stream_read"` // max idle time between incoming response bytes, streaming or not (default: 10m)
}

// DefaultAdapterTimeout returns bounded connection and response-idle defaults, without a total deadline.
func DefaultAdapterTimeout() AdapterTimeout {
	return AdapterTimeout{
		Connect:    10 * time.Second,
		Request:    0,
		StreamRead: 10 * time.Minute,
	}
}

// Response is a provider-agnostic chat-completion response.
type Response struct {
	// FallbackContent holds provider reasoning that accompanies usable output reconstructed from stream events.
	FallbackContent []ContentPart `json:"-"`

	ID        string         `json:"id"`
	Model     string         `json:"model"`
	Provider  string         `json:"provider"`
	Message   Message        `json:"message"`
	Finish    FinishReason   `json:"finish_reason"`
	Usage     Usage          `json:"usage"`
	Raw       map[string]any `json:"raw,omitempty"`
	Warnings  []Warning      `json:"warnings,omitempty"`
	RateLimit *RateLimitInfo `json:"rate_limit,omitempty"`
	// OutputSettled tells stream consumers to keep the supplied output even when empty.
	OutputSettled bool `json:"-"`
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

// MinimumThinkingBudgetTokens is the smallest thinking budget Anthropic
// documents accepting; a lower value is wire-rejectable on budget-shaped
// rows (#714).
const MinimumThinkingBudgetTokens = 1024

// ReasoningBudget converts a reasoning effort level to a token budget.
// Returns 0 for unrecognized values.
func ReasoningBudget(effort string) int {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "minimal", "low":
		return MinimumThinkingBudgetTokens
	case "medium":
		return 8192
	case "high":
		return 32768
	case "xhigh", "max":
		// max ranks above xhigh as an effort level, but the manual thinking
		// budget table caps at 131072 for both: budgets only drive the legacy
		// budget_tokens path, and the models that distinguish xhigh from max
		// all take the effort parameter instead.
		return 131072
	default:
		return 0
	}
}

// ReasoningEffortVocabulary returns the six recognized reasoning-effort
// levels in rank order (least to most), from llm/effort, the one definition
// of the lattice.
func ReasoningEffortVocabulary() []string {
	return effort.Tiers()
}

// AcceptedReasoningEfforts returns every value a session entry point accepts:
// the six ranked levels plus the explicit off sentinel. It is the set a tool
// schema or CLI flag should advertise, so a surface cannot enumerate fewer
// levels than ValidateReasoningEffort allows.
func AcceptedReasoningEfforts() []string {
	return append(ReasoningEffortVocabulary(), ReasoningEffortNone)
}

// ValidateReasoningEffort reports whether effort is a value NewSession, a
// delegate dispatch, or a plugin-agent task config may safely accept. The
// empty string (unset) and ReasoningEffortNone (explicit off) are always
// valid. Any of the six known levels is valid, case-insensitive and trimmed.
// Anything else — including the CLI's other disable-alias sugar (off/0/etc,
// already normalized to "none" by NormalizeReasoningEffort before reaching
// this function) — is rejected with an error naming the bad value and the
// full vocabulary, so a typo or a stale/historical level (e.g. "ultra") fails
// loudly at config load instead of silently reaching a provider on a
// session's first turn.
func ValidateReasoningEffort(level string) error {
	if effort.Valid(level) {
		return nil
	}
	return fmt.Errorf("invalid reasoning_effort %q (expected one of: %s)", level, strings.Join(ReasoningEffortVocabulary(), ", "))
}

// ReasoningEffortNone is the canonical configured value for "the user turned
// thinking off". It is distinct from "" (nothing configured), which lets the
// session apply a default effort without overriding an explicit off.
const ReasoningEffortNone = effort.None

// NormalizeReasoningEffort lowercases and trims a reasoning-effort value and maps
// the "disable" aliases (none/null/off/false/0) to ReasoningEffortNone. It does
// not validate the level — unknown non-empty values pass through lowercased.
// This is the single place the disable-aliases are defined, shared by the CLI
// resolver and the runtime setter so they cannot drift.
func NormalizeReasoningEffort(s string) string {
	v := strings.ToLower(strings.TrimSpace(s))
	switch v {
	case ReasoningEffortNone, "null", "off", "false", "0":
		return ReasoningEffortNone
	default:
		return v
	}
}

// StatedDefaultEffort is the effort a row says its model runs at when the
// request asks for nothing, normalized to the canonical vocabulary so a
// hand-written "High" or "off" means what it says. Empty means the row states
// none and the caller applies its own fallback.
func StatedDefaultEffort(caps registry.Caps) string {
	return NormalizeReasoningEffort(registry.StringValue(caps.DefaultEffort))
}

// ReasoningEffortRank returns the ordinal rank of a reasoning-effort level
// (minimal=1 … max=6), or 0 for empty/unknown values. Use it instead of a
// local hierarchy so effort comparisons (e.g. a "floor at high" side-channel)
// honor the full vocabulary and cannot drift as levels are added.
func ReasoningEffortRank(level string) int {
	return effort.Rank(level)
}

// ClampReasoningEffort clamps a requested effort to the levels a model supports.
// An unsupported request rounds up to the lowest supported level above it (Pi's
// thinkingLevelMap parity — asking for more than a tier grants the next tier,
// e.g. "xhigh" against a wire-spelled ["high","max"] resolves to "max"); a
// request above the model's top supported level is lowered to that level.
// Empty, "none", unknown vocabulary, or an empty supported set pass through
// unchanged so the provider can decide; callers that spell the level on the
// wire use VouchedEffort instead, which refuses a level the ladder does not
// list. This is the single guard that keeps
// loop-detector escalation, the --reasoning-effort flag, and the UI selector
// from sending a level a model rejects (e.g. "max" to a model that only
// supports minimal/low/medium/high).
func ClampReasoningEffort(requested string, supportedLevels []string) string {
	req := strings.ToLower(strings.TrimSpace(requested))
	if req == "" || req == ReasoningEffortNone || len(supportedLevels) == 0 {
		return requested
	}
	reqRank := effort.Rank(req)
	if reqRank == 0 {
		return requested
	}
	ceil, ceilRank := "", 1<<30
	highest, highestRank := "", -1
	for _, lvl := range supportedLevels {
		l := strings.ToLower(strings.TrimSpace(lvl))
		r := effort.Rank(l)
		if r == 0 {
			continue
		}
		if l == req {
			return l
		}
		if r >= reqRank && r < ceilRank {
			ceil, ceilRank = l, r
		}
		if r > highestRank {
			highest, highestRank = l, r
		}
	}
	if ceil != "" {
		return ceil
	}
	if highest != "" {
		return highest
	}
	return requested
}

// VouchedEffort returns the level to write onto a field that spells the effort
// NAME — reasoning_effort, reasoning.effort, output_config.effort, or the
// string-thinking "thinking" value — clamped within the row's listed
// EffortValues. It returns "" when the row lists no ladder, or when the
// requested value clamps to nothing the ladder lists: evener never forces an
// effort name that the resolved row cannot vouch for. The explicit off
// sentinel ("none") is handled by the adapters' thinking-format logic, not
// here, so it also returns "".
//
// Rows that derive a numeric budget instead of spelling the level
// (anthropic budget_tokens/budget+effort, the google thinkingConfig) do not
// use this: such a row legitimately states no effort ladder and still needs
// the requested effort to size its budget.
func VouchedEffort(requested string, levels []string) string {
	v := ClampReasoningEffort(requested, levels)
	if v == "" || v == ReasoningEffortNone {
		return ""
	}
	for _, l := range levels {
		if strings.EqualFold(strings.TrimSpace(l), v) {
			return v
		}
	}
	return ""
}

// EffortReachesWire reports whether res's protocol builder puts tier in
// effect, which is the difference between a tier a caller may report as the
// one a request runs at and an unknown provider default. It is the reporting
// counterpart to VouchedEffort: a builder that spells the effort NAME drops a
// level res's ladder cannot vouch for, leaving the provider's own default in
// charge, while budget-shaped thinking and the OpenRouter pass-through put an
// unlisted level to work.
//
// The branches below restate each builder's own gate — chatcompletions'
// applyThinkingFormat, responses' reasoningObject, anthropic's
// applyThinkingShape, and google's thinkingConfig — rather than asking the
// builder, which reports its decision only as a side effect on the request
// body. That is deliberate: the one caller needs a pure, cheap answer on the
// envelope read path, and the alternative is a return value threaded through
// four wire implementations. It also means this is a second copy of those
// gates, so a branch here is only as right as the last person to read both.
// TestDefaultReasoningEffort_NamesTheTierTheWireCarries drives the real
// builders over the pruned body and is what catches a divergence; it does not
// make divergence impossible. The google branch models the one prune rule that
// drops the whole thinkingConfig object; any other gate that runs after the
// builder (a provider's generationConfig overlay) stays outside what this can
// see.
//
// Rows that derive a numeric budget instead of spelling the level (anthropic
// budget_tokens/budget+effort, the google thinkingConfig) count as reaching
// the wire: such a row legitimately states no effort ladder and still sizes its
// budget from the effort.
func EffortReachesWire(tier string, res registry.Resolved) bool {
	caps := res.Caps
	shape := registry.StringValue(caps.ThinkingShape)
	format := registry.StringValue(caps.ThinkingFormat)
	capable := caps.EffortCapable()
	budgetSized := ReasoningBudget(tier) > 0
	// Every builder returns before writing anything for a row whose reasoning
	// is off. The only caller pre-filters this, but the arms below would
	// otherwise each have to know that.
	if caps.ReasoningDisabled() {
		return false
	}
	// The level name the builders would spell: empty when the ladder refuses
	// it, which is the vouch gate every name-carrying dialect shares.
	vouched := VouchedEffort(tier, caps.EffortValues)
	nameCarried := vouched != ""
	nameGated := capable && nameCarried
	// The off tier takes a different gate: only the two dialects with a value
	// that says "off" write it, and only for a row whose ladder lists the off
	// level. Every other shape omits the control entirely.
	if tier == ReasoningEffortNone {
		if !caps.EffortOffCapable() {
			return false
		}
		// The chat-completions and Responses dialects spell an off value; the
		// anthropic and google bodies omit the control entirely.
		switch res.Protocol {
		case registry.ProtocolOpenAIChat:
			switch format {
			case "", "openai":
				return capable
			case "openrouter":
				return true
			}
		case registry.ProtocolOpenAIResponses:
			return capable
		}
		return false
	}
	switch res.Protocol {
	case registry.ProtocolAnthropic:
		// A numeric budget is sized from the level, so it puts the tier to work
		// without spelling a name; the name-carrying shapes write
		// output_config.effort only for a level the ladder vouches for.
		switch shape {
		case "budget":
			return budgetSized
		case "budget+effort":
			return budgetSized || nameCarried
		case "adaptive":
			return nameCarried
		}
		return false
	case registry.ProtocolGoogle:
		// registry.Prune runs after the builder and deletes the whole
		// thinkingConfig object, whichever shape spelled into it, so a row that
		// turns the field off carries neither a level nor a budget.
		if sends, pinned := caps.Fields[registry.FieldGoogleThinkingConfig]; pinned && !sends {
			return false
		}
		switch shape {
		case "budget":
			return budgetSized
		case "level":
			// A gateway's no-effort verdict keeps includeThoughts but spells no
			// level, whatever the row's ladder lists.
			if registry.BoolValue(caps.EffortParameterUnsupported) {
				return false
			}
			// The builder spells the tier through a row with no ladder and only
			// a vouched level through a row that names one.
			spelled := tier
			if len(caps.EffortValues) != 0 {
				spelled = vouched
			}
			// It then writes its wire enum: only the four tiers that enum spells
			// are on the wire. Evener's top two are renamed onto the ceiling and
			// anything else is dropped, so neither carries the tier that was
			// asked for — reporting either would name a tier the body never
			// spells, which is the defect this predicate exists to prevent.
			switch strings.ToUpper(spelled) {
			case "MINIMAL", "LOW", "MEDIUM", "HIGH":
				return true
			}
			return false
		}
		return false
	case registry.ProtocolOpenAIResponses:
		return nameGated
	case registry.ProtocolOpenAIChat:
		switch format {
		case "openrouter":
			// The documented exception to the vouch gate: the level passes
			// through and the gateway maps it to a budget. Only an explicit
			// no-effort verdict closes it.
			return !registry.BoolValue(caps.EffortParameterUnsupported)
		case "qwen", "qwen-chat-template", "chat-template":
			// These three write only their enable/template control: no level
			// name and no budget carries the tier, whatever the ladder lists.
			return false
		case "string-thinking":
			// This one spells "thinking" on the ladder's word alone, without
			// the effort-capability check the name-carrying fields make.
			return nameCarried
		}
		// The unset/openai dialect and the zai, deepseek and together enable
		// objects write the level only on an effort-capable row — which is false
		// for a row stating the no-effort verdict — and only when vouched.
		return nameGated
	}
	return false
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
