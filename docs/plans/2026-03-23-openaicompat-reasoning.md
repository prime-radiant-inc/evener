# OpenAI-Compatible Adapter: Reasoning Content & Provider Quirk Support

> **For agentic workers:** REQUIRED SUB-SKILL: Use trycycle-executing to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend the `openaicompat` adapter to parse and emit `reasoning_content` (the de facto standard for reasoning output from Kimi K2.5, GLM 5.0, DeepSeek, QwQ, etc.) and add a per-provider quirk configuration system that handles locked hyperparameters, tool_choice restrictions, empty content stripping, non-standard finish reasons, and stop sequence limits.

**Architecture:** The `reasoning_content` field appears in both non-streaming assistant messages and streaming delta chunks, alongside the standard `content` field. We add parsing for this field into the existing `chatMessage`/`chatDelta` types, emit the unified `ContentThinking` / `StreamEventReasoning*` events that the rest of the codebase already understands, and serialize `ContentThinking` parts back as `reasoning_content` in outgoing assistant messages. Provider-specific quirks are expressed as a `ProviderQuirks` struct that `buildRequestBody` consults to strip/clamp/transform request parameters before serialization.

**Tech Stack:** Go, `net/http`, `encoding/json`, `httptest` (tests)

---

## Design Decisions

### D1: Why extend `openaicompat` instead of creating new provider adapters

The Kimi K2.5 and GLM 5.0 APIs are Chat Completions compatible. Their deviations are small, well-enumerated quirks — not fundamental protocol differences. Creating separate adapters would duplicate ~90% of the code. A quirk configuration system inside `openaicompat` is cleaner, has a single SSE parser to maintain, and naturally extends to future providers (DeepSeek R1, QwQ, etc.) that use the same `reasoning_content` pattern.

### D2: `reasoning_content` is parsed unconditionally, not gated by quirk config

The `reasoning_content` field is harmless when absent (just an empty string in the JSON). Parsing it unconditionally means any OpenAI-compatible provider that starts emitting reasoning content will work automatically — no configuration required. This matches how the adapter already handles optional fields like `tool_calls`.

### D3: Quirks are configured at `Adapter` construction time, not per-request

The quirks apply to the remote API endpoint, not to individual requests. Configuring them on the `Adapter` struct (populated from env vars or programmatic construction) keeps `buildRequestBody` simple and avoids per-request overhead. A new env var `OPENAI_COMPATIBLE_PROVIDER_QUIRKS` accepts a comma-separated list of named quirk presets (e.g., `kimi-k2.5`, `glm-5`) or individual quirk flags (e.g., `lock-temperature,lock-top-p,tool-choice-auto-only`).

### D4: Thinking parameter injection uses `ProviderOptions` passthrough

The Kimi `"thinking": {"type": "enabled"}` and GLM `"thinking": {"type": "enabled", "clear_thinking": true}` parameters are provider-specific. Rather than adding thinking-parameter-injection to the core quirks system, they flow through the existing `ProviderOptions["openai-compatible"]` passthrough mechanism. Users who want thinking enabled pass the appropriate JSON. This avoids the adapter needing to know the exact shape of every provider's thinking parameter. The plan includes documentation of this in the adapter's env var help text.

### D5: Non-standard finish reasons are mapped via a quirk-specific translation table

GLM returns `"sensitive"` and `"network_error"` as finish reasons. Rather than adding these to the global `NormalizeFinishReason` function (which would affect all providers), the quirk config includes a `FinishReasonMap` that the adapter consults before falling through to the default normalization. This keeps the mapping localized and explicit.

### D6: `reasoning_content` round-trip in assistant messages

When the adapter builds outgoing messages and encounters a `ContentThinking` part, it serializes it as `reasoning_content` on the assistant message. This is required because Kimi K2.5 documents that `reasoning_content` from assistant tool-call turns must be preserved in subsequent messages. GLM likely has the same requirement. The serialization is straightforward — a single field on the message JSON object.

### D7: Usage.ReasoningTokens estimation from reasoning_content

Both Kimi and GLM may include `completion_tokens_details.reasoning_tokens` in usage. The adapter checks for this first (native count). If absent but `reasoning_content` was present in the response, it estimates using the same chars/4 heuristic the Anthropic adapter uses. This ensures `Usage.ReasoningTokens` is always populated when reasoning happened.

---

## File Structure

| File | Responsibility |
|------|----------------|
| `llm/providers/openaicompat/adapter.go` | All changes: struct types, request building, response parsing, streaming, quirk system |
| `llm/providers/openaicompat/adapter_test.go` | All new tests: reasoning parsing, quirk behavior, round-tripping |

No new files are created. All changes land in the existing two files, following the established pattern.

---

### Task 1: Add `reasoning_content` to response type structs

**Files:**
- Modify: `llm/providers/openaicompat/adapter.go:617-651` (chatMessage, chatDelta types)

- [ ] **Step 1: Identify or write the failing test**

Write a test that sends a non-streaming request and receives a response with `reasoning_content` alongside `content`. Assert that the response contains a `ContentThinking` part with the reasoning text, followed by a `ContentText` part.

```go
func TestComplete_ReasoningContent_ParsedAsThinking(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        _, _ = w.Write([]byte(`{
  "id": "chatcmpl-rc1",
  "model": "kimi-k2.5",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "reasoning_content": "Let me think step by step...\nFirst, I need to consider...",
      "content": "The answer is 42."
    },
    "finish_reason": "stop"
  }],
  "usage": {"prompt_tokens": 10, "completion_tokens": 50, "total_tokens": 60}
}`))
    }))
    t.Cleanup(srv.Close)

    a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
    resp, err := a.Complete(context.Background(), llm.Request{
        Model:    "kimi-k2.5",
        Messages: []llm.Message{llm.User("What is the meaning of life?")},
    })
    if err != nil {
        t.Fatalf("Complete: %v", err)
    }

    // Should have thinking content first, then text.
    if len(resp.Message.Content) < 2 {
        t.Fatalf("expected at least 2 content parts, got %d: %+v", len(resp.Message.Content), resp.Message.Content)
    }
    if resp.Message.Content[0].Kind != llm.ContentThinking {
        t.Fatalf("first part kind: %v, want thinking", resp.Message.Content[0].Kind)
    }
    if resp.Message.Content[0].Thinking == nil || resp.Message.Content[0].Thinking.Text != "Let me think step by step...\nFirst, I need to consider..." {
        t.Fatalf("thinking text: %+v", resp.Message.Content[0].Thinking)
    }
    if resp.Message.Content[1].Kind != llm.ContentText {
        t.Fatalf("second part kind: %v, want text", resp.Message.Content[1].Kind)
    }
    if resp.Message.Content[1].Text != "The answer is 42." {
        t.Fatalf("text: %q", resp.Message.Content[1].Text)
    }
    if resp.ReasoningText() != "Let me think step by step...\nFirst, I need to consider..." {
        t.Fatalf("ReasoningText(): %q", resp.ReasoningText())
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestComplete_ReasoningContent_ParsedAsThinking -v ./llm/providers/openaicompat/`
Expected: FAIL — `chatMessage` struct has no `ReasoningContent` field; `fromChatCompletionResponse` does not parse it.

