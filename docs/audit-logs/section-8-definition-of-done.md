# Section 8: Definition of Done -- Audit Report

**Date:** 2026-02-11
**Auditor:** Bot (Claude Opus 4.6)
**Spec:** unified-llm-spec.md, Section 8 (lines 1967-2153)
**Codebase:** internal/llm/ and providers/

---

## Summary

Total checklist items: ~117 (7 + 30 + 7 + 10 + 6 + 9 + 11 + 9 + 45 + 6 = 140 cells, some overlap)
- PASS: ~104
- FAIL: 9
- PARTIAL: 4

---

## 8.1 Core Infrastructure

| # | Item | Status | Evidence |
|---|------|--------|----------|
| 1 | Client from env vars (`NewFromEnv()`) | PASS | `env_registry.go:31` -- `NewFromEnv()` constructs client from registered factories. All three providers register via `init()`. |
| 2 | Client programmatic construction | PASS | `client.go:20-32` -- `NewClient()` + `Register()` takes explicit adapter instances. |
| 3 | Provider routing by `provider` field | PASS | `client.go:53-64` -- dispatches to correct adapter by name, with `normalizeProviderName` for gemini->google. |
| 4 | Default provider when omitted | PASS | `client.go:54-56` -- falls back to `c.defaultProvider`, set to first registered adapter. |
| 5 | ConfigurationError when no provider | PASS | `client.go:57-59` -- returns `ConfigurationError` when prov is empty. Test: `TestClient_NoProviderConfiguredError`. |
| 6 | Middleware chain order | PASS | `middleware.go:38-58` -- reverse iteration for wrapping gives registration-order request and reverse-order response. Test: `TestClient_MiddlewareChainOrder`, `TestClient_Stream_MiddlewareChainOrder`. |
| 7 | Module-level default client (`SetDefaultClient` + lazy init) | PASS | `env_registry.go:59-87` -- `SetDefaultClient()` and `DefaultClient()` with lazy `NewFromEnv()`. |
| 8 | Model catalog populated | PASS | `model_catalog_embedded.go` -- embedded LiteLLM catalog; `model_catalog.go` provides `GetModelInfo()`, `ListModels()`, `GetLatestModel()`. Tests in `model_catalog_test.go`. |

**8.1 Gaps: None**

---

## 8.2 Provider Adapters

### OpenAI

| # | Item | Status | Evidence |
|---|------|--------|----------|
| 1 | Native API (Responses API) | PASS | `openai/adapter.go:151` -- posts to `/v1/responses`. |
| 2 | Authentication | PASS | `openai/adapter.go:40-56` -- reads `OPENAI_API_KEY` from env. `setHeaders` sets Bearer token. |
| 3 | `Complete()` returns correct Response | PASS | `openai/adapter.go:75-178` -- `fromResponses()` maps output items to unified `Response`. |
| 4 | `Stream()` returns StreamEvent objects | PASS | `openai/adapter.go:180-452` -- full SSE parsing with all event types. |
| 5 | System messages extracted | PASS | `openai/adapter.go:607-616` -- system/developer messages concatenated into `instructions`. |
| 6 | All 5 roles translated | PASS | `openai/adapter.go:609-726` -- system, developer, user, assistant, tool all handled. |
| 7 | `provider_options` escape hatch | PASS | `openai/adapter.go:135-141` -- merges `provider_options.openai` into body. Test: `TestAdapter_ProviderOptions_PassThrough`. |
| 8 | Beta headers | **FAIL** | OpenAI adapter does not support beta headers via `provider_options`. The spec calls for beta header support "especially Anthropic's", but the OpenAI adapter has no mechanism to pass custom headers via provider options. Only `DefaultHeaders` on the struct are supported, which is not accessible via `provider_options`. |
| 9 | HTTP errors translated | PASS | `openai/adapter.go:170-173` -- `ErrorFromHTTPStatus("openai", ...)`. Test: `TestAdapter_Complete_Error429_MapsToRateLimitError`. |
| 10 | Retry-After parsed | PASS | `openai/adapter.go:170` -- `ParseRetryAfter(resp.Header...)`. Test: verifies RetryAfter on error. |

