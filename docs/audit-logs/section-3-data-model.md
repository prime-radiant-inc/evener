# Audit: Spec Section 3 -- Data Model

**Auditor:** Bot (Claude Opus 4.6)
**Date:** 2026-02-11
**Spec file:** `unified-llm-spec.md` lines 346-793
**Implementation:** `internal/llm/types.go`, `internal/llm/stream.go`, `internal/llm/ratelimit.go`, provider adapters

---

## Summary

| Subsection | Status | Gaps Found |
|------------|--------|------------|
| 3.1 Message | IMPLEMENTED | 0 |
| 3.2 Role | IMPLEMENTED | 0 |
| 3.3 ContentPart | PARTIAL | 1 -- extra `WebSearch` field not in spec |
| 3.4 ContentKind | PARTIAL | 1 -- extra `ContentWebSearch` not in spec |
| 3.5 Content Data Structures | PARTIAL | 5 gaps |
| 3.6 Request | PARTIAL | 1 -- extra fields not in spec |
| 3.7 Response | PARTIAL | 2 gaps |
| 3.8 FinishReason | PARTIAL | 1 -- extra values not in spec |
| 3.9 Usage | PARTIAL | 1 gap |
| 3.10 ResponseFormat | IMPLEMENTED | 0 |
| 3.11 Warning | PARTIAL | 1 gap |
| 3.12 RateLimitInfo | IMPLEMENTED | 0 |
| 3.13 StreamEvent | PARTIAL | 2 gaps |
| 3.14 StreamEventType | PARTIAL | 2 -- extra types not in spec |

**Total gaps found: 17**

---

## Detailed Findings

### 3.1 Message

**Status: IMPLEMENTED**

| Spec Requirement | Implementation | Status |
|-----------------|----------------|--------|
| `role: Role` | `Role Role` | OK |
| `content: List<ContentPart>` | `Content []ContentPart` | OK |
| `name: String \| None` | `Name string` (omitempty) | OK |
| `tool_call_id: String \| None` | `ToolCallID string` (omitempty) | OK |
| `Message.system()` constructor | `System(text string)` | OK |
| `Message.user()` constructor | `User(text string)` | OK |
| `Message.assistant()` constructor | `Assistant(text string)` | OK |
| `Message.tool_result()` constructor | `ToolResult(toolCallID, content, isError)` | OK |
| `message.text` accessor | `(m Message) Text() string` | OK |

**Notes:**
- Extra constructor `Developer(text string)` exists for the DEVELOPER role -- reasonable extension.
- Extra constructor `ToolResultNamed(...)` exists -- reasonable extension.

### 3.2 Role

**Status: IMPLEMENTED**

| Spec Value | Implementation | Status |
|-----------|----------------|--------|
| SYSTEM | `RoleSystem = "system"` | OK |
| USER | `RoleUser = "user"` | OK |
| ASSISTANT | `RoleAssistant = "assistant"` | OK |
| TOOL | `RoleTool = "tool"` | OK |
| DEVELOPER | `RoleDeveloper = "developer"` | OK |

Provider mapping verified in all three adapters:
- OpenAI: SYSTEM/DEVELOPER merged into `instructions` (Responses API) -- correct
- Anthropic: SYSTEM/DEVELOPER extracted to `system` parameter -- correct
- Gemini: SYSTEM/DEVELOPER extracted to `systemInstruction` -- correct
- ASSISTANT maps to `model` role in Gemini -- correct
- TOOL maps to `tool_result` blocks in user messages for Anthropic -- correct
- TOOL maps to `functionResponse` in user messages for Gemini -- correct

### 3.3 ContentPart (Tagged Union)

**Status: PARTIAL**

| Spec Field | Implementation | Status |
|-----------|----------------|--------|
| `kind: ContentKind \| String` | `Kind ContentKind` | OK |
| `text: String \| None` | `Text string` | OK |
| `image: ImageData \| None` | `Image *ImageData` | OK |
| `audio: AudioData \| None` | `Audio *AudioData` | OK |
| `document: DocumentData \| None` | `Document *DocumentData` | OK |
| `tool_call: ToolCallData \| None` | `ToolCall *ToolCallData` | OK |
| `tool_result: ToolResultData \| None` | `ToolResult *ToolResultData` | OK |
| `thinking: ThinkingData \| None` | `Thinking *ThinkingData` | OK |

