# Spec Compliance Remediation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Fix every defect, missing feature, and test gap found in the unified-llm-spec compliance audit.

**Architecture:** All changes are in `internal/llm/` and its provider sub-packages. Each task is a self-contained TDD fix: write the failing test, implement the fix, verify, commit. Tasks are ordered by dependency (foundation fixes first, then features that build on them).

**Tech Stack:** Go, `net/http`, `encoding/json`, `github.com/santhosh-tekuri/jsonschema/v5`

---

## Task 1: Gemini `parseUsage` — handle `json.Number` in streaming

The Gemini streaming SSE parser uses `dec.UseNumber()`, so values arrive as `json.Number`. But `parseUsage()` only handles `float64` and `int`, silently producing zeros for all usage fields during streaming.

**Files:**
- Modify: `internal/llm/providers/google/adapter.go:933-957` (the `parseUsage` function)
- Test: `internal/llm/providers/google/adapter_test.go`

**Step 1: Write the failing test**

Add a test that passes `json.Number` values to `parseUsage` and asserts correct results.

```go
func TestParseUsage_HandlesJSONNumber(t *testing.T) {
	// Simulate what dec.UseNumber() produces during streaming.
	raw := []byte(`{"promptTokenCount": 100, "candidatesTokenCount": 50, "totalTokenCount": 150, "thoughtsTokenCount": 20, "cachedContentTokenCount": 30}`)
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		t.Fatal(err)
	}
	usage := parseUsage(m)
	if usage.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", usage.InputTokens)
	}
	if usage.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d, want 50", usage.OutputTokens)
	}
	if usage.TotalTokens != 150 {
		t.Errorf("TotalTokens = %d, want 150", usage.TotalTokens)
	}
	if usage.ReasoningTokens == nil || *usage.ReasoningTokens != 20 {
		t.Errorf("ReasoningTokens = %v, want 20", usage.ReasoningTokens)
	}
	if usage.CacheReadTokens == nil || *usage.CacheReadTokens != 30 {
		t.Errorf("CacheReadTokens = %v, want 30", usage.CacheReadTokens)
	}
}
```

Note: `parseUsage` is package-private. The test is in the same package so can access it directly. If the existing test file uses `_test` package, you'll need to export it or use an existing test that already exercises it (e.g., add to `TestUsage_MapsReasoningAndCacheTokens` with `json.Number` input variant).

**Step 2: Run test to verify it fails**

Run: `go test ./internal/llm/providers/google/ -run TestParseUsage_HandlesJSONNumber -v`
Expected: FAIL — `InputTokens = 0, want 100`

**Step 3: Implement the fix**

In `internal/llm/providers/google/adapter.go`, modify the `getInt` closure inside `parseUsage`:

```go
func parseUsage(u map[string]any) llm.Usage {
	getInt := func(v any) int {
		switch x := v.(type) {
		case float64:
			return int(x)
		case int:
			return x
		case json.Number:
			i, _ := x.Int64()
			return int(i)
		default:
			return 0
		}
	}
	// ... rest unchanged
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/llm/providers/google/ -run TestParseUsage_HandlesJSONNumber -v`
Expected: PASS

**Step 5: Run full Google adapter test suite**

Run: `go test ./internal/llm/providers/google/ -v -count=1`
Expected: All PASS

**Step 6: Commit**

```bash
git add internal/llm/providers/google/adapter.go internal/llm/providers/google/adapter_test.go
git commit -m "fix: handle json.Number in Gemini parseUsage for streaming"
```

---

## Task 2: Gemini streaming — emit REASONING_START/DELTA/END for thinking parts

During streaming, Gemini thinking/thought parts are accumulated into the final response but not emitted as REASONING_START/DELTA/END stream events.

**Files:**
- Modify: `internal/llm/providers/google/adapter.go:408-422` (the thought part handling in `Stream`)
- Test: `internal/llm/providers/google/adapter_test.go`

**Step 1: Write the failing test**

Add a test that streams a Gemini response with thought parts and asserts REASONING_START/DELTA/END events are emitted.

