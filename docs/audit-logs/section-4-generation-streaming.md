# Audit: Section 4 - Generation and Streaming

**Date:** 2026-02-11
**Auditor:** Bot (Claude Opus 4.6)
**Spec version:** unified-llm-spec.md, lines 794-1049
**Codebase state:** main branch, commit 5e0be40

## Summary

Section 4 covers the full generation and streaming stack: low-level `Client.complete()`/`Client.stream()`, high-level `generate()`/`stream()`, structured output (`generate_object()`/`stream_object()`), and cancellation/timeout support.

The codebase has solid implementations of the core generation pipeline, but has **10 gaps** against the spec, ranging from missing parameter pass-through to incomplete timeout enforcement.

---

## 4.1 Low-Level: Client.complete() -- COMPLIANT

**Spec:** Routes to resolved provider adapter, blocks, returns Response, raises on error, no auto-retry.

**Implementation:** `internal/llm/client.go` lines 49-72.

- Routes to adapter via `c.providers[prov]` after resolving the provider name.
- Validates request before routing.
- Returns `(Response, error)` -- blocking.
- Does NOT retry (retries are in `Generate()` layer).
- Middleware chain applied correctly.

**Verdict:** Fully compliant.

---

## 4.2 Low-Level: Client.stream() -- COMPLIANT

**Spec:** Returns async iterator of StreamEvent, terminates with FINISH, no auto-retry.

**Implementation:** `internal/llm/client.go` lines 74-97.

- Returns `(Stream, error)` where `Stream` has `Events() <-chan StreamEvent` and `Close() error`.
- All adapter implementations (OpenAI, Anthropic, Google, openai-compat) emit a FINISH event with the complete Response.
- Close() cancels the context, which stops the goroutine reading from the HTTP body.
- Does NOT retry.

**Verdict:** Fully compliant.

---

## 4.3 High-Level: generate() -- 5 GAPS

### 4.3.1 GenerateOptions parameter list

**Spec requires:**
```
model, prompt, messages, system, tools, tool_choice, max_tool_rounds,
stop_when, response_format, temperature, top_p, max_tokens, stop_sequences,
reasoning_effort, provider, provider_options, max_retries, timeout,
abort_signal, client
```

**Implementation:** `internal/llm/generate.go` `GenerateOptions` struct (lines 35-82).

| Spec parameter     | Implementation field    | Status |
|--------------------|-------------------------|--------|
| model              | Model                   | OK     |
| prompt             | Prompt                  | OK     |
| messages           | Messages                | OK     |
| system             | System                  | OK     |
| tools              | Tools                   | OK     |
| tool_choice        | ToolChoice              | OK     |
| max_tool_rounds    | MaxToolRounds           | OK     |
| stop_when          | StopWhen                | OK     |
| response_format    | ResponseFormat          | OK     |
| temperature        | Temperature             | OK     |
| top_p              | TopP                    | OK     |
| max_tokens         | MaxTokens               | OK     |
| stop_sequences     | StopSequences           | OK     |
| reasoning_effort   | ReasoningEffort         | OK     |
| provider           | Provider                | OK     |
| provider_options   | ProviderOptions         | OK     |
| max_retries        | RetryPolicy.MaxRetries  | OK (via RetryPolicy struct) |
| timeout            | TimeoutTotal/PerStep    | OK (split into two fields) |
| abort_signal       | (context.Context)       | OK (Go idiom: ctx cancellation) |
| client             | Client                  | OK     |

All spec-required parameters are present. The Go idiom of using `context.Context` for abort signals is appropriate.

### GAP 1: WebSearch field missing from GenerateOptions

**Severity:** Medium
**Location:** `internal/llm/generate.go` lines 35-82, `internal/llm/stream_generate.go` lines 72-316

The `Request` type has a `WebSearch bool` field (types.go line 204), and all adapters check `req.WebSearch` to decide whether to include web search tools. However, `GenerateOptions` does not have a `WebSearch` field, and neither `Generate()` nor `StreamGenerate()` set `req.WebSearch` when building the `Request`.

This means web search cannot be requested through the high-level `Generate()`/`StreamGenerate()` API. The agent session works around this by calling `Client.Complete()` directly (via the lower-level API), but the high-level API has a hole.