**GAP C3-1: Extra field `WebSearch *WebSearchData` not in spec.**
The implementation adds a `WebSearch` field and `WebSearchData` type to ContentPart. This is an extension beyond the spec, added for provider-native web search support. The spec's ContentPart has no `web_search` variant. This should be documented as an extension or the spec should be updated.

**File:** `internal/llm/types.go:99`

### 3.4 ContentKind

**Status: PARTIAL**

| Spec Value | Implementation | Status |
|-----------|----------------|--------|
| TEXT | `ContentText = "text"` | OK |
| IMAGE | `ContentImage = "image"` | OK |
| AUDIO | `ContentAudio = "audio"` | OK |
| DOCUMENT | `ContentDocument = "document"` | OK |
| TOOL_CALL | `ContentToolCall = "tool_call"` | OK |
| TOOL_RESULT | `ContentToolResult = "tool_result"` | OK |
| THINKING | `ContentThinking = "thinking"` | OK |
| REDACTED_THINKING | `ContentRedThinking = "redacted_thinking"` | OK |

**GAP C4-1: Extra kind `ContentWebSearch = "web_search"` not in spec.**
Same root cause as C3-1. The `web_search` content kind is an implementation extension not covered by Section 3.4. The spec does not define a WEB_SEARCH content kind.

**File:** `internal/llm/types.go:31`

### 3.5 Content Data Structures

#### ImageData

**Status: IMPLEMENTED**

| Spec Field | Implementation | Status |
|-----------|----------------|--------|
| `url: String \| None` | `URL string` | OK |
| `data: Bytes \| None` | `Data []byte` | OK |
| `media_type: String \| None` | `MediaType string` | OK |
| `detail: String \| None` | `Detail string` | OK |

File-path convenience (spec section 3.5 ImageData table) is implemented in all three adapters via `IsLocalPath()` + `ExpandTilde()` + `os.ReadFile()` + `InferMimeTypeFromPath()`.

#### AudioData

**Status: IMPLEMENTED**

All three fields present (`URL`, `Data`, `MediaType`). However, all adapters return an error when encountering AudioData -- the type exists but is not functionally supported by any provider adapter. This is not strictly a spec gap (the spec defines the type; support is adapter-dependent).

#### DocumentData

**Status: IMPLEMENTED**

All four fields present (`URL`, `Data`, `MediaType`, `FileName`). Same note as AudioData -- all adapters return an error when encountering DocumentData.

#### ToolCallData

**Status: PARTIAL**

| Spec Field | Implementation | Status |
|-----------|----------------|--------|
| `id: String` | `ID string` | OK |
| `name: String` | `Name string` | OK |
| `arguments: Dict \| String` | `Arguments json.RawMessage` | PARTIAL |
| `type: String` | `Type string` | OK |

**GAP C5-1: `ToolCallData.Arguments` type differs from spec.**
The spec says `Dict | String` but the implementation uses `json.RawMessage` (which is `[]byte`). This is arguably a reasonable Go-idiomatic representation of the union type, since `json.RawMessage` can hold both a JSON object string and a plain string. However, the `Parse()` method only handles the object case (unmarshals to `map[string]any`), not the raw string case. If a provider returns arguments as a plain string (not JSON object), `Parse()` would fail.

**GAP C5-2: Extra field `ParsedArguments map[string]any` not in spec.**
The implementation adds a `ParsedArguments` field and a `Parse()` method. While useful, this is not defined in the spec. This is a convenience extension.

**GAP C5-3: Extra field `ThoughtSignature string` not in spec.**
`ToolCallData` has a `ThoughtSignature` field used for Gemini's thought-signature replay requirement. The spec has no `thought_signature` field on ToolCallData -- the spec's `ThinkingData.signature` is the only signature field. This field was added to solve a Gemini-specific protocol requirement where thought signatures must accompany function calls.

**File:** `internal/llm/types.go:122-131`

#### ToolResultData

**Status: PARTIAL**

