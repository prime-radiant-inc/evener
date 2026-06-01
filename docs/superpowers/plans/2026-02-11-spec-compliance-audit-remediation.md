# Spec Compliance Audit Remediation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Remediate all actionable gaps found in the comprehensive unified-llm-spec.md compliance audit.

**Architecture:** Fix provider adapter bugs, complete streaming infrastructure, enforce timeout contracts, and fill integration test gaps. All changes are backward-compatible modifications to existing files in `internal/llm/`.

**Tech Stack:** Go, `internal/llm/` package, provider adapters (OpenAI, Anthropic, Google, openai-compat), `encoding/json`, `net/http`, `context`, `sync`

**Audit source:** `docs/audit-logs/section-{1-2,3,4,5,6,7,8}-*.md`

---

## Batch 1: Critical Data Corruption and Silent Failures

### Task 1: Fix Anthropic fmt.Sprint for structured tool results

The Anthropic adapter uses `fmt.Sprint(p.ToolResult.Content)` to convert tool result content to a string. For non-string content (maps, slices), this produces Go-formatted garbage like `map[key:value]` instead of JSON. The OpenAI and openai-compat adapters correctly use a type switch with `json.Marshal`.

**Files:**
- Modify: `internal/llm/providers/anthropic/adapter.go:998-1003`
- Test: `internal/llm/providers/anthropic/adapter_test.go`

**Step 1: Write the failing test**

```go
func TestAdapter_Complete_ToolResultStructuredContent_MarshaledAsJSON(t *testing.T) {
	// Capture what the adapter sends to the API when tool result content is a map.
	var sentBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &sentBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":    "msg_1",
			"type":  "message",
			"role":  "assistant",
			"model": "test",
			"content": []any{
				map[string]any{"type": "text", "text": "ok"},
			},
			"stop_reason": "end_turn",
			"usage":       map[string]any{"input_tokens": 10, "output_tokens": 5},
		})
	}))
	defer srv.Close()

	a := &Adapter{APIKey: "test-key", BaseURL: srv.URL}
	structuredContent := map[string]any{"temperature": 72, "unit": "F"}
	_, err := a.Complete(context.Background(), llm.Request{
		Model: "test",
		Messages: []llm.Message{
			llm.User("What's the weather?"),
			{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
				Kind:     llm.ContentToolCall,
				ToolCall: &llm.ToolCallData{ID: "call_1", Name: "weather", Arguments: json.RawMessage(`{}`)},
			}}},
			llm.ToolResult("call_1", structuredContent, false),
		},
	})
	require.NoError(t, err)

	// Find the tool_result block in the sent messages.
	msgs, _ := sentBody["messages"].([]any)
	require.NotEmpty(t, msgs)
	lastMsg, _ := msgs[len(msgs)-1].(map[string]any)
	content, _ := lastMsg["content"].([]any)
	require.NotEmpty(t, content)
	block, _ := content[0].(map[string]any)
	contentStr, _ := block["content"].(string)

	// MUST be valid JSON, not Go fmt.Sprint output.
	assert.NotContains(t, contentStr, "map[", "content should be JSON, not Go fmt.Sprint")
	var parsed map[string]any
	err = json.Unmarshal([]byte(contentStr), &parsed)
	assert.NoError(t, err, "content should be valid JSON")
	assert.Equal(t, float64(72), parsed["temperature"])
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/llm/providers/anthropic/ -run TestAdapter_Complete_ToolResultStructuredContent -v`
Expected: FAIL -- contentStr contains `map[temperature:72 unit:F]`

**Step 3: Write minimal implementation**

In `internal/llm/providers/anthropic/adapter.go`, replace the `fmt.Sprint` call (around line 1001) with a type switch matching the OpenAI adapter pattern:

```go
// Replace:
//   "content": fmt.Sprint(p.ToolResult.Content),
// With:
outStr := ""
switch v := p.ToolResult.Content.(type) {
case string:
	outStr = v
default:
	b, _ := json.Marshal(v)
	outStr = string(b)
}
```

Then use `outStr` instead of `fmt.Sprint(p.ToolResult.Content)` in the map literal.

**Step 4: Run test to verify it passes**

Run: `go test ./internal/llm/providers/anthropic/ -run TestAdapter_Complete_ToolResultStructuredContent -v`
Expected: PASS

**Step 5: Run full adapter test suite**

Run: `go test ./internal/llm/providers/anthropic/ -v -count=1`
Expected: All PASS

**Step 6: Commit**

```bash
git add internal/llm/providers/anthropic/adapter.go internal/llm/providers/anthropic/adapter_test.go
git commit -m "fix: use json.Marshal for Anthropic tool result content instead of fmt.Sprint"
```

---

### Task 2: Add is_error to OpenAI tool results

The OpenAI adapter builds `function_call_output` items without an `is_error` field. The Responses API supports this field. When a tool execution fails and `IsError` is true, the model doesn't know the result is an error.

**Files:**
- Modify: `internal/llm/providers/openai/adapter.go:717-721`
- Test: `internal/llm/providers/openai/adapter_test.go`

**Step 1: Write the failing test**

```go
func TestAdapter_Complete_ToolResultIsError_SentToAPI(t *testing.T) {
	var sentBody map[string]any
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &sentBody)
		json.NewEncoder(w).Encode(map[string]any{
			"id":     "resp_1",
			"status": "completed",
			"output": []any{
				map[string]any{"type": "message", "role": "assistant", "content": []any{
					map[string]any{"type": "output_text", "text": "ok"},
				}},
			},
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15},
		})
	})
	defer srv.Close()

	a := &Adapter{APIKey: "test-key", BaseURL: srv.URL}
	_, err := a.Complete(context.Background(), llm.Request{
		Model: "test",
		Messages: []llm.Message{
			llm.User("call tool"),
			{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
				Kind:     llm.ContentToolCall,
				ToolCall: &llm.ToolCallData{ID: "call_1", Name: "failing_tool", Arguments: json.RawMessage(`{}`)},
			}}},
			llm.ToolResult("call_1", "connection refused", true),
		},
	})
	require.NoError(t, err)

	// Find the function_call_output item.
	input, _ := sentBody["input"].([]any)
	var found bool
	for _, item := range input {
		m, _ := item.(map[string]any)
		if m["type"] == "function_call_output" {
			isErr, ok := m["is_error"]
			assert.True(t, ok, "is_error field must be present")
			assert.Equal(t, true, isErr, "is_error must be true for error results")
			found = true
		}
	}
	assert.True(t, found, "must find a function_call_output item")
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/llm/providers/openai/ -run TestAdapter_Complete_ToolResultIsError -v`
Expected: FAIL -- `is_error` field not present

**Step 3: Write minimal implementation**

In `internal/llm/providers/openai/adapter.go`, around line 717-721, add `is_error` to the output map when `p.ToolResult.IsError` is true:

```go
item := map[string]any{
	"type":    "function_call_output",
	"call_id": p.ToolResult.ToolCallID,
	"output":  outStr,
}
if p.ToolResult.IsError {
	item["is_error"] = true
}
items = append(items, item)
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/llm/providers/openai/ -run TestAdapter_Complete_ToolResultIsError -v`
Expected: PASS

**Step 5: Run full adapter test suite**

Run: `go test ./internal/llm/providers/openai/ -v -count=1`
Expected: All PASS

**Step 6: Commit**

```bash
git add internal/llm/providers/openai/adapter.go internal/llm/providers/openai/adapter_test.go
git commit -m "fix: pass is_error on OpenAI tool results"
```

---

### Task 3: Add is_error to Gemini tool results

Gemini's `functionResponse` doesn't have a native `is_error` field, but error status should be conveyed by wrapping the error in the response object so the model can distinguish success from failure.

**Files:**
- Modify: `internal/llm/providers/google/adapter.go:753-769`
- Test: `internal/llm/providers/google/adapter_test.go`

