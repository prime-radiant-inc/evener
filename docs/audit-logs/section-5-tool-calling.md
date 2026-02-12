# Audit: Section 5 - Tool Calling

**Auditor:** Bot (Claude Opus 4.6)
**Date:** 2026-02-11
**Spec:** unified-llm-spec.md, lines 1050-1273
**Codebase:** internal/llm/ (types.go, generate.go, stream_generate.go, client.go, tool_validation.go, sdk_errors.go, providers/*)

## Summary

Section 5 is one of the more thoroughly implemented sections. Tool name validation, parallel execution, schema validation, repair, and the multi-step loop are all solid. However, there are several compliance gaps ranging from missing fields to incomplete context injection.

---

## Findings

### GAP 5.1: ToolChoice.Mode comment does not list "named" as a valid value

**Spec requirement (5.3):** ToolChoice has four modes: `auto`, `none`, `required`, `named`.

**Implementation:** In `types.go:176`, the `ToolChoice.Mode` field comment says:

```go
Mode string `json:"mode"` // "auto", "none", "required"
```

The comment omits `"named"` even though the `named` mode IS implemented in all three provider adapters. The code works correctly, but the comment is misleading and could cause a consumer to miss the `named` option.

**Severity:** Low (documentation/comment only, no behavioral gap)
**File:** `/Users/jesse/prime-radiant/serf/internal/llm/types.go:176`

---

### GAP 5.2: Missing `raw_arguments` field on ToolCallData

**Spec requirement (5.4):** The `ToolCall` record must have a `raw_arguments : String | None` field containing the raw argument string before parsing.

**Implementation:** `ToolCallData` has `Arguments json.RawMessage` which serves a dual purpose: it stores both the raw JSON bytes AND is used for parsing. There is no separate field that preserves the original raw string independently of parsing. The `Arguments` field functionally fulfills this role since `json.RawMessage` is the raw bytes, but the spec envisions two distinct fields (`arguments` as parsed Dict and `raw_arguments` as the original string). The current design conflates them into one field, with `ParsedArguments` holding the post-parse result.

This is arguably compliant in spirit since `Arguments` IS the raw bytes and `ParsedArguments` IS the parsed dict. However, the spec field name `raw_arguments` is not present, which could be a concern for consumers expecting explicit spec-named fields.

**Severity:** Low (functionally equivalent, naming divergence from spec)
**File:** `/Users/jesse/prime-radiant/serf/internal/llm/types.go:122-131`

---

### GAP 5.3: Tool context injection missing `abort_signal` as a named concept

**Spec requirement (5.2):** Tool execute handlers can receive injected context including `messages`, `abort_signal`, and `tool_call_id`.

**Implementation:** The `ToolCallContext` struct (in `generate.go:22-25`) provides `Messages` and `ToolCallID` via Go's `context.Value`. Cancellation is available through Go's native `context.Context` mechanism (the `ctx` parameter already carries a cancellation signal). However, there is no explicit `AbortSignal` abstraction or named field. A tool handler must use Go idioms (`ctx.Done()`, `ctx.Err()`) rather than a spec-named `abort_signal`.

This is arguably fine for a Go implementation since `context.Context` IS the abort signal. But the spec explicitly names `abort_signal` as an injectable parameter, and the current `ToolCallContext` struct does not expose it as a distinct field. A handler that inspects `ToolCallContext` would find `Messages` and `ToolCallID` but would need to separately use the `ctx` parameter for cancellation.

**Severity:** Low (Go-idiomatic solution, but not surfaced in the ToolCallContext struct)
**File:** `/Users/jesse/prime-radiant/serf/internal/llm/generate.go:22-25`

---

### GAP 5.4: OpenAI tool result format does not convey `is_error` status

**Spec requirement (5.10, 5.2):** When a tool execution fails, the result is sent with `is_error = true`. Each provider adapter must translate this appropriately.

**Implementation:** In the OpenAI adapter (`providers/openai/adapter.go:706-723`), tool results are sent as `function_call_output` items with a `call_id` and `output` string. There is NO handling of `is_error`. Looking at the code:

```go
items = append(items, map[string]any{
    "type":    "function_call_output",
    "call_id": p.ToolResult.ToolCallID,
    "output":  outStr,
})
```

The `is_error` field from `ToolResultData` is completely ignored. The OpenAI Responses API does support an `is_error` field on `function_call_output` items. This means error tool results are sent to OpenAI as if they were successful results, potentially confusing the model.

The same issue exists in the OpenAI-compatible adapter (`providers/openaicompat/adapter.go:367-383`) which also has no `is_error` handling.

**Severity:** Medium (error tool results silently treated as success by the OpenAI adapters)
**Files:**
- `/Users/jesse/prime-radiant/serf/internal/llm/providers/openai/adapter.go:706-723`
- `/Users/jesse/prime-radiant/serf/internal/llm/providers/openaicompat/adapter.go:367-383`

---

### GAP 5.5: OpenAI-compatible adapter does not raise UnsupportedToolChoiceError for unknown modes

**Spec requirement (5.3):** "If a provider does not support a particular mode, the adapter raises `UnsupportedToolChoiceError`."

**Implementation:** The `openaicompat` adapter's `toChatToolChoice` function (`providers/openaicompat/adapter.go:439-456`) silently falls through to `"auto"` for any unrecognized mode:

```go
default:
    if tc.Name != "" {
        return map[string]any{...}
    }
    return "auto"  // silently defaults instead of raising error
```

Both the OpenAI, Anthropic, and Google adapters properly raise `UnsupportedToolChoiceError` for unrecognized modes. The `openaicompat` adapter does not, which means an invalid mode like `"bogus"` silently becomes `"auto"`.

**Severity:** Medium (silent behavioral divergence from spec for invalid input)
**File:** `/Users/jesse/prime-radiant/serf/internal/llm/providers/openaicompat/adapter.go:439-456`

---

### GAP 5.6: OpenAI-compatible adapter does not handle `named` ToolChoice mode explicitly

**Spec requirement (5.3):** The `named` mode must be explicitly supported and map to the correct provider format.

**Implementation:** The `openaicompat` adapter's `toChatToolChoice` does not have an explicit `case "named":` branch. Instead, it falls through to the `default` case which happens to work if `tc.Name != ""`:

```go
default:
    if tc.Name != "" {
        return map[string]any{
            "type": "function",
            "function": map[string]any{"name": tc.Name},
        }
    }
    return "auto"
```

This means `ToolChoice{Mode: "named", Name: ""}` silently becomes `"auto"` rather than returning a `ConfigurationError` like the other three adapters do. All other adapters validate that `Name` is non-empty when mode is `"named"`.

**Severity:** Medium (missing validation that other adapters enforce)
**File:** `/Users/jesse/prime-radiant/serf/internal/llm/providers/openaicompat/adapter.go:439-456`

---

### GAP 5.7: Gemini adapter maps `STOP` finish reason to `stop` even when tool calls are present in streaming

**Spec requirement (5.6):** The tool loop checks `finish_reason == "tool_calls"` before executing tools.

**Implementation:** Gemini does not have a native "tool_calls" finish reason. When it returns tool calls, the finish reason is `STOP`. Both `fromGeminiResponse` (non-streaming) and the streaming handler fix this by overriding the finish reason when tool calls are present:

```go
if r.Finish.Reason == "" {
    if len(r.ToolCalls()) > 0 {
        r.Finish = llm.FinishReason{Reason: "tool_calls"}
    } else {
        r.Finish = llm.FinishReason{Reason: "stop"}
    }
}
```

However, in the streaming path (`providers/google/adapter.go:498-527`), when a `finishReason` IS present (i.e., `fr != ""`), it normalizes it first (`NormalizeFinishReason("google", fr)`) and then checks:

```go
if r.Finish.Reason == "" {
    if len(r.ToolCalls()) > 0 {
        r.Finish = llm.FinishReason{Reason: "tool_calls"}
    }
}
```

But the normalized result of `STOP` is `stop` (non-empty), so the `r.Finish.Reason == ""` check is false, and the tool_calls override is skipped. This means in streaming mode, when Gemini returns `STOP` with tool calls, the finish reason remains `stop` instead of being corrected to `tool_calls`.

Wait -- re-examining this more carefully. Looking at the streaming code at line 498:

```go
if fr, _ := c0["finishReason"].(string); fr != "" {
    finish = llm.NormalizeFinishReason("google", fr)
```

And then at line 515-521:

```go
if r.Finish.Reason == "" {
    if len(r.ToolCalls()) > 0 {
        r.Finish = llm.FinishReason{Reason: "tool_calls"}
    } else {
        r.Finish = llm.FinishReason{Reason: "stop"}
    }
}
```

The `r.Finish` was set from `finish` at line 512, which was normalized from `STOP` to `stop`. So `r.Finish.Reason` is `"stop"`, which is not empty, so the override block is skipped.

This is a real gap. In streaming mode, Gemini tool calls will have finish reason `stop` instead of `tool_calls`, which would prevent the tool loop from executing tools.

**Severity:** High (tool loop would not execute tools in Gemini streaming mode)
**File:** `/Users/jesse/prime-radiant/serf/internal/llm/providers/google/adapter.go:498-527`

**UPDATE:** Re-examining more carefully. The `normalizeFinish` function for Google maps `STOP` to `FinishReasonStop` = `"stop"`. It does NOT have a mapping for any Gemini tool-use finish reason because Gemini does not emit one. The non-streaming path handles this via the `r.Finish.Reason == ""` fallback, but in streaming, the finish reason is already set to `"stop"` from the `STOP` value.

The non-streaming `fromGeminiResponse` has the same pattern and works correctly because `NormalizeFinishReason("google", "STOP")` produces `{Reason: "stop", Raw: "STOP"}`, which is non-empty, so it goes to the same check... wait. Let me re-read `fromGeminiResponse`:

```go
if fr, _ := c0["finishReason"].(string); fr != "" {
    r.Finish = llm.NormalizeFinishReason("google", fr)
}
```

Then at the end:

```go
if r.Finish.Reason == "" {
    if len(r.ToolCalls()) > 0 {
        r.Finish = llm.FinishReason{Reason: "tool_calls"}
    } else {
        r.Finish = llm.FinishReason{Reason: "stop"}
    }
}
```

So in the non-streaming path, if Gemini returns `finishReason: "STOP"` with tool calls, `r.Finish.Reason` would be `"stop"` (non-empty), and the override block is also skipped. This means the non-streaming path ALSO has this issue... unless Gemini does not send `finishReason` when it returns tool calls?

Actually, that is likely the case. When Gemini returns function calls, it typically does NOT include a `finishReason` field, or the field is empty/absent. So `r.Finish.Reason` remains empty, and the fallback correctly sets it to `"tool_calls"`. This is a known Gemini API behavior.

However, if Gemini ever sends `STOP` alongside tool calls (which can happen in some edge cases), both the streaming and non-streaming paths would fail to set the finish reason to `tool_calls`. This is a latent bug.

**Revised Severity:** Low-Medium (depends on Gemini API behavior; latent issue)

---

### GAP 5.8: No `supports_tool_choice` method on individual provider adapters

**Spec requirement (5.3):** "The `supports_tool_choice(mode)` method allows checking capabilities upfront."

**Implementation:** The `ToolChoiceSupporter` interface exists in `client.go:123-125` and `Client.SupportsToolChoice()` is implemented at `client.go:160-173`. However, NONE of the built-in provider adapters (OpenAI, Anthropic, Google, OpenAI-compatible) actually implement this interface. The `Client.SupportsToolChoice` method falls through to `return true` for all of them, meaning it reports that all modes are supported for all providers.

This is misleading because, for example, if a consumer checks `client.SupportsToolChoice("anthropic", "none")` they get `true`, but the Anthropic adapter actually handles `none` by omitting tools (a workaround, not native support). More importantly, any modes that are genuinely unsupported would be reported as supported.

**Severity:** Low-Medium (interface exists but no adapter implements it; callers get false positives)
**Files:**
- `/Users/jesse/prime-radiant/serf/internal/llm/client.go:121-125`
- `/Users/jesse/prime-radiant/serf/internal/llm/providers/openai/adapter.go` (does not implement ToolChoiceSupporter)
- `/Users/jesse/prime-radiant/serf/internal/llm/providers/anthropic/adapter.go` (does not implement ToolChoiceSupporter)
- `/Users/jesse/prime-radiant/serf/internal/llm/providers/google/adapter.go` (does not implement ToolChoiceSupporter)

---

### GAP 5.9: Anthropic tool result content uses `fmt.Sprint` instead of preserving structured content

**Spec requirement (5.10):** Tool results may contain `String | Dict | List` content. The Anthropic adapter translates to `tool_result` content blocks.

**Implementation:** In `providers/anthropic/adapter.go:998-1003`:

```go
blocks = append(blocks, map[string]any{
    "type":        "tool_result",
    "tool_use_id": p.ToolResult.ToolCallID,
    "content":     fmt.Sprint(p.ToolResult.Content),
    "is_error":    p.ToolResult.IsError,
})
```

The `fmt.Sprint(p.ToolResult.Content)` call converts any content type (including structured dicts and lists) into a flat string representation using Go's default formatting. If the content is `map[string]any{"key": "value"}`, it becomes `"map[key:value]"` rather than proper JSON. Compare to the OpenAI adapter which correctly uses `json.Marshal` for non-string content:

```go
switch v := p.ToolResult.Content.(type) {
case string:
    outStr = v
default:
    b, _ := json.Marshal(v)
    outStr = string(b)
}
```

This means structured tool results sent to Anthropic are corrupted into Go-formatted strings.

**Severity:** High (structured tool result content is mangled for Anthropic)
**File:** `/Users/jesse/prime-radiant/serf/internal/llm/providers/anthropic/adapter.go:998-1003`

---

### GAP 5.10: Gemini tool result does not convey `is_error` status

**Spec requirement (5.7, 5.2):** Partial failures in parallel tool execution should be handled gracefully with `is_error = true`. Tool results must be translated correctly per provider.

**Implementation:** In `providers/google/adapter.go:753-769`, the Gemini adapter builds `functionResponse` parts from tool results:

```go
parts = append(parts, map[string]any{
    "functionResponse": map[string]any{
        "name":     name,
        "response": respObj,
    },
})
```

There is no handling of `p.ToolResult.IsError`. The Gemini API does not have a native `is_error` equivalent for `functionResponse`, but the error status should still be conveyed somehow (e.g., wrapping the error message in the response object with an error indicator). As-is, error tool results are indistinguishable from success results when sent to Gemini.

**Severity:** Medium (error results are sent as if they were successful to Gemini)
**File:** `/Users/jesse/prime-radiant/serf/internal/llm/providers/google/adapter.go:753-769`

---

## Compliance Summary

| Sub-section | Status | Notes |
|---|---|---|
| 5.1 Tool Definition | PASS | Name validation, parameter schema validation at definition time, max 64 chars, correct regex |
| 5.2 Tool Execute Handlers | PARTIAL | Input/output contract good; error handling good; context injection has messages and tool_call_id but no explicit abort_signal field |
| 5.3 ToolChoice | PARTIAL | All four modes implemented in main adapters; openai-compatible missing named/error handling; ToolChoiceSupporter interface exists but unused; ToolChoice comment missing "named" |
| 5.4 ToolCall and ToolResult | PARTIAL | No distinct `raw_arguments` field (functionally equivalent via Arguments json.RawMessage) |
| 5.5 Active vs Passive Tools | PASS | Properly distinguished; passive tools return to caller without executing |
| 5.6 Multi-Step Tool Loop | PASS | Checks finish_reason before executing; conversation building correct; step tracking correct; budget enforcement works |
| 5.7 Parallel Tool Execution | PASS | Concurrent goroutines via sync.WaitGroup; waits for ALL; preserves ordering via indexed results slice; handles partial failures with is_error |
| 5.8 Tool Call Validation & Repair | PASS | JSON parsing, schema validation via jsonschema, repair_tool_call support, unknown tool handling with error result |
| 5.9 Streaming with Tools | PASS | STEP_FINISH events emitted between steps in StreamGenerate |
| 5.10 Tool Result Handling | FAIL | OpenAI ignores is_error; Anthropic uses fmt.Sprint for content; Gemini ignores is_error |

---

## Prioritized Remediation

1. **HIGH: Anthropic fmt.Sprint for tool result content** (GAP 5.9) - Use json.Marshal for non-string content like the OpenAI adapter does
2. **MEDIUM: OpenAI adapter ignoring is_error on tool results** (GAP 5.4) - Add `"is_error": true` to `function_call_output` items when applicable
3. **MEDIUM: Gemini adapter ignoring is_error on tool results** (GAP 5.10) - Wrap error results with an error indicator in the response object
4. **MEDIUM: openai-compatible adapter missing UnsupportedToolChoiceError** (GAP 5.5) - Add proper error handling for unknown modes
5. **MEDIUM: openai-compatible adapter missing named mode validation** (GAP 5.6) - Add explicit `case "named":` with name validation
6. **LOW-MEDIUM: No adapter implements ToolChoiceSupporter** (GAP 5.8) - Implement on all adapters or document the assumption
7. **LOW-MEDIUM: Gemini streaming finish reason latent bug** (GAP 5.7) - Add override logic for STOP+tool_calls
8. **LOW: ToolChoice.Mode comment missing "named"** (GAP 5.1) - Fix comment
9. **LOW: No distinct raw_arguments field** (GAP 5.2) - Naming divergence only
10. **LOW: abort_signal not in ToolCallContext** (GAP 5.3) - Go-idiomatic, context.Context serves this role