| Spec Field | Implementation | Status |
|-----------|----------------|--------|
| `tool_call_id: String` | `ToolCallID string` | OK |
| `content: String \| Dict` | `Content any` | PARTIAL |
| `is_error: Boolean` | `IsError bool` | OK |
| `image_data: Bytes \| None` | `ImageData []byte` | OK |
| `image_media_type: String \| None` | `ImageMediaType string` | OK |

**GAP C5-4: Extra field `Name string` not in spec.**
`ToolResultData` has a `Name` field that is not defined in spec Section 3.5. This is used by the Gemini adapter to map tool results back to function names (since Gemini uses function names rather than call IDs). While necessary for Gemini interop, it should be documented in the spec.

**File:** `internal/llm/types.go:149`

**GAP C5-5: `ToolResultData.image_data` and `image_media_type` are never used by any adapter.**
While the fields exist on the struct, none of the three provider adapters (OpenAI, Anthropic, Google) read or transmit `ImageData`/`ImageMediaType` from ToolResultData when converting to provider format. This means the feature is defined in both spec and implementation, but is dead code -- tool results with images will silently lose the image.

**Files:** `internal/llm/providers/openai/adapter.go` (toResponsesInput, line 706-723), `internal/llm/providers/anthropic/adapter.go` (toAnthropicMessages, line 991-1005), `internal/llm/providers/google/adapter.go` (toGeminiContents, line 740-771)

#### ThinkingData

**Status: IMPLEMENTED**

| Spec Field | Implementation | Status |
|-----------|----------------|--------|
| `text: String` | `Text string` | OK |
| `signature: String \| None` | `Signature string` | OK |
| `redacted: Boolean` | `Redacted bool` | OK |

### 3.6 Request

**Status: PARTIAL**

| Spec Field | Implementation | Status |
|-----------|----------------|--------|
| `model: String` | `Model string` | OK |
| `messages: List<Message>` | `Messages []Message` | OK |
| `provider: String \| None` | `Provider string` | OK |
| `tools: List<ToolDefinition> \| None` | `Tools []ToolDefinition` | OK |
| `tool_choice: ToolChoice \| None` | `ToolChoice *ToolChoice` | OK |
| `response_format: ResponseFormat \| None` | `ResponseFormat *ResponseFormat` | OK |
| `temperature: Float \| None` | `Temperature *float64` | OK |
| `top_p: Float \| None` | `TopP *float64` | OK |
| `max_tokens: Integer \| None` | `MaxTokens *int` | OK |
| `stop_sequences: List<String> \| None` | `StopSequences []string` | OK |
| `reasoning_effort: String \| None` | `ReasoningEffort *string` | OK |
| `metadata: Dict<String, String> \| None` | `Metadata map[string]string` | OK |
| `provider_options: Dict \| None` | `ProviderOptions map[string]any` | OK |

**GAP C6-1: Extra fields on Request not in spec.**
The implementation adds two fields not defined in Section 3.6:
- `WebSearch bool` -- enables provider-native web search
- `AdapterTimeout *AdapterTimeout` -- per-request timeout configuration

These are implementation extensions. `WebSearch` is related to GAP C3-1/C4-1 (the web search extension). `AdapterTimeout` is related to Section 7 timeout requirements.

**File:** `internal/llm/types.go:204-206`

### 3.7 Response

**Status: PARTIAL**

| Spec Field | Implementation | Status |
|-----------|----------------|--------|
| `id: String` | `ID string` | OK |
| `model: String` | `Model string` | OK |
| `provider: String` | `Provider string` | OK |
| `message: Message` | `Message Message` | OK |
| `finish_reason: FinishReason` | `Finish FinishReason` (json:"finish_reason") | OK |
| `usage: Usage` | `Usage Usage` | OK |
| `raw: Dict \| None` | `Raw map[string]any` | OK |
| `warnings: List<Warning>` | `Warnings []Warning` | PARTIAL |
| `rate_limit: RateLimitInfo \| None` | `RateLimit *RateLimitInfo` | OK |
| `response.text` accessor | `(r Response) Text() string` | OK |
| `response.tool_calls` accessor | `(r Response) ToolCalls() []ToolCallData` | OK |
| `response.reasoning` accessor | `(r Response) ReasoningText() string` | PARTIAL |