**Step 1: Write the failing test**

```go
func TestAdapter_Complete_ToolResultIsError_ConveyedInResponse(t *testing.T) {
	var sentBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &sentBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"candidates": []any{map[string]any{
				"content": map[string]any{
					"role":  "model",
					"parts": []any{map[string]any{"text": "ok"}},
				},
			}},
			"usageMetadata": map[string]any{
				"promptTokenCount":     10,
				"candidatesTokenCount": 5,
				"totalTokenCount":      15,
			},
		})
	}))
	defer srv.Close()

	a := &Adapter{APIKey: "test-key", BaseURL: srv.URL}
	_, err := a.Complete(context.Background(), llm.Request{
		Model: "test",
		Messages: []llm.Message{
			llm.User("call tool"),
			{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
				Kind:     llm.ContentToolCall,
				ToolCall: &llm.ToolCallData{ID: "call_1", Name: "failing_tool", Arguments: json.RawMessage(`{}`)},
			}}},
			llm.ToolResultNamed("call_1", "failing_tool", "connection refused", true),
		},
	})
	require.NoError(t, err)

	// Find the functionResponse part in sent body.
	contents, _ := sentBody["contents"].([]any)
	require.NotEmpty(t, contents)
	lastContent, _ := contents[len(contents)-1].(map[string]any)
	parts, _ := lastContent["parts"].([]any)
	require.NotEmpty(t, parts)
	part, _ := parts[0].(map[string]any)
	fr, _ := part["functionResponse"].(map[string]any)
	resp, _ := fr["response"].(map[string]any)

	// Error results should include an error indicator.
	assert.Equal(t, true, resp["error"], "error tool results must include error: true in response")
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/llm/providers/google/ -run TestAdapter_Complete_ToolResultIsError -v`
Expected: FAIL -- no `error` key in response

**Step 3: Write minimal implementation**

In `internal/llm/providers/google/adapter.go`, after building `respObj` (around line 763), add an error indicator when `IsError` is true:

```go
if p.ToolResult.IsError {
	respObj["error"] = true
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/llm/providers/google/ -run TestAdapter_Complete_ToolResultIsError -v`
Expected: PASS

**Step 5: Run full adapter test suite**

Run: `go test ./internal/llm/providers/google/ -v -count=1`
Expected: All PASS

**Step 6: Commit**

```bash
git add internal/llm/providers/google/adapter.go internal/llm/providers/google/adapter_test.go
git commit -m "fix: convey is_error in Gemini tool result response objects"
```

---

### Task 4: Populate Usage.Raw with actual provider data