**Fix:** Add `WebSearch bool` to `GenerateOptions` and propagate it to `req.WebSearch` in both `Generate()` and `StreamGenerate()`.

### GAP 2: StreamGenerate does not check StopWhen

**Severity:** Medium
**Location:** `internal/llm/stream_generate.go`

`Generate()` checks `opts.StopWhen` after each tool execution round (generate.go line 262). `StreamGenerate()` has no corresponding check -- the `StopWhen` field exists on `GenerateOptions` but is silently ignored by `StreamGenerate()`.

**Spec (4.4):** "Accepts the same parameters as `generate()`" -- which includes `stop_when`.

**Fix:** After executing tools and emitting STEP_FINISH in StreamGenerate, check `opts.StopWhen` and terminate the stream early if it returns true.

### GAP 3: StreamResult lacks TotalUsage tracking

**Severity:** Low
**Location:** `internal/llm/stream_generate.go`

The spec (4.3 GenerateResult) says `total_usage` is "aggregated usage across ALL steps." `GenerateResult` correctly has `TotalUsage Usage` and aggregates it across steps. However, `StreamResult` only exposes the final `Response()` (which contains usage from the last step). There is no way to get aggregated total usage across all streaming steps.

**Fix:** Add `TotalUsage()` method to `StreamResult` that aggregates usage from all STEP_FINISH and FINISH events.

### GAP 4: StreamResult lacks Steps tracking

**Severity:** Low
**Location:** `internal/llm/stream_generate.go`

The spec (4.3 GenerateResult) includes `steps: List<StepResult>` -- detailed results for each step. `GenerateResult` correctly has `Steps []StepResult`. `StreamResult` does not track `StepResult` objects at all -- it only exposes the final `Response()`.

While the spec's `stream()` description (4.4) defines `StreamResult` without an explicit `steps` field, the spec also says "Accepts the same parameters as `generate()`" and the `StreamResult.response()` documentation implies the full response should be available. The STEP_FINISH events carry step data but there is no way to access them programmatically after the stream is consumed.

**Fix:** Consider adding a `Steps()` method to `StreamResult` that collects StepResult data from STEP_FINISH events.

### GAP 5: Anthropic adapter does not map reasoning_effort

**Severity:** Low
**Location:** `internal/llm/providers/anthropic/adapter.go`

The OpenAI adapter maps `reasoning_effort` to `reasoning.effort` (line 127). The Google adapter maps it to `thinkingConfig.thinkingBudget` (lines 103-110). The Anthropic adapter does not map `reasoning_effort` at all -- the field is silently ignored.

Anthropic's extended thinking is controlled by the `thinking` parameter in the API body (with a `budget_tokens` field), but the adapter does not translate `reasoning_effort` to this parameter.

**Fix:** Map `reasoning_effort` to Anthropic's `thinking` API parameter with appropriate budget_tokens values (similar to Google's approach).

---

## 4.4 High-Level: stream() -- 1 GAP (plus gaps shared with 4.3)

### StreamResult properties

**Spec requires:**
```
ASYNC ITERATOR over StreamEvent     -- Events() <-chan StreamEvent       OK
response() -> Response              -- Response() (*Response, error)     OK
text_stream -> AsyncIterator<String> -- TextStream() <-chan string       OK
partial_response -> Response | None  -- PartialResponse() *Response      OK
```

All four properties are implemented in `internal/llm/stream_generate.go` lines 13-67.

### GAP 6: StreamAccumulator ignores reasoning, tool calls, and provider events

**Severity:** Medium
**Location:** `internal/llm/stream_accumulator.go`

The spec says StreamAccumulator should produce "a complete Response" equivalent to what `complete()` would return. The current implementation only accumulates:
- TEXT_START / TEXT_DELTA / TEXT_END -- text content
- FINISH -- finish reason, usage, and response

It ignores:
- REASONING_START / REASONING_DELTA / REASONING_END -- reasoning text is lost
- TOOL_CALL_START / TOOL_CALL_DELTA / TOOL_CALL_END -- tool calls are lost
- The accumulated `buildResponse()` only creates a text ContentPart

This means if you use `StreamAccumulator` on a stream that contains reasoning or tool calls, the resulting `Response()` will be missing those content parts. The `StreamGenerate()` function works around this by using the FINISH event's embedded Response (which adapters build with full content), but standalone accumulator usage is broken.