**GAP C7-1: `Warnings` field is never populated by any adapter.**
The struct field exists and is propagated in `generate.go`, but no provider adapter ever sets warnings on a response. The spec says this should capture non-fatal issues. If a provider returns warnings (e.g., model deprecation notices), they are silently dropped.

**Files:** All adapter `fromXxxResponse()` functions

**GAP C7-2: `ReasoningText()` method name differs from spec accessor name.**
The spec defines `response.reasoning` as the accessor name. The implementation uses `ReasoningText()`. While this is a minor naming discrepancy (Go methods are typically more descriptive), it means code following the spec literally would look for a `Reasoning()` method and not find it.

**File:** `internal/llm/types.go:360`

### 3.8 FinishReason

**Status: PARTIAL**

| Spec Field | Implementation | Status |
|-----------|----------------|--------|
| `reason: String` | `Reason string` | OK |
| `raw: String \| None` | `Raw string` | OK |

Spec-defined values:

| Spec Value | Implementation Constant | Status |
|-----------|------------------------|--------|
| `stop` | `FinishReasonStop = "stop"` | OK |
| `length` | `FinishReasonLength = "length"` | OK |
| `tool_calls` | `FinishReasonToolCalls = "tool_calls"` | OK |
| `content_filter` | `FinishReasonContentFilter = "content_filter"` | OK |
| `error` | `FinishReasonError = "error"` | OK |
| `other` | `FinishReasonOther = "other"` | OK |

**GAP C8-1: Extra finish reason value `pause_turn` not in spec.**
The implementation defines `FinishReasonPauseTurn = "pause_turn"` which is not one of the six spec-defined values. This is an Anthropic-specific extension used for web search turn pausing. Per the spec, unmapped provider-specific reasons should map to `other`, but this has its own named constant. The `NormalizeFinishReason` function returns `pause_turn` instead of `other` for Anthropic's `pause_turn` raw value.

**File:** `internal/llm/types.go:222`

Provider mapping verified:

| Provider | Provider Value | Expected Unified | Actual Unified | Status |
|----------|---------------|-----------------|----------------|--------|
| OpenAI | stop | stop | stop | OK |
| OpenAI | length | length | length | OK |
| OpenAI | tool_calls | tool_calls | tool_calls | OK |
| OpenAI | content_filter | content_filter | content_filter | OK |
| Anthropic | end_turn | stop | stop | OK |
| Anthropic | stop_sequence | stop | stop | OK |
| Anthropic | max_tokens | length | length | OK |
| Anthropic | tool_use | tool_calls | tool_calls | OK |
| Google | STOP | stop | stop | OK |
| Google | MAX_TOKENS | length | length | OK |
| Google | SAFETY | content_filter | content_filter | OK |
| Google | RECITATION | content_filter | content_filter | OK |
| Google | (has tool calls) | tool_calls | tool_calls | OK |

### 3.9 Usage

**Status: PARTIAL**

| Spec Field | Implementation | Status |
|-----------|----------------|--------|
| `input_tokens: Integer` | `InputTokens int` | OK |
| `output_tokens: Integer` | `OutputTokens int` | OK |
| `total_tokens: Integer` | `TotalTokens int` | OK |
| `reasoning_tokens: Integer \| None` | `ReasoningTokens *int` | OK |
| `cache_read_tokens: Integer \| None` | `CacheReadTokens *int` | OK |
| `cache_write_tokens: Integer \| None` | `CacheWriteTokens *int` | OK |
| `raw: Dict \| None` | `Raw map[string]any` | PARTIAL |
| Addition support (`usage_a + usage_b`) | `(u Usage) Add(v Usage) Usage` | OK |

**GAP C9-1: `Usage.Raw` is always set to an empty map, never to the actual provider usage data.**
All three parseUsage functions set `Raw: map[string]any{}` (empty map) but never populate it with the actual provider usage response. The spec says this should contain "raw provider usage data" for debugging. The raw data is available in the parent `Response.Raw` but not specifically on the `Usage` struct.

**Files:**
- `internal/llm/providers/openai/adapter.go:854`
- `internal/llm/providers/anthropic/adapter.go:1264`
- `internal/llm/providers/google/adapter.go:971`

