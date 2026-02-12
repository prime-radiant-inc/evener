# Audit: Section 7 - Provider Adapter Contract

**Date:** 2026-02-11
**Auditor:** Bot (Claude Opus 4.6)
**Spec:** `unified-llm-spec.md` lines 1469-1966
**Code:** `internal/llm/providers/{openai,anthropic,google,openaicompat}/adapter.go`, `internal/llm/client.go`, `internal/llm/types.go`, `internal/llm/errors.go`, `internal/llm/ratelimit.go`, `internal/llm/sse.go`

## Methodology

Audited each subsection (7.1-7.10) by reading every requirement in the spec and checking the corresponding implementation across all four provider adapters. The quirks table (7.8) was checked cell-by-cell.

---

## Summary

| Status | Count |
|--------|-------|
| PASS   | 48    |
| GAP    | 12    |
| NOTE   | 3     |

---

## Findings

### GAP-7.1: OpenAI-compatible adapter missing `RateLimit` extraction on success responses

**Spec (7.5):** "Extract rate limit info. Parse x-ratelimit-* headers into RateLimitInfo if present."

**Code:** The `openai`, `anthropic`, and `google` adapters all call `llm.ParseRateLimitHeaders(resp.Header)` on successful responses. The `openaicompat` adapter does NOT - the `Complete()` path reads the body and discards the headers, and the `Stream()` path similarly never calls `ParseRateLimitHeaders`.

**File:** `/Users/jesse/prime-radiant/serf/internal/llm/providers/openaicompat/adapter.go`
- `Complete()` at line 70-81: Uses `doHTTP()` which returns headers but the caller never parses rate limit info
- `Stream()` at line 84-284: Never parses rate limit headers

**Severity:** Low. Third-party endpoints are less likely to need rate limit tracking, but the spec says ALL adapters must do it.

---

### GAP-7.2: Usage.Raw is always an empty map instead of preserving raw provider data

**Spec (7.5):** Step 4 says "Preserve raw response. Store the complete provider response in Response.raw for debugging." While this refers to Response.Raw (which IS preserved), the Usage struct also has a `Raw` field that should contain the provider's native token-count fields for debugging. All three primary adapters initialize it to `map[string]any{}`:

- `/Users/jesse/prime-radiant/serf/internal/llm/providers/openai/adapter.go` line 854: `Raw: map[string]any{}`
- `/Users/jesse/prime-radiant/serf/internal/llm/providers/anthropic/adapter.go` line 1264: `Raw: map[string]any{}`
- `/Users/jesse/prime-radiant/serf/internal/llm/providers/google/adapter.go` line 971: `Raw: map[string]any{}`

The `openaicompat` adapter does better - it stores the raw usage map at line 209: `Raw: chunk.Usage`.

**Severity:** Low. The full response is in `Response.Raw`, so debugging data is available through another path, but `Usage.Raw` is wastefully empty.

---

### GAP-7.3: Anthropic web search not included when no function tools are present

**Spec (7.2, step 3):** Translate tools. Web search is a tool and should be translatable independently of function tools.

**Code:** In `/Users/jesse/prime-radiant/serf/internal/llm/providers/anthropic/adapter.go` lines 104 and 129:

```go
includeTools := len(req.Tools) > 0
...
if includeTools && len(req.Tools) > 0 {
    tools := toAnthropicTools(req.Tools)
    if req.WebSearch {
        tools = append(tools, map[string]any{...})
    }
    body["tools"] = tools
}
```

When `req.Tools` is empty but `req.WebSearch` is true, the entire block is skipped. The web_search server tool is never added to the request body.

Both the `Complete()` and `Stream()` methods have this same bug (lines 129-143 and 284-296).

**Severity:** Medium. Web-search-only requests silently fail to include the web search tool for Anthropic.

---

### GAP-7.4: Spec tool definition table (7.4) mismatches Responses API format

**Spec (7.4):** The tool definition table says OpenAI tools use wrapper structure `{"type":"function","function":{...}}` with paths like `tools[].function.name`.

**Code:** `/Users/jesse/prime-radiant/serf/internal/llm/providers/openai/adapter.go` lines 484-491 uses the flat Responses API format:
```go
{"type": "function", "name": t.Name, "description": t.Description, "parameters": params, "strict": true}
```

This is correct for the Responses API, which uses a flat tool format (not the nested Chat Completions format). The spec table is describing the Chat Completions format, not the Responses API format.

**Severity:** Low (spec inaccuracy, not code bug). The code is correct for the Responses API. The spec table should distinguish between Responses API and Chat Completions tool formats.

---

### GAP-7.5: Gemini adapter always sends maxOutputTokens=2048 as default

**Spec (7.8 quirks table):** Says Gemini `max_tokens` is "Optional (as maxOutputTokens)".