### Anthropic

| # | Item | Status | Evidence |
|---|------|--------|----------|
| 1 | Native API (Messages API) | PASS | `anthropic/adapter.go:169` -- posts to `/v1/messages`. |
| 2 | Authentication | PASS | `anthropic/adapter.go:39-52` -- reads `ANTHROPIC_API_KEY`. Sets `x-api-key` header. |
| 3 | `Complete()` returns correct Response | PASS | `anthropic/adapter.go:57-206` -- `fromAnthropicResponse()` maps content blocks to unified format. |
| 4 | `Stream()` returns StreamEvent objects | PASS | `anthropic/adapter.go:208-729` -- full SSE parsing with text, tool, thinking blocks. |
| 5 | System messages extracted | PASS | `anthropic/adapter.go:806-833` -- system/developer roles collected into `sysParts` string, sent as `system` field. |
| 6 | All 5 roles translated | PASS | `anthropic/adapter.go:828-1008` -- system, developer, user, assistant, tool all handled (tool->user with tool_result). |
| 7 | `provider_options` escape hatch | PASS | `anthropic/adapter.go:144-156` -- merges `provider_options.anthropic` into body. Test: `TestAdapter_ProviderOptions_PassThrough`. |
| 8 | Beta headers supported | PASS | `anthropic/adapter.go:180-186, 756-788` -- `betaHeaderFromProviderOptions()` reads `provider_options.anthropic.beta_headers`. Test: `TestAdapter_Complete_MapsToMessagesAPI_AndSetsBetaHeaders`. |
| 9 | HTTP errors translated | PASS | `anthropic/adapter.go:197-201` -- `ErrorFromHTTPStatus("anthropic", ...)`. |
| 10 | Retry-After parsed | PASS | `anthropic/adapter.go:198` -- `ParseRetryAfter(resp.Header...)`. Test confirms. |

### Gemini (Google)

| # | Item | Status | Evidence |
|---|------|--------|----------|
| 1 | Native API (Gemini API) | PASS | `google/adapter.go:180` -- posts to `v1beta/models/{model}:generateContent`. |
| 2 | Authentication | PASS | `google/adapter.go:41-59` -- reads `GEMINI_API_KEY` / `GOOGLE_API_KEY`. API key in query string. |
| 3 | `Complete()` returns correct Response | PASS | `google/adapter.go:64-217` -- `fromGeminiResponse()` maps candidates to unified format. |
| 4 | `Stream()` returns StreamEvent objects | PASS | `google/adapter.go:219-547` -- full SSE parsing with text, tool, thinking parts. |
| 5 | System messages extracted | PASS | `google/adapter.go:591-607` -- system/developer roles collected into `systemInstruction`. |
| 6 | All 5 roles translated | PASS | `google/adapter.go:603-774` -- system, developer (->system), user, assistant (->model), tool (->user with functionResponse). |
| 7 | `provider_options` escape hatch | PASS | `google/adapter.go:159-170` -- merges both `provider_options.google` and `provider_options.gemini`. Test: `TestAdapter_ProviderOptions_PassThrough`. |
| 8 | Beta headers | **FAIL** | Gemini adapter has no beta header support. The API key goes in the query string, and there is no mechanism for custom headers via `provider_options`. `DefaultHeaders` on the struct is only available programmatically, not via provider_options. |
| 9 | HTTP errors translated | PASS | `google/adapter.go:208-211` -- `ErrorFromHTTPStatus("google", ...)`. |
| 10 | Retry-After parsed | PASS | `google/adapter.go:209` -- `ParseRetryAfter(resp.Header...)`. |

