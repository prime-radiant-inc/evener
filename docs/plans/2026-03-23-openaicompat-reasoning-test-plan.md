# Test Plan: OpenAI-Compatible Adapter Reasoning Content & Provider Quirk Support

## Sources of Truth

1. **Kimi K2.5 / GLM 5.0 API documentation** -- defines the `reasoning_content` field shape, locked hyperparameters, `tool_choice` restrictions, non-standard finish reasons, empty content rejection, and stop sequence limits.
2. **Unified LLM spec** (`unified-llm-spec.md`) -- defines `ContentThinking`, `ThinkingData`, `StreamEventReasoningStart/Delta/End`, `Usage.ReasoningTokens`, `FinishReason` with `.Reason` and `.Raw`.
3. **`llm/types.go`** -- concrete Go types for the unified spec: `ContentPart`, `ThinkingData`, `StreamEvent`, `FinishReason`, `Usage`, `NormalizeFinishReason`.
4. **Existing Anthropic and Google adapters** -- working reference implementations for thinking content parsing, reasoning token estimation (chars/4 heuristic), and reasoning stream events.
5. **Existing `openaicompat` test suite** -- 24 tests in `adapter_test.go`, all passing. These form the regression baseline and must pass unchanged throughout.

## Harness

All tests use the existing `httptest.NewServer` pattern already established in `llm/providers/openaicompat/adapter_test.go`. Mock HTTP servers return hand-crafted JSON payloads matching real Kimi K2.5 and GLM 5.0 response shapes. This is deterministic, fast, and requires no API keys. No new harness infrastructure is needed.

The test file is `llm/providers/openaicompat/adapter_test.go`. All new tests land there alongside the existing 24 tests.

---

## Test Plan

### 1. Non-streaming: reasoning_content parsed as ContentThinking

- **Name**: Non-streaming response with reasoning_content produces ContentThinking part before ContentText part
- **Type**: scenario
- **Disposition**: new
- **Harness**: httptest mock returning `{"reasoning_content": "...", "content": "..."}` in `choices[0].message`
- **Preconditions**: Adapter with no quirks configured. Mock server returns a response with both `reasoning_content` and `content` fields populated.
- **Actions**: Call `a.Complete()` with a simple user message.
- **Expected outcome**:
  - `resp.Message.Content` has at least 2 parts.
  - First part: `Kind == ContentThinking`, `Thinking.Text` matches the reasoning_content string exactly.
  - Second part: `Kind == ContentText`, `Text` matches the content string exactly.
  - `resp.ReasoningText()` returns the reasoning_content string.
  - Source: Kimi K2.5 API docs (reasoning_content field), unified spec (ContentThinking kind), `llm/types.go:362` (`ReasoningText()`).
- **Interactions**: Tests the `fromChatCompletionResponse` parsing path. Incidentally verifies `chatMessage` struct deserialization.

### 2. Non-streaming: response without reasoning_content is unchanged

- **Name**: Non-streaming response without reasoning_content produces only ContentText (no spurious thinking)
- **Type**: regression
- **Disposition**: new
- **Harness**: httptest mock returning standard `{"content": "..."}` response (no reasoning_content field)
- **Preconditions**: Adapter with no quirks. Mock server returns a standard response.
- **Actions**: Call `a.Complete()` with a simple user message.
- **Expected outcome**:
  - `resp.Message.Content` has exactly 1 part with `Kind == ContentText`.
  - `resp.ReasoningText()` returns empty string.
  - Source: unified spec -- `ContentThinking` should not appear when no reasoning occurred.
- **Interactions**: Guards against regressions in the existing text-only parsing path.

### 3. Streaming: reasoning_content deltas emit REASONING_START/DELTA/END then TEXT_START/DELTA/END

- **Name**: Streaming reasoning_content deltas followed by content deltas produce correct event ordering
- **Type**: scenario
- **Disposition**: new
- **Harness**: httptest SSE mock sending reasoning_content delta chunks then content delta chunks then `[DONE]`
- **Preconditions**: Adapter with no quirks. Mock SSE server sends: 2 reasoning_content deltas, then 2 content deltas, then finish_reason stop, then `[DONE]`.
- **Actions**: Call `a.Stream()`, drain all events from `st.Events()`.
- **Expected outcome**:
  - Collected reasoning delta text (joined) equals the full reasoning string.
  - Collected text delta text (joined) equals the full content string.
  - Event ordering: `REASONING_START` appears before any `REASONING_DELTA`; `REASONING_END` appears after last `REASONING_DELTA` and before `TEXT_START`; `TEXT_START` appears before `TEXT_DELTA`; `TEXT_END` appears before `FINISH`.
  - Source: Kimi K2.5 streaming docs (reasoning_content in deltas), unified spec (REASONING_START/DELTA/END event types and ordering).