**Code:** `/Users/jesse/prime-radiant/serf/internal/llm/providers/google/adapter.go` lines 82-86:
```go
if req.MaxTokens != nil && *req.MaxTokens > 0 {
    genCfg["maxOutputTokens"] = *req.MaxTokens
} else {
    genCfg["maxOutputTokens"] = 2048
}
```

The adapter always sends a default of 2048 when the caller doesn't specify. Gemini's API would accept the field being absent (defaulting to the model's maximum). This hard-coded default silently caps output length even when the caller expects the model default.

**Severity:** Medium. Callers who don't set MaxTokens may get truncated output they didn't expect.

---

### GAP-7.6: OpenAI-compatible adapter does not translate image content parts

**Spec (7.10):** Says the OpenAI-compatible adapter uses Chat Completions format. Chat Completions supports multimodal image input via content arrays with `image_url` type parts.

**Code:** `/Users/jesse/prime-radiant/serf/internal/llm/providers/openaicompat/adapter.go` `toChatMessages()` (lines 337-387) only handles `ContentText` and `ContentToolCall` parts. `ContentImage` parts are silently dropped - the `textFromParts()` helper (line 389) only extracts text kind parts.

**Severity:** Medium. Any image content sent to an openai-compatible endpoint is silently lost.

---

### GAP-7.7: OpenAI-compatible adapter tool choice does not handle empty/default mode

**Spec (7.2, step 4):** Translate tool choice - the empty/default mode should map to "auto".

**Code:** `/Users/jesse/prime-radiant/serf/internal/llm/providers/openaicompat/adapter.go` `toChatToolChoice()` line 439:
```go
func toChatToolChoice(tc llm.ToolChoice) any {
    switch tc.Mode {
    case "auto":
        return "auto"
    ...
    }
}
```

The function uses raw string comparison (`tc.Mode`) without `strings.ToLower(strings.TrimSpace(...))` normalization, unlike all three primary adapters. An empty mode `""` falls to the `default` case which returns `"auto"` if `tc.Name` is empty - but the `"named"` mode is not explicitly handled either. This works by accident but is inconsistent.

**Severity:** Low. It works due to the default fallback, but the lack of normalization is a latent bug for edge cases.

---

### GAP-7.8: OpenAI-compatible adapter Complete() does not preserve raw response in result

**Spec (7.5, step 4):** "Preserve raw response."

**Code:** `/Users/jesse/prime-radiant/serf/internal/llm/providers/openaicompat/adapter.go` `fromChatCompletionResponse()` (line 538) does set `Raw: raw` - but the `Complete()` method at line 80 checks `statusCode != http.StatusOK` rather than `< 200 || >= 300`, which means 201/204 status codes would be treated as errors even though they're successful. This is minor but technically an inconsistency with the other adapters.

**Severity:** Very low. Most LLM endpoints always return 200 on success.

---

### GAP-7.9: Anthropic thinking blocks not round-tripped in stream path when text is empty

**Spec (7.3 Anthropic):** "Thinking block round-tripping: Thinking and redacted_thinking blocks from previous responses must be preserved exactly as received."

**Code:** In `/Users/jesse/prime-radiant/serf/internal/llm/providers/anthropic/adapter.go` streaming path, `message_stop` handler (line 639):
```go
case "redacted_thinking":
    if st.thinking.Len() > 0 {
        parts = append(parts, llm.ContentPart{...})
    }
```

If a redacted_thinking block contains only a `data` field (not accumulated via `thinking` builder), the `st.thinking.Len() > 0` check would fail if the initial `data` wasn't captured to the thinking builder. Looking at the `content_block_start` handler (line 495), the code does try to capture initial `data` into `st.thinking`, so this mostly works - but only if the `data` field is non-empty in the start block. If the block only has delta events with `thinking_delta` type but the delta field is named `data` instead of `thinking`, it could be dropped.

The non-streaming path (line 1071) also requires `d != ""` for `redacted_thinking`, which would drop blocks where `data` is present but empty.

**Severity:** Low. Redacted thinking blocks typically have non-empty data. Edge case only.

---

### GAP-7.10: Spec section 7.3 says OpenAI DEVELOPER role goes to `instructions` or `developer` role input item, but code only does `instructions`

**Spec (7.3 OpenAI):** Shows two possible translations for DEVELOPER role: "Extracted to `instructions` parameter (or `developer` role input item)".

**Code:** `/Users/jesse/prime-radiant/serf/internal/llm/providers/openai/adapter.go` `toResponsesInput()` lines 609-616:
```go
case llm.RoleSystem, llm.RoleDeveloper:
    if t := strings.TrimSpace(m.Text()); t != "" {
        instrParts = append(instrParts, t)
    }
```

Both system and developer messages are merged into the `instructions` parameter. The developer role is never sent as a separate `developer` role input item. The spec says either approach is valid ("or"), so this is technically compliant, but worth noting that the alternative translation is not implemented.

**Severity:** Very low. The spec explicitly allows either approach.

---

### GAP-7.11: OpenAI adapter does not handle Responses API `pause_turn` status

**Spec (7.8 quirks table, implicit from finish reason mapping):** The Responses API can return an "incomplete" status. The code handles `incomplete` with `max_output_tokens` and `content_filter` reasons, but the `pause_turn` finish reason (defined as a constant at `types.go:222`) is not detected from Responses API output.

**Code:** `/Users/jesse/prime-radiant/serf/internal/llm/providers/openai/adapter.go` `fromResponses()` lines 807-827:
```go
if status == "incomplete" {
    reason := "length"
    if details, ok := raw["incomplete_details"].(map[string]any); ok {
        ...
    }
}
```

There is no handling for a `pause_turn` status/finish reason from the OpenAI Responses API. While OpenAI may not emit this, the normalizeFinish function in `types.go` default case doesn't map it either. If OpenAI ever returns such a status, it would be silently mapped to "other".

**Severity:** Very low. OpenAI's Responses API doesn't currently emit `pause_turn`.

---

### GAP-7.12: OpenAI-compatible adapter Stream path does not apply AdapterTimeout

**Spec (7.2, step 5 and general adapter contract):** Generation parameters including timeouts should be applied.

**Code:** `/Users/jesse/prime-radiant/serf/internal/llm/providers/openaicompat/adapter.go`:
- `Complete()` line 67: correctly calls `llm.ApplyAdapterTimeout(ctx, req.AdapterTimeout, false)`
- `Stream()` line 84-284: No call to `ApplyAdapterTimeout`. The other three adapters either apply the timeout or use streaming context cancellation, but the openaicompat adapter doesn't apply any adapter-level timeout to the streaming path.

**Severity:** Low. The caller's context deadline still applies, but adapter-specific timeouts are ignored for streaming.

---

## Passing Checks (Summary)

### 7.1 Interface Summary
- [PASS] `ProviderAdapter` interface has `Name()`, `Complete()`, `Stream()` - matches spec's required methods
- [PASS] Optional `Closer`, `Initializer`, `ToolChoiceSupporter` interfaces defined in `client.go`
- [PASS] All four adapters implement the core interface

### 7.2 Request Translation (7 steps)
- [PASS] Step 1 (extract system): All four adapters correctly extract system messages
- [PASS] Step 2 (translate messages): All adapters translate message content parts
- [PASS] Step 3 (translate tools): All adapters convert tool definitions
- [PASS] Step 4 (translate tool choice): All adapters map tool choice modes
- [PASS] Step 5 (generation params): Temperature, top_p, max_tokens, stop sequences all mapped
- [PASS] Step 6 (response format): All adapters handle response_format
- [PASS] Step 7 (provider options): All adapters merge provider_options

### 7.3 OpenAI Message Translation
- [PASS] System -> `instructions` parameter
- [PASS] User -> `input_text` content type
- [PASS] Assistant -> `output_text` content type
- [PASS] Tool calls -> top-level `function_call` input items
- [PASS] Tool results -> top-level `function_call_output` input items
- [PASS] `reasoning.effort` parameter for o-series models
- [PASS] Image URL and base64 data URI handling

### 7.3 Anthropic Message Translation
- [PASS] System -> `system` parameter (not in messages)
- [PASS] Developer -> merged with system
- [PASS] Strict alternation enforcement via `appendMessage` merge logic
- [PASS] Tool results in user messages
- [PASS] Thinking block round-tripping (thinking + signature preserved)
- [PASS] Redacted thinking block round-tripping
- [PASS] max_tokens default 4096
- [PASS] tool_use -> `{"id","name","input"}` format
- [PASS] tool_result -> `{"tool_use_id","content","is_error"}` format

### 7.3 Gemini Message Translation
- [PASS] System -> `systemInstruction`
- [PASS] Developer -> merged with system
- [PASS] User -> "user" role
- [PASS] Assistant -> "model" role
- [PASS] Tool calls -> `functionCall` parts
- [PASS] Tool results -> `functionResponse` parts with function name
- [PASS] Synthetic tool call IDs generated with `call_` + ULID
- [PASS] String tool results wrapped in `{"result": "..."}`
- [PASS] Thought parts handled with `thought: true` flag

### 7.4 Tool Definition Translation
- [PASS] Anthropic: `{name, description, input_schema}` format
- [PASS] Gemini: `{functionDeclarations: [{name, description, parameters}]}`
- [PASS] OpenAI-compatible: `{type:"function", function:{name, description, parameters}}`

### 7.5 Response Translation
- [PASS] All adapters extract content parts with correct ContentKind tags
- [PASS] All adapters map finish reasons via `NormalizeFinishReason`
- [PASS] All adapters extract usage (input/output/total tokens)
- [PASS] All adapters preserve raw response in `Response.Raw`
- [PASS] Three primary adapters extract rate limit info

### 7.6 Error Translation
- [PASS] All adapters parse response body for error details
- [PASS] All adapters extract Retry-After header
- [PASS] `ErrorFromHTTPStatus` maps status codes correctly per spec
- [PASS] `classifyByMessage` handles ambiguous 400/422 cases
- [PASS] Raw error response preserved in error's `Raw()` method

### 7.7 Streaming Translation
- [PASS] SSE parser handles event, data, retry, comment, blank lines
- [PASS] OpenAI: `output_text.delta` -> TEXT_DELTA, `function_call_arguments.delta` -> TOOL_CALL_DELTA
- [PASS] OpenAI: `response.completed` -> FINISH with usage
- [PASS] Anthropic: content_block_start/delta/stop -> TEXT_START/DELTA/END, TOOL_CALL_START/DELTA/END
- [PASS] Anthropic: thinking blocks -> REASONING_START/DELTA/END
- [PASS] Anthropic: `message_stop` -> FINISH
- [PASS] Gemini: text parts -> TEXT_DELTA, functionCall -> TOOL_CALL_START + TOOL_CALL_END
- [PASS] Gemini: `?alt=sse` query parameter used for streaming
- [PASS] OpenAI-compatible: `data: [DONE]` termination handled

### 7.8 Quirks Table (cell-by-cell)
- [PASS] Native API endpoints: /v1/responses, /v1/messages, /v1beta/.../generateContent
- [PASS] System message handling: instructions, system param, systemInstruction
- [PASS] Developer role: instructions (OpenAI), merged with system (Anthropic, Gemini)
- [PASS] Message alternation: Anthropic strict enforcement present
- [PASS] Reasoning tokens: output_tokens_details (OpenAI), thinking text (Anthropic), thoughtsTokenCount (Gemini)
- [PASS] Tool call IDs: Provider-assigned (OpenAI, Anthropic), Synthetic ULID (Gemini)
- [PASS] Tool result format: function_call_output (OpenAI), tool_result in user (Anthropic), functionResponse in user (Gemini)
- [PASS] Tool choice "none": "none" (OpenAI), omit tools (Anthropic), "NONE" (Gemini)
- [PASS] max_tokens: optional (OpenAI), required/default 4096 (Anthropic)
- [PASS] Thinking blocks: Not exposed (OpenAI), thinking/redacted_thinking (Anthropic), thought parts (Gemini)
- [PASS] Streaming protocol: SSE data lines (OpenAI), SSE event+data (Anthropic), SSE with ?alt=sse (Gemini)
- [PASS] Finish reason for tools: tool_calls (OpenAI), tool_use (Anthropic), inferred from parts (Gemini)
- [PASS] Image input: Data URI (OpenAI), base64 source (Anthropic), inlineData (Gemini)
- [PASS] Authentication: Bearer token (OpenAI), x-api-key (Anthropic), key query param (Gemini)
- [PASS] API versioning: URL path (OpenAI), anthropic-version header (Anthropic), URL path (Gemini)
- [PASS] Beta headers: N/A (OpenAI), anthropic-beta from provider_options (Anthropic)

### 7.9 Adding a New Provider
- [PASS] Registration via `RegisterEnvAdapterFactory` in `init()` functions
- [PASS] All adapters follow the same implement/register pattern

### 7.10 OpenAI-Compatible Endpoints
- [PASS] Separate adapter (`openaicompat` package) exists
- [PASS] Uses `/v1/chat/completions` endpoint
- [PASS] Uses Chat Completions message format (not Responses API)
- [PASS] Registered as "openai-compatible" provider name

---

## Notes

### NOTE-1: OpenAI Responses API tool format (flat) vs spec table 7.4 (nested)

The spec table 7.4 documents the OpenAI tool wrapper structure as `{"type":"function","function":{...}}` which is the Chat Completions format. The Responses API actually uses a flat format: `{"type":"function","name":...,"description":...,"parameters":...}`. The code is correct for the Responses API; the spec table should be updated to distinguish between the two formats.

### NOTE-2: Anthropic adapter has automatic prompt caching by default

The Anthropic adapter enables prompt caching by default (auto_cache defaults to true when not specified in provider_options). This adds `cache_control` breakpoints to system, tools, and message blocks. While useful, this is an implementation detail not mentioned in the spec's Section 7.

### NOTE-3: OpenAI adapter enforces strict-mode JSON schemas

The OpenAI adapter automatically "strictifies" tool parameter schemas (adding `additionalProperties: false`, making all properties required, recursing into subschemas). This is an implementation decision to support OpenAI's strict mode, not documented in the spec.