**Fix:** Process REASONING_* events into ThinkingData and TOOL_CALL_* events into ToolCallData, and include them in `buildResponse()`.

---

## 4.5 generate_object() -- COMPLIANT

**Spec:** Structured output with schema validation, provider-specific strategies, NoObjectGeneratedError.

**Implementation:** `internal/llm/generate_object.go` lines 14-71.

- Sets `ResponseFormat` with `json_schema` type and the provided schema.
- Calls `Generate()` to get text output.
- Parses JSON output and validates against schema using `jsonschema` library.
- Raises `NoObjectGeneratedError` on parse or validation failure (lines 46, 54).
- Provider strategies:
  - OpenAI: Native `response_format: { type: "json_schema", ... }` with strict mode -- OK (adapter.go line 461-466).
  - Gemini: Native `responseMimeType: "application/json"` with `responseSchema` -- OK (adapter.go line 95-99).
  - Anthropic: Fallback via system prompt injection -- OK (adapter.go lines 731-754).

**Verdict:** Fully compliant.

---

## 4.6 stream_object() -- COMPLIANT

**Spec:** Streaming structured output with incremental JSON parsing.

**Implementation:** `internal/llm/generate_object.go` lines 73-287.

- `StreamGenerateObject()` wraps `StreamGenerate()`.
- Incremental JSON parsing via `tryParsePartialJSON()` (lines 212-275).
- Emits `OBJECT_DELTA` events with partially parsed objects.
- Final validation against schema after stream completes.
- `Output()` method blocks until stream ends and returns validated object.
- `NoObjectGeneratedError` raised on parse/validation failure.

**Verdict:** Fully compliant.

---

## 4.7 Cancellation and Timeouts -- 4 GAPS

### Abort Signals -- COMPLIANT

**Spec:** `abort_signal` for cooperative cancellation; AbortError on cancel.

Go uses `context.Context` cancellation as the idiomatic equivalent. Both `Generate()` and `StreamGenerate()` accept a `context.Context` and propagate cancellation. `wrapContextError()` in `context_errors.go` converts `context.Canceled` to `AbortError` and `context.DeadlineExceeded` to `RequestTimeoutError`.

**Verdict:** Fully compliant.

### TimeoutConfig -- COMPLIANT

**Spec requires:**
```
RECORD TimeoutConfig:
    total       : Float | None
    per_step    : Float | None
```

**Implementation:** `GenerateOptions.TimeoutTotal` and `GenerateOptions.TimeoutPerStep` (generate.go lines 79-81).

- `Generate()` wraps context with `TimeoutTotal` (line 121) and per-step with `TimeoutPerStep` (line 188).
- `StreamGenerate()` also applies both (lines 127, 175).

**Verdict:** Fully compliant.

### AdapterTimeout -- 4 GAPS

**Spec requires:**
```
RECORD AdapterTimeout:
    connect     : Float             -- time to establish HTTP connection (default: 10s)
    request     : Float             -- time for entire request/response cycle (default: 120s)
    stream_read : Float             -- max time between consecutive stream events (default: 30s)
```

**Implementation:** `internal/llm/types.go` lines 321-334, `internal/llm/adapter_timeout.go`.

The type definition is correct with all three fields and spec-compliant defaults. However:

### GAP 7: AdapterTimeout.Connect is never enforced

**Severity:** Medium
**Location:** `internal/llm/adapter_timeout.go`

`ApplyAdapterTimeout()` only applies the `Request` timeout for non-streaming calls. The `Connect` timeout is declared in the struct but never used anywhere in the codebase. No adapter sets up an `http.Transport` with a connect timeout derived from `AdapterTimeout.Connect`.

All adapters use `&http.Client{Timeout: 0}` and rely solely on context deadlines. A slow DNS resolution or TCP handshake would only be caught by the overall request timeout, not a dedicated connect timeout.

**Fix:** Configure `http.Transport.DialContext` with a timeout derived from `AdapterTimeout.Connect` when an `AdapterTimeout` is provided.

### GAP 8: AdapterTimeout.StreamRead is never enforced

**Severity:** Medium
**Location:** `internal/llm/adapter_timeout.go`, `internal/llm/sse.go`