**8.2 Gaps:**
1. **OpenAI beta headers via provider_options** -- No mechanism to set arbitrary HTTP headers through `provider_options`. Only struct-level `DefaultHeaders` is available.
2. **Gemini beta headers via provider_options** -- Same issue. No headers passthrough for Gemini.

---

## 8.3 Message & Content Model

| # | Item | Status | Evidence |
|---|------|--------|----------|
| 1 | Text-only messages across providers | PASS | All three adapters handle `ContentText`. Integration tests confirm. |
| 2 | Image input (URL, base64, local file) | PASS | All three adapters handle `ContentImage` with URL, Data (base64), and local file paths (`IsLocalPath` + `ExpandTilde`). Integration tests: `TestIntegration_ImageInput`, `TestIntegration_ImageInputURL`. |
| 3 | Audio and document gracefully rejected | PASS | All three adapters return `ConfigurationError` for `ContentAudio` and `ContentDocument`. |
| 4 | Tool call content round-trip | PASS | All adapters translate `ContentToolCall` and `ContentToolResult` correctly. Unit tests confirm round-trip in `generate_test.go`. |
| 5 | Thinking blocks preserved with signatures | PASS | Anthropic adapter: `fromAnthropicResponse()` preserves `signature` field on thinking blocks. `toAnthropicMessages()` replays `thinking` and `signature`. |
| 6 | Redacted thinking blocks passed verbatim | PASS | Anthropic adapter: `ContentRedThinking` mapped to/from `redacted_thinking` type with `data` field. |
| 7 | Multimodal messages (text + images) | PASS | All adapters build multi-part content arrays supporting text + image in same message. Integration test `TestIntegration_ImageInput` sends text + image together. |

**8.3 Gaps: None**

---

## 8.4 Generation

| # | Item | Status | Evidence |
|---|------|--------|----------|
| 1 | `generate()` with simple text `prompt` | PASS | `generate.go:141-142` -- Prompt converted to User message. Test: `TestGenerate_SimplePrompt`. |
| 2 | `generate()` with full `messages` list | PASS | `generate.go:143-144` -- Messages used directly. Test: `TestGenerate_MessagesList`. |
| 3 | Rejects prompt + messages together | PASS | `generate.go:134-136` -- Returns ConfigurationError. Test: `TestGenerate_RejectsPromptAndMessagesTogether`. |
| 4 | `stream()` yields TEXT_DELTA events | PASS | All three adapters emit `StreamEventTextDelta`. Integration: `TestIntegration_Streaming`. |
| 5 | `stream()` yields STREAM_START and FINISH | PASS | All adapters emit `StreamEventStreamStart` before data and `StreamEventFinish` at end. |
| 6 | Start/delta/end pattern for text | PASS | All adapters emit `TextStart` -> `TextDelta`* -> `TextEnd` sequence. |
| 7 | `generate_object()` returns parsed output | PASS | `generate_object.go:20-58` -- Parses JSON, validates against schema, sets `res.Output`. Integration: `TestIntegration_StructuredOutput`. |
| 8 | `generate_object()` raises NoObjectGeneratedError | PASS | `generate_object.go:46-54` -- Returns `NewNoObjectGeneratedError` on parse/validation failure. Unit test: `TestGenerateObject_ParseError`, `TestGenerateObject_ValidationError` in `generate_object_test.go`. |
| 9 | Cancellation via abort signal | PASS | `generate.go:121,188` -- context passed through. Test: `TestGenerate_Cancellation_ReturnsAbortError`. |
| 10 | Timeouts (total and per-step) | PASS | `generate.go:121,188` -- `WithTimeout` for both. Tests: `TestGenerate_TimeoutPerStep_CancelsLLMCall`, `TestGenerate_TimeoutTotal_CancelsOperation`. |

**8.4 Gaps: None**

---

## 8.5 Reasoning Tokens