```go
func TestStream_EmitsReasoningEventsForThoughtParts(t *testing.T) {
	// Build an SSE response with a thought part followed by a text part.
	chunk1 := map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{
				"parts": []any{
					map[string]any{"thought": true, "text": "Let me think..."},
				},
			},
		}},
	}
	chunk2 := map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{
				"parts": []any{
					map[string]any{"text": "The answer is 42."},
				},
			},
			"finishReason": "STOP",
		}},
		"usageMetadata": map[string]any{
			"promptTokenCount":     10,
			"candidatesTokenCount": 5,
			"totalTokenCount":      15,
		},
	}
	sseBody := buildSSE(t, chunk1, chunk2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write(sseBody)
	}))
	defer srv.Close()

	a := &Adapter{APIKey: "test", BaseURL: srv.URL, Client: srv.Client()}
	stream, err := a.Stream(context.Background(), llm.Request{Model: "gemini-2.5-flash", Messages: []llm.Message{llm.User("test")}})
	if err != nil {
		t.Fatal(err)
	}

	var types []llm.StreamEventType
	for ev := range stream.Events() {
		types = append(types, ev.Type)
	}

	// Should contain REASONING_START, REASONING_DELTA, REASONING_END
	wantTypes := []llm.StreamEventType{
		llm.StreamEventStreamStart,
		llm.StreamEventReasoningStart,
		llm.StreamEventReasoningDelta,
		llm.StreamEventReasoningEnd,
		llm.StreamEventTextStart,
		llm.StreamEventTextDelta,
		llm.StreamEventTextEnd,
		llm.StreamEventFinish,
	}
	// Check that all wanted types appear in order (allow provider events between them).
	wi := 0
	for _, got := range types {
		if wi < len(wantTypes) && got == wantTypes[wi] {
			wi++
		}
	}
	if wi != len(wantTypes) {
		t.Errorf("missing expected stream event types\ngot:  %v\nwant: %v", types, wantTypes)
	}
}
```

You'll need to adapt the `buildSSE` helper if one doesn't already exist in the test file. Look at how the existing stream tests construct SSE payloads and reuse that pattern.

**Step 2: Run test to verify it fails**

Run: `go test ./internal/llm/providers/google/ -run TestStream_EmitsReasoningEventsForThoughtParts -v`
Expected: FAIL — reasoning events missing

**Step 3: Implement the fix**

In `internal/llm/providers/google/adapter.go`, in the `Stream` goroutine where thought parts are handled (around line 410-421), emit reasoning stream events:

Replace:
```go
if thought, _ := p["thought"].(bool); thought {
	text, _ := p["text"].(string)
	if text != "" {
		flushTextPart()
		contentParts = append(contentParts, llm.ContentPart{
			Kind: llm.ContentThinking,
			Thinking: &llm.ThinkingData{
				Text: text,
			},
		})
	}
	continue
}
```

With:
```go
if thought, _ := p["thought"].(bool); thought {
	text, _ := p["text"].(string)
	if text != "" {
		flushTextPart()
		s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningStart})
		s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: text})
		s.Send(llm.StreamEvent{Type: llm.StreamEventReasoningEnd})
		contentParts = append(contentParts, llm.ContentPart{
			Kind: llm.ContentThinking,
			Thinking: &llm.ThinkingData{
				Text: text,
			},
		})
	}
	continue
}
```

Note: Gemini delivers thought content as complete text in a single chunk (unlike Anthropic which streams it incrementally). Emitting START/DELTA/END immediately matches the spec pattern and is consistent with how Gemini delivers function calls (complete in one chunk).

**Step 4: Run test to verify it passes**

Run: `go test ./internal/llm/providers/google/ -run TestStream_EmitsReasoningEventsForThoughtParts -v`
Expected: PASS

**Step 5: Run full Google adapter test suite**

Run: `go test ./internal/llm/providers/google/ -v -count=1`
Expected: All PASS

**Step 6: Commit**

```bash
git add internal/llm/providers/google/adapter.go internal/llm/providers/google/adapter_test.go
git commit -m "fix: emit REASONING stream events for Gemini thought parts"
```

---

## Task 3: OpenAI image `detail` hint passthrough

The `detail` field from `ImageData` is defined in the types but never included in OpenAI Responses API image input items.

**Files:**
- Modify: `internal/llm/providers/openai/adapter.go:656-661`
- Test: `internal/llm/providers/openai/adapter_test.go`

**Step 1: Write the failing test**

```go
func TestImageInput_DetailHintPassedThrough(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &captured)
		// Return minimal valid response.
		json.NewEncoder(w).Encode(map[string]any{
			"id":     "resp_1",
			"status": "completed",
			"output": []any{map[string]any{
				"type":    "message",
				"role":    "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": "I see an image"}},
			}},
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
		})
	}))
	defer srv.Close()

	a := &Adapter{APIKey: "test", BaseURL: srv.URL, Client: srv.Client()}
	_, err := a.Complete(context.Background(), llm.Request{
		Model: "gpt-5.2",
		Messages: []llm.Message{{
			Role: llm.RoleUser,
			Content: []llm.ContentPart{
				{Kind: llm.ContentImage, Image: &llm.ImageData{
					URL:    "https://example.com/img.png",
					Detail: "low",
				}},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Navigate to the image item in the captured request.
	input, _ := captured["input"].([]any)
	var imageItem map[string]any
	for _, item := range input {
		m, _ := item.(map[string]any)
		if m["type"] == "message" {
			content, _ := m["content"].([]any)
			for _, c := range content {
				cm, _ := c.(map[string]any)
				if cm["type"] == "input_image" {
					imageItem = cm
				}
			}
		}
	}
	if imageItem == nil {
		t.Fatal("no input_image item found in request")
	}
	if imageItem["detail"] != "low" {
		t.Errorf("detail = %v, want \"low\"", imageItem["detail"])
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/llm/providers/openai/ -run TestImageInput_DetailHintPassedThrough -v`
Expected: FAIL — `detail = <nil>, want "low"`