The `StreamRead` field exists and has a default of 30s, but it is never enforced. The `ApplyAdapterTimeout()` function explicitly says "stream_read is checked per-event" in its comment, but neither the SSE parser nor any adapter code implements per-event read timeouts.

The SSE parser (`ParseSSE` in sse.go) does a blocking `br.ReadString('\n')` with no timeout between lines. If a provider stops sending data mid-stream, the client will hang indefinitely (until the parent context expires, if one is set).

**Fix:** Either wrap the reader with a deadline that resets on each successful read, or implement a timer in the SSE parse loop that fires `StreamRead` seconds after the last received event.

### GAP 9: Streaming adapters do not call ApplyAdapterTimeout

**Severity:** Medium
**Location:** All adapter `Stream()` methods

All four adapters call `ApplyAdapterTimeout(ctx, req.AdapterTimeout, false)` in their `Complete()` methods but none call it in their `Stream()` methods. The `ApplyAdapterTimeout` function with `streaming=true` currently returns the context unchanged anyway (since only `Request` is enforced for non-streaming), but the adapters should still call it for consistency and to enable future streaming timeout support.

Specific locations:
- `providers/openai/adapter.go` `Stream()` -- no AdapterTimeout call
- `providers/anthropic/adapter.go` `Stream()` -- no AdapterTimeout call
- `providers/google/adapter.go` `Stream()` -- no AdapterTimeout call
- `providers/openaicompat/adapter.go` `Stream()` -- no AdapterTimeout call

**Fix:** Add `ApplyAdapterTimeout(ctx, req.AdapterTimeout, true)` calls to all adapter `Stream()` methods. This is a no-op today but ensures the plumbing exists when `Connect` and `StreamRead` enforcement is added.

### GAP 10: openai-compat adapter ignores reasoning_effort, metadata, and web_search

**Severity:** Low
**Location:** `internal/llm/providers/openaicompat/adapter.go` `buildRequestBody()` (lines 288-335)

The openai-compatible adapter does not propagate several Request fields to the API call:
- `reasoning_effort` -- silently ignored
- `metadata` -- silently ignored
- `web_search` -- silently ignored (though this is arguably expected since compat endpoints typically don't support web search)

The other three adapters (OpenAI, Anthropic, Google) all propagate these fields. While compat endpoints may not support all features, silently dropping them violates least-surprise. At minimum, `reasoning_effort` should be passed through since some compat endpoints (e.g., vLLM with reasoning models) may support it.

**Fix:** Pass through `reasoning_effort` and `metadata` fields in the openai-compat adapter. Web search can remain omitted with a comment explaining why.

---

## Full Gap Summary

| # | Gap | Severity | Location | Spec Section |
|---|-----|----------|----------|-------------|
| 1 | WebSearch field missing from GenerateOptions | Medium | generate.go, stream_generate.go | 4.3 |
| 2 | StreamGenerate ignores StopWhen | Medium | stream_generate.go | 4.4 |
| 3 | StreamResult lacks TotalUsage tracking | Low | stream_generate.go | 4.3 / 4.4 |
| 4 | StreamResult lacks Steps tracking | Low | stream_generate.go | 4.3 / 4.4 |
| 5 | Anthropic adapter ignores reasoning_effort | Low | providers/anthropic/adapter.go | 4.3 |
| 6 | StreamAccumulator ignores reasoning and tool calls | Medium | stream_accumulator.go | 4.4 |
| 7 | AdapterTimeout.Connect never enforced | Medium | adapter_timeout.go, all adapters | 4.7 |
| 8 | AdapterTimeout.StreamRead never enforced | Medium | adapter_timeout.go, sse.go | 4.7 |
| 9 | Streaming adapters skip ApplyAdapterTimeout | Medium | all adapter Stream() methods | 4.7 |
| 10 | openai-compat drops reasoning_effort and metadata | Low | providers/openaicompat/adapter.go | 4.3 |

**Compliant areas:**
- Client.complete() and Client.stream() low-level API
- Generate() core pipeline (prompt standardization, tool loop, max_tool_rounds, retries)
- GenerateResult and StepResult field completeness
- generate_object() with schema validation and provider strategies
- stream_object() with incremental JSON parsing
- Abort signal support via context.Context
- TimeoutConfig (total/per_step) enforcement
- AdapterTimeout type definition and defaults