| # | Item | Status | Evidence |
|---|------|--------|----------|
| 1 | OpenAI reasoning models return `reasoning_tokens` via Responses API | PASS | `openai/adapter.go:856-859` -- `parseUsage` reads `output_tokens_details.reasoning_tokens`. Test: `TestAdapter_Complete_Usage_MapsReasoningAndCacheTokens`. |
| 2 | `reasoning_effort` passed through to OpenAI | PASS | `openai/adapter.go:126-128` -- maps to `reasoning: {effort: ...}`. Test in `adapter_test.go` line 62. |
| 3 | Anthropic thinking blocks as THINKING content parts | PASS | `anthropic/adapter.go:1054-1069` -- `thinking` type mapped to `ContentThinking`. Streaming also emits `ReasoningStart/Delta/End`. |
| 4 | Thinking block `signature` preserved | PASS | `anthropic/adapter.go:1060-1061` -- `sig` field preserved. `toAnthropicMessages` replays it at line 966. |
| 5 | Gemini `thoughtsTokenCount` mapped to `reasoning_tokens` | PASS | `google/adapter.go:976-978` -- `parseUsage` reads `thoughtsTokenCount`. Test: `TestAdapter_Complete_Usage_MapsReasoningAndCacheTokens`. |
| 6 | `reasoning_tokens` distinct from `output_tokens` | PASS | `types.go:271` -- `ReasoningTokens` is a separate `*int` field. Test: `TestParseUsage_ReasoningTokensDistinctFromOutputTokens`. |

**8.5 Gaps: None**

---

## 8.6 Prompt Caching

| # | Item | Status | Evidence |
|---|------|--------|----------|
| 1 | OpenAI: caching automatic via Responses API | PASS | No client-side cache config needed. Responses API handles it. |
| 2 | OpenAI: `cache_read_tokens` from `cached_tokens` | PASS | `openai/adapter.go:860-863` -- reads `input_tokens_details.cached_tokens`. |
| 3 | Anthropic: auto `cache_control` on system, tools, conversation | PASS | `anthropic/adapter.go:84-89` (system), `140-141` (tools), `1188-1244` (conversation prefix via `addCacheControlBreakpoint`). |
| 4 | Anthropic: `prompt-caching-2024-07-31` beta header auto-included | PASS | `anthropic/adapter.go:181-183` -- `appendBetaHeader(beta, "prompt-caching-2024-07-31")` when autoCache. |
| 5 | Anthropic: `cache_read_tokens` and `cache_write_tokens` | PASS | `anthropic/adapter.go:1266-1273` -- reads `cache_read_input_tokens` and `cache_creation_input_tokens`. |
| 6 | Anthropic: auto caching can be disabled | PASS | `anthropic/adapter.go:1139-1160` -- `anthropicAutoCacheEnabled()` checks `provider_options.anthropic.auto_cache`. |
| 7 | Gemini: automatic prefix caching | PASS | No client-side config needed. Gemini handles prefix caching automatically. |
| 8 | Gemini: `cache_read_tokens` from `cachedContentTokenCount` | PASS | `google/adapter.go:973-975` -- reads `cachedContentTokenCount`. |
| 9 | Multi-turn session: cache_read > 50% by turn 5+ | **PARTIAL** | Integration test `TestIntegration_PromptCaching_MultiTurn` exists and tests this for OpenAI and Anthropic. However, Gemini is **explicitly skipped** (lines 480-481: "Gemini's cachedContentTokenCount only reflects explicit CachedContent resources, not implicit prompt caching"). The test acknowledges this is a platform limitation, not a code gap. |

**8.6 Gaps:**
1. **Gemini multi-turn cache ratio not verified** -- Gemini's implicit prefix caching does not report `cachedContentTokenCount`, so the test skips the assertion. This is a known platform limitation.

---

## 8.7 Tool Calling

