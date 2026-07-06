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
		APIKey:  "test-key",
		BaseURL: srv.URL,
		Client:  srv.Client(),
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
	if !strings.Contains(fmt.Sprint(result.Content), "arguments were not valid JSON") {
		t.Fatalf("tool result content = %q, want invalid-JSON repair diagnostic", fmt.Sprint(result.Content))
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
