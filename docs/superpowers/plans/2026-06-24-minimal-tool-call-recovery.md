# Minimal Tool-Call Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prove the original failed-tool-call recovery path end to end: malformed model tool-call arguments are retained in local session history, converted into an error tool result, and serialized safely when replayed to OpenAI Responses.

**Architecture:** Keep the existing recovery model: tool-call parsing fails at Serf's tool execution boundary and produces a normal error tool result. Keep provider-safe argument rewriting in OpenAI-family replay serialization only, so raw local history remains useful for forensics while provider validation cannot reject full-history replay before the model sees the error result.

**Tech Stack:** Go tests, `httptest`, real Serf `agent.Session`, real `llm/providers/openai.Adapter`, fake OpenAI Responses SSE server.

---

## Scope

This plan covers the minimal recovery slice only.

In scope:

- A deterministic session-level regression using the real OpenAI Responses adapter against a local fake HTTP server.
- Verification that malformed assistant tool-call arguments remain raw in local history.
- Verification that malformed arguments become an error `ToolResult`.
- Verification that the next OpenAI Responses request serializes the historical malformed `function_call.arguments` as `{}` and includes the linked error `function_call_output`.

Out of scope:

- `previous_response_id` continuation.
- Continuation-owned provider storage.
- Attempt-group metadata.
- Provider-handle redaction.
- Any live provider request.

Spec coverage for this plan:

- Covers section 13, `Provider-Safe Replay Sanitizer`, for the current full-history replay path.
- Covers the malformed-tool-call recovery sequence described in section 13: raw malformed model arguments, local parse failure, error tool result, provider-safe full-history replay.
- Re-asserts raw logging coverage only by running the already-landed bad-key and incomplete-stream tests; this plan does not add continuation attempt logging from section 12.
- Does not cover the section 16 continuation acceptance criteria. Those require phase-by-phase continuation plans after Jesse approves that broader V1-public cut.

## Current Substrate Facts

- `docs/testing.md` requires default tests to be deterministic and forbids provider credentials alone from issuing live requests.
- `llm/providers/internal/openaichat.ToolArgumentsString` already returns `{}` for malformed, empty, non-object, or null JSON.
- `llm/providers/openai.toResponsesInput` already uses `ToolArgumentsString` for historical Responses `function_call.arguments`.
- `agent/internal/tool.Registry.ExecuteCall` returns an error result with `invalid tool arguments JSON` when a tool call has malformed JSON arguments.
- `agent.Session.appendAssistantTurn` persists the raw `llm.Response.Message` to session history before tool execution.
- Existing raw HTTP error coverage already includes a bad-key matrix across provider types in `llm/provider_error_raw_logging_test.go`.

## File Structure

- Create: `agent/session_openai_malformed_tool_call_test.go`
  - Owns the new session-through-OpenAI-Responses regression and small local test helpers for parsing fake server request bodies.
- No production file is expected to change for this slice.
  - If the new test fails, the likely fix points are `llm/providers/internal/openaichat/openaichat.go`, `llm/providers/openai/responses.go`, or `agent/internal/tool/registry.go`.

---

### Task 1: Add Session-Level OpenAI Responses Recovery Regression

**Files:**
- Create: `agent/session_openai_malformed_tool_call_test.go`

- [ ] **Step 1: Write the failing test**

Create `agent/session_openai_malformed_tool_call_test.go` with this complete content:

```go
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/openai"
)

func TestSession_OpenAIResponsesMalformedToolCallRecoveryUsesSafeReplay(t *testing.T) {
	dir := t.TempDir()
	const malformedArgs = `{"value": broken`

	var mu sync.Mutex
	var requestBodies [][]byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		mu.Lock()
		requestBodies = append(requestBodies, append([]byte(nil), body...))
		requestIndex := len(requestBodies)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		switch requestIndex {
		case 1:
			writeResponsesFunctionCall(t, w, flusher, "resp_bad", "call_bad", "my_strict_tool", malformedArgs)
		case 2:
			args := mustJSON(t, map[string]any{
				"message":  "recovered",
				"end_turn": true,
				"output": map[string]any{
					"message":   "",
					"data":      map[string]any{},
					"artifacts": []string{},
				},
			})
			writeResponsesFunctionCall(t, w, flusher, "resp_done", "call_done", "communicate", args)
		default:
			t.Errorf("unexpected request %d body: %s", requestIndex, string(body))
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)

	client := llm.NewClient()
	client.Register(&openai.Adapter{
		APIKey: "test-key",
		BaseURL: srv.URL,
		Client: srv.Client(),
	})

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: dir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	var toolRuns int
	sess.RegisterTool("my_strict_tool", "requires valid JSON arguments", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value": map[string]any{"type": "string"},
		},
		"required": []string{"value"},
	}, func(context.Context, any) (any, error) {
		toolRuns++
		return "should not run", nil
	})

	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := sess.ProcessInput(ctx, "trigger malformed tool call", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !strings.Contains(got, "recovered") {
		t.Fatalf("ProcessInput output = %q, want recovered", got)
	}

	sess.Close()
	<-eventsDone

	if toolRuns != 0 {
		t.Fatalf("malformed tool call executed %d time(s), want 0", toolRuns)
	}

	rawCall, ok := findToolCallInHistory(sess.history, "call_bad")
	if !ok {
		t.Fatalf("missing raw assistant tool call in session history: %s", turnKinds(sess.history))
	}
	if string(rawCall.Arguments) != malformedArgs {
		t.Fatalf("stored tool-call arguments = %q, want raw malformed %q", string(rawCall.Arguments), malformedArgs)
	}

	result, ok := findToolResultInHistory(sess.history, "call_bad")
	if !ok {
		t.Fatalf("missing error tool result for call_bad: %s", turnKinds(sess.history))
	}
	if !result.IsError {
		t.Fatalf("tool result IsError = false, want true: %+v", result)
	}
	if !strings.Contains(fmt.Sprint(result.Content), "invalid tool arguments JSON") {
		t.Fatalf("tool result content = %q, want invalid JSON diagnostic", fmt.Sprint(result.Content))
	}

	mu.Lock()
	bodies := append([][]byte(nil), requestBodies...)
	mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("OpenAI Responses request count = %d, want 2", len(bodies))
	}

	second := decodeResponsesRequest(t, bodies[1])
	if _, ok := second["previous_response_id"]; ok {
		t.Fatalf("minimal recovery slice must use full-history replay, got previous_response_id in %s", string(bodies[1]))
	}

	input := responsesInputItems(t, second)
	replayedCall := findResponsesItem(t, input, "function_call", "call_id", "call_bad")
	if replayedCall == nil {
		t.Fatalf("second request missing replayed function_call for call_bad: %#v", input)
	}
	if gotArgs, _ := replayedCall["arguments"].(string); gotArgs != "{}" {
		t.Fatalf("replayed malformed function_call arguments = %q, want {}", gotArgs)
	}

	errorOutput := findResponsesItem(t, input, "function_call_output", "call_id", "call_bad")
	if errorOutput == nil {
		t.Fatalf("second request missing function_call_output for call_bad: %#v", input)
	}
	if _, exists := errorOutput["is_error"]; exists {
		t.Fatalf("function_call_output carried rejected top-level is_error field: %#v", errorOutput)
	}
	output, ok := errorOutput["output"].(string)
	if !ok {
		t.Fatalf("function_call_output.output = %#v, want string", errorOutput["output"])
	}
	if !strings.Contains(output, `"is_error":true`) || !strings.Contains(output, "invalid tool arguments JSON") {
		t.Fatalf("function_call_output.output = %q, want wrapped error content", output)
	}
}

func writeResponsesFunctionCall(t *testing.T, w io.Writer, flusher http.Flusher, responseID, callID, name, args string) {
	t.Helper()

	item := map[string]any{
		"id":        "item_" + callID,
		"type":      "function_call",
		"status":    "completed",
		"call_id":   callID,
		"name":      name,
		"arguments": args,
	}
	writeSSE(t, w, flusher, "response.output_item.done", map[string]any{
		"type": "response.output_item.done",
		"item": item,
	})
	writeSSE(t, w, flusher, "response.completed", map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id":     responseID,
			"model":  "gpt-5.2",
			"status": "completed",
			"output": []any{item},
			"usage": map[string]any{
				"input_tokens":  1,
				"output_tokens": 1,
				"total_tokens":  2,
			},
		},
	})
}

func writeSSE(t *testing.T, w io.Writer, flusher http.Flusher, event string, payload any) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal SSE payload: %v", err)
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body); err != nil {
		t.Fatalf("write SSE payload: %v", err)
	}
	if flusher != nil {
		flusher.Flush()
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return string(body)
}

func decodeResponsesRequest(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode Responses request: %v\n%s", err, string(body))
	}
	return req
}

func responsesInputItems(t *testing.T, req map[string]any) []any {
	t.Helper()
	input, ok := req["input"].([]any)
	if !ok {
		t.Fatalf("Responses request input = %#v, want []any", req["input"])
	}
	return input
}

func findResponsesItem(t *testing.T, items []any, itemType, key, value string) map[string]any {
	t.Helper()
	for _, itemAny := range items {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		if item["type"] == itemType && item[key] == value {
			return item
		}
	}
	return nil
}

func findToolCallInHistory(history []schema.Turn, callID string) (*llm.ToolCallData, bool) {
	for i := range history {
		for j := range history[i].Message.Content {
			part := history[i].Message.Content[j]
			if part.Kind == llm.ContentToolCall && part.ToolCall != nil && part.ToolCall.ID == callID {
				return part.ToolCall, true
			}
		}
	}
	return nil, false
}
```

- [ ] **Step 2: Run the focused test to verify current behavior**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run TestSession_OpenAIResponsesMalformedToolCallRecoveryUsesSafeReplay -count=1 -v
```

Expected before implementation review:

- If it passes, the recovery behavior is already implemented and this task is a test-only proof.
- If it fails on missing error tool results, inspect `agent/internal/tool/registry.go:441-470` and keep malformed JSON returning an error result without invoking the executor.
- If it fails on lost raw arguments, inspect `agent/session.go:649-666` and keep `appendAssistantTurn` storing the model message before tool execution mutates anything.
- If it fails on replayed arguments not being `{}`, inspect `llm/providers/openai/responses.go:850-875` and keep Responses full-history replay calling `openaichat.ToolArgumentsString`.

- [ ] **Step 3: Apply the minimal implementation only if the test fails**

If replay serialization is the failing part, the expected implementation shape is:

```go
items = append(items, map[string]any{
	"type":      "function_call",
	"call_id":   p.ToolCall.ID,
	"name":      p.ToolCall.Name,
	"arguments": openaichat.ToolArgumentsString(p.ToolCall.Arguments),
})
```

If malformed JSON execution is the failing part, the expected implementation shape is:

```go
if len(call.Arguments) > 0 {
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		msg := fmt.Sprintf("invalid tool arguments JSON: %v", err)
		return truncateResult(name, callID, msg, true, t.Limit)
	}
}
```

If raw local persistence is the failing part, do not sanitize `resp.Message` before calling `appendAssistantTurn`. Sanitization belongs in provider request serialization.

- [ ] **Step 4: Run the focused recovery tests**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesMalformedToolCallRecoveryUsesSafeReplay|TestSession_ProcessInputRepairsOrphanedAssistantToolCallsBeforeModelRequest|TestResumeHistoryRepairsOrphanedAssistantToolCallsBeforeLaterUserInput' -count=1 -v
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestToResponsesInput_SanitizesMalformedHistoricalToolCallArguments|TestBuildChatCompletionsBody_SanitizesMalformedHistoricalToolCallArguments' -count=1 -v
GOCACHE=/tmp/serf-gocache go test ./llm/providers/internal/openaichat -run TestToolArgumentsString -count=1 -v
```