| # | Item | Status | Evidence |
|---|------|--------|----------|
| 1 | Active tools trigger auto execution loops | PASS | `generate.go:213-275` -- tool loop continues when `hasActiveTool` and calls present. Test: `TestGenerate_ToolLoop_ExecutesToolsAndContinues`. |
| 2 | Passive tools return calls without looping | PASS | `generate.go:233-248` -- checks `t.Execute == nil` to break. Test: `TestGenerate_PassiveToolCall_ReturnsToolCallsWithoutLooping`. |
| 3 | `max_tool_rounds` respected | PASS | `generate.go:126-131, 213` -- checks `toolRoundsUsed >= maxToolRounds`. Test: `TestGenerate_MultiStepToolLoop_ThreeRounds`. |
| 4 | `max_tool_rounds = 0` disables auto execution | PASS | `generate.go:213` -- `maxToolRounds == 0` exits loop. Test: `TestGenerate_MaxToolRoundsZero_DisablesAutoExecution`. |
| 5 | Parallel tool calls: N calls executed concurrently | PASS | `generate.go:317-372` -- `sync.WaitGroup` with goroutines per call. Test: `TestGenerate_ParallelToolCalls_ExecuteConcurrently`. |
| 6 | Parallel results sent in single continuation | PASS | `generate.go:256-258` -- all results appended to history before next loop iteration. Tested via `TestGenerate_ToolLoop_ExecutesToolsAndContinues`. |
| 7 | Tool errors sent as `is_error = true` | PASS | `generate.go:360-365` -- on `Execute` error, `r.IsError = true`. Test: `TestGenerate_ToolExecuteError_SentAsIsError`. |
| 8 | Unknown tool calls send error result | PASS | `generate.go:331-337` -- `unknown tool: %s`. Test: `TestGenerate_UnknownToolCall_SendsErrorResultToModel`. |
| 9 | ToolChoice modes translated per provider | PASS | OpenAI: `toResponsesToolChoice` (auto/none/required/named). Anthropic: auto/none(omit tools)/required(any)/named(tool). Gemini: AUTO/NONE/ANY/named. |
| 10 | Tool call args JSON parsed and validated | PASS | `generate.go:339,374-388` -- `parseAndValidateArgs` with jsonschema. Test: `TestGenerate_ToolArgsSchemaValidationError_SentAsErrorResult_AndDoesNotExecute`. |
| 11 | `StepResult` objects track calls, results, usage | PASS | `generate.go:84-93,202-211` -- `StepResult` has `ToolCalls`, `ToolResults`, `Usage`. Multiple tests verify. |

**8.7 Gaps: None**

---

## 8.8 Error Handling & Retry

| # | Item | Status | Evidence |
|---|------|--------|----------|
| 1 | Correct error types for HTTP status codes | PASS | `errors.go:95-138` -- comprehensive switch on status code. 400/422->InvalidRequest (with message classification), 401->Authentication, 403->AccessDenied, 404->NotFound, 408->RequestTimeout, 413->ContextLength, 429->RateLimit, 5xx->ServerError. Tests in `errors_test.go`. |
| 2 | `retryable` flag set correctly | PASS | `errors.go:107-136` -- 401/403/404 non-retryable, 408/429/5xx retryable. Tests verify. |
| 3 | Exponential backoff with jitter | PASS | `retry_util.go:89-123` -- `math.Pow(mult, n)` with `[0.5, 1.5]` jitter factor. Test: `TestRetry_ExponentialBackoff`, `TestRetry_Jitter`. |
| 4 | Retry-After overrides calculated backoff | PASS | `retry_util.go:91-102` -- uses `e.RetryAfter()` when present. Test: `TestRetry_RetryAfterHeader_OverridesCalculatedBackoff`. |
| 5 | `max_retries = 0` disables retries | PASS | `retry_util.go:58-62` -- `maxRetries < 0` clamped to 0. When 0, loop runs once (`attempt <= 0`). Test: `TestRetry_MaxRetriesZero`. |
| 6 | Rate limit (429) retried transparently | PASS | `errors.go:129` -- 429 is `retryable: true`. `Retry()` checks `retryableError()`. |
| 7 | Non-retryable (401/403/404) raised immediately | PASS | `errors.go:113-119` -- all `retryable: false`. `retry_util.go:71` -- exits on non-retryable. Test: `TestRetry_NonRetryable_RaisedImmediately`. |
| 8 | Retries per-step, not entire operation | PASS | `generate.go:189-191` -- `Retry()` wraps each individual `client.Complete()` call. Test: `TestGenerate_RetriesApplyPerStep_NotWholeOperation`. |
| 9 | Streaming does not retry after partial data | PASS | `stream_generate.go:216-219` -- on `StreamEventError` after partial data, returns immediately. Test: `TestStreamGenerate_DoesNotRetryAfterPartialDataDelivered`. |