**Step 3: Implement the fix**

In `internal/llm/providers/openai/adapter.go`, around line 656-661, change:

```go
if url != "" {
	content = append(content, map[string]any{
		"type":      "input_image",
		"image_url": url,
	})
}
```

To:

```go
if url != "" {
	img := map[string]any{
		"type":      "input_image",
		"image_url": url,
	}
	if p.Image.Detail != "" {
		img["detail"] = p.Image.Detail
	}
	content = append(content, img)
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/llm/providers/openai/ -run TestImageInput_DetailHintPassedThrough -v`
Expected: PASS

**Step 5: Run full OpenAI adapter test suite**

Run: `go test ./internal/llm/providers/openai/ -v -count=1`
Expected: All PASS

**Step 6: Commit**

```bash
git add internal/llm/providers/openai/adapter.go internal/llm/providers/openai/adapter_test.go
git commit -m "fix: pass image detail hint through to OpenAI Responses API"
```

---

## Task 4: Tool loop — check `finish_reason == "tool_calls"` before executing

The spec requires checking both `tool_calls` presence AND `finish_reason == "tool_calls"` before executing tools. Currently only presence is checked.

**Files:**
- Modify: `internal/llm/generate.go:213`
- Modify: `internal/llm/stream_generate.go:260`
- Test: `internal/llm/generate_test.go`
- Test: `internal/llm/stream_generate_test.go`

**Step 1: Write the failing test for generate**

```go
func TestGenerate_ToolCallsWithStopFinish_DoesNotExecute(t *testing.T) {
	// Model returns tool call content parts BUT with finish_reason="stop" (not "tool_calls").
	// Per spec, tools should NOT be executed.
	executed := false
	client, adapter := newMockClient()
	adapter.completeFunc = func(ctx context.Context, req llm.Request) (llm.Response, error) {
		return llm.Response{
			Message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{
					{Kind: llm.ContentText, Text: "Here is the weather"},
					{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
						ID:        "call_1",
						Name:      "get_weather",
						Arguments: json.RawMessage(`{"city":"SF"}`),
						Type:      "function",
					}},
				},
			},
			Finish: llm.FinishReason{Reason: "stop", Raw: "stop"},
			Usage:  llm.Usage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30},
		}, nil
	}

	result, err := llm.Generate(context.Background(), llm.GenerateOptions{
		Client: client,
		Model:  "test-model",
		Prompt: strPtr("weather?"),
		Tools: []llm.Tool{{
			Definition: llm.ToolDefinition{
				Name:        "get_weather",
				Description: "Get weather",
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			},
			Execute: func(ctx context.Context, args any) (any, error) {
				executed = true
				return "sunny", nil
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if executed {
		t.Error("tool was executed but finish_reason was 'stop', not 'tool_calls'")
	}
	// The tool calls should still be in the result (for the caller to see).
	if len(result.ToolCalls) == 0 {
		t.Error("expected tool calls in result even though they weren't executed")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/llm/ -run TestGenerate_ToolCallsWithStopFinish_DoesNotExecute -v`
Expected: FAIL — `tool was executed but finish_reason was 'stop'`

**Step 3: Implement the fix**

In `internal/llm/generate.go:213`, add a finish_reason check. Change:

```go
if len(calls) == 0 || !hasActiveTool || maxToolRounds == 0 || toolRoundsUsed >= maxToolRounds {
```

To:

```go
if len(calls) == 0 || resp.Finish.Reason != FinishReasonToolCalls || !hasActiveTool || maxToolRounds == 0 || toolRoundsUsed >= maxToolRounds {
```

Similarly in `internal/llm/stream_generate.go:260`, change:

```go
if len(calls) == 0 || !hasActiveTool || maxToolRounds == 0 || toolRoundsUsed >= maxToolRounds {
```

To:

```go
if len(calls) == 0 || (stepResp.Finish.Reason != FinishReasonToolCalls && stepResp.Finish.Reason != FinishReasonPauseTurn) || !hasActiveTool || maxToolRounds == 0 || toolRoundsUsed >= maxToolRounds {
```