- [ ] **Step 3: Write minimal implementation**

Add `ReasoningContent` field to the `chatMessage` struct:

```go
type chatMessage struct {
    Role             string         `json:"role"`
    Content          string         `json:"content"`
    ReasoningContent string         `json:"reasoning_content,omitempty"`
    ToolCalls        []chatToolCall `json:"tool_calls,omitempty"`
}
```

Update `fromChatCompletionResponse` to emit a `ContentThinking` part before the text part when `ReasoningContent` is non-empty:

```go
// In fromChatCompletionResponse, replace the current parts-building block:
parts := []llm.ContentPart{}
if choice.Message.ReasoningContent != "" {
    parts = append(parts, llm.ContentPart{
        Kind:     llm.ContentThinking,
        Thinking: &llm.ThinkingData{Text: choice.Message.ReasoningContent},
    })
}
if choice.Message.Content != "" {
    parts = append(parts, llm.ContentPart{Kind: llm.ContentText, Text: choice.Message.Content})
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestComplete_ReasoningContent_ParsedAsThinking -v ./llm/providers/openaicompat/`
Expected: PASS

- [ ] **Step 5: Refactor and verify**

Run full suite to confirm no regression:
Run: `go test -count=1 ./llm/providers/openaicompat/`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git -C /home/user/code/serf/.worktrees/openaicompat-reasoning add llm/providers/openaicompat/adapter.go llm/providers/openaicompat/adapter_test.go
git -C /home/user/code/serf/.worktrees/openaicompat-reasoning commit -m "feat(openaicompat): parse reasoning_content in non-streaming responses"
```

---

### Task 2: Add `reasoning_content` streaming support

**Files:**
- Modify: `llm/providers/openaicompat/adapter.go:647-651` (chatDelta type), `adapter.go:191-343` (Stream goroutine)
- Test: `llm/providers/openaicompat/adapter_test.go`

- [ ] **Step 1: Identify or write the failing test**

Write a streaming test where the server returns chunks with `reasoning_content` deltas before `content` deltas. Assert that `REASONING_START`, `REASONING_DELTA`, `REASONING_END` events are emitted in the correct order, followed by `TEXT_START`, `TEXT_DELTA`, `TEXT_END`.

```go
func TestStream_ReasoningContent_EmitsReasoningEvents(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/event-stream")
        flusher, _ := w.(http.Flusher)

        chunks := []string{
            // Reasoning content deltas come first.
            `{"id":"chatcmpl-1","model":"kimi-k2.5","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"Let me "},"finish_reason":null}]}`,
            `{"id":"chatcmpl-1","model":"kimi-k2.5","choices":[{"index":0,"delta":{"reasoning_content":"think..."},"finish_reason":null}]}`,
            // Then text content.
            `{"id":"chatcmpl-1","model":"kimi-k2.5","choices":[{"index":0,"delta":{"content":"The answer"},"finish_reason":null}]}`,
            `{"id":"chatcmpl-1","model":"kimi-k2.5","choices":[{"index":0,"delta":{"content":" is 42."},"finish_reason":null}]}`,
            `{"id":"chatcmpl-1","model":"kimi-k2.5","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":20,"total_tokens":25}}`,
        }
        for _, c := range chunks {
            fmt.Fprintf(w, "data: %s\n\n", c)
            flusher.Flush()
        }
        fmt.Fprintf(w, "data: [DONE]\n\n")
        flusher.Flush()
    }))
    t.Cleanup(srv.Close)

    a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()

    st, err := a.Stream(ctx, llm.Request{
        Model:    "kimi-k2.5",
        Messages: []llm.Message{llm.User("What is the meaning of life?")},
    })
    if err != nil {
        t.Fatalf("Stream: %v", err)
    }
    defer st.Close()

    var kinds []llm.StreamEventType
    var reasoningDeltas []string
    var textDeltas []string
    for ev := range st.Events() {
        kinds = append(kinds, ev.Type)
        if ev.Type == llm.StreamEventReasoningDelta {
            reasoningDeltas = append(reasoningDeltas, ev.ReasoningDelta)
        }
        if ev.Type == llm.StreamEventTextDelta {
            textDeltas = append(textDeltas, ev.Delta)
        }
    }

    // Verify reasoning events.
    if strings.Join(reasoningDeltas, "") != "Let me think..." {
        t.Fatalf("reasoning deltas: %q", strings.Join(reasoningDeltas, ""))
    }
    if strings.Join(textDeltas, "") != "The answer is 42." {
        t.Fatalf("text deltas: %q", strings.Join(textDeltas, ""))
    }

    // Verify event ordering: REASONING_START before REASONING_DELTA, REASONING_END before TEXT_START.
    var reasoningStartIdx, reasoningEndIdx, textStartIdx int
    for i, k := range kinds {
        switch k {
        case llm.StreamEventReasoningStart:
            reasoningStartIdx = i
        case llm.StreamEventReasoningEnd:
            reasoningEndIdx = i
        case llm.StreamEventTextStart:
            textStartIdx = i
        }
    }
    if reasoningStartIdx >= reasoningEndIdx {
        t.Fatalf("REASONING_START (%d) should come before REASONING_END (%d)", reasoningStartIdx, reasoningEndIdx)
    }
    if reasoningEndIdx >= textStartIdx {
        t.Fatalf("REASONING_END (%d) should come before TEXT_START (%d)", reasoningEndIdx, textStartIdx)
    }

    // Verify the final response includes thinking content.
    var finalResp *llm.Response
    for ev := range st.Events() {
        if ev.Type == llm.StreamEventFinish && ev.Response != nil {
            finalResp = ev.Response
        }
    }
    // The stream is already drained. Re-check from kinds for FINISH.
    // Actually, we need to capture it in the loop above.
}
```

Note: The test above should be refined to capture the FINISH event's response in the main loop. The implementation step will correct the test to capture the final response properly.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestStream_ReasoningContent_EmitsReasoningEvents -v ./llm/providers/openaicompat/`
Expected: FAIL — `chatDelta` has no `ReasoningContent` field; streaming goroutine doesn't handle it.

- [ ] **Step 3: Write minimal implementation**

Add `ReasoningContent` to `chatDelta`:

```go
type chatDelta struct {
    Role             string              `json:"role"`
    Content          string              `json:"content"`
    ReasoningContent string              `json:"reasoning_content,omitempty"`
    ToolCalls        []chatChunkToolCall `json:"tool_calls,omitempty"`
}
```

In the streaming goroutine (inside the `ParseSSE` callback), add reasoning content handling before the text delta handling:

```go
// Track reasoning state alongside textStarted.
var reasoningStarted bool
var reasoningBuf strings.Builder

// Inside the SSE callback, after choice is extracted:

// Reasoning content delta (must be checked before text delta).
if choice.Delta.ReasoningContent != "" {
    if !reasoningStarted {
        s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningStart})
        reasoningStarted = true
    }
    reasoningBuf.WriteString(choice.Delta.ReasoningContent)
    s.Send(llm.StreamEvent{
        Type:           llm.StreamEventReasoningDelta,
        ReasoningDelta: choice.Delta.ReasoningContent,
    })
}

// Text delta — if reasoning was active and text is now starting, close reasoning first.
if choice.Delta.Content != "" {
    if reasoningStarted && !textStarted {
        s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningEnd})
    }
    if !textStarted {
        s.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: "text_0"})
        textStarted = true
    }
    textBuf.WriteString(choice.Delta.Content)
    s.Send(llm.StreamEvent{
        Type:   llm.StreamEventTextDelta,
        TextID: "text_0",
        Delta:  choice.Delta.Content,
    })
}
```