**8.8 Gaps: None**

---

## 8.9 Cross-Provider Parity

Each cell is an integration test that must pass for all three providers.

| Test Case | OpenAI | Anthropic | Gemini | Evidence |
|-----------|--------|-----------|--------|----------|
| Simple text generation | PASS | PASS | PASS | `TestIntegration_BasicGeneration` -- all 3 providers |
| Streaming text generation | PASS | PASS | PASS | `TestIntegration_Streaming` -- all 3 providers |
| Image input (base64) | PASS | PASS | PASS | `TestIntegration_ImageInput` -- all 3 providers |
| Image input (URL) | PASS | PASS | PASS | `TestIntegration_ImageInputURL` -- all 3 providers |
| Single tool call + execution | PASS | PASS | PASS | `TestIntegration_ToolCalling` -- all 3 providers (passive mode) |
| **Multiple parallel tool calls** | **FAIL** | **FAIL** | **FAIL** | No integration test covers multiple parallel tool calls across all three providers. `TestGenerate_ParallelToolCalls_ExecuteConcurrently` is a unit test with a mock adapter, not an integration test. |
| **Multi-step tool loop (3+ rounds)** | **FAIL** | **FAIL** | **FAIL** | No integration test covers multi-step tool loops (3+ rounds) across providers. `TestGenerate_MultiStepToolLoop_ThreeRounds` is a unit test with mocks. |
| Streaming with tool calls | PASS | PASS | PASS | `TestIntegration_StreamingWithTools` -- all 3 providers |
| Structured output (generate_object) | PASS | PASS | PASS | `TestIntegration_StructuredOutput` -- all 3 providers |
| **Reasoning/thinking token reporting** | **FAIL** | **FAIL** | **FAIL** | No integration test verifies reasoning/thinking token reporting across providers. Unit tests exist per-adapter but no cross-provider integration test. |
| Error handling (invalid key -> 401) | **FAIL** | **FAIL** | **FAIL** | No integration test for invalid API key / 401 errors. `TestIntegration_ErrorHandling` tests nonexistent models (404 or InvalidRequest), not authentication failures. |
| **Error handling (rate limit -> 429)** | **FAIL** | **FAIL** | **FAIL** | No integration test for rate limit 429 errors across providers. Unit tests in per-adapter test files test 429 mapping, but no cross-provider integration test. |
| Usage token counts accurate | PASS | PASS | PASS | `TestIntegration_BasicGeneration` asserts `InputTokens > 0` and `OutputTokens > 0`. |
| Prompt caching (cache_read > 0 on turn 2+) | PASS | PASS | PARTIAL | `TestIntegration_PromptCaching_MultiTurn` tests all 3 but skips cache ratio assertion for Gemini. |
| **Provider-specific options pass through** | **FAIL** | **FAIL** | **FAIL** | No integration test verifies provider-specific options pass through for all three providers. Unit tests exist per-adapter but no cross-provider integration test. |