All three primary adapters set `Usage.Raw` to an empty `map[string]any{}` instead of the actual provider usage data. The raw data is available (it's the `u map[string]any` parameter) and should be passed through.

**Files:**
- Modify: `internal/llm/providers/openai/adapter.go` (parseUsage, ~line 854)
- Modify: `internal/llm/providers/anthropic/adapter.go` (parseUsage, ~line 1264)
- Modify: `internal/llm/providers/google/adapter.go` (parseUsage, ~line 971)
- Test: one test per adapter file

**Step 1: Write the failing tests**

In each adapter's test file, add a test that verifies `Usage.Raw` contains the raw provider data:

```go
// In openai/adapter_test.go:
func TestAdapter_Complete_UsageRaw_ContainsProviderData(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": "resp_1", "status": "completed",
			"output": []any{map[string]any{"type": "message", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": "hi"}}}},
			"usage": map[string]any{
				"input_tokens": 10, "output_tokens": 5, "total_tokens": 15,
				"output_tokens_details": map[string]any{"reasoning_tokens": 2},
			},
		})
	})
	defer srv.Close()
	a := &Adapter{APIKey: "k", BaseURL: srv.URL}
	resp, err := a.Complete(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.User("hi")}})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.Usage.Raw, "Usage.Raw must contain provider data")
	assert.Contains(t, resp.Usage.Raw, "input_tokens")
}
```

Write analogous tests for Anthropic (checking for `"input_tokens"` key) and Google (checking for `"promptTokenCount"` key).

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/llm/providers/openai/ -run TestAdapter_Complete_UsageRaw -v`
Run: `go test ./internal/llm/providers/anthropic/ -run TestAdapter_Complete_UsageRaw -v`
Run: `go test ./internal/llm/providers/google/ -run TestAdapter_Complete_UsageRaw -v`
Expected: All FAIL -- `Usage.Raw` is empty

**Step 3: Write minimal implementation**

In each adapter's `parseUsage` function, change `Raw: map[string]any{}` to `Raw: u`:

```go
// In all three parseUsage functions, replace:
//   Raw: map[string]any{},
// With:
Raw: u,
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/llm/providers/{openai,anthropic,google}/ -run TestAdapter_Complete_UsageRaw -v`
Expected: All PASS

**Step 5: Run full test suites**

Run: `go test ./internal/llm/providers/... -v -count=1`
Expected: All PASS

**Step 6: Commit**

```bash
git add internal/llm/providers/openai/adapter.go internal/llm/providers/anthropic/adapter.go internal/llm/providers/google/adapter.go
git add internal/llm/providers/openai/adapter_test.go internal/llm/providers/anthropic/adapter_test.go internal/llm/providers/google/adapter_test.go
git commit -m "fix: populate Usage.Raw with actual provider usage data"
```

---

## Batch 2: Streaming Completeness

### Task 5: Complete StreamAccumulator to handle reasoning and tool calls

`StreamAccumulator` only accumulates text content. It drops REASONING and TOOL_CALL events entirely, producing an incomplete Response when used standalone. The `buildResponse()` method only creates text ContentParts.

**Files:**
- Modify: `internal/llm/stream_accumulator.go`
- Test: `internal/llm/stream_accumulator_test.go`

**Step 1: Write the failing tests**

```go
func TestStreamAccumulator_ReasoningEvents_AccumulatedInResponse(t *testing.T) {
	acc := NewStreamAccumulator()
	acc.Process(StreamEvent{Type: StreamEventStreamStart})
	acc.Process(StreamEvent{Type: StreamEventReasoningStart})
	acc.Process(StreamEvent{Type: StreamEventReasoningDelta, ReasoningDelta: "Let me think"})
	acc.Process(StreamEvent{Type: StreamEventReasoningDelta, ReasoningDelta: " about this."})
	acc.Process(StreamEvent{Type: StreamEventReasoningEnd})
	acc.Process(StreamEvent{Type: StreamEventTextStart, TextID: "t1"})
	acc.Process(StreamEvent{Type: StreamEventTextDelta, TextID: "t1", Delta: "Result"})
	acc.Process(StreamEvent{Type: StreamEventTextEnd, TextID: "t1"})
	acc.Process(StreamEvent{Type: StreamEventFinish, FinishReason: &FinishReason{Reason: "stop"}})

	resp := acc.Response()
	assert.Equal(t, "Result", resp.Text())
	assert.Equal(t, "Let me think about this.", resp.ReasoningText())
}

func TestStreamAccumulator_ToolCallEvents_AccumulatedInResponse(t *testing.T) {
	acc := NewStreamAccumulator()
	acc.Process(StreamEvent{Type: StreamEventStreamStart})
	acc.Process(StreamEvent{Type: StreamEventToolCallStart, ToolCall: &ToolCallData{
		ID: "call_1", Name: "get_weather", Type: "function",
	}})
	acc.Process(StreamEvent{Type: StreamEventToolCallDelta, ToolCall: &ToolCallData{
		ID: "call_1", Arguments: json.RawMessage(`{"city":`),
	}})
	acc.Process(StreamEvent{Type: StreamEventToolCallDelta, ToolCall: &ToolCallData{
		ID: "call_1", Arguments: json.RawMessage(`"SF"}`),
	}})
	acc.Process(StreamEvent{Type: StreamEventToolCallEnd, ToolCall: &ToolCallData{ID: "call_1"}})
	acc.Process(StreamEvent{Type: StreamEventFinish, FinishReason: &FinishReason{Reason: "tool_calls"}})

	resp := acc.Response()
	calls := resp.ToolCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, "call_1", calls[0].ID)
	assert.Equal(t, "get_weather", calls[0].Name)
	assert.Equal(t, `{"city":"SF"}`, string(calls[0].Arguments))
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/llm/ -run TestStreamAccumulator_Reasoning -v`
Run: `go test ./internal/llm/ -run TestStreamAccumulator_ToolCall -v`
Expected: Both FAIL

**Step 3: Write minimal implementation**

Extend `StreamAccumulator` to track reasoning text and tool calls:

1. Add fields to the struct: `reasoning strings.Builder`, `toolCalls map[string]*ToolCallData`, `toolCallOrder []string`
2. Process REASONING_DELTA events by appending to `reasoning`
3. Process TOOL_CALL_START by creating an entry in `toolCalls`
4. Process TOOL_CALL_DELTA by appending to the arguments buffer
5. In `buildResponse()`, include ThinkingData and ToolCallData ContentParts alongside text

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/llm/ -run TestStreamAccumulator -v`
Expected: All PASS

**Step 5: Run full package tests**

Run: `go test ./internal/llm/ -v -count=1 -short`
Expected: All PASS

**Step 6: Commit**

```bash
git add internal/llm/stream_accumulator.go internal/llm/stream_accumulator_test.go
git commit -m "fix: StreamAccumulator handles reasoning and tool call events"
```

---

### Task 6: Add StopWhen check to StreamGenerate

`Generate()` checks `opts.StopWhen` after each tool execution round (generate.go:262-274). `StreamGenerate()` has the `StopWhen` field on `GenerateOptions` but never checks it.

**Files:**
- Modify: `internal/llm/stream_generate.go` (after tool execution, before continuing loop)
- Test: `internal/llm/stream_generate_test.go`

**Step 1: Write the failing test**

```go
func TestStreamGenerate_StopWhen_TerminatesToolLoop(t *testing.T) {
	// Adapter returns tool calls on every step.
	adapter := &scriptedStreamAdapter{
		name: "test",
		scripts: []func(ctx context.Context, req Request) (Stream, error){
			makeToolCallStream("call_1", "counter", `{}`),
			makeToolCallStream("call_2", "counter", `{}`),
			makeToolCallStream("call_3", "counter", `{}`), // should not be reached
		},
	}
	c := NewClient()
	c.Register(adapter)

	stepCount := 0
	maxRounds := 10
	res, err := StreamGenerate(context.Background(), GenerateOptions{
		Client:        c,
		Model:         "m",
		Prompt:        strPtr("go"),
		MaxToolRounds: &maxRounds,
		Tools: []Tool{{
			Definition: ToolDefinition{Name: "counter", Parameters: map[string]any{"type": "object"}},
			Execute:    func(ctx context.Context, args json.RawMessage) (any, error) { return "ok", nil },
		}},
		StopWhen: func(steps []StepResult) bool {
			stepCount++
			return stepCount >= 2 // Stop after 2 rounds
		},
	})
	require.NoError(t, err)
	// Drain events.
	for range res.Events() {}
	resp, err := res.Response()
	require.NoError(t, err)
	// Should have stopped after 2 rounds, not used all 3 scripts.
	assert.LessOrEqual(t, len(adapter.requests), 2)
	assert.NotNil(t, resp)
}
```

Note: `makeToolCallStream` is a helper you'll need to write or adapt from existing test helpers in `stream_generate_test.go`. It should produce a `Stream` that emits `STREAM_START`, `TOOL_CALL_START`, `TOOL_CALL_END`, `FINISH` events.

**Step 2: Run test to verify it fails**

Run: `go test ./internal/llm/ -run TestStreamGenerate_StopWhen -v`
Expected: FAIL -- all 3 scripts consumed because StopWhen is never checked

**Step 3: Write minimal implementation**

In `stream_generate.go`, after tool results are collected and appended to history (around where STEP_FINISH is emitted), add a `StopWhen` check mirroring `generate.go`:

```go
// After emitting STEP_FINISH and before continuing the loop:
if opts.StopWhen != nil && opts.StopWhen(steps) {
	// StopWhen triggered: emit FINISH and close.
	if finishEv.Response == nil {
		cp := *stepResp
		finishEv.Response = &cp
	}
	outStream.Send(*finishEv)
	res.mu.Lock()
	cp := *stepResp
	res.final = &cp
	res.partial = &cp
	res.mu.Unlock()
	return
}
```

This requires tracking `steps []StepResult` in `StreamGenerate` the same way `Generate` does.

**Step 4: Run test to verify it passes**

Run: `go test ./internal/llm/ -run TestStreamGenerate_StopWhen -v`
Expected: PASS

**Step 5: Run full package tests**

Run: `go test ./internal/llm/ -v -count=1 -short`
Expected: All PASS

**Step 6: Commit**

```bash
git add internal/llm/stream_generate.go internal/llm/stream_generate_test.go
git commit -m "fix: StreamGenerate checks StopWhen after each tool round"
```

---

### Task 7: Add WebSearch to GenerateOptions

`Request.WebSearch` exists and all adapters check it, but `GenerateOptions` has no `WebSearch` field. Neither `Generate()` nor `StreamGenerate()` propagate it to the `Request`. Web search can't be requested through the high-level API.

**Files:**
- Modify: `internal/llm/generate.go` (add field to GenerateOptions, set on Request)
- Modify: `internal/llm/stream_generate.go` (set on Request)
- Test: `internal/llm/generate_test.go`

**Step 1: Write the failing test**

```go
func TestGenerate_WebSearch_PropagatedToRequest(t *testing.T) {
	adapter := &scriptedAdapter{
		name: "test",
		steps: []func(req Request) (Response, error){
			func(req Request) (Response, error) {
				assert.True(t, req.WebSearch, "WebSearch must be propagated to Request")
				return Response{
					Message: Assistant("result"),
					Finish:  FinishReason{Reason: FinishReasonStop},
					Usage:   Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
				}, nil
			},
		},
	}
	c := NewClient()
	c.Register(adapter)

	_, err := Generate(context.Background(), GenerateOptions{
		Client:    c,
		Model:     "m",
		Prompt:    strPtr("search the web"),
		WebSearch: true,
	})
	require.NoError(t, err)
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/llm/ -run TestGenerate_WebSearch_Propagated -v`
Expected: FAIL -- compile error (no WebSearch field) or assertion fails

**Step 3: Write minimal implementation**

1. Add to `GenerateOptions`:
   ```go
   WebSearch bool
   ```

2. In `Generate()`, when building the `Request` (around line 170-186), add:
   ```go
   WebSearch: opts.WebSearch,
   ```

3. In `StreamGenerate()`, same addition when building the `Request`.

**Step 4: Run test to verify it passes**

Run: `go test ./internal/llm/ -run TestGenerate_WebSearch_Propagated -v`
Expected: PASS

**Step 5: Run full package tests**

Run: `go test ./internal/llm/ -v -count=1 -short`
Expected: All PASS

**Step 6: Commit**

```bash
git add internal/llm/generate.go internal/llm/stream_generate.go internal/llm/generate_test.go
git commit -m "feat: add WebSearch to GenerateOptions and propagate to Request"
```

---

## Batch 3: Provider Adapter Fixes

### Task 8: Fix Anthropic web search without function tools

When `req.Tools` is empty but `req.WebSearch` is true, the Anthropic adapter skips the entire tools block because it's gated by `includeTools && len(req.Tools) > 0`. The web search server tool is never added.

**Files:**
- Modify: `internal/llm/providers/anthropic/adapter.go` (Complete and Stream paths)
- Test: `internal/llm/providers/anthropic/adapter_test.go`

**Step 1: Write the failing test**

```go
func TestAdapter_Complete_WebSearchOnly_IncludesWebSearchTool(t *testing.T) {
	var sentBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &sentBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "test",
			"content":     []any{map[string]any{"type": "text", "text": "search result"}},
			"stop_reason": "end_turn",
			"usage":       map[string]any{"input_tokens": 10, "output_tokens": 5},
		})
	}))
	defer srv.Close()

	a := &Adapter{APIKey: "test-key", BaseURL: srv.URL}
	_, err := a.Complete(context.Background(), llm.Request{
		Model:     "test",
		Messages:  []llm.Message{llm.User("search the web")},
		WebSearch: true,
		// Note: no Tools
	})
	require.NoError(t, err)

	tools, ok := sentBody["tools"].([]any)
	assert.True(t, ok, "tools must be present in request body")
	require.Len(t, tools, 1)
	tool, _ := tools[0].(map[string]any)
	assert.Equal(t, "web_search_20250305", tool["type"])
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/llm/providers/anthropic/ -run TestAdapter_Complete_WebSearchOnly -v`
Expected: FAIL -- tools not present in sentBody

**Step 3: Write minimal implementation**

In both the `Complete()` and `Stream()` paths, change the condition from `includeTools && len(req.Tools) > 0` to also handle the web-search-only case. For example:

```go
// Replace:
//   if includeTools && len(req.Tools) > 0 {
// With:
if len(req.Tools) > 0 || req.WebSearch {
	tools := toAnthropicTools(req.Tools) // returns empty slice if no tools
	if req.WebSearch {
		tools = append(tools, map[string]any{
			"type": "web_search_20250305",
			"name": "web_search",
		})
	}
	if autoCache && len(tools) > 0 {
		tools[len(tools)-1]["cache_control"] = map[string]any{"type": "ephemeral"}
	}
	body["tools"] = tools
}
```

Apply this change to both `Complete()` (around line 129) and `Stream()` (around line 284).

**Step 4: Run test to verify it passes**

Run: `go test ./internal/llm/providers/anthropic/ -run TestAdapter_Complete_WebSearchOnly -v`
Expected: PASS

**Step 5: Run full adapter test suite**

Run: `go test ./internal/llm/providers/anthropic/ -v -count=1`
Expected: All PASS

**Step 6: Commit**

```bash
git add internal/llm/providers/anthropic/adapter.go internal/llm/providers/anthropic/adapter_test.go
git commit -m "fix: Anthropic web search works without function tools"
```

---

### Task 9: Remove Gemini maxOutputTokens hard default

The Gemini adapter always sends `maxOutputTokens: 2048` when the caller doesn't specify `MaxTokens`. This silently caps output. Gemini's API accepts the field being absent, defaulting to the model's maximum.

**Files:**
- Modify: `internal/llm/providers/google/adapter.go` (Complete and Stream paths)
- Test: `internal/llm/providers/google/adapter_test.go`

**Step 1: Write the failing test**

```go
func TestAdapter_Complete_NoMaxTokens_OmitsMaxOutputTokens(t *testing.T) {
	var sentBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &sentBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"candidates": []any{map[string]any{
				"content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": "hi"}}},
			}},
			"usageMetadata": map[string]any{"promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2},
		})
	}))
	defer srv.Close()

	a := &Adapter{APIKey: "test-key", BaseURL: srv.URL}
	_, err := a.Complete(context.Background(), llm.Request{
		Model:    "test",
		Messages: []llm.Message{llm.User("hi")},
		// MaxTokens is nil
	})
	require.NoError(t, err)

	genCfg, _ := sentBody["generationConfig"].(map[string]any)
	_, hasMaxOutput := genCfg["maxOutputTokens"]
	assert.False(t, hasMaxOutput, "maxOutputTokens should not be sent when MaxTokens is nil")
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/llm/providers/google/ -run TestAdapter_Complete_NoMaxTokens -v`
Expected: FAIL -- maxOutputTokens is 2048

**Step 3: Write minimal implementation**

In both `Complete()` (line 82-86) and `Stream()` (line 238-242), remove the `else` branch:

```go
// Replace:
//   if req.MaxTokens != nil && *req.MaxTokens > 0 {
//       genCfg["maxOutputTokens"] = *req.MaxTokens
//   } else {
//       genCfg["maxOutputTokens"] = 2048
//   }
// With:
if req.MaxTokens != nil && *req.MaxTokens > 0 {
	genCfg["maxOutputTokens"] = *req.MaxTokens
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/llm/providers/google/ -run TestAdapter_Complete_NoMaxTokens -v`
Expected: PASS

**Step 5: Run full adapter test suite**

Run: `go test ./internal/llm/providers/google/ -v -count=1`
Expected: All PASS (check that no existing test depends on the 2048 default)

**Step 6: Commit**

```bash
git add internal/llm/providers/google/adapter.go internal/llm/providers/google/adapter_test.go
git commit -m "fix: Gemini adapter omits maxOutputTokens when caller doesn't specify"
```

---

### Task 10: Add Anthropic reasoning_effort mapping

OpenAI maps `reasoning_effort` to `reasoning.effort`. Google maps it to `thinkingConfig.thinkingBudget`. Anthropic silently ignores it. Anthropic's extended thinking is controlled by the `thinking` parameter with `budget_tokens`.

**Files:**
- Modify: `internal/llm/providers/anthropic/adapter.go` (Complete and Stream body building)
- Test: `internal/llm/providers/anthropic/adapter_test.go`

**Step 1: Write the failing test**

```go
func TestAdapter_Complete_ReasoningEffort_MappedToThinking(t *testing.T) {
	var sentBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &sentBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "test",
			"content":     []any{map[string]any{"type": "text", "text": "thought"}},
			"stop_reason": "end_turn",
			"usage":       map[string]any{"input_tokens": 10, "output_tokens": 5},
		})
	}))
	defer srv.Close()

	a := &Adapter{APIKey: "test-key", BaseURL: srv.URL}
	effort := "high"
	_, err := a.Complete(context.Background(), llm.Request{
		Model:           "test",
		Messages:        []llm.Message{llm.User("think hard")},
		ReasoningEffort: &effort,
	})
	require.NoError(t, err)

	thinking, ok := sentBody["thinking"].(map[string]any)
	assert.True(t, ok, "thinking parameter must be present")
	assert.Equal(t, "enabled", thinking["type"])
	budget, _ := thinking["budget_tokens"].(json.Number)
	assert.NotZero(t, budget, "budget_tokens must be set")
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/llm/providers/anthropic/ -run TestAdapter_Complete_ReasoningEffort -v`
Expected: FAIL -- no `thinking` key in sentBody

**Step 3: Write minimal implementation**

Add reasoning_effort handling in both `Complete()` and `Stream()` body building sections. Add a helper function similar to Google's `reasoningEffortToBudget`:

```go
func anthropicReasoningBudget(effort string) int {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "low":
		return 1024
	case "medium":
		return 8192
	case "high":
		return 32768
	default:
		return 0
	}
}
```

In the body building:
```go
if req.ReasoningEffort != nil {
	budget := anthropicReasoningBudget(*req.ReasoningEffort)
	if budget > 0 {
		body["thinking"] = map[string]any{
			"type":          "enabled",
			"budget_tokens": budget,
		}
	}
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/llm/providers/anthropic/ -run TestAdapter_Complete_ReasoningEffort -v`
Expected: PASS

**Step 5: Run full adapter test suite**

Run: `go test ./internal/llm/providers/anthropic/ -v -count=1`
Expected: All PASS

**Step 6: Commit**

```bash
git add internal/llm/providers/anthropic/adapter.go internal/llm/providers/anthropic/adapter_test.go
git commit -m "feat: map reasoning_effort to Anthropic thinking parameter"
```

---

## Batch 4: openai-compat Adapter Parity

### Task 11: Add image content support to openai-compat adapter

The openai-compat adapter's `toChatMessages()` only handles `ContentText` and `ContentToolCall` parts. `ContentImage` parts are silently dropped. Chat Completions API supports multimodal via content arrays.

**Files:**
- Modify: `internal/llm/providers/openaicompat/adapter.go` (toChatMessages)
- Test: `internal/llm/providers/openaicompat/adapter_test.go`

**Step 1: Write the failing test**

```go
func TestAdapter_Complete_ImageContent_IncludedInRequest(t *testing.T) {
	var sentBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &sentBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "c1", "object": "chat.completion",
			"choices": []any{map[string]any{
				"index":         0,
				"finish_reason": "stop",
				"message":       map[string]any{"role": "assistant", "content": "I see an image"},
			}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
	}))
	defer srv.Close()

	a := &Adapter{APIKey: "k", BaseURL: srv.URL}
	_, err := a.Complete(context.Background(), llm.Request{
		Model: "m",
		Messages: []llm.Message{{
			Role: llm.RoleUser,
			Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "What's in this image?"},
				{Kind: llm.ContentImage, Image: &llm.ImageData{URL: "https://example.com/img.png"}},
			},
		}},
	})
	require.NoError(t, err)

	msgs, _ := sentBody["messages"].([]any)
	require.NotEmpty(t, msgs)
	userMsg, _ := msgs[0].(map[string]any)
	content, _ := userMsg["content"].([]any)
	require.Len(t, content, 2, "should have both text and image parts")
	imgPart, _ := content[1].(map[string]any)
	assert.Equal(t, "image_url", imgPart["type"])
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/llm/providers/openaicompat/ -run TestAdapter_Complete_ImageContent -v`
Expected: FAIL -- content is a plain string, not an array

**Step 3: Write minimal implementation**

Modify `toChatMessages()` to detect multimodal content (has non-text parts) and switch from a plain string `"content"` to a content array format. For user and system messages, check if any content part is non-text:

```go
case llm.RoleUser:
	if hasMultimodalContent(m.Content) {
		parts := buildChatContentParts(m.Content)
		out = append(out, map[string]any{"role": "user", "content": parts})
	} else {
		out = append(out, map[string]any{"role": "user", "content": textFromParts(m.Content)})
	}
```

Helper functions:
```go
func hasMultimodalContent(parts []llm.ContentPart) bool {
	for _, p := range parts {
		if p.Kind == llm.ContentImage {
			return true
		}
	}
	return false
}

func buildChatContentParts(parts []llm.ContentPart) []map[string]any {
	var out []map[string]any
	for _, p := range parts {
		switch p.Kind {
		case llm.ContentText:
			out = append(out, map[string]any{"type": "text", "text": p.Text})
		case llm.ContentImage:
			if p.Image != nil {
				imgURL := p.Image.URL
				if imgURL == "" && len(p.Image.Data) > 0 {
					mt := p.Image.MediaType
					if mt == "" { mt = "image/png" }
					imgURL = "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(p.Image.Data)
				}
				entry := map[string]any{"type": "image_url", "image_url": map[string]any{"url": imgURL}}
				if p.Image.Detail != "" {
					entry["image_url"].(map[string]any)["detail"] = p.Image.Detail
				}
				out = append(out, entry)
			}
		}
	}
	return out
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/llm/providers/openaicompat/ -run TestAdapter_Complete_ImageContent -v`
Expected: PASS

**Step 5: Run full adapter test suite**

Run: `go test ./internal/llm/providers/openaicompat/ -v -count=1`
Expected: All PASS

**Step 6: Commit**

```bash
git add internal/llm/providers/openaicompat/adapter.go internal/llm/providers/openaicompat/adapter_test.go
git commit -m "feat: openai-compat adapter supports image content parts"
```

---

### Task 12: Fix openai-compat tool choice validation

The openai-compat adapter silently defaults to `"auto"` for unknown tool choice modes instead of raising `UnsupportedToolChoiceError`. It also doesn't validate that `"named"` mode has a non-empty `Name`.

**Files:**
- Modify: `internal/llm/providers/openaicompat/adapter.go` (toChatToolChoice)
- Test: `internal/llm/providers/openaicompat/adapter_test.go`

**Step 1: Write the failing tests**

```go
func TestAdapter_Complete_UnknownToolChoiceMode_ReturnsError(t *testing.T) {
	a := &Adapter{APIKey: "k", BaseURL: "http://unused"}
	_, err := a.Complete(context.Background(), llm.Request{
		Model:      "m",
		Messages:   []llm.Message{llm.User("hi")},
		Tools:      []llm.ToolDefinition{{Name: "t", Parameters: map[string]any{"type": "object"}}},
		ToolChoice: &llm.ToolChoice{Mode: "bogus"},
	})
	var unsupported *llm.UnsupportedToolChoiceError
	assert.ErrorAs(t, err, &unsupported)
}

func TestAdapter_Complete_NamedToolChoice_EmptyName_ReturnsError(t *testing.T) {
	a := &Adapter{APIKey: "k", BaseURL: "http://unused"}
	_, err := a.Complete(context.Background(), llm.Request{
		Model:      "m",
		Messages:   []llm.Message{llm.User("hi")},
		Tools:      []llm.ToolDefinition{{Name: "t", Parameters: map[string]any{"type": "object"}}},
		ToolChoice: &llm.ToolChoice{Mode: "named", Name: ""},
	})
	assert.Error(t, err)
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/llm/providers/openaicompat/ -run TestAdapter_Complete_UnknownToolChoiceMode -v`
Run: `go test ./internal/llm/providers/openaicompat/ -run TestAdapter_Complete_NamedToolChoice_EmptyName -v`
Expected: Both FAIL -- no error returned

**Step 3: Write minimal implementation**

Rewrite `toChatToolChoice` to match the validation patterns of the other adapters:

```go
func toChatToolChoice(tc llm.ToolChoice) (any, error) {
	switch strings.ToLower(strings.TrimSpace(tc.Mode)) {
	case "", "auto":
		return "auto", nil
	case "none":
		return "none", nil
	case "required":
		return "required", nil
	case "named":
		if tc.Name == "" {
			return nil, &llm.ConfigurationError{Message: "tool_choice mode 'named' requires a non-empty tool name"}
		}
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": tc.Name},
		}, nil
	default:
		return nil, &llm.UnsupportedToolChoiceError{
			Mode:     tc.Mode,
			Provider: "openai-compatible",
		}
	}
}
```

Update callers of `toChatToolChoice` in `buildRequestBody()` to handle the error return.

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/llm/providers/openaicompat/ -run "TestAdapter_Complete_.*ToolChoice" -v`
Expected: All PASS

**Step 5: Run full adapter test suite**

Run: `go test ./internal/llm/providers/openaicompat/ -v -count=1`
Expected: All PASS

**Step 6: Commit**

```bash
git add internal/llm/providers/openaicompat/adapter.go internal/llm/providers/openaicompat/adapter_test.go
git commit -m "fix: openai-compat validates tool choice modes and named tool names"
```

---

### Task 13: Add openai-compat RateLimit extraction and field passthrough

The openai-compat adapter doesn't parse rate limit headers on success responses (unlike the other three adapters). It also doesn't propagate `reasoning_effort` or `metadata` in `buildRequestBody()`.

**Files:**
- Modify: `internal/llm/providers/openaicompat/adapter.go`
- Test: `internal/llm/providers/openaicompat/adapter_test.go`

**Step 1: Write the failing tests**

```go
func TestAdapter_Complete_RateLimitHeaders_Parsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-ratelimit-remaining-requests", "99")
		w.Header().Set("x-ratelimit-limit-requests", "100")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "c1", "object": "chat.completion",
			"choices": []any{map[string]any{
				"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": "hi"},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer srv.Close()

	a := &Adapter{APIKey: "k", BaseURL: srv.URL}
	resp, err := a.Complete(context.Background(), llm.Request{
		Model: "m", Messages: []llm.Message{llm.User("hi")},
	})
	require.NoError(t, err)
	require.NotNil(t, resp.RateLimit)
	assert.Equal(t, 99, *resp.RateLimit.RequestsRemaining)
}

func TestAdapter_Complete_ReasoningEffort_Propagated(t *testing.T) {
	var sentBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &sentBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "c1", "object": "chat.completion",
			"choices": []any{map[string]any{
				"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": "hi"},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer srv.Close()

	a := &Adapter{APIKey: "k", BaseURL: srv.URL}
	effort := "high"
	_, err := a.Complete(context.Background(), llm.Request{
		Model: "m", Messages: []llm.Message{llm.User("hi")},
		ReasoningEffort: &effort,
	})
	require.NoError(t, err)
	assert.Equal(t, "high", sentBody["reasoning_effort"])
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/llm/providers/openaicompat/ -run "TestAdapter_Complete_(RateLimit|ReasoningEffort)" -v`
Expected: Both FAIL

**Step 3: Write minimal implementation**

1. In `Complete()`, after parsing the response, add rate limit extraction:
   ```go
   r.RateLimit = llm.ParseRateLimitHeaders(headers)
   ```
   (The `doHTTP` function already returns `http.Header`.)

2. In `buildRequestBody()`, add:
   ```go
   if req.ReasoningEffort != nil {
       body["reasoning_effort"] = *req.ReasoningEffort
   }
   if len(req.Metadata) > 0 {
       body["metadata"] = req.Metadata
   }
   ```

3. In `Stream()`, also parse rate limit headers from the initial HTTP response.

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/llm/providers/openaicompat/ -run "TestAdapter_Complete_(RateLimit|ReasoningEffort)" -v`
Expected: Both PASS

**Step 5: Run full adapter test suite**

Run: `go test ./internal/llm/providers/openaicompat/ -v -count=1`
Expected: All PASS

**Step 6: Commit**

```bash
git add internal/llm/providers/openaicompat/adapter.go internal/llm/providers/openaicompat/adapter_test.go
git commit -m "fix: openai-compat extracts rate limit headers and propagates reasoning_effort"
```

---

## Batch 5: Timeout Enforcement

### Task 14: Wire ApplyAdapterTimeout into all adapter Stream() methods

No adapter's `Stream()` method calls `ApplyAdapterTimeout`. While `ApplyAdapterTimeout` with `streaming=true` is currently a no-op, the plumbing must be in place for Connect and StreamRead enforcement (Tasks 15-16). All `Complete()` methods already call it.

**Files:**
- Modify: `internal/llm/providers/openai/adapter.go` (Stream method)
- Modify: `internal/llm/providers/anthropic/adapter.go` (Stream method)
- Modify: `internal/llm/providers/google/adapter.go` (Stream method)
- Modify: `internal/llm/providers/openaicompat/adapter.go` (Stream method)
- Test: one per adapter

**Step 1: Write the failing tests**

In each adapter test file, add a test that verifies the context passed to the HTTP request has the adapter timeout applied. Since streaming currently doesn't set a timeout, test that `ApplyAdapterTimeout` is at least called by checking that a per-request timeout with `Request` scope is applied when `streaming=false` is incorrectly passed, or simpler: just verify the code path runs without error when `AdapterTimeout` is set:

```go
func TestAdapter_Stream_AcceptsAdapterTimeout(t *testing.T) {
	// Just verify Stream() doesn't panic or ignore AdapterTimeout.
	srv := newStreamTestServer(t, /* ... appropriate response for adapter ... */)
	defer srv.Close()

	a := &Adapter{APIKey: "k", BaseURL: srv.URL}
	s, err := a.Stream(context.Background(), llm.Request{
		Model:    "m",
		Messages: []llm.Message{llm.User("hi")},
		AdapterTimeout: &llm.AdapterTimeout{
			Request:    30 * time.Second,
			Connect:    5 * time.Second,
			StreamRead: 10 * time.Second,
		},
	})
	require.NoError(t, err)
	defer s.Close()
	for range s.Events() {} // drain
}
```

**Step 2: Run tests to verify they pass (they should, since it's a no-op)**

This is more about establishing the plumbing. The real test will be in Tasks 15-16.

**Step 3: Write implementation**

In each adapter's `Stream()` method, add at the beginning (after context creation):

```go
sctx, timeoutCancel := llm.ApplyAdapterTimeout(sctx, req.AdapterTimeout, true)
defer timeoutCancel()
```

**Step 4: Run full test suites**

Run: `go test ./internal/llm/providers/... -v -count=1`
Expected: All PASS

**Step 5: Commit**

```bash
git add internal/llm/providers/openai/adapter.go internal/llm/providers/anthropic/adapter.go internal/llm/providers/google/adapter.go internal/llm/providers/openaicompat/adapter.go
git commit -m "fix: all adapter Stream() methods wire ApplyAdapterTimeout"
```

---

### Task 15: Implement AdapterTimeout.Connect enforcement

`AdapterTimeout.Connect` is declared with a default of 10s but never enforced. No adapter sets up an `http.Transport` with a connect timeout. Implement this by configuring `DialContext` with a deadline derived from `AdapterTimeout.Connect`.

**Files:**
- Modify: `internal/llm/adapter_timeout.go`
- Modify: all adapter files (lazy `http.Client` initialization)
- Test: `internal/llm/adapter_timeout_test.go`

**Step 1: Write the failing test**

```go
func TestApplyAdapterTimeout_ConnectTimeout_ReturnsContextWithDeadline(t *testing.T) {
	at := &AdapterTimeout{Connect: 5 * time.Second}
	transport := AdapterTransport(at)
	require.NotNil(t, transport)
	// The transport should have a DialContext with timeout.
	// We can't easily test DialContext directly, so test via a real connection to a non-routable IP.
	client := &http.Client{Transport: transport, Timeout: 0}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://192.0.2.1/", nil) // non-routable
	_, err := client.Do(req)
	assert.Error(t, err) // should timeout
}
```

**Step 2: Run test to verify it fails**

Expected: Fails because `AdapterTransport` doesn't exist yet

**Step 3: Write minimal implementation**

Add a new function to `adapter_timeout.go`:

```go
// AdapterTransport returns an http.Transport with connect timeout derived from
// AdapterTimeout.Connect. Returns nil if at is nil or Connect is 0.
func AdapterTransport(at *AdapterTimeout) *http.Transport {
	if at == nil || at.Connect <= 0 {
		return nil
	}
	dialer := &net.Dialer{Timeout: at.Connect}
	return &http.Transport{DialContext: dialer.DialContext}
}
```

Then in each adapter's lazy `http.Client` initialization (the `if a.Client == nil` blocks), use:

```go
if a.Client == nil {
	a.Client = &http.Client{Timeout: 0}
}
```

And in `Complete()`/`Stream()`, when `req.AdapterTimeout` has a non-zero `Connect`:

```go
// If the caller's http.Client is the default (no custom transport),
// apply connect timeout.
if req.AdapterTimeout != nil && req.AdapterTimeout.Connect > 0 {
	if t := AdapterTransport(req.AdapterTimeout); t != nil {
		// Clone the client with the transport.
		clientCopy := *a.Client
		clientCopy.Transport = t
		// Use clientCopy for this request only.
	}
}
```

This is per-request so it doesn't modify the shared adapter client. The exact implementation approach may need refinement - consider adding a helper that returns the appropriate `*http.Client` to use for a given request.

**Step 4: Run tests**

Run: `go test ./internal/llm/ -run TestApplyAdapterTimeout -v`
Expected: PASS

**Step 5: Run full test suites**

Run: `go test ./internal/llm/... -v -count=1 -short`
Expected: All PASS

**Step 6: Commit**

```bash
git add internal/llm/adapter_timeout.go internal/llm/adapter_timeout_test.go internal/llm/providers/*/adapter.go
git commit -m "feat: enforce AdapterTimeout.Connect via DialContext timeout"
```

---

### Task 16: Implement AdapterTimeout.StreamRead enforcement

`AdapterTimeout.StreamRead` (default 30s) should fire if no SSE event is received within the deadline. Currently the SSE parser (`ParseSSE`) blocks indefinitely on `br.ReadString('\n')`.

**Files:**
- Modify: `internal/llm/sse.go` (add per-read deadline option)
- Modify: `internal/llm/adapter_timeout.go` (expose StreamRead wiring)
- Test: `internal/llm/sse_test.go`

**Step 1: Write the failing test**

```go
func TestParseSSE_StreamReadTimeout_FiresOnStall(t *testing.T) {
	// Create a reader that sends one event then stalls.
	pr, pw := io.Pipe()
	go func() {
		pw.Write([]byte("data: hello\n\n"))
		// Stall forever (don't write anything else, don't close).
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var events []SSEEvent
	err := ParseSSE(ctx, pr, func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	}, WithStreamReadTimeout(500*time.Millisecond))

	assert.Error(t, err, "should timeout after 500ms of no data")
	assert.Len(t, events, 1, "should have received the first event")
	pw.Close()
}
```

**Step 2: Run test to verify it fails**

Expected: Hangs or times out at the 5s context level instead of the 500ms StreamRead level

**Step 3: Write minimal implementation**

Add an options pattern to `ParseSSE`:

```go
type sseOption struct {
	streamReadTimeout time.Duration
}
type SSEOption func(*sseOption)

func WithStreamReadTimeout(d time.Duration) SSEOption {
	return func(o *sseOption) { o.streamReadTimeout = d }
}
```

In `ParseSSE`, wrap the reader with a deadline-resetting mechanism. Use a goroutine + timer pattern: read lines in a goroutine, select on the line channel and a timer that resets after each successful read.

Alternatively, use `net.Conn.SetReadDeadline` if available, or a simpler approach with a context that gets cancelled by a timer:

```go
// In the read loop, use a per-line timeout:
readCh := make(chan readResult, 1)
go func() {
	line, err := br.ReadString('\n')
	readCh <- readResult{line, err}
}()

timer := time.NewTimer(opts.streamReadTimeout)
select {
case r := <-readCh:
	timer.Stop()
	// process r
case <-timer.C:
	return fmt.Errorf("stream read timeout after %v", opts.streamReadTimeout)
case <-ctx.Done():
	return ctx.Err()
}
```

Then wire `AdapterTimeout.StreamRead` into the adapter Stream methods by passing it as an SSEOption.

**Step 4: Run tests**

Run: `go test ./internal/llm/ -run TestParseSSE_StreamReadTimeout -v`
Expected: PASS

**Step 5: Run full test suites**

Run: `go test ./internal/llm/... -v -count=1 -short`
Expected: All PASS

**Step 6: Commit**

```bash
git add internal/llm/sse.go internal/llm/sse_test.go internal/llm/adapter_timeout.go internal/llm/providers/*/adapter.go
git commit -m "feat: enforce AdapterTimeout.StreamRead via per-event read timeout"
```

---

## Batch 6: Error Handling Fixes

### Task 17: Implement Gemini gRPC status code mapping

The spec defines gRPC-to-error-type mappings for Gemini (e.g., `RESOURCE_EXHAUSTED` -> `RateLimitError`). The adapter only uses HTTP status codes. Gemini can return `HTTP 400` with gRPC `RESOURCE_EXHAUSTED`, which gets misclassified as `InvalidRequestError`.

**Files:**
- Modify: `internal/llm/providers/google/adapter.go` (error handling in Complete and Stream)
- Test: `internal/llm/providers/google/adapter_test.go`

**Step 1: Write the failing test**

```go
func TestAdapter_Complete_GRPCResourceExhausted_MapsToRateLimitError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"code":    400,
				"message": "Resource has been exhausted",
				"status":  "RESOURCE_EXHAUSTED",
			},
		})
	}))
	defer srv.Close()

	a := &Adapter{APIKey: "k", BaseURL: srv.URL}
	_, err := a.Complete(context.Background(), llm.Request{
		Model: "m", Messages: []llm.Message{llm.User("hi")},
	})
	require.Error(t, err)
	var rle *llm.RateLimitError
	assert.ErrorAs(t, err, &rle, "RESOURCE_EXHAUSTED should map to RateLimitError")
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/llm/providers/google/ -run TestAdapter_Complete_GRPCResourceExhausted -v`
Expected: FAIL -- gets InvalidRequestError (from HTTP 400) instead of RateLimitError

**Step 3: Write minimal implementation**

After calling `ErrorFromHTTPStatus` in both `Complete()` and `Stream()`, check if the raw body contains a gRPC status and reclassify if needed:

```go
func classifyGeminiError(raw map[string]any, httpErr error) error {
	errObj, _ := raw["error"].(map[string]any)
	if errObj == nil {
		return httpErr
	}
	status, _ := errObj["status"].(string)
	switch status {
	case "RESOURCE_EXHAUSTED":
		return llm.ErrorFromHTTPStatus("google", 429, fmt.Sprint(errObj["message"]), raw, nil)
	case "DEADLINE_EXCEEDED":
		return llm.ErrorFromHTTPStatus("google", 408, fmt.Sprint(errObj["message"]), raw, nil)
	case "UNAVAILABLE", "INTERNAL":
		return llm.ErrorFromHTTPStatus("google", 503, fmt.Sprint(errObj["message"]), raw, nil)
	default:
		return httpErr
	}
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/llm/providers/google/ -run TestAdapter_Complete_GRPCResourceExhausted -v`
Expected: PASS

**Step 5: Run full adapter test suite**

Run: `go test ./internal/llm/providers/google/ -v -count=1`
Expected: All PASS

**Step 6: Commit**

```bash
git add internal/llm/providers/google/adapter.go internal/llm/providers/google/adapter_test.go
git commit -m "fix: Gemini adapter maps gRPC status codes to correct error types"
```

---

## Batch 7: Client Infrastructure

### Task 18: Register() calls Initialize() on adapters that implement Initializer

The spec says `Initialize()` is "called by Client on registration." Currently, `Register()` just stores the adapter. The `Initializer` interface exists but is never called automatically.

**Files:**
- Modify: `internal/llm/client.go` (Register method)
- Test: `internal/llm/client_test.go`

**Step 1: Write the failing test**

```go
func TestClient_Register_CallsInitialize(t *testing.T) {
	var initialized bool
	adapter := &initializableAdapter{
		name: "test",
		initFn: func(ctx context.Context) error {
			initialized = true
			return nil
		},
	}
	c := NewClient()
	c.Register(adapter)
	assert.True(t, initialized, "Register should call Initialize on adapters that implement Initializer")
}

type initializableAdapter struct {
	name   string
	initFn func(ctx context.Context) error
}

func (a *initializableAdapter) Name() string { return a.name }
func (a *initializableAdapter) Complete(ctx context.Context, req Request) (Response, error) {
	return Response{}, nil
}
func (a *initializableAdapter) Stream(ctx context.Context, req Request) (Stream, error) {
	return nil, nil
}
func (a *initializableAdapter) Initialize(ctx context.Context) error { return a.initFn(ctx) }
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/llm/ -run TestClient_Register_CallsInitialize -v`
Expected: FAIL -- initialized is false

**Step 3: Write minimal implementation**

In `client.go` `Register()`, after storing the adapter, check for the Initializer interface:

```go
func (c *Client) Register(adapter ProviderAdapter) {
	if c.providers == nil {
		c.providers = map[string]ProviderAdapter{}
	}
	c.providers[adapter.Name()] = adapter
	if c.defaultProvider == "" {
		c.defaultProvider = adapter.Name()
	}
	if init, ok := adapter.(Initializer); ok {
		// Best-effort initialization; errors logged but don't prevent registration.
		_ = init.Initialize(context.Background())
	}
}
```

Note: Consider whether Initialize errors should be returned or logged. The spec says "Called by Client on registration" but doesn't specify error handling. A best-effort approach (ignore errors) is safest for backward compatibility. Alternatively, `Register` could return an error. Discuss with Jesse if uncertain.

**Step 4: Run test to verify it passes**

Run: `go test ./internal/llm/ -run TestClient_Register_CallsInitialize -v`
Expected: PASS

**Step 5: Run full package tests**

Run: `go test ./internal/llm/ -v -count=1 -short`
Expected: All PASS

**Step 6: Commit**

```bash
git add internal/llm/client.go internal/llm/client_test.go
git commit -m "fix: Register() calls Initialize() on adapters implementing Initializer"
```

---

## Batch 8: StreamResult Improvements

### Task 19: Add TotalUsage and Steps tracking to StreamResult

`GenerateResult` has `TotalUsage` and `Steps []StepResult`. `StreamResult` only exposes the final `Response()`. There's no way to get aggregated usage or per-step details from streaming.

**Files:**
- Modify: `internal/llm/stream_generate.go` (StreamResult struct, StreamGenerate function)
- Test: `internal/llm/stream_generate_test.go`

**Step 1: Write the failing tests**

```go
func TestStreamGenerate_TotalUsage_AggregatesAcrossSteps(t *testing.T) {
	// Two-step tool loop: step 1 (tool call) + step 2 (final response).
	adapter := &scriptedStreamAdapter{
		name: "test",
		scripts: []func(ctx context.Context, req Request) (Stream, error){
			// Step 1: return tool call with usage
			makeToolCallStreamWithUsage("call_1", "echo", `{}`, Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}),
			// Step 2: final text with usage
			makeTextStreamWithUsage("done", Usage{InputTokens: 20, OutputTokens: 10, TotalTokens: 30}),
		},
	}
	c := NewClient()
	c.Register(adapter)

	maxRounds := 5
	res, err := StreamGenerate(context.Background(), GenerateOptions{
		Client: c, Model: "m", Prompt: strPtr("go"), MaxToolRounds: &maxRounds,
		Tools: []Tool{{
			Definition: ToolDefinition{Name: "echo", Parameters: map[string]any{"type": "object"}},
			Execute:    func(ctx context.Context, args json.RawMessage) (any, error) { return "ok", nil },
		}},
	})
	require.NoError(t, err)
	for range res.Events() {}

	assert.Equal(t, 30, res.TotalUsage().InputTokens)
	assert.Equal(t, 15, res.TotalUsage().OutputTokens)
	assert.Len(t, res.Steps(), 2)
}
```

**Step 2: Run test to verify it fails**

Expected: Compile error -- no `TotalUsage()` or `Steps()` methods

**Step 3: Write minimal implementation**

Add to `StreamResult`:
```go
type StreamResult struct {
	// ... existing fields ...
	totalUsage Usage
	steps      []StepResult
}

func (r *StreamResult) TotalUsage() Usage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.totalUsage
}

func (r *StreamResult) Steps() []StepResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]StepResult{}, r.steps...)
}
```

In the `StreamGenerate` goroutine, accumulate `totalUsage` and `steps` the same way `Generate` does.

**Step 4: Run test to verify it passes**

Run: `go test ./internal/llm/ -run TestStreamGenerate_TotalUsage -v`
Expected: PASS

**Step 5: Run full package tests**

Run: `go test ./internal/llm/ -v -count=1 -short`
Expected: All PASS

**Step 6: Commit**

```bash
git add internal/llm/stream_generate.go internal/llm/stream_generate_test.go
git commit -m "feat: StreamResult tracks TotalUsage and Steps across streaming tool rounds"
```

---

## Batch 9: Integration Test Gaps

### Task 20: Add missing cross-provider integration tests

The audit identified 6 missing integration test categories in Section 8.9. Add tests for the most important gaps.

**Files:**
- Modify: `internal/llm/integration_smoke_test.go`

**Note:** These are integration tests requiring real API keys. They should be skipped when keys are absent. Follow existing patterns in the file.

**Step 1: Add parallel tool call integration test**

```go
func TestIntegration_ParallelToolCalls(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	for _, p := range providers {
		t.Run(p.provider, func(t *testing.T) {
			key := os.Getenv(p.envKey)
			if key == "" {
				t.Skipf("%s not set", p.envKey)
			}
			client := newTestClient(t, p)
			maxRounds := 3
			callCount := 0
			var mu sync.Mutex
			res, err := Generate(context.WithTimeout(context.Background(), 120*time.Second), GenerateOptions{
				Client: client, Model: p.model, Provider: p.provider,
				Prompt:        strPtr("What is the weather in San Francisco AND New York? Use the weather tool for both cities."),
				MaxToolRounds: &maxRounds,
				Tools: []Tool{{
					Definition: ToolDefinition{
						Name:        "get_weather",
						Description: "Get weather for a city",
						Parameters: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"city": map[string]any{"type": "string"},
							},
							"required": []any{"city"},
						},
					},
					Execute: func(ctx context.Context, args json.RawMessage) (any, error) {
						mu.Lock()
						callCount++
						mu.Unlock()
						return map[string]any{"temperature": 72, "condition": "sunny"}, nil
					},
				}},
			})
			require.NoError(t, err)
			assert.GreaterOrEqual(t, callCount, 2, "should call tool at least twice (one per city)")
			assert.NotEmpty(t, res.Text)
		})
	}
}
```

**Step 2: Add multi-step tool loop integration test**

```go
func TestIntegration_MultiStepToolLoop(t *testing.T) {
	// Test that the model can do 3+ rounds of tool calls.
	// Use a "counter" tool that requires multiple calls to reach a target.
	// ...
}
```

**Step 3: Add reasoning tokens integration test**

```go
func TestIntegration_ReasoningTokens(t *testing.T) {
	// Use reasoning models and verify ReasoningTokens is populated.
	// ...
}
```

**Step 4: Run integration tests**

Run: `export $(cat .env | xargs) && go test ./internal/llm/ -run TestIntegration_ -v -count=1`
Expected: All PASS (or SKIP for missing keys)

**Step 5: Commit**

```bash
git add internal/llm/integration_smoke_test.go
git commit -m "test: add cross-provider integration tests for parallel tools, multi-step loop, reasoning tokens"
```

---

## Task 21: Final verification

**Step 1: Run all unit tests**

Run: `go test ./internal/llm/... -v -count=1 -short`
Expected: All PASS

**Step 2: Run integration tests (if API keys available)**

Run: `export $(cat .env | xargs) && go test ./internal/llm/... -v -count=1`
Expected: All PASS

**Step 3: Run full project test suite**

Run: `go test ./... -short -count=1`
Expected: All PASS

---

## Summary

| Batch | Tasks | Focus |
|-------|-------|-------|
| 1 | 1-4 | Critical data corruption and silent failures |
| 2 | 5-7 | Streaming completeness |
| 3 | 8-10 | Provider adapter fixes |
| 4 | 11-13 | openai-compat adapter parity |
| 5 | 14-16 | Timeout enforcement |
| 6 | 17 | Error handling |
| 7 | 18 | Client infrastructure |
| 8 | 19 | StreamResult improvements |
| 9 | 20-21 | Integration tests and verification |

Total: 21 tasks, ~115 TDD steps