- **Interactions**: Tests the SSE parsing goroutine's reasoning state machine. Interacts with the `llm.ChanStream` event channel.

### 4. Streaming: reasoning_content in final FINISH response

- **Name**: Final FINISH event's Response contains ContentThinking from streamed reasoning
- **Type**: scenario
- **Disposition**: new
- **Harness**: httptest SSE mock with reasoning_content deltas then content deltas
- **Preconditions**: Same mock as test 3.
- **Actions**: Call `a.Stream()`, drain events, capture the FINISH event's `Response`.
- **Expected outcome**:
  - `finalResp.ReasoningText()` equals the full reasoning string.
  - `finalResp.Text()` equals the full content string.
  - `finalResp.Message.Content` has a ContentThinking part followed by a ContentText part.
  - Source: unified spec (Response.ReasoningText(), Message content ordering).
- **Interactions**: Tests the `[DONE]` handler's message assembly. Verifies that the streaming reasoning buffer is correctly accumulated and included in the final response.

### 5. Streaming: reasoning_content followed by tool_calls (no text) emits REASONING_END before TOOL_CALL_START

- **Name**: Reasoning followed by tool call without intervening text closes reasoning before tool call starts
- **Type**: boundary
- **Disposition**: new
- **Harness**: httptest SSE mock sending reasoning_content deltas then tool_call deltas (no content deltas)
- **Preconditions**: Adapter with no quirks. Mock SSE server sends reasoning_content deltas, then a tool_call chunk, then finish_reason tool_calls, then `[DONE]`.
- **Actions**: Call `a.Stream()`, drain events, record event types.
- **Expected outcome**:
  - `REASONING_END` appears in the event sequence before `TOOL_CALL_START`.
  - No `TEXT_START` or `TEXT_DELTA` events appear.
  - The FINISH response contains a ContentThinking part and a ContentToolCall part (no ContentText).
  - Source: Kimi K2.5 docs (reasoning_content precedes tool_calls), plan design decision (REASONING_END before TOOL_CALL_START, matching Anthropic's event ordering).
- **Interactions**: Tests the edge case where reasoning transitions directly to tool calls. This is the bug caught by the plan-editor (missing REASONING_END before tool calls).

### 6. Round-trip: ContentThinking serialized as reasoning_content in outgoing assistant messages

- **Name**: Multi-turn conversation preserves reasoning_content from previous assistant turns
- **Type**: scenario
- **Disposition**: new
- **Harness**: httptest mock that captures the request body and returns a simple response
- **Preconditions**: Adapter with no quirks. Request includes a previous assistant message with ContentThinking + ContentText parts, followed by a user message.
- **Actions**: Call `a.Complete()` with the multi-turn conversation.
- **Expected outcome**:
  - The captured request body's assistant message has `"reasoning_content"` set to the ThinkingData text.
  - The assistant message's `"content"` is set to the text part.
  - Source: Kimi K2.5 docs (reasoning_content must be preserved in assistant tool-call turns), plan design decision D6.
- **Interactions**: Tests `toChatMessages` serialization for the `RoleAssistant` case.

### 7. Round-trip: assistant message without thinking has no reasoning_content field

- **Name**: Assistant message without ContentThinking omits reasoning_content from outgoing JSON
- **Type**: regression
- **Disposition**: new
- **Harness**: httptest mock that captures request body
- **Preconditions**: Adapter with no quirks. Request includes a previous assistant message with only ContentText (no thinking).
- **Actions**: Call `a.Complete()` with the conversation.
- **Expected outcome**:
  - The captured assistant message JSON does NOT contain a `"reasoning_content"` key.
  - Source: reasoning_content is optional and should only appear when thinking was present.
- **Interactions**: Guards against adding spurious reasoning_content to all assistant messages.

### 8. Usage: native reasoning_tokens from completion_tokens_details

- **Name**: ReasoningTokens populated from native completion_tokens_details.reasoning_tokens
- **Type**: unit
- **Disposition**: new
- **Harness**: httptest mock returning usage with `completion_tokens_details.reasoning_tokens`
- **Preconditions**: Adapter with no quirks. Mock response includes `"usage": {"prompt_tokens": 10, "completion_tokens": 50, "total_tokens": 60, "completion_tokens_details": {"reasoning_tokens": 35}}` and reasoning_content.
- **Actions**: Call `a.Complete()`.
- **Expected outcome**:
  - `resp.Usage.ReasoningTokens` is non-nil and equals 35.
  - Source: Kimi K2.5 docs (completion_tokens_details.reasoning_tokens), unified spec (Usage.ReasoningTokens).
- **Interactions**: Tests the native reasoning token extraction path in `fromChatCompletionResponse`.

### 9. Usage: estimated reasoning_tokens when native count absent

- **Name**: ReasoningTokens estimated from reasoning_content length when native count is absent
- **Type**: unit
- **Disposition**: new
- **Harness**: httptest mock returning usage WITHOUT completion_tokens_details but WITH reasoning_content
- **Preconditions**: Adapter with no quirks. Mock response has reasoning_content of known length (e.g., 80 chars) but no reasoning_tokens in usage.
- **Actions**: Call `a.Complete()`.
- **Expected outcome**:
  - `resp.Usage.ReasoningTokens` is non-nil and equals `len(reasoning_content) / 4` (e.g., 80/4 = 20).
  - Source: Anthropic adapter reference (chars/4 heuristic at `adapter.go:694-706`), plan design decision D7.
- **Interactions**: Tests the estimation fallback path. Uses the same heuristic as the Anthropic adapter.

### 10. ProviderQuirks: locked temperature and top_p stripped from request

- **Name**: Quirks with LockTemperature and LockTopP remove those fields from the API request
- **Type**: scenario
- **Disposition**: new
- **Harness**: httptest mock that captures request body
- **Preconditions**: Adapter with `Quirks: ProviderQuirks{LockTemperature: true, LockTopP: true}`. Request has Temperature and TopP set.
- **Actions**: Call `a.Complete()`.
- **Expected outcome**:
  - Captured request body does NOT contain `"temperature"` key.
  - Captured request body does NOT contain `"top_p"` key.
  - Source: Kimi K2.5 docs (temperature, top_p are fixed for K2.5), plan design decision D3.
- **Interactions**: Tests `buildRequestBody` quirk application. Verifies that the quirk system interacts correctly with the existing parameter serialization.

### 11. ProviderQuirks: locked frequency_penalty and presence_penalty stripped

- **Name**: Quirks with LockFrequencyPenalty and LockPresencePenalty remove those fields from the API request
- **Type**: unit
- **Disposition**: new
- **Harness**: httptest mock that captures request body
- **Preconditions**: Adapter with all four lock quirks enabled. Request sets frequency_penalty and presence_penalty via ProviderOptions passthrough (since Request doesn't have dedicated fields for these, or if it does, via the appropriate field).
- **Actions**: Call `a.Complete()`.
- **Expected outcome**:
  - Captured request body does NOT contain `"frequency_penalty"` or `"presence_penalty"` keys.
  - Source: Kimi K2.5 docs (all four params are fixed).
- **Interactions**: Tests additional locked parameter paths in `buildRequestBody`.

### 12. ProviderQuirks: ToolChoiceAutoOnly clamps required to auto

- **Name**: Quirks with ToolChoiceAutoOnly downgrades tool_choice "required" to "auto"
- **Type**: scenario
- **Disposition**: new
- **Harness**: httptest mock that captures request body
- **Preconditions**: Adapter with `Quirks: ProviderQuirks{ToolChoiceAutoOnly: true}`. Request has `ToolChoice.Mode = "required"` and tools defined.
- **Actions**: Call `a.Complete()`.
- **Expected outcome**:
  - Captured request body has `"tool_choice": "auto"` (not "required").
  - Source: Kimi K2.5 docs (tool_choice restricted to auto/none with thinking), GLM 5.0 docs (only auto supported).
- **Interactions**: Tests `buildRequestBody` tool_choice quirk. Interacts with `toChatToolChoice` output.

### 13. ProviderQuirks: ToolChoiceAutoOnly preserves auto and none

- **Name**: Quirks with ToolChoiceAutoOnly allows "auto" and "none" through unchanged
- **Type**: boundary
- **Disposition**: new
- **Harness**: httptest mock that captures request body
- **Preconditions**: Adapter with `ToolChoiceAutoOnly: true`. Request has `ToolChoice.Mode = "auto"`.
- **Actions**: Call `a.Complete()`.
- **Expected outcome**:
  - Captured request body has `"tool_choice": "auto"` (unchanged).
  - Source: Both Kimi and GLM accept auto/none.
- **Interactions**: Guards against over-clamping valid values.

### 14. ProviderQuirks: MaxStopSequences truncates stop array

- **Name**: Quirks with MaxStopSequences=1 truncates stop sequences to first entry
- **Type**: scenario
- **Disposition**: new
- **Harness**: httptest mock that captures request body
- **Preconditions**: Adapter with `Quirks: ProviderQuirks{MaxStopSequences: 1}`. Request has 3 stop sequences.
- **Actions**: Call `a.Complete()`.
- **Expected outcome**:
  - Captured request body `"stop"` array has exactly 1 element (the first one).
  - Source: GLM 5.0 docs (max 1 stop sequence).
- **Interactions**: Tests `buildRequestBody` stop sequence quirk.

### 15. ProviderQuirks: NoJSONSchema downgrades response_format

- **Name**: Quirks with NoJSONSchema downgrades json_schema to json_object
- **Type**: scenario
- **Disposition**: new
- **Harness**: httptest mock that captures request body
- **Preconditions**: Adapter with `Quirks: ProviderQuirks{NoJSONSchema: true}`. Request has `ResponseFormat.Type = "json_schema"`.
- **Actions**: Call `a.Complete()`.
- **Expected outcome**:
  - Captured request body `"response_format"` has `"type": "json_object"` (not json_schema).
  - The json_schema property is removed.
  - Source: Both Kimi and GLM docs (no json_schema support).
- **Interactions**: Tests `buildRequestBody` response format quirk.

### 16. ProviderQuirks: StripEmptyContent removes empty text parts

- **Name**: Quirks with StripEmptyContent filters empty text parts from user messages
- **Type**: scenario
- **Disposition**: new
- **Harness**: httptest mock that captures request body
- **Preconditions**: Adapter with `Quirks: ProviderQuirks{StripEmptyContent: true}`. Request has a user message with an empty text part and a non-empty text part.
- **Actions**: Call `a.Complete()`.
- **Expected outcome**:
  - The captured user message content equals only the non-empty text (empty part stripped).
  - Source: GLM 5.0 docs (API rejects messages with empty content[].text).
- **Interactions**: Tests `toChatMessages` empty content filtering. Interacts with `textFromParts` or its replacement.

### 17. ProviderQuirks: FinishReasonMap translates non-standard finish reasons (non-streaming)

- **Name**: Non-streaming response with quirk-mapped finish reason "sensitive" becomes "content_filter"
- **Type**: scenario
- **Disposition**: new
- **Harness**: httptest mock returning `"finish_reason": "sensitive"`
- **Preconditions**: Adapter with `Quirks: ProviderQuirks{FinishReasonMap: map[string]string{"sensitive": "content_filter", "network_error": "error"}}`.
- **Actions**: Call `a.Complete()`.
- **Expected outcome**:
  - `resp.Finish.Reason` equals `"content_filter"`.
  - `resp.Finish.Raw` equals `"sensitive"` (original provider value preserved).
  - Source: GLM 5.0 docs (sensitive finish reason), plan design decision D5 (localized mapping, raw preserved).
- **Interactions**: Tests `fromChatCompletionResponse` finish reason mapping. This is the bug caught by the plan-editor (Raw must preserve the original provider value, not the mapped one).

### 18. ProviderQuirks: FinishReasonMap in streaming

- **Name**: Streaming finish event with quirk-mapped finish reason preserves original in Raw
- **Type**: scenario
- **Disposition**: new
- **Harness**: httptest SSE mock with `"finish_reason": "sensitive"` in the last chunk
- **Preconditions**: Adapter with GLM quirk preset (includes FinishReasonMap).
- **Actions**: Call `a.Stream()`, drain events, capture FINISH event.
- **Expected outcome**:
  - FINISH event's `FinishReason.Reason` equals `"content_filter"`.
  - FINISH event's `FinishReason.Raw` equals `"sensitive"`.
  - Source: Same as test 17, but for the streaming path.
- **Interactions**: Tests the streaming [DONE] handler's finish reason mapping. Verifies parity between streaming and non-streaming quirk behavior.

### 19. Named preset: QuirksPreset("kimi-k2.5") returns correct configuration

- **Name**: QuirksPreset for kimi-k2.5 enables all Kimi-specific quirk flags
- **Type**: unit
- **Disposition**: new
- **Harness**: Direct function call (no HTTP)
- **Preconditions**: None.
- **Actions**: Call `QuirksPreset("kimi-k2.5")`.
- **Expected outcome**:
  - `LockTemperature`, `LockTopP`, `LockFrequencyPenalty`, `LockPresencePenalty` are all true.
  - `ToolChoiceAutoOnly` is true.
  - `NoJSONSchema` is true.
  - Source: Kimi K2.5 API docs (all these restrictions documented).
- **Interactions**: None. Pure function test.

### 20. Named preset: QuirksPreset("glm-5") returns correct configuration

- **Name**: QuirksPreset for glm-5 enables all GLM-specific quirk flags
- **Type**: unit
- **Disposition**: new
- **Harness**: Direct function call (no HTTP)
- **Preconditions**: None.
- **Actions**: Call `QuirksPreset("glm-5")`.
- **Expected outcome**:
  - `StripEmptyContent` is true.
  - `ToolChoiceAutoOnly` is true.
  - `MaxStopSequences` equals 1.
  - `NoJSONSchema` is true.
  - `FinishReasonMap["sensitive"]` equals `"content_filter"`.
  - `FinishReasonMap["network_error"]` equals `"error"`.
  - Source: GLM 5.0 API docs (all these restrictions documented).
- **Interactions**: None. Pure function test.

### 21. Named preset: unknown provider returns zero-value quirks

- **Name**: QuirksPreset for unknown provider name returns empty/default quirks
- **Type**: boundary
- **Disposition**: new
- **Harness**: Direct function call
- **Preconditions**: None.
- **Actions**: Call `QuirksPreset("unknown-provider")`.
- **Expected outcome**:
  - All boolean fields are false, `MaxStopSequences` is 0, `FinishReasonMap` is nil.
  - Source: Plan Task 7 (unknown names return zero-value quirks).
- **Interactions**: None.

### 22. Env var: OPENAI_COMPATIBLE_PROVIDER_QUIRKS configures adapter quirks

- **Name**: NewFromEnv reads OPENAI_COMPATIBLE_PROVIDER_QUIRKS env var and applies preset
- **Type**: scenario
- **Disposition**: new
- **Harness**: `t.Setenv` to configure env vars, then call `NewFromEnv()`
- **Preconditions**: OPENAI_COMPATIBLE_BASE_URL, OPENAI_COMPATIBLE_API_KEY, and OPENAI_COMPATIBLE_PROVIDER_QUIRKS set.
- **Actions**: Call `NewFromEnv()`.
- **Expected outcome**:
  - Returned adapter has `Quirks.LockTemperature == true` (from kimi-k2.5 preset).
  - Returned adapter has `Quirks.ToolChoiceAutoOnly == true`.
  - Source: Plan Task 7 (env var parsing), plan design decision D3.
- **Interactions**: Tests the `NewFromEnv` initialization path. Interacts with the `init()` auto-registration.

### 23. Integration: Kimi K2.5 multi-turn reasoning with tool call

- **Name**: Full Kimi K2.5 conversation: user question, reasoning+tool call, tool result, reasoning+answer
- **Type**: integration
- **Disposition**: new
- **Harness**: httptest mock with request counter serving different responses per call
- **Preconditions**: Adapter with `QuirksPreset("kimi-k2.5")`. Temperature set on request.
- **Actions**:
  1. First `a.Complete()`: user asks question. Mock returns reasoning_content + tool_call.
  2. Second `a.Complete()`: send original user message, previous assistant response (with ContentThinking + ContentToolCall), tool result. Mock returns reasoning_content + content.
- **Expected outcome**:
  - First request body: no `"temperature"` key (locked).
  - First response: `ReasoningText()` matches, `ToolCalls()` has 1 call, `Usage.ReasoningTokens` is 20 (native).
  - Second request body: assistant message has `"reasoning_content"` round-tripped.
  - Second response: `ReasoningText()` matches, `Text()` matches, `Usage.ReasoningTokens` is estimated (no native count).
  - Source: Kimi K2.5 docs (reasoning_content preservation, locked params), plan Task 9.
- **Interactions**: End-to-end test exercising reasoning parsing, round-tripping, quirk application, and usage tracking together. This is the highest-fidelity non-live test for the Kimi path.

### 24. Integration: GLM 5.0 empty content stripping and sensitive filter

- **Name**: Full GLM 5.0 conversation with empty content parts, stop truncation, and sensitive finish
- **Type**: integration
- **Disposition**: new
- **Harness**: httptest mock that captures request body and returns sensitive finish
- **Preconditions**: Adapter with `QuirksPreset("glm-5")`. Request has user message with empty + non-empty text parts and 3 stop sequences.
- **Actions**: Call `a.Complete()`.
- **Expected outcome**:
  - Captured request body: stop array has 1 element (truncated).
  - Captured request body: user message content is "tell me something" (empty part stripped).
  - `resp.Finish.Reason` equals `"content_filter"` (mapped from sensitive).
  - Source: GLM 5.0 docs (empty content rejection, stop limit, finish reasons), plan Task 9.
- **Interactions**: End-to-end test exercising multiple GLM quirks together in a single request.

### 25. Regression: all 24 existing tests pass unchanged

- **Name**: Existing openaicompat test suite continues to pass with no modifications
- **Type**: regression
- **Disposition**: existing
- **Harness**: `go test -count=1 ./llm/providers/openaicompat/`
- **Preconditions**: All implementation changes applied.
- **Actions**: Run the full test suite.
- **Expected outcome**:
  - All 24 existing tests pass. No test function names changed, no test logic modified.
  - Source: Regression baseline requirement from testing strategy.
- **Interactions**: Validates that reasoning_content parsing (unconditional) and ProviderQuirks (zero-value by default) do not affect existing behavior.

### 26. Build verification: project compiles and all tests pass

- **Name**: Full project builds and passes go vet and all tests
- **Type**: invariant
- **Disposition**: new
- **Harness**: `make build && go vet ./... && go test -short -count=1 ./...`
- **Preconditions**: All implementation changes applied.
- **Actions**: Run build, vet, and full test suite.
- **Expected outcome**:
  - Build succeeds with no errors.
  - No vet warnings.
  - All tests across all packages pass.
  - Source: Standard CI gate.
- **Interactions**: Catches compilation errors, import cycles, or cross-package regressions.

---

## Coverage Summary

### Covered

| Area | Tests | Coverage |
|------|-------|----------|
| **reasoning_content parsing (non-streaming)** | 1, 2 | With and without reasoning_content |
| **reasoning_content streaming events** | 3, 4, 5 | Text follow-up, tool-call follow-up, final response assembly |
| **reasoning_content round-tripping** | 6, 7 | With and without thinking content |
| **Reasoning token tracking** | 8, 9 | Native extraction and estimation fallback |
| **Locked hyperparameters** | 10, 11 | All four lockable params |
| **ToolChoice restriction** | 12, 13 | Clamping and passthrough |
| **Stop sequence truncation** | 14 | GLM limit |
| **JSON schema downgrade** | 15 | json_schema to json_object |
| **Empty content stripping** | 16 | GLM empty text rejection |
| **Finish reason mapping** | 17, 18 | Non-streaming and streaming, Raw preservation |
| **Named presets** | 19, 20, 21 | kimi-k2.5, glm-5, unknown |
| **Env var configuration** | 22 | OPENAI_COMPATIBLE_PROVIDER_QUIRKS |
| **End-to-end integration** | 23, 24 | Kimi multi-turn, GLM quirk combo |
| **Regression baseline** | 25 | All 24 existing tests |
| **Build verification** | 26 | Compilation, vet, cross-package |

### Explicitly Excluded

| Area | Reason | Risk |
|------|--------|------|
| **Live API calls to Kimi/GLM** | Requires API keys not available in test environment. Per agreed strategy, mock tests cover documented response shapes. | Moderate: real API may have undocumented quirks. Mitigated by basing mocks on actual API documentation. |
| **Thinking parameter injection** | Flows through existing `ProviderOptions` passthrough (design decision D4), which is already tested by `TestAdapter_Complete_MapsToChatCompletionsAPI` and the provider options passthrough code path. No new code needed. | Low: passthrough mechanism is generic and proven. |
| **`video_url` content type** | Kimi-specific, out of scope for this task per user description. | Low: no user demand, no implementation planned. |
| **`do_sample` GLM parameter** | GLM-specific, flows through ProviderOptions passthrough. | Low: same passthrough mechanism. |
| **Performance benchmarks** | All changes are in JSON parsing and map operations, which are fast by nature. No performance-critical paths modified. | Low: no measurable performance risk from these changes. |