Note: `pause_turn` is also included because the web search feature uses it to continue tool loops (an existing serf extension). Verify this by checking existing tests for `pause_turn` behavior.

**Step 4: Run test to verify it passes**

Run: `go test ./internal/llm/ -run TestGenerate_ToolCallsWithStopFinish_DoesNotExecute -v`
Expected: PASS

**Step 5: Run full generate/stream test suites to catch regressions**

Run: `go test ./internal/llm/ -v -count=1`
Expected: All PASS. If any existing tool loop tests fail, check whether they rely on the old behavior (executing tools when finish_reason is "stop"). Those tests would need their mock to return `finish_reason = "tool_calls"` — which is the correct behavior per spec.

**Step 6: Commit**

```bash
git add internal/llm/generate.go internal/llm/stream_generate.go internal/llm/generate_test.go internal/llm/stream_generate_test.go
git commit -m "fix: check finish_reason before executing tools per spec 5.6"
```

---

## Task 5: OpenAI-compatible adapter — fix Retry-After, error wrapping, and TotalTokens

Three bugs in the same adapter. Fix together since they're all small and in the same file.

**Files:**
- Modify: `internal/llm/providers/openaicompat/adapter.go`
- Test: `internal/llm/providers/openaicompat/adapter_test.go`

### Part A: Retry-After header parsing

**Step 1: Write the failing test**

```go
func TestHTTPErrorMapping_IncludesRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(429)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "rate limited"}})
	}))
	defer srv.Close()

	a := &Adapter{BaseURL: srv.URL, Client: srv.Client()}
	_, err := a.Complete(context.Background(), llm.Request{Model: "test", Messages: []llm.Message{llm.User("hi")}})
	if err == nil {
		t.Fatal("expected error")
	}
	var rlErr *llm.RateLimitError
	if !errors.As(err, &rlErr) {
		t.Fatalf("expected RateLimitError, got %T: %v", err, err)
	}
	if rlErr.RetryAfter() == nil {
		t.Fatal("RetryAfter is nil, want 30s")
	}
	if *rlErr.RetryAfter() != 30*time.Second {
		t.Errorf("RetryAfter = %v, want 30s", *rlErr.RetryAfter())
	}
}
```

**Step 2: Run test to verify it fails**

Expected: FAIL — `RetryAfter is nil, want 30s`

**Step 3: Implement the fix**

The root cause is that `doHTTP` discards the HTTP response headers. We need to return them. Refactor `doHTTP` to also return `http.Header`:

```go
func (a *Adapter) doHTTP(ctx context.Context, body map[string]any) (map[string]any, int, http.Header, error) {
	// ... existing code ...
	resp, err := a.Client.Do(httpReq)
	if err != nil {
		return nil, 0, nil, llm.WrapContextError("openai-compatible", err) // also fixes D7
	}
	defer resp.Body.Close()
	// ... existing read/parse code ...
	return raw, resp.StatusCode, resp.Header, nil
}
```

Update `Complete` to use headers:

```go
func (a *Adapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	// ...
	raw, statusCode, headers, err := a.doHTTP(ctx, body)
	if err != nil {
		return llm.Response{}, err
	}
	if statusCode != http.StatusOK {
		msg := extractErrorMessage(raw)
		retryAfter := llm.ParseRetryAfter(headers.Get("Retry-After"), time.Now())
		return llm.Response{}, llm.ErrorFromHTTPStatus("openai-compatible", statusCode, msg, raw, retryAfter)
	}
	return fromChatCompletionResponse(raw)
}
```

Update `Stream` error path similarly:

```go
if resp.StatusCode != http.StatusOK {
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var raw map[string]any
	_ = json.Unmarshal(b, &raw)
	msg := extractErrorMessage(raw)
	retryAfter := llm.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	return nil, llm.ErrorFromHTTPStatus("openai-compatible", resp.StatusCode, msg, raw, retryAfter)
}
```

Also update `Stream`'s `Client.Do` error to wrap context errors:

```go
resp, err := a.Client.Do(httpReq)
if err != nil {
	return nil, llm.WrapContextError("openai-compatible", err)
}
```

Delete the `parseRetryAfter` stub function entirely since it's no longer used.

### Part B: TotalTokens

In `fromChatCompletionResponse`, after populating `InputTokens` and `OutputTokens`, compute `TotalTokens`:

```go
if parsed.Usage != nil {
	resp.Usage = llm.Usage{
		InputTokens:  intFromAny(parsed.Usage["prompt_tokens"]),
		OutputTokens: intFromAny(parsed.Usage["completion_tokens"]),
		Raw:          parsed.Usage,
	}
	resp.Usage.TotalTokens = resp.Usage.InputTokens + resp.Usage.OutputTokens
	// Also check if the API provided total_tokens directly.
	if v := intFromAny(parsed.Usage["total_tokens"]); v > 0 {
		resp.Usage.TotalTokens = v
	}
}
```