Provider field mapping verified:

| SDK Field | OpenAI (Responses API) | Anthropic | Gemini |
|-----------|----------------------|-----------|--------|
| input_tokens | `input_tokens` | `input_tokens` | `promptTokenCount` |
| output_tokens | `output_tokens` | `output_tokens` | `candidatesTokenCount` |
| reasoning_tokens | `output_tokens_details.reasoning_tokens` | estimated (chars/4) | `thoughtsTokenCount` |
| cache_read_tokens | `input_tokens_details.cached_tokens` | `cache_read_input_tokens` | `cachedContentTokenCount` |
| cache_write_tokens | (not mapped) | `cache_creation_input_tokens` | (not mapped) |

Note: The spec table references `usage.prompt_tokens` for OpenAI, but the Responses API uses `input_tokens`. The implementation correctly uses the Responses API field names (`input_tokens`, `output_tokens`, `output_tokens_details`). The spec table appears to reference the Chat Completions API field names, not the Responses API -- this is a spec documentation issue rather than an implementation gap, since the code uses the Responses API as intended.

### 3.10 ResponseFormat

**Status: IMPLEMENTED**

| Spec Field | Implementation | Status |
|-----------|----------------|--------|
| `type: String` | `Type string` | OK |
| `json_schema: Dict \| None` | `JSONSchema map[string]any` | OK |
| `strict: Boolean` | `Strict bool` | OK |

### 3.11 Warning

**Status: PARTIAL**

| Spec Field | Implementation | Status |
|-----------|----------------|--------|
| `message: String` | `Message string` | OK |
| `code: String \| None` | `Code string` | OK |

**GAP C11-1: Warning type exists but is never instantiated.**
See GAP C7-1. The Warning struct is correctly defined per spec, but no code in the codebase ever creates Warning instances. The type is dead code.

**File:** `internal/llm/types.go:307-310`

### 3.12 RateLimitInfo

**Status: IMPLEMENTED**

| Spec Field | Implementation | Status |
|-----------|----------------|--------|
| `requests_remaining: Integer \| None` | `RequestsRemaining *int` | OK |
| `requests_limit: Integer \| None` | `RequestsLimit *int` | OK |
| `tokens_remaining: Integer \| None` | `TokensRemaining *int` | OK |
| `tokens_limit: Integer \| None` | `TokensLimit *int` | OK |
| `reset_at: Timestamp \| None` | `ResetAt *time.Time` | OK |

`ParseRateLimitHeaders()` correctly reads from `x-ratelimit-*` response headers. All three adapters call it.

### 3.13 StreamEvent

**Status: PARTIAL**

| Spec Field | Implementation | Status |
|-----------|----------------|--------|
| `type: StreamEventType \| String` | `Type StreamEventType` | OK |
| `delta: String \| None` | `Delta string` | OK |
| `text_id: String \| None` | `TextID string` | OK |
| `reasoning_delta: String \| None` | `ReasoningDelta string` | OK |
| `tool_call: ToolCall \| None` | `ToolCall *ToolCallData` | OK |
| `finish_reason: FinishReason \| None` | `FinishReason *FinishReason` | OK |
| `usage: Usage \| None` | `Usage *Usage` | OK |
| `response: Response \| None` | `Response *Response` | OK |
| `error: SDKError \| None` | `Err error` (json:"-") | PARTIAL |
| `raw: Dict \| None` | `Raw map[string]any` | OK |

**GAP C13-1: `StreamEvent.error` field is named `Err` and typed as `error`, not `SDKError`.**
The spec calls for `error: SDKError | None` but the implementation uses `Err error` with a `json:"-"` tag (excluded from serialization). The Go `error` interface is broader than the spec's `SDKError` type. The `json:"-"` tag means this field is lost during JSON serialization/deserialization of stream events. While Go idiomatic, this deviates from the spec's intent that stream errors carry structured error information.

**File:** `internal/llm/stream.go:52`

**GAP C13-2: Extra field `ObjectDelta any` not in spec.**
The implementation adds an `ObjectDelta` field for streaming structured output (used by `generate_object.go`). This is not defined in spec Section 3.13.