In the `[DONE]` handler, add reasoning content to the final response message and close reasoning if still open:

```go
// Before the existing text end/tool call end handling in [DONE]:
if reasoningStarted && !textStarted {
    // Reasoning was never followed by text — close it now.
    s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningEnd})
}

// In the final message building:
msg := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{}}
if reasoningBuf.Len() > 0 {
    msg.Content = append(msg.Content, llm.ContentPart{
        Kind:     llm.ContentThinking,
        Thinking: &llm.ThinkingData{Text: reasoningBuf.String()},
    })
}
if textBuf.Len() > 0 {
    msg.Content = append(msg.Content, llm.ContentPart{Kind: llm.ContentText, Text: textBuf.String()})
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestStream_ReasoningContent_EmitsReasoningEvents -v ./llm/providers/openaicompat/`
Expected: PASS

- [ ] **Step 5: Refactor and verify**

Clean up the test (remove the dead second `range st.Events()` loop, capture FINISH response in the main loop). Run full suite:
Run: `go test -count=1 ./llm/providers/openaicompat/`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git -C /home/user/code/serf/.worktrees/openaicompat-reasoning add llm/providers/openaicompat/adapter.go llm/providers/openaicompat/adapter_test.go
git -C /home/user/code/serf/.worktrees/openaicompat-reasoning commit -m "feat(openaicompat): emit reasoning events from streaming reasoning_content"
```

---

### Task 3: Round-trip `reasoning_content` in outgoing assistant messages

**Files:**
- Modify: `llm/providers/openaicompat/adapter.go:407-464` (toChatMessages, RoleAssistant case)
- Test: `llm/providers/openaicompat/adapter_test.go`

- [ ] **Step 1: Identify or write the failing test**

Write a test that sends a multi-turn conversation where a previous assistant message has `ContentThinking` parts. Assert that the outgoing JSON request includes `reasoning_content` on the assistant message.

```go
func TestComplete_ThinkingContentRoundTripped_AsReasoningContent(t *testing.T) {
    var gotBody map[string]any

    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        b, _ := io.ReadAll(r.Body)
        _ = json.Unmarshal(b, &gotBody)
        w.Header().Set("Content-Type", "application/json")
        _, _ = w.Write([]byte(`{
  "id": "chatcmpl-rt1",
  "model": "kimi-k2.5",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "OK"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 10, "completion_tokens": 2, "total_tokens": 12}
}`))
    }))
    t.Cleanup(srv.Close)

    a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
    _, err := a.Complete(context.Background(), llm.Request{
        Model: "kimi-k2.5",
        Messages: []llm.Message{
            llm.User("What is 2+2?"),
            {Role: llm.RoleAssistant, Content: []llm.ContentPart{
                {Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "I need to add 2 and 2."}},
                {Kind: llm.ContentText, Text: "The answer is 4."},
            }},
            llm.User("Are you sure?"),
        },
    })
    if err != nil {
        t.Fatalf("Complete: %v", err)
    }

    msgs, _ := gotBody["messages"].([]any)
    if len(msgs) != 3 {
        t.Fatalf("messages count: %d", len(msgs))
    }
    assistantMsg, _ := msgs[1].(map[string]any)
    rc, ok := assistantMsg["reasoning_content"].(string)
    if !ok || rc != "I need to add 2 and 2." {
        t.Fatalf("reasoning_content: %v (ok=%v)", assistantMsg["reasoning_content"], ok)
    }
    content, _ := assistantMsg["content"].(string)
    if content != "The answer is 4." {
        t.Fatalf("content: %v", assistantMsg["content"])
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestComplete_ThinkingContentRoundTripped_AsReasoningContent -v ./llm/providers/openaicompat/`
Expected: FAIL — `toChatMessages` ignores `ContentThinking` parts for assistant messages.

- [ ] **Step 3: Write minimal implementation**

In `toChatMessages`, update the `RoleAssistant` case to extract thinking content and serialize it as `reasoning_content`:

```go
case llm.RoleAssistant:
    msg := map[string]any{
        "role": "assistant",
    }
    text := textFromParts(m.Content)
    calls := toolCallsFromParts(m.Content)
    reasoning := thinkingFromParts(m.Content)
    if reasoning != "" {
        msg["reasoning_content"] = reasoning
    }
    if len(calls) > 0 {
        msg["tool_calls"] = calls
        if text != "" {
            msg["content"] = text
        }
    } else {
        msg["content"] = text
    }
    out = append(out, msg)
```

Add a helper function:

```go
func thinkingFromParts(parts []llm.ContentPart) string {
    var b strings.Builder
    for _, p := range parts {
        if p.Kind == llm.ContentThinking && p.Thinking != nil && p.Thinking.Text != "" {
            if b.Len() > 0 {
                b.WriteString("\n")
            }
            b.WriteString(p.Thinking.Text)
        }
    }
    return b.String()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestComplete_ThinkingContentRoundTripped_AsReasoningContent -v ./llm/providers/openaicompat/`
Expected: PASS

- [ ] **Step 5: Refactor and verify**

Run full suite:
Run: `go test -count=1 ./llm/providers/openaicompat/`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git -C /home/user/code/serf/.worktrees/openaicompat-reasoning add llm/providers/openaicompat/adapter.go llm/providers/openaicompat/adapter_test.go
git -C /home/user/code/serf/.worktrees/openaicompat-reasoning commit -m "feat(openaicompat): round-trip ContentThinking as reasoning_content in outgoing messages"
```

---

### Task 4: Parse `reasoning_tokens` from usage and estimate when absent

**Files:**
- Modify: `llm/providers/openaicompat/adapter.go:660-717` (fromChatCompletionResponse usage section), `adapter.go:264-276` (streaming usage)
- Test: `llm/providers/openaicompat/adapter_test.go`

- [ ] **Step 1: Identify or write the failing test**

Write two tests:
1. A test where usage includes `completion_tokens_details.reasoning_tokens` — assert `Usage.ReasoningTokens` is populated with the native value.
2. A test where usage does NOT include reasoning_tokens but the response has `reasoning_content` — assert `Usage.ReasoningTokens` is estimated from the content length.

```go
func TestComplete_ReasoningTokens_NativeFromUsage(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        _, _ = w.Write([]byte(`{
  "id": "chatcmpl-u1",
  "model": "kimi-k2.5",
  "choices": [{"index": 0, "message": {"role": "assistant", "reasoning_content": "thinking...", "content": "done"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 10, "completion_tokens": 50, "total_tokens": 60, "completion_tokens_details": {"reasoning_tokens": 35}}
}`))
    }))
    t.Cleanup(srv.Close)

    a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
    resp, err := a.Complete(context.Background(), llm.Request{
        Model:    "kimi-k2.5",
        Messages: []llm.Message{llm.User("think")},
    })
    if err != nil {
        t.Fatalf("Complete: %v", err)
    }
    if resp.Usage.ReasoningTokens == nil || *resp.Usage.ReasoningTokens != 35 {
        t.Fatalf("ReasoningTokens: %v, want 35", resp.Usage.ReasoningTokens)
    }
}

func TestComplete_ReasoningTokens_EstimatedFromContent(t *testing.T) {
    // 80 characters of reasoning = 80/4 = 20 estimated tokens.
    reasoning := strings.Repeat("abcd", 20) // 80 chars
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        fmt.Fprintf(w, `{
  "id": "chatcmpl-u2",
  "model": "glm-5",
  "choices": [{"index": 0, "message": {"role": "assistant", "reasoning_content": %q, "content": "done"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 10, "completion_tokens": 50, "total_tokens": 60}
}`, reasoning)
    }))
    t.Cleanup(srv.Close)

    a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
    resp, err := a.Complete(context.Background(), llm.Request{
        Model:    "glm-5",
        Messages: []llm.Message{llm.User("think")},
    })
    if err != nil {
        t.Fatalf("Complete: %v", err)
    }
    if resp.Usage.ReasoningTokens == nil || *resp.Usage.ReasoningTokens != 20 {
        t.Fatalf("ReasoningTokens: %v, want 20", resp.Usage.ReasoningTokens)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run "TestComplete_ReasoningTokens" -v ./llm/providers/openaicompat/`
Expected: FAIL — usage parsing doesn't check `completion_tokens_details.reasoning_tokens` and doesn't estimate.

- [ ] **Step 3: Write minimal implementation**

In `fromChatCompletionResponse`, after the existing usage parsing block, add reasoning token extraction:

```go
// After existing usage parsing:
if parsed.Usage != nil {
    // ... existing code ...

    // Extract native reasoning tokens from completion_tokens_details.
    if details, ok := parsed.Usage["completion_tokens_details"].(map[string]any); ok {
        if rt := llm.IntFromAny(details["reasoning_tokens"]); rt > 0 {
            resp.Usage.ReasoningTokens = &rt
        }
    }
}

// Estimate reasoning tokens from thinking content when not natively reported.
if resp.Usage.ReasoningTokens == nil {
    var thinkingChars int
    for _, p := range parts {
        if p.Kind == llm.ContentThinking && p.Thinking != nil {
            thinkingChars += len(p.Thinking.Text)
        }
    }
    if thinkingChars > 0 {
        estimated := thinkingChars / 4
        if estimated < 1 {
            estimated = 1
        }
        resp.Usage.ReasoningTokens = &estimated
    }
}
```

Apply the same logic to streaming usage: in the `[DONE]` handler, after building the `Usage`, check for `completion_tokens_details.reasoning_tokens` in `chunk.Usage`, and if not present, estimate from `reasoningBuf`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run "TestComplete_ReasoningTokens" -v ./llm/providers/openaicompat/`
Expected: PASS

- [ ] **Step 5: Refactor and verify**

Run full suite:
Run: `go test -count=1 ./llm/providers/openaicompat/`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git -C /home/user/code/serf/.worktrees/openaicompat-reasoning add llm/providers/openaicompat/adapter.go llm/providers/openaicompat/adapter_test.go
git -C /home/user/code/serf/.worktrees/openaicompat-reasoning commit -m "feat(openaicompat): extract and estimate reasoning_tokens in usage"
```

---

### Task 5: Add `ProviderQuirks` configuration system

**Files:**
- Modify: `llm/providers/openaicompat/adapter.go` (new type + NewFromEnv + buildRequestBody)
- Test: `llm/providers/openaicompat/adapter_test.go`

- [ ] **Step 1: Identify or write the failing test**

Write a test that configures `ProviderQuirks` with `LockTemperature: true` and `LockTopP: true`, sends a request with `Temperature` and `TopP` set, and asserts they are NOT present in the outgoing request body.

```go
func TestBuildRequestBody_QuirksLockTemperatureAndTopP(t *testing.T) {
    var gotBody map[string]any

    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        b, _ := io.ReadAll(r.Body)
        _ = json.Unmarshal(b, &gotBody)
        w.Header().Set("Content-Type", "application/json")
        _, _ = w.Write([]byte(`{
  "id": "chatcmpl-q1", "model": "kimi-k2.5",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "hi"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
}`))
    }))
    t.Cleanup(srv.Close)

    temp := 0.7
    topP := 0.9
    a := &Adapter{
        APIKey:  "k",
        BaseURL: srv.URL,
        Client:  srv.Client(),
        Quirks: ProviderQuirks{
            LockTemperature: true,
            LockTopP:        true,
        },
    }
    _, err := a.Complete(context.Background(), llm.Request{
        Model:       "kimi-k2.5",
        Messages:    []llm.Message{llm.User("hi")},
        Temperature: &temp,
        TopP:        &topP,
    })
    if err != nil {
        t.Fatalf("Complete: %v", err)
    }
    if _, ok := gotBody["temperature"]; ok {
        t.Fatalf("temperature should be stripped when locked, got %v", gotBody["temperature"])
    }
    if _, ok := gotBody["top_p"]; ok {
        t.Fatalf("top_p should be stripped when locked, got %v", gotBody["top_p"])
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestBuildRequestBody_QuirksLockTemperatureAndTopP -v ./llm/providers/openaicompat/`
Expected: FAIL — `ProviderQuirks` type doesn't exist, `Adapter` has no `Quirks` field.

- [ ] **Step 3: Write minimal implementation**

Define the `ProviderQuirks` struct and add it to `Adapter`:

```go
// ProviderQuirks configures per-provider behavioral overrides for OpenAI-compatible
// APIs that deviate from the standard Chat Completions contract.
type ProviderQuirks struct {
    // LockTemperature strips temperature from requests (provider fixes it).
    LockTemperature bool
    // LockTopP strips top_p from requests (provider fixes it).
    LockTopP bool
    // LockFrequencyPenalty strips frequency_penalty from requests.
    LockFrequencyPenalty bool
    // LockPresencePenalty strips presence_penalty from requests.
    LockPresencePenalty bool
    // ToolChoiceAutoOnly restricts tool_choice to "auto" or "none" (no "required" or named).
    ToolChoiceAutoOnly bool
    // MaxStopSequences limits the number of stop sequences (0 = unlimited).
    MaxStopSequences int
    // StripEmptyContent removes message content parts with empty text (e.g., GLM rejects them).
    StripEmptyContent bool
    // NoJSONSchema downgrades json_schema response_format to json_object (provider doesn't support it).
    NoJSONSchema bool
    // FinishReasonMap maps non-standard finish reasons to canonical values.
    // E.g., {"sensitive": "content_filter", "network_error": "error"}
    FinishReasonMap map[string]string
}
```

Add to `Adapter`:

```go
type Adapter struct {
    APIKey         string
    BaseURL        string
    Client         *http.Client
    DefaultHeaders map[string]string
    Quirks         ProviderQuirks
}
```

Thread `a.Quirks` into `buildRequestBody` by changing the signature:

```go
func buildRequestBody(req llm.Request, stream bool, quirks ProviderQuirks) (map[string]any, error) {
```

Apply quirks in `buildRequestBody`:

```go
// After building the base body, apply quirks.
if quirks.LockTemperature {
    delete(body, "temperature")
}
if quirks.LockTopP {
    delete(body, "top_p")
}
if quirks.LockFrequencyPenalty {
    delete(body, "frequency_penalty")
}
if quirks.LockPresencePenalty {
    delete(body, "presence_penalty")
}
```

Update all call sites of `buildRequestBody` to pass `a.Quirks`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestBuildRequestBody_QuirksLockTemperatureAndTopP -v ./llm/providers/openaicompat/`
Expected: PASS

- [ ] **Step 5: Refactor and verify**

Run full suite:
Run: `go test -count=1 ./llm/providers/openaicompat/`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git -C /home/user/code/serf/.worktrees/openaicompat-reasoning add llm/providers/openaicompat/adapter.go llm/providers/openaicompat/adapter_test.go
git -C /home/user/code/serf/.worktrees/openaicompat-reasoning commit -m "feat(openaicompat): add ProviderQuirks struct with locked hyperparameter support"
```

---

### Task 6: Implement remaining quirk behaviors (tool_choice, stop, empty content, json_schema, finish reasons)

**Files:**
- Modify: `llm/providers/openaicompat/adapter.go` (buildRequestBody, toChatMessages, fromChatCompletionResponse)
- Test: `llm/providers/openaicompat/adapter_test.go`

- [ ] **Step 1: Identify or write the failing tests**

Write individual tests for each quirk:

```go
func TestBuildRequestBody_QuirksToolChoiceAutoOnly(t *testing.T) {
    // When ToolChoiceAutoOnly is true and tool_choice is "required", it should be clamped to "auto".
    quirks := ProviderQuirks{ToolChoiceAutoOnly: true}
    req := llm.Request{
        Model:    "kimi-k2.5",
        Messages: []llm.Message{llm.User("hi")},
        Tools:    []llm.ToolDefinition{{Name: "test", Parameters: map[string]any{"type": "object"}}},
        ToolChoice: &llm.ToolChoice{Mode: "required"},
    }
    body, err := buildRequestBody(req, false, quirks)
    if err != nil {
        t.Fatalf("buildRequestBody: %v", err)
    }
    if body["tool_choice"] != "auto" {
        t.Fatalf("tool_choice: %v, want auto", body["tool_choice"])
    }
}

func TestBuildRequestBody_QuirksMaxStopSequences(t *testing.T) {
    quirks := ProviderQuirks{MaxStopSequences: 1}
    req := llm.Request{
        Model:         "glm-5",
        Messages:      []llm.Message{llm.User("hi")},
        StopSequences: []string{"STOP1", "STOP2", "STOP3"},
    }
    body, err := buildRequestBody(req, false, quirks)
    if err != nil {
        t.Fatalf("buildRequestBody: %v", err)
    }
    stops, ok := body["stop"].([]string)
    if !ok || len(stops) != 1 {
        t.Fatalf("stop: %v", body["stop"])
    }
    if stops[0] != "STOP1" {
        t.Fatalf("stop[0]: %v, want STOP1", stops[0])
    }
}

func TestBuildRequestBody_QuirksNoJSONSchema(t *testing.T) {
    quirks := ProviderQuirks{NoJSONSchema: true}
    req := llm.Request{
        Model:    "glm-5",
        Messages: []llm.Message{llm.User("hi")},
        ResponseFormat: &llm.ResponseFormat{
            Type:       "json_schema",
            JSONSchema: map[string]any{"type": "object"},
        },
    }
    body, err := buildRequestBody(req, false, quirks)
    if err != nil {
        t.Fatalf("buildRequestBody: %v", err)
    }
    rf, ok := body["response_format"].(map[string]any)
    if !ok {
        t.Fatalf("response_format: %T", body["response_format"])
    }
    if rf["type"] != "json_object" {
        t.Fatalf("response_format.type: %v, want json_object", rf["type"])
    }
}

func TestBuildRequestBody_QuirksStripEmptyContent(t *testing.T) {
    quirks := ProviderQuirks{StripEmptyContent: true}
    req := llm.Request{
        Model: "glm-5",
        Messages: []llm.Message{
            {Role: llm.RoleUser, Content: []llm.ContentPart{
                {Kind: llm.ContentText, Text: ""},
                {Kind: llm.ContentText, Text: "hello"},
            }},
        },
    }
    body, err := buildRequestBody(req, false, quirks)
    if err != nil {
        t.Fatalf("buildRequestBody: %v", err)
    }
    msgs, _ := body["messages"].([]map[string]any)
    // The user message content should be "hello" only (empty part stripped).
    content, _ := msgs[0]["content"].(string)
    if content != "hello" {
        t.Fatalf("content: %q, want 'hello'", content)
    }
}

func TestComplete_QuirksFinishReasonMap(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        _, _ = w.Write([]byte(`{
  "id": "chatcmpl-fr1", "model": "glm-5",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "filtered"}, "finish_reason": "sensitive"}],
  "usage": {"prompt_tokens": 5, "completion_tokens": 1, "total_tokens": 6}
}`))
    }))
    t.Cleanup(srv.Close)

    a := &Adapter{
        APIKey: "k", BaseURL: srv.URL, Client: srv.Client(),
        Quirks: ProviderQuirks{
            FinishReasonMap: map[string]string{
                "sensitive":     "content_filter",
                "network_error": "error",
            },
        },
    }
    resp, err := a.Complete(context.Background(), llm.Request{
        Model:    "glm-5",
        Messages: []llm.Message{llm.User("hi")},
    })
    if err != nil {
        t.Fatalf("Complete: %v", err)
    }
    if resp.Finish.Reason != "content_filter" {
        t.Fatalf("finish reason: %q, want content_filter", resp.Finish.Reason)
    }
    if resp.Finish.Raw != "sensitive" {
        t.Fatalf("finish raw: %q, want sensitive", resp.Finish.Raw)
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run "TestBuildRequestBody_Quirks|TestComplete_QuirksFinishReasonMap" -v ./llm/providers/openaicompat/`
Expected: FAIL — quirk behaviors not implemented yet.

- [ ] **Step 3: Write minimal implementation**

In `buildRequestBody`, add after the locked-hyperparameter deletions:

```go
// tool_choice restriction.
if quirks.ToolChoiceAutoOnly {
    if tc, ok := body["tool_choice"]; ok {
        switch tc {
        case "auto", "none":
            // allowed
        default:
            body["tool_choice"] = "auto"
        }
    }
}

// Stop sequence limit.
if quirks.MaxStopSequences > 0 {
    if stops, ok := body["stop"].([]string); ok && len(stops) > quirks.MaxStopSequences {
        body["stop"] = stops[:quirks.MaxStopSequences]
    }
}

// json_schema downgrade.
if quirks.NoJSONSchema {
    if rf, ok := body["response_format"].(map[string]any); ok {
        if rf["type"] == "json_schema" {
            body["response_format"] = map[string]any{"type": "json_object"}
        }
    }
}
```

For `StripEmptyContent`, modify `toChatMessages` to accept quirks and filter empty text parts when `StripEmptyContent` is true. Change `textFromParts` to optionally skip empty texts, or add a post-processing step.

For `FinishReasonMap`, modify `fromChatCompletionResponse` and the streaming `[DONE]` handler to apply the map before `NormalizeFinishReason`. Thread `a.Quirks` through to the response parsing functions. The finish reason mapping logic:

```go
func (q ProviderQuirks) mapFinishReason(raw string) string {
    if q.FinishReasonMap == nil {
        return raw
    }
    if mapped, ok := q.FinishReasonMap[raw]; ok {
        return mapped
    }
    return raw
}
```

Then in `fromChatCompletionResponse` (which needs to accept quirks now):

```go
finish := llm.NormalizeFinishReason("", quirks.mapFinishReason(choice.FinishReason))
```

Update function signatures for `fromChatCompletionResponse`, `toChatMessages`, and `buildRequestBody` to accept `ProviderQuirks`, and update all call sites.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -run "TestBuildRequestBody_Quirks|TestComplete_QuirksFinishReasonMap" -v ./llm/providers/openaicompat/`
Expected: PASS

- [ ] **Step 5: Refactor and verify**

Run full suite:
Run: `go test -count=1 ./llm/providers/openaicompat/`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git -C /home/user/code/serf/.worktrees/openaicompat-reasoning add llm/providers/openaicompat/adapter.go llm/providers/openaicompat/adapter_test.go
git -C /home/user/code/serf/.worktrees/openaicompat-reasoning commit -m "feat(openaicompat): implement tool_choice, stop, empty content, json_schema, and finish reason quirks"
```

---

### Task 7: Add named quirk presets and env var configuration

**Files:**
- Modify: `llm/providers/openaicompat/adapter.go` (NewFromEnv, new preset functions)
- Test: `llm/providers/openaicompat/adapter_test.go`

- [ ] **Step 1: Identify or write the failing test**

Write tests that verify preset construction and env var parsing:

```go
func TestQuirksPreset_KimiK2_5(t *testing.T) {
    q := QuirksPreset("kimi-k2.5")
    if !q.LockTemperature {
        t.Error("LockTemperature should be true")
    }
    if !q.LockTopP {
        t.Error("LockTopP should be true")
    }
    if !q.LockFrequencyPenalty {
        t.Error("LockFrequencyPenalty should be true")
    }
    if !q.LockPresencePenalty {
        t.Error("LockPresencePenalty should be true")
    }
    if !q.ToolChoiceAutoOnly {
        t.Error("ToolChoiceAutoOnly should be true")
    }
    if !q.NoJSONSchema {
        t.Error("NoJSONSchema should be true")
    }
}

func TestQuirksPreset_GLM5(t *testing.T) {
    q := QuirksPreset("glm-5")
    if !q.StripEmptyContent {
        t.Error("StripEmptyContent should be true")
    }
    if !q.ToolChoiceAutoOnly {
        t.Error("ToolChoiceAutoOnly should be true")
    }
    if q.MaxStopSequences != 1 {
        t.Errorf("MaxStopSequences: %d, want 1", q.MaxStopSequences)
    }
    if !q.NoJSONSchema {
        t.Error("NoJSONSchema should be true")
    }
    if q.FinishReasonMap["sensitive"] != "content_filter" {
        t.Errorf("FinishReasonMap[sensitive]: %v", q.FinishReasonMap["sensitive"])
    }
    if q.FinishReasonMap["network_error"] != "error" {
        t.Errorf("FinishReasonMap[network_error]: %v", q.FinishReasonMap["network_error"])
    }
}

func TestQuirksPreset_Unknown_ReturnsEmpty(t *testing.T) {
    q := QuirksPreset("unknown-provider")
    if q.LockTemperature || q.StripEmptyContent || q.ToolChoiceAutoOnly {
        t.Error("unknown preset should return zero-value quirks")
    }
}

func TestNewFromEnv_ParsesQuirksEnvVar(t *testing.T) {
    t.Setenv("OPENAI_COMPATIBLE_BASE_URL", "http://example.com")
    t.Setenv("OPENAI_COMPATIBLE_API_KEY", "test-key")
    t.Setenv("OPENAI_COMPATIBLE_PROVIDER_QUIRKS", "kimi-k2.5")

    a, err := NewFromEnv()
    if err != nil {
        t.Fatalf("NewFromEnv: %v", err)
    }
    if !a.Quirks.LockTemperature {
        t.Error("expected LockTemperature from kimi-k2.5 preset")
    }
    if !a.Quirks.ToolChoiceAutoOnly {
        t.Error("expected ToolChoiceAutoOnly from kimi-k2.5 preset")
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run "TestQuirksPreset|TestNewFromEnv_ParsesQuirksEnvVar" -v ./llm/providers/openaicompat/`
Expected: FAIL — `QuirksPreset` function doesn't exist, `NewFromEnv` doesn't read the env var.

- [ ] **Step 3: Write minimal implementation**

```go
// QuirksPreset returns a ProviderQuirks configuration for a known provider name.
// Unknown names return zero-value quirks (no restrictions).
func QuirksPreset(name string) ProviderQuirks {
    switch strings.ToLower(strings.TrimSpace(name)) {
    case "kimi-k2.5", "kimi", "moonshot":
        return ProviderQuirks{
            LockTemperature:      true,
            LockTopP:             true,
            LockFrequencyPenalty: true,
            LockPresencePenalty:  true,
            ToolChoiceAutoOnly:   true, // When thinking is enabled
            NoJSONSchema:         true,
        }
    case "glm-5", "glm-5-turbo", "glm", "zhipu":
        return ProviderQuirks{
            StripEmptyContent:  true,
            ToolChoiceAutoOnly: true,
            MaxStopSequences:   1,
            NoJSONSchema:       true,
            FinishReasonMap: map[string]string{
                "sensitive":     "content_filter",
                "network_error": "error",
            },
        }
    default:
        return ProviderQuirks{}
    }
}
```

Update `NewFromEnv`:

```go
func NewFromEnv() (*Adapter, error) {
    base := strings.TrimSpace(os.Getenv("OPENAI_COMPATIBLE_BASE_URL"))
    if base == "" {
        return nil, fmt.Errorf("OPENAI_COMPATIBLE_BASE_URL is required")
    }
    key := strings.TrimSpace(os.Getenv("OPENAI_COMPATIBLE_API_KEY"))

    var quirks ProviderQuirks
    if preset := strings.TrimSpace(os.Getenv("OPENAI_COMPATIBLE_PROVIDER_QUIRKS")); preset != "" {
        quirks = QuirksPreset(preset)
    }

    return &Adapter{
        APIKey:  key,
        BaseURL: strings.TrimRight(base, "/"),
        Client:  &http.Client{Timeout: 0},
        Quirks:  quirks,
    }, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -run "TestQuirksPreset|TestNewFromEnv_ParsesQuirksEnvVar" -v ./llm/providers/openaicompat/`
Expected: PASS

- [ ] **Step 5: Refactor and verify**

Run full suite and `go vet`:
Run: `go test -count=1 ./llm/providers/openaicompat/ && go vet ./llm/providers/openaicompat/`
Expected: all PASS, no vet issues

- [ ] **Step 6: Commit**

```bash
git -C /home/user/code/serf/.worktrees/openaicompat-reasoning add llm/providers/openaicompat/adapter.go llm/providers/openaicompat/adapter_test.go
git -C /home/user/code/serf/.worktrees/openaicompat-reasoning commit -m "feat(openaicompat): add QuirksPreset for kimi-k2.5 and glm-5, with env var config"
```

---

### Task 8: Streaming finish reason quirk mapping and streaming reasoning in final response

**Files:**
- Modify: `llm/providers/openaicompat/adapter.go` (Stream goroutine's [DONE] handler and finish_reason tracking)
- Test: `llm/providers/openaicompat/adapter_test.go`

- [ ] **Step 1: Identify or write the failing test**

Write a streaming test where the finish_reason is `"sensitive"` and the adapter has the GLM quirk preset. Assert the FINISH event has `content_filter` as the reason.

```go
func TestStream_QuirksFinishReasonMap(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/event-stream")
        flusher, _ := w.(http.Flusher)
        chunks := []string{
            `{"id":"chatcmpl-1","model":"glm-5","choices":[{"index":0,"delta":{"role":"assistant","content":"filtered"},"finish_reason":null}]}`,
            `{"id":"chatcmpl-1","model":"glm-5","choices":[{"index":0,"delta":{},"finish_reason":"sensitive"}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`,
        }
        for _, c := range chunks {
            fmt.Fprintf(w, "data: %s\n\n", c)
            flusher.Flush()
        }
        fmt.Fprintf(w, "data: [DONE]\n\n")
        flusher.Flush()
    }))
    t.Cleanup(srv.Close)

    a := &Adapter{
        APIKey: "k", BaseURL: srv.URL, Client: srv.Client(),
        Quirks: QuirksPreset("glm-5"),
    }
    st, err := a.Stream(context.Background(), llm.Request{
        Model:    "glm-5",
        Messages: []llm.Message{llm.User("hi")},
    })
    if err != nil {
        t.Fatalf("Stream: %v", err)
    }
    defer st.Close()

    var finishReason string
    for ev := range st.Events() {
        if ev.Type == llm.StreamEventFinish && ev.FinishReason != nil {
            finishReason = ev.FinishReason.Reason
        }
    }
    if finishReason != "content_filter" {
        t.Fatalf("finish reason: %q, want content_filter", finishReason)
    }
}
```

Also write a streaming test that verifies the final response from FINISH contains `ContentThinking` when reasoning was streamed:

```go
func TestStream_ReasoningContent_InFinalResponse(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/event-stream")
        flusher, _ := w.(http.Flusher)
        chunks := []string{
            `{"id":"chatcmpl-1","model":"kimi-k2.5","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"thinking..."},"finish_reason":null}]}`,
            `{"id":"chatcmpl-1","model":"kimi-k2.5","choices":[{"index":0,"delta":{"content":"answer"},"finish_reason":null}]}`,
            `{"id":"chatcmpl-1","model":"kimi-k2.5","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
        }
        for _, c := range chunks {
            fmt.Fprintf(w, "data: %s\n\n", c)
            flusher.Flush()
        }
        fmt.Fprintf(w, "data: [DONE]\n\n")
        flusher.Flush()
    }))
    t.Cleanup(srv.Close)

    a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
    st, err := a.Stream(context.Background(), llm.Request{
        Model:    "kimi-k2.5",
        Messages: []llm.Message{llm.User("think")},
    })
    if err != nil {
        t.Fatalf("Stream: %v", err)
    }
    defer st.Close()

    var finalResp *llm.Response
    for ev := range st.Events() {
        if ev.Type == llm.StreamEventFinish && ev.Response != nil {
            finalResp = ev.Response
        }
    }
    if finalResp == nil {
        t.Fatal("no FINISH response")
    }
    if finalResp.ReasoningText() != "thinking..." {
        t.Fatalf("ReasoningText(): %q", finalResp.ReasoningText())
    }
    if finalResp.Text() != "answer" {
        t.Fatalf("Text(): %q", finalResp.Text())
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run "TestStream_QuirksFinishReasonMap|TestStream_ReasoningContent_InFinalResponse" -v ./llm/providers/openaicompat/`
Expected: FAIL — streaming doesn't apply quirk finish reason mapping, and the final response doesn't include thinking content.

- [ ] **Step 3: Write minimal implementation**

The Stream goroutine needs access to `a.Quirks`. It is already a method on `*Adapter`, so `a.Quirks` is directly accessible.

In the streaming goroutine's `[DONE]` handler, apply the quirk mapping to `finishReason`:

```go
// Apply quirk finish reason mapping.
finishReason = a.Quirks.mapFinishReason(finishReason)
```

The reasoning content in the final response should already work from Task 2's implementation (the `[DONE]` handler builds the message with `reasoningBuf`). If it doesn't because Task 2's implementation isn't quite complete, fix it here.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -run "TestStream_QuirksFinishReasonMap|TestStream_ReasoningContent_InFinalResponse" -v ./llm/providers/openaicompat/`
Expected: PASS

- [ ] **Step 5: Refactor and verify**

Run full suite:
Run: `go test -count=1 ./llm/providers/openaicompat/`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git -C /home/user/code/serf/.worktrees/openaicompat-reasoning add llm/providers/openaicompat/adapter.go llm/providers/openaicompat/adapter_test.go
git -C /home/user/code/serf/.worktrees/openaicompat-reasoning commit -m "feat(openaicompat): apply quirk finish reason mapping in streaming and verify reasoning in final response"
```

---

### Task 9: Full integration test with Kimi K2.5 and GLM 5.0 mock scenarios

**Files:**
- Test: `llm/providers/openaicompat/adapter_test.go`

- [ ] **Step 1: Write comprehensive integration tests**

Write two end-to-end style tests using httptest that simulate realistic Kimi K2.5 and GLM 5.0 conversations including reasoning, tool calls, and quirk behaviors.

```go
func TestIntegration_KimiK2_5_ReasoningWithToolCall(t *testing.T) {
    // Simulates: user asks question -> model thinks -> model calls tool -> user provides result -> model thinks -> model answers.
    // Verifies: reasoning_content parsed, tool calls work, reasoning round-tripped in follow-up, locked params stripped.
    requestCount := 0
    var firstReqBody, secondReqBody map[string]any

    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        requestCount++
        b, _ := io.ReadAll(r.Body)
        w.Header().Set("Content-Type", "application/json")

        if requestCount == 1 {
            _ = json.Unmarshal(b, &firstReqBody)
            // First response: thinking + tool call.
            _, _ = w.Write([]byte(`{
  "id": "chatcmpl-k1", "model": "kimi-k2.5",
  "choices": [{"index": 0, "message": {
    "role": "assistant",
    "reasoning_content": "I need to check the weather API.",
    "content": null,
    "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "get_weather", "arguments": "{\"city\":\"SF\"}"}}]
  }, "finish_reason": "tool_calls"}],
  "usage": {"prompt_tokens": 10, "completion_tokens": 30, "total_tokens": 40, "completion_tokens_details": {"reasoning_tokens": 20}}
}`))
        } else {
            _ = json.Unmarshal(b, &secondReqBody)
            // Second response: thinking + final answer.
            _, _ = w.Write([]byte(`{
  "id": "chatcmpl-k2", "model": "kimi-k2.5",
  "choices": [{"index": 0, "message": {
    "role": "assistant",
    "reasoning_content": "The weather data says sunny and 72F.",
    "content": "It's sunny and 72F in San Francisco!"
  }, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 50, "completion_tokens": 40, "total_tokens": 90}
}`))
        }
    }))
    t.Cleanup(srv.Close)

    temp := 0.7
    a := &Adapter{
        APIKey: "k", BaseURL: srv.URL, Client: srv.Client(),
        Quirks: QuirksPreset("kimi-k2.5"),
    }

    // First request: user asks, model thinks and calls tool.
    resp1, err := a.Complete(context.Background(), llm.Request{
        Model:       "kimi-k2.5",
        Messages:    []llm.Message{llm.User("What's the weather in SF?")},
        Tools:       []llm.ToolDefinition{{Name: "get_weather", Parameters: map[string]any{"type": "object"}}},
        Temperature: &temp,
    })
    if err != nil {
        t.Fatalf("first Complete: %v", err)
    }

    // Verify temperature was stripped.
    if _, ok := firstReqBody["temperature"]; ok {
        t.Error("temperature should be stripped for kimi-k2.5")
    }

    // Verify reasoning was parsed.
    if resp1.ReasoningText() != "I need to check the weather API." {
        t.Fatalf("first reasoning: %q", resp1.ReasoningText())
    }
    // Verify native reasoning tokens.
    if resp1.Usage.ReasoningTokens == nil || *resp1.Usage.ReasoningTokens != 20 {
        t.Fatalf("first reasoning tokens: %v", resp1.Usage.ReasoningTokens)
    }

    // Second request: include previous assistant message (with thinking) and tool result.
    resp2, err := a.Complete(context.Background(), llm.Request{
        Model: "kimi-k2.5",
        Messages: []llm.Message{
            llm.User("What's the weather in SF?"),
            resp1.Message, // Should include ContentThinking + ContentToolCall
            llm.ToolResultNamed("call_1", "get_weather", "Sunny, 72F", false),
        },
    })
    if err != nil {
        t.Fatalf("second Complete: %v", err)
    }

    // Verify reasoning_content was round-tripped in the second request's assistant message.
    msgs, _ := secondReqBody["messages"].([]any)
    assistantMsg, _ := msgs[1].(map[string]any)
    if rc, _ := assistantMsg["reasoning_content"].(string); rc != "I need to check the weather API." {
        t.Fatalf("round-tripped reasoning_content: %q", rc)
    }

    // Verify second response.
    if resp2.ReasoningText() != "The weather data says sunny and 72F." {
        t.Fatalf("second reasoning: %q", resp2.ReasoningText())
    }
    if resp2.Text() != "It's sunny and 72F in San Francisco!" {
        t.Fatalf("second text: %q", resp2.Text())
    }
    // Verify estimated reasoning tokens (no native count in second response).
    if resp2.Usage.ReasoningTokens == nil {
        t.Fatal("second reasoning tokens should be estimated")
    }
}

func TestIntegration_GLM5_EmptyContentAndSensitiveFilter(t *testing.T) {
    var gotBody map[string]any

    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        b, _ := io.ReadAll(r.Body)
        _ = json.Unmarshal(b, &gotBody)
        w.Header().Set("Content-Type", "application/json")
        _, _ = w.Write([]byte(`{
  "id": "chatcmpl-g1", "model": "glm-5",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "I cannot answer that."}, "finish_reason": "sensitive"}],
  "usage": {"prompt_tokens": 5, "completion_tokens": 5, "total_tokens": 10}
}`))
    }))
    t.Cleanup(srv.Close)

    a := &Adapter{
        APIKey: "k", BaseURL: srv.URL, Client: srv.Client(),
        Quirks: QuirksPreset("glm-5"),
    }

    resp, err := a.Complete(context.Background(), llm.Request{
        Model: "glm-5",
        Messages: []llm.Message{
            {Role: llm.RoleUser, Content: []llm.ContentPart{
                {Kind: llm.ContentText, Text: ""},
                {Kind: llm.ContentText, Text: "tell me something"},
            }},
        },
        StopSequences: []string{"STOP1", "STOP2", "STOP3"},
    })
    if err != nil {
        t.Fatalf("Complete: %v", err)
    }

    // Verify stop sequences truncated to 1.
    stops, _ := gotBody["stop"].([]any)
    if len(stops) != 1 {
        t.Fatalf("stop sequences: %v, want 1 element", stops)
    }

    // Verify finish reason mapped.
    if resp.Finish.Reason != "content_filter" {
        t.Fatalf("finish reason: %q, want content_filter", resp.Finish.Reason)
    }
}
```

- [ ] **Step 2: Run tests to verify they pass**

Run: `go test -run "TestIntegration_KimiK2_5|TestIntegration_GLM5" -v ./llm/providers/openaicompat/`
Expected: PASS (all previous tasks have implemented the required functionality)

- [ ] **Step 3: Run the complete test suite**

Run: `go test -count=1 -v ./llm/providers/openaicompat/`
Expected: All tests PASS including the 12 original tests (regression baseline).

- [ ] **Step 4: Run full project tests**

Run: `go test -short -count=1 ./...`
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git -C /home/user/code/serf/.worktrees/openaicompat-reasoning add llm/providers/openaicompat/adapter_test.go
git -C /home/user/code/serf/.worktrees/openaicompat-reasoning commit -m "test(openaicompat): add integration tests for kimi-k2.5 and glm-5 scenarios"
```

---

### Task 10: Build verification and final cleanup

**Files:**
- Verify: all files in `llm/providers/openaicompat/`

- [ ] **Step 1: Build the project**

Run: `cd /home/user/code/serf/.worktrees/openaicompat-reasoning && make build`
Expected: successful build with no errors

- [ ] **Step 2: Run vet and full tests**

Run: `cd /home/user/code/serf/.worktrees/openaicompat-reasoning && go vet ./... && go test -count=1 ./...`
Expected: no vet issues, all tests pass

- [ ] **Step 3: Review for code quality**

Verify:
- No `TODO` or `FIXME` comments left behind
- All exported types and functions have doc comments
- `ProviderQuirks`, `QuirksPreset`, and all quirk fields are documented
- No unnecessary imports
- Code follows existing patterns (e.g., `llm.IntFromAny` usage, error handling style)

- [ ] **Step 4: Final commit if any cleanup was needed**

```bash
git -C /home/user/code/serf/.worktrees/openaicompat-reasoning add llm/providers/openaicompat/adapter.go llm/providers/openaicompat/adapter_test.go
git -C /home/user/code/serf/.worktrees/openaicompat-reasoning commit -m "chore(openaicompat): final cleanup and documentation"
```