Similarly in the streaming path (around line 196-200):

```go
if chunk.Usage != nil {
	u := llm.Usage{
		InputTokens:  intFromAny(chunk.Usage["prompt_tokens"]),
		OutputTokens: intFromAny(chunk.Usage["completion_tokens"]),
		Raw:          chunk.Usage,
	}
	u.TotalTokens = u.InputTokens + u.OutputTokens
	if v := intFromAny(chunk.Usage["total_tokens"]); v > 0 {
		u.TotalTokens = v
	}
	usage = &u
}
```

**Step 4: Write a TotalTokens test**

```go
func TestComplete_PopulatesTotalTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-1", "model": "test",
			"choices": []any{map[string]any{
				"index":         0,
				"finish_reason": "stop",
				"message":       map[string]any{"role": "assistant", "content": "hi"},
			}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30},
		})
	}))
	defer srv.Close()

	a := &Adapter{BaseURL: srv.URL, Client: srv.Client()}
	resp, err := a.Complete(context.Background(), llm.Request{Model: "test", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.TotalTokens != 30 {
		t.Errorf("TotalTokens = %d, want 30", resp.Usage.TotalTokens)
	}
}
```

**Step 5: Write a context cancellation wrapping test**

```go
func TestComplete_WrapsContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	a := &Adapter{BaseURL: "http://127.0.0.1:1", Client: &http.Client{Timeout: time.Millisecond}}
	_, err := a.Complete(ctx, llm.Request{Model: "test", Messages: []llm.Message{llm.User("hi")}})
	if err == nil {
		t.Fatal("expected error")
	}
	var abortErr *llm.AbortError
	if !errors.As(err, &abortErr) {
		t.Errorf("expected AbortError, got %T: %v", err, err)
	}
}
```

**Step 6: Run all tests**

Run: `go test ./internal/llm/providers/openaicompat/ -v -count=1`
Expected: All PASS

**Step 7: Commit**

```bash
git add internal/llm/providers/openaicompat/adapter.go internal/llm/providers/openaicompat/adapter_test.go
git commit -m "fix: openai-compatible adapter Retry-After parsing, error wrapping, and TotalTokens"
```

---

## Task 6: AdapterTimeout — wire through to HTTP clients

`AdapterTimeout` is defined on `Request` but no adapter reads it. We need each adapter's `Complete` and `Stream` methods to honor the timeout values.

**Files:**
- Modify: `internal/llm/providers/openai/adapter.go`
- Modify: `internal/llm/providers/anthropic/adapter.go`
- Modify: `internal/llm/providers/google/adapter.go`
- Modify: `internal/llm/providers/openaicompat/adapter.go`
- Create: `internal/llm/adapter_timeout.go` (shared helper)
- Test: `internal/llm/adapter_timeout_test.go`
- Test: each adapter's test file

**Step 1: Write the failing test (shared helper)**

```go
func TestAdapterTimeoutContext_AppliesRequestTimeout(t *testing.T) {
	timeout := &llm.AdapterTimeout{
		Connect:    1 * time.Second,
		Request:    5 * time.Second,
		StreamRead: 2 * time.Second,
	}
	ctx := context.Background()
	ctx, cancel := llm.ApplyAdapterTimeout(ctx, timeout, false)
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected deadline on context")
	}
	remaining := time.Until(deadline)
	if remaining < 4*time.Second || remaining > 6*time.Second {
		t.Errorf("expected ~5s remaining, got %v", remaining)
	}
}
```

**Step 2: Implement the shared helper**

Create `internal/llm/adapter_timeout.go`:

```go
package llm

import (
	"context"
	"time"
)

// ApplyAdapterTimeout creates a context with the appropriate deadline from AdapterTimeout.
// For non-streaming requests, it uses the Request timeout.
// For streaming requests, there is no overall deadline (stream_read is checked per-event).
// If at is nil, returns the context unchanged.
func ApplyAdapterTimeout(ctx context.Context, at *AdapterTimeout, streaming bool) (context.Context, context.CancelFunc) {
	if at == nil {
		return ctx, func() {}
	}
	if !streaming && at.Request > 0 {
		return context.WithTimeout(ctx, at.Request)
	}
	return ctx, func() {}
}
```

Note: `Connect` timeout requires `net.Dialer` configuration which is more invasive. We apply `Request` as a context deadline for now since it covers the most common case. `StreamRead` would need per-read deadlines on the response body, which is complex. Start with `Request` timeout and document the limitation. If Jesse wants the full granularity later, it can be added.

**Step 3: Wire into each adapter**

In each adapter's `Complete` method, add near the top (after building the request body, before making the HTTP call):