**8.9 Gaps (6 test gaps affecting 18 matrix cells):**
1. **Multiple parallel tool calls** -- No integration test.
2. **Multi-step tool loop (3+ rounds)** -- No integration test.
3. **Reasoning/thinking token reporting** -- No integration test.
4. **Error handling (invalid API key -> 401)** -- Not tested; existing test uses nonexistent model, not bad key.
5. **Error handling (rate limit -> 429)** -- No integration test (hard to trigger reliably).
6. **Provider-specific options pass through** -- No integration test.

---

## 8.10 Integration Smoke Test

| # | Scenario | Status | Evidence |
|---|----------|--------|----------|
| 1 | Basic generation across all providers | PASS | `TestIntegration_BasicGeneration` -- asserts non-empty text, positive usage, finish=stop (implicit). |
| 2 | Streaming | PASS | `TestIntegration_Streaming` -- verifies deltas concatenate to final text. |
| 3 | Tool calling with parallel execution | **PARTIAL** | `TestIntegration_StreamingWithTools` tests single-tool execution across providers with a streaming tool loop. However, the spec's smoke test asks for parallel tool calls (weather in SF AND NY). No integration test calls for >1 parallel tool call. |
| 4 | Image input | PASS | `TestIntegration_ImageInput` (base64) + `TestIntegration_ImageInputURL` (URL). |
| 5 | Structured output | PASS | `TestIntegration_StructuredOutput` -- schema-validated output. |
| 6 | Error handling | **PARTIAL** | `TestIntegration_ErrorHandling` uses nonexistent model which may return NotFound or InvalidRequest. Spec asks for `NotFoundError` specifically. Test allows both, which is reasonable, but doesn't test with an actual `provider="openai"` and a truly invalid model name to ensure `NotFoundError`. |

**8.10 Gaps:**
1. **Tool calling smoke test does not verify parallel execution** -- The spec test expects "What is the weather in San Francisco and New York?" prompting parallel tool calls. Existing test uses a single-city prompt.
2. **Error handling smoke test is lenient** -- Accepts `InvalidRequestError` in addition to `NotFoundError`, which may hide incorrect classification.

---

## Gap Summary

### Implementation Gaps (code changes needed)

| # | Section | Gap | Severity | Details |
|---|---------|-----|----------|---------|
| G1 | 8.2 | OpenAI beta headers via provider_options | Low | No way to pass HTTP headers through provider_options for OpenAI. DefaultHeaders on the struct works but is not the dynamic, per-request escape hatch the spec intends. |
| G2 | 8.2 | Gemini beta headers via provider_options | Low | Same as G1 for Gemini. |

### Test Coverage Gaps (test additions needed)

| # | Section | Gap | Severity | Details |
|---|---------|-----|----------|---------|
| G3 | 8.9 | No integration test for multiple parallel tool calls | Medium | Unit test exists but no cross-provider integration test with real APIs. |
| G4 | 8.9 | No integration test for multi-step tool loop (3+ rounds) | Medium | Unit test exists but no cross-provider integration test. |
| G5 | 8.9 | No integration test for reasoning/thinking token reporting | Medium | Per-adapter unit tests exist, but no integration test verifying across all providers. |
| G6 | 8.9 | No integration test for 401 auth error | Low | Difficult to test reliably without invalid keys, but spec requires it. |
| G7 | 8.9 | No integration test for 429 rate limit | Low | Hard to trigger on demand, but spec requires it. |
| G8 | 8.9 | No integration test for provider-specific options | Low | Unit tests per-adapter exist; no cross-provider integration test. |
| G9 | 8.10 | Integration smoke test does not verify parallel tool execution | Medium | Existing test uses single-city prompt, not the dual-city pattern spec calls for. |
| G10 | 8.6 | Gemini cache ratio not verifiable | Info | Platform limitation, not a code gap. Gemini does not report implicit cache hits in `cachedContentTokenCount`. Test correctly skips assertion. |
| G11 | 8.10 | Error handling smoke test overly lenient | Low | Accepts InvalidRequestError where spec expects NotFoundError. |
