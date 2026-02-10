package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/internal/llm"
)

func TestAdapter_Complete_MapsToChatCompletionsAPI(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "chatcmpl-1",
  "model": "gpt-4o",
  "choices": [{
    "index": 0,
    "message": {"role": "assistant", "content": "Hello"},
    "finish_reason": "stop"
  }],
  "usage": {"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := a.Complete(ctx, llm.Request{
		Model: "gpt-4o",
		Messages: []llm.Message{
			llm.System("you are helpful"),
			llm.User("hi"),
			llm.Assistant("hey"),
			llm.User("what?"),
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if strings.TrimSpace(resp.Text()) != "Hello" {
		t.Fatalf("resp text: %q", resp.Text())
	}
	if resp.Provider != "openai-compatible" {
		t.Fatalf("provider: %q", resp.Provider)
	}
	if resp.Usage.InputTokens != 5 {
		t.Fatalf("input tokens: %d", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 3 {
		t.Fatalf("output tokens: %d", resp.Usage.OutputTokens)
	}

	// Verify request mapping: messages should be in Chat Completions format.
	if gotBody == nil {
		t.Fatalf("no request body captured")
	}
	if gotBody["model"] != "gpt-4o" {
		t.Fatalf("model: %v", gotBody["model"])
	}
	msgs, ok := gotBody["messages"].([]any)
	if !ok {
		t.Fatalf("messages not array: %T", gotBody["messages"])
	}
	if len(msgs) != 4 {
		t.Fatalf("messages count: %d", len(msgs))
	}
	// First message should be system.
	first, _ := msgs[0].(map[string]any)
	if first["role"] != "system" {
		t.Fatalf("first message role: %v", first["role"])
	}
}

func TestAdapter_Complete_ToolCalling(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "chatcmpl-2",
  "model": "gpt-4o",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": null,
      "tool_calls": [{
        "id": "call_1",
        "type": "function",
        "function": {"name": "get_weather", "arguments": "{\"city\":\"SF\"}"}
      }]
    },
    "finish_reason": "tool_calls"
  }],
  "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := a.Complete(ctx, llm.Request{
		Model:    "gpt-4o",
		Messages: []llm.Message{llm.User("weather in SF?")},
		Tools: []llm.ToolDefinition{{
			Name:        "get_weather",
			Description: "get weather",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}},
		}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	calls := resp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("tool calls: %d", len(calls))
	}
	if calls[0].Name != "get_weather" {
		t.Fatalf("tool name: %q", calls[0].Name)
	}
	if calls[0].ID != "call_1" {
		t.Fatalf("tool call ID: %q", calls[0].ID)
	}
	if resp.Finish.Reason != "tool_calls" {
		t.Fatalf("finish reason: %q", resp.Finish.Reason)
	}

	// Verify tools were sent in request.
	tools, ok := gotBody["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools: %#v", gotBody["tools"])
	}
}

func TestAdapter_Complete_ToolResults(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "chatcmpl-3",
  "model": "gpt-4o",
  "choices": [{
    "index": 0,
    "message": {"role": "assistant", "content": "Sunny in SF"},
    "finish_reason": "stop"
  }],
  "usage": {"prompt_tokens": 15, "completion_tokens": 5, "total_tokens": 20}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := a.Complete(ctx, llm.Request{
		Model: "gpt-4o",
		Messages: []llm.Message{
			llm.User("weather in SF?"),
			{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_1", Name: "get_weather", Arguments: json.RawMessage(`{"city":"SF"}`), Type: "function"}},
			}},
			llm.ToolResultNamed("call_1", "get_weather", "Sunny, 72F", false),
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if strings.TrimSpace(resp.Text()) != "Sunny in SF" {
		t.Fatalf("resp text: %q", resp.Text())
	}

	// Verify the tool result message is in the request.
	msgs, _ := gotBody["messages"].([]any)
	foundToolMsg := false
	for _, m := range msgs {
		msg, _ := m.(map[string]any)
		if msg["role"] == "tool" {
			foundToolMsg = true
			if msg["tool_call_id"] != "call_1" {
				t.Fatalf("tool_call_id: %v", msg["tool_call_id"])
			}
		}
	}
	if !foundToolMsg {
		t.Fatalf("no tool message in request")
	}
}

func TestAdapter_Complete_HTTPError_MapsToErrorType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{
		Model:    "gpt-4o",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	var rle *llm.RateLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("expected RateLimitError, got %T (%v)", err, err)
	}
}

func TestAdapter_Stream_YieldsTextDeltasAndFinish(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		chunks := []string{
			`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`,
			`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Hel"},"finish_reason":null}]}`,
			`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}]}`,
			`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`,
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
		Model:    "gpt-4o",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer st.Close()

	var deltas []string
	var kinds []llm.StreamEventType
	for ev := range st.Events() {
		kinds = append(kinds, ev.Type)
		if ev.Type == llm.StreamEventTextDelta {
			deltas = append(deltas, ev.Delta)
		}
	}
	joined := strings.Join(deltas, "")
	if joined != "Hello" {
		t.Fatalf("deltas: %q", joined)
	}

	foundStart := false
	foundFinish := false
	for _, k := range kinds {
		if k == llm.StreamEventStreamStart {
			foundStart = true
		}
		if k == llm.StreamEventFinish {
			foundFinish = true
		}
	}
	if !foundStart {
		t.Fatalf("expected STREAM_START (kinds=%v)", kinds)
	}
	if !foundFinish {
		t.Fatalf("expected FINISH (kinds=%v)", kinds)
	}
}

func TestAdapter_Stream_ToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		chunks := []string{
			`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}`,
			`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"ci"}}]},"finish_reason":null}]}`,
			`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ty\":\"SF\"}"}}]},"finish_reason":null}]}`,
			`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
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
		Model:    "gpt-4o",
		Messages: []llm.Message{llm.User("weather in SF?")},
		Tools: []llm.ToolDefinition{{
			Name:        "get_weather",
			Description: "get weather",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}},
		}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer st.Close()

	var kinds []llm.StreamEventType
	var toolStartSeen, toolEndSeen bool
	for ev := range st.Events() {
		kinds = append(kinds, ev.Type)
		if ev.Type == llm.StreamEventToolCallStart {
			toolStartSeen = true
			if ev.ToolCall == nil || ev.ToolCall.Name != "get_weather" {
				t.Fatalf("tool call start: %+v", ev.ToolCall)
			}
		}
		if ev.Type == llm.StreamEventToolCallEnd {
			toolEndSeen = true
			if ev.ToolCall == nil || string(ev.ToolCall.Arguments) != `{"city":"SF"}` {
				t.Fatalf("tool call end args: %s", ev.ToolCall.Arguments)
			}
		}
	}
	if !toolStartSeen {
		t.Fatalf("expected TOOL_CALL_START (kinds=%v)", kinds)
	}
	if !toolEndSeen {
		t.Fatalf("expected TOOL_CALL_END (kinds=%v)", kinds)
	}
}