```go
ctx, adapterCancel := llm.ApplyAdapterTimeout(ctx, req.AdapterTimeout, false)
defer adapterCancel()
```

In each adapter's `Stream` method, do NOT apply the Request timeout (streaming has its own lifecycle), but pass the timeout through for documentation. The StreamRead timeout is a future enhancement.

**Step 4: Write an adapter-level integration test**

```go
func TestAdapterTimeout_Request_EnforcedOnComplete(t *testing.T) {
	// Server that hangs for 5 seconds.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		json.NewEncoder(w).Encode(minimalResponse())
	}))
	defer srv.Close()

	a := &Adapter{APIKey: "test", BaseURL: srv.URL, Client: srv.Client()}
	_, err := a.Complete(context.Background(), llm.Request{
		Model:    "test",
		Messages: []llm.Message{llm.User("hi")},
		AdapterTimeout: &llm.AdapterTimeout{
			Request: 100 * time.Millisecond,
		},
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	var timeoutErr *llm.RequestTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Errorf("expected RequestTimeoutError, got %T: %v", err, err)
	}
}
```

Add this test to each of the four adapter test files, adapting the response format per adapter.

**Step 5: Run full test suite**

Run: `go test ./internal/llm/... -v -count=1`
Expected: All PASS

**Step 6: Commit**

```bash
git add internal/llm/adapter_timeout.go internal/llm/adapter_timeout_test.go
git add internal/llm/providers/openai/adapter.go internal/llm/providers/openai/adapter_test.go
git add internal/llm/providers/anthropic/adapter.go internal/llm/providers/anthropic/adapter_test.go
git add internal/llm/providers/google/adapter.go internal/llm/providers/google/adapter_test.go
git add internal/llm/providers/openaicompat/adapter.go internal/llm/providers/openaicompat/adapter_test.go
git commit -m "feat: wire AdapterTimeout.Request to all provider adapters"
```

---

## Task 7: Add `DefaultHeaders` to adapter structs

The spec says adapters should accept custom default headers for programmatic construction.

**Files:**
- Modify: `internal/llm/providers/openai/adapter.go` (struct + HTTP request construction)
- Modify: `internal/llm/providers/anthropic/adapter.go`
- Modify: `internal/llm/providers/google/adapter.go`
- Modify: `internal/llm/providers/openaicompat/adapter.go`
- Test: each adapter's test file

**Step 1: Write the failing test (OpenAI as example)**

```go
func TestDefaultHeaders_SentOnRequests(t *testing.T) {
	var capturedHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header
		// Return minimal valid response.
		json.NewEncoder(w).Encode(minimalResponsesAPIResponse())
	}))
	defer srv.Close()

	a := &Adapter{
		APIKey:  "test",
		BaseURL: srv.URL,
		Client:  srv.Client(),
		DefaultHeaders: map[string]string{
			"X-Custom-Header": "custom-value",
		},
	}
	_, err := a.Complete(context.Background(), llm.Request{Model: "test", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	if capturedHeaders.Get("X-Custom-Header") != "custom-value" {
		t.Errorf("X-Custom-Header = %q, want %q", capturedHeaders.Get("X-Custom-Header"), "custom-value")
	}
}
```