Expected: all selected tests pass.

- [ ] **Step 5: Run the raw-logging regression tests that already cover bad-key and incomplete-stream cases**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm -run 'TestProviderHTTPErrorRawLogging|TestAPILoggerWrapStreamWritesRawLogOnFinish' -count=1 -v
GOCACHE=/tmp/serf-gocache go test ./llm/providers/internal/transport -run 'TestRun_IncompleteErrorCarriesRawBodiesWhenEnabled|TestRun_TeeOnlyWhenRawBodyEnabled' -count=1 -v
```

Expected: all selected tests pass.

- [ ] **Step 6: Format and commit**

Run:

```sh
gofmt -w agent/session_openai_malformed_tool_call_test.go
git status --short
git add agent/session_openai_malformed_tool_call_test.go
git commit -m "test(agent): prove malformed tool-call recovery over OpenAI Responses" -m "Add a deterministic session-level regression that drives the real OpenAI Responses adapter through a local httptest SSE server. The test preserves the raw malformed assistant tool-call arguments in local session history, verifies Serf records an error tool result without executing the tool, and asserts the next Responses wire request replays that historical function call with provider-safe {} arguments plus a linked function_call_output error. This locks down the minimal recovery behavior before any Responses continuation work begins."
```

Expected: commit succeeds and `git status --short` does not show uncommitted changes from this task.

---

## Verification

Run after all tasks in this plan:

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesMalformedToolCallRecoveryUsesSafeReplay|TestSession_ProcessInputRepairsOrphanedAssistantToolCallsBeforeModelRequest|TestResumeHistoryRepairsOrphanedAssistantToolCallsBeforeLaterUserInput' -count=1 -v
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestToResponsesInput_SanitizesMalformedHistoricalToolCallArguments|TestBuildChatCompletionsBody_SanitizesMalformedHistoricalToolCallArguments' -count=1 -v
GOCACHE=/tmp/serf-gocache go test ./llm/providers/internal/openaichat -run TestToolArgumentsString -count=1 -v
GOCACHE=/tmp/serf-gocache go test ./llm -run 'TestProviderHTTPErrorRawLogging|TestAPILoggerWrapStreamWritesRawLogOnFinish' -count=1 -v
GOCACHE=/tmp/serf-gocache go test ./llm/providers/internal/transport -run 'TestRun_IncompleteErrorCarriesRawBodiesWhenEnabled|TestRun_TeeOnlyWhenRawBodyEnabled' -count=1 -v
git diff --check HEAD~1..HEAD
```

Expected: all tests pass and `git diff --check` reports no whitespace errors.

## Next Plans

After this plan lands, the continuation spec should proceed as separate plans in phase order. The next plan should be Phase 0A-audits only: endpoint-family support registry defaults, per-session model-call serialization audit, and `llm.APILogger` final-attempt-count shape decision. It must not enable runtime continuation.