**File:** `internal/llm/stream.go:41`

### 3.14 StreamEventType

**Status: PARTIAL**

| Spec Value | Implementation | Status |
|-----------|----------------|--------|
| STREAM_START | `StreamEventStreamStart = "STREAM_START"` | OK |
| TEXT_START | `StreamEventTextStart = "TEXT_START"` | OK |
| TEXT_DELTA | `StreamEventTextDelta = "TEXT_DELTA"` | OK |
| TEXT_END | `StreamEventTextEnd = "TEXT_END"` | OK |
| REASONING_START | `StreamEventReasoningStart = "REASONING_START"` | OK |
| REASONING_DELTA | `StreamEventReasoningDelta = "REASONING_DELTA"` | OK |
| REASONING_END | `StreamEventReasoningEnd = "REASONING_END"` | OK |
| TOOL_CALL_START | `StreamEventToolCallStart = "TOOL_CALL_START"` | OK |
| TOOL_CALL_DELTA | `StreamEventToolCallDelta = "TOOL_CALL_DELTA"` | OK |
| TOOL_CALL_END | `StreamEventToolCallEnd = "TOOL_CALL_END"` | OK |
| FINISH | `StreamEventFinish = "FINISH"` | OK |
| ERROR | `StreamEventError = "ERROR"` | OK |
| PROVIDER_EVENT | `StreamEventProviderEvent = "PROVIDER_EVENT"` | OK |

**GAP C14-1: Extra event type `STEP_FINISH` not in spec.**
`StreamEventStepFinish = "STEP_FINISH"` is defined and used in `stream_generate.go` to signal tool-execution step boundaries. The spec's Section 3.14 does not define this event type.

**File:** `internal/llm/stream.go:23`

**GAP C14-2: Extra event type `OBJECT_DELTA` not in spec.**
`StreamEventObjectDelta = "OBJECT_DELTA"` is defined for streaming structured output. The spec's Section 3.14 does not define this event type.

**File:** `internal/llm/stream.go:24`

---

## Gap Summary (Actionable)

### Gaps requiring spec update (implementation is correct, spec is incomplete)

| ID | Description | Severity | Recommendation |
|----|-------------|----------|----------------|
| C3-1/C4-1 | WebSearch content kind and ContentPart field not in spec | Low | Add `WEB_SEARCH` to ContentKind and `web_search: WebSearchData` to ContentPart in spec |
| C5-3 | ToolCallData.ThoughtSignature not in spec | Low | Document as Gemini-specific extension in spec |
| C5-4 | ToolResultData.Name not in spec | Low | Add `name: String \| None` to ToolResultData in spec |
| C6-1 | Request.WebSearch and Request.AdapterTimeout not in spec | Low | Add these fields to spec Section 3.6 |
| C8-1 | FinishReason `pause_turn` not in spec | Low | Add `pause_turn` to spec Section 3.8 or map to `other` |
| C14-1 | STEP_FINISH event type not in spec | Low | Add to spec Section 3.14 |
| C14-2 | OBJECT_DELTA event type not in spec | Low | Add to spec Section 3.14 |
| C5-2 | ToolCallData.ParsedArguments not in spec | Low | Document as convenience extension |
| C13-2 | StreamEvent.ObjectDelta not in spec | Low | Add to spec Section 3.13 |

### Gaps requiring code fix (implementation deviates from spec)

| ID | Description | Severity | Recommendation |
|----|-------------|----------|----------------|
| C7-1/C11-1 | Warnings never populated by any adapter | Medium | Implement warning extraction (e.g., model deprecation headers) in adapters |
| C7-2 | ReasoningText() vs spec's .reasoning accessor name | Low | Rename to `Reasoning()` or add alias |
| C9-1 | Usage.Raw always empty map instead of raw provider data | Medium | Pass the raw provider usage map through to Usage.Raw |
| C13-1 | StreamEvent.Err typed as `error` not `SDKError`, excluded from JSON | Medium | Consider using the `llm.Error` interface and making it serializable |
| C5-5 | ToolResultData.image_data/image_media_type never transmitted by adapters | Medium | Implement image passing in tool results for adapters that support it, or document limitation |