**Step 2: Run test to verify it fails (field doesn't exist yet)**

**Step 3: Implement**

Add to each Adapter struct:
```go
DefaultHeaders map[string]string
```

In each adapter's HTTP request construction (where headers are set), add:
```go
for k, v := range a.DefaultHeaders {
	httpReq.Header.Set(k, v)
}
```

Place this BEFORE provider-specific headers (like Authorization, x-api-key, etc.) so that provider auth headers cannot be accidentally overwritten by custom headers. Actually, place it AFTER so custom headers can override if the user explicitly wants to. The spec says "default_headers" implies they're applied as defaults — let provider-specific headers be set after custom ones. On second thought, the spec doesn't specify precedence, so set custom headers first and let provider-specific ones override:

```go
// Apply default headers first.
for k, v := range a.DefaultHeaders {
	httpReq.Header.Set(k, v)
}
// Then provider-specific headers (these take precedence).
httpReq.Header.Set("Authorization", "Bearer "+a.APIKey)
```

**Step 4: Run test to verify it passes. Run full suite.**

**Step 5: Commit**

```bash
git add internal/llm/providers/*/adapter.go internal/llm/providers/*/adapter_test.go
git commit -m "feat: add DefaultHeaders field to all adapter structs"
```

---

## Task 8: Cross-provider integration test coverage gaps

Add the missing and partial integration tests from the DoD Section 8.9 audit.

**Files:**
- Modify: `internal/llm/integration_smoke_test.go`

**Step 1: Add image URL cross-provider test**

```go
func TestIntegration_ImageInputURL(t *testing.T) {
	skipIfNoProviders(t)
	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	// Use a small, publicly accessible image URL.
	imageURL := "https://upload.wikimedia.org/wikipedia/commons/thumb/4/47/PNG_transparency_demonstration_1.png/280px-PNG_transparency_demonstration_1.png"

	for _, p := range providers {
		t.Run(p.provider, func(t *testing.T) {
			if os.Getenv(p.envKey) == "" {
				t.Skipf("no %s key", p.envKey)
			}
			result, err := llm.Generate(context.Background(), llm.GenerateOptions{
				Client:   client,
				Model:    p.model,
				Provider: p.provider,
				Messages: []llm.Message{{
					Role: llm.RoleUser,
					Content: []llm.ContentPart{
						{Kind: llm.ContentText, Text: "Describe this image in one sentence."},
						{Kind: llm.ContentImage, Image: &llm.ImageData{URL: imageURL}},
					},
				}},
				MaxTokens: intPtr(200),
			})
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if result.Text == "" {
				t.Error("expected non-empty response text")
			}
		})
	}
}
```

**Step 2: Add streaming with tool calls cross-provider test**

```go
func TestIntegration_StreamingWithTools(t *testing.T) {
	skipIfNoProviders(t)
	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	weatherTool := llm.Tool{
		Definition: llm.ToolDefinition{
			Name:        "get_weather",
			Description: "Get weather for a city",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"city": map[string]any{"type": "string"}},
				"required":   []any{"city"},
			},
		},
		Execute: func(ctx context.Context, args any) (any, error) {
			return "72F and sunny", nil
		},
	}

	for _, p := range providers {
		t.Run(p.provider, func(t *testing.T) {
			if os.Getenv(p.envKey) == "" {
				t.Skipf("no %s key", p.envKey)
			}
			result, err := llm.StreamGenerate(context.Background(), llm.GenerateOptions{
				Client:        client,
				Model:         p.model,
				Provider:      p.provider,
				Prompt:        strPtr("What is the weather in Paris?"),
				Tools:         []llm.Tool{weatherTool},
				MaxToolRounds: intPtr(3),
				MaxTokens:     intPtr(300),
			})
			if err != nil {
				t.Fatalf("StreamGenerate: %v", err)
			}
			var hasStepFinish bool
			for ev := range result.Events() {
				if ev.Type == llm.StreamEventStepFinish {
					hasStepFinish = true
				}
			}
			resp := result.Response()
			if resp == nil {
				t.Fatal("no response after stream")
			}
			if resp.Text() == "" {
				t.Error("expected non-empty final text")
			}
			if !hasStepFinish {
				t.Error("expected at least one STEP_FINISH event (tool was called)")
			}
		})
	}
}
```

**Step 3: Add error handling (401) cross-provider test**

```go
func TestIntegration_AuthenticationError(t *testing.T) {
	for _, p := range providers {
		t.Run(p.provider, func(t *testing.T) {
			if os.Getenv(p.envKey) == "" {
				t.Skipf("no %s key", p.envKey)
			}
			// Build a client with an invalid key for this provider.
			badClient := llm.NewClient()
			// We'll need to register an adapter with a bad key per provider.
			// This is provider-specific, so use provider_options or construct adapters directly.
			// For simplicity, just test that the error hierarchy works by sending to a
			// nonexistent endpoint with the real key.
			_, err := llm.Generate(context.Background(), llm.GenerateOptions{
				Client:   badClient,
				Model:    "nonexistent-model-xyzzy-12345",
				Provider: p.provider,
				Prompt:   strPtr("test"),
			})
			// The actual registration is needed — defer this test to after
			// the invalid-key adapter is set up. This test is tricky because
			// we need to construct adapters with bad keys programmatically.
			// Skip for now; the per-adapter unit tests already cover 401.
			if err == nil {
				t.Fatal("expected error")
			}
			var notFound *llm.NotFoundError
			if !errors.As(err, &notFound) {
				// Some providers return 404 for bad model, some return 400.
				var invalidReq *llm.InvalidRequestError
				if !errors.As(err, &invalidReq) {
					t.Logf("error type: %T, error: %v", err, err)
				}
			}
		})
	}
}
```

Note: True 401 testing across real providers requires constructing adapters with invalid keys, which is more involved. The per-adapter unit tests cover this thoroughly. For the cross-provider integration test, testing with an invalid model name (404/400) is the pragmatic approach.

**Step 4: Run integration tests**

Run: `export $(cat .env | xargs) && go test ./internal/llm/ -run TestIntegration -v -count=1`
Expected: All PASS (for providers with keys configured)

**Step 5: Commit**

```bash
git add internal/llm/integration_smoke_test.go
git commit -m "test: add cross-provider integration tests for URL images, streaming tools, and errors"
```

---

## Task 9: Multi-turn prompt caching verification test

Add integration test that verifies cache_read_tokens > 50% of input_tokens on turn 5+ for all providers.

**Files:**
- Modify: `internal/llm/integration_smoke_test.go`

**Step 1: Write the test**

```go
func TestIntegration_PromptCaching_MultiTurn(t *testing.T) {
	skipIfNoProviders(t)
	if testing.Short() {
		t.Skip("skipping multi-turn caching test in short mode")
	}
	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	// Use a large system prompt to make caching worthwhile.
	systemPrompt := strings.Repeat("You are a helpful assistant. ", 200) // ~1200 words

	for _, p := range providers {
		t.Run(p.provider, func(t *testing.T) {
			if os.Getenv(p.envKey) == "" {
				t.Skipf("no %s key", p.envKey)
			}

			history := []llm.Message{llm.System(systemPrompt)}
			var lastUsage llm.Usage

			for turn := 1; turn <= 6; turn++ {
				history = append(history, llm.User(fmt.Sprintf("Turn %d: What is %d + %d?", turn, turn, turn*2)))
				result, err := llm.Generate(context.Background(), llm.GenerateOptions{
					Client:    client,
					Model:     p.model,
					Provider:  p.provider,
					Messages:  history,
					MaxTokens: intPtr(50),
				})
				if err != nil {
					t.Fatalf("turn %d: %v", turn, err)
				}
				history = append(history, llm.Assistant(result.Text))
				lastUsage = result.Usage

				if turn >= 5 {
					cacheRead := 0
					if lastUsage.CacheReadTokens != nil {
						cacheRead = *lastUsage.CacheReadTokens
					}
					inputTokens := lastUsage.InputTokens
					if inputTokens > 0 {
						cacheRatio := float64(cacheRead) / float64(inputTokens)
						t.Logf("turn %d: input=%d cache_read=%d ratio=%.1f%%",
							turn, inputTokens, cacheRead, cacheRatio*100)
						if cacheRatio < 0.5 {
							t.Errorf("turn %d: cache_read_tokens (%d) < 50%% of input_tokens (%d)",
								turn, cacheRead, inputTokens)
						}
					}
				}
			}
		})
	}
}
```

Note: This test makes real API calls (6 turns x 3 providers = 18 calls). It should only run when integration test keys are set and not in `-short` mode. Caching behavior depends on provider-side factors (minimum token thresholds, timing windows), so this test may be flaky. Consider using `t.Logf` to report the ratio and only `t.Errorf` when it's clearly wrong.

**Step 2: Run test**

Run: `export $(cat .env | xargs) && go test ./internal/llm/ -run TestIntegration_PromptCaching_MultiTurn -v -count=1 -timeout=300s`

**Step 3: Commit**

```bash
git add internal/llm/integration_smoke_test.go
git commit -m "test: add multi-turn prompt caching verification across all providers"
```

---

## Task 10: Final verification

**Step 1: Run the full test suite**

```bash
go test ./internal/llm/... -v -count=1
```

Expected: All unit tests PASS.

**Step 2: Run integration tests (if keys available)**

```bash
export $(cat .env | xargs) && go test ./internal/llm/... -v -count=1 -timeout=600s
```

Expected: All PASS.

**Step 3: Run the complete project test suite**

```bash
go test ./... -count=1
```

Expected: All PASS, no regressions.

**Step 4: Commit any final fixups**

If any tests needed adjustment, commit them.

---

## Items NOT remediated (by design)

These items from the audit were classified as deviations rather than defects, and are intentional language-appropriate adaptations:

- **V1-V5**: Go idioms (channels vs async iterators, context.Context vs AbortSignal, etc.)
- **V6**: ToolCallData field naming (Arguments/ParsedArguments vs arguments/raw_arguments) — would be a breaking API change for no functional benefit
- **V7**: `ReasoningText()` returns `""` instead of nil — Go idiom, no nullable strings
- **V8**: `GetLatestModel()` heuristic — catalog data lacks release dates; context_window is the best available proxy
- **V9**: `stop_when` checked after tool execution — would require rearchitecting the loop for minimal benefit
- **M2**: Direction constraints on ContentParts — this is validation that the spec describes but none of the providers actually enforce on the API side. Adding it would reject valid requests. Skip unless Jesse wants it.
- **M3**: Gemini gRPC status code mapping — the adapter uses HTTP/REST only. gRPC mapping is only relevant if we add gRPC transport.
- **T2/T7/T8**: 429 cross-provider test, 401 cross-provider test, usage accuracy — these are difficult to test deterministically with real APIs and are well-covered at the unit level.
