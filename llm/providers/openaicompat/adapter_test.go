package openaicompat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

func TestAdapter_Complete_MapsToChatCompletionsAPI(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
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
	// All four messages should have the correct roles.
	roleChecks := []struct {
		idx  int
		want string
	}{{0, "system"}, {1, "user"}, {2, "assistant"}, {3, "user"}}
	for _, rc := range roleChecks {
		msg, _ := msgs[rc.idx].(map[string]any)
		if msg["role"] != rc.want {
			t.Fatalf("msgs[%d] role: got %v, want %s", rc.idx, msg["role"], rc.want)
		}
	}
}

func TestNewFromEnv_CompleteAutoPrefersResponsesAPI(t *testing.T) {
	var responsesCalled bool
	var chatCalled bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/responses":
			responsesCalled = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
  "id": "resp_compat",
  "model": "gpt-4o",
  "output": [
    {"type": "message", "content": [{"type":"output_text", "text":"responses ok"}]}
  ],
  "usage": {"input_tokens": 1, "output_tokens": 2, "total_tokens": 3}
}`))
		case "/chat/completions":
			chatCalled = true
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	t.Setenv("OPENAI_COMPATIBLE_BASE_URL", srv.URL)
	t.Setenv("OPENAI_COMPATIBLE_API_KEY", "envkey")
	t.Setenv("OPENAI_COMPATIBLE_PROVIDER_QUIRKS", "")
	a, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	a.Client = srv.Client()

	resp, err := a.Complete(context.Background(), llm.Request{
		Model:    "gpt-4o",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got := strings.TrimSpace(resp.Text()); got != "responses ok" {
		t.Fatalf("response text = %q, want responses ok", got)
	}
	if resp.Provider != "openai-compatible" {
		t.Fatalf("provider = %q, want openai-compatible", resp.Provider)
	}
	if !responsesCalled {
		t.Fatal("Responses API was not called")
	}
	if chatCalled {
		t.Fatal("Chat Completions should not be called when Responses succeeds")
	}
}

func TestNewFromEnv_CompleteAutoFallsBackToChatCompletions(t *testing.T) {
	var responsesCalled bool
	var chatCalled bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/responses":
			responsesCalled = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"model not found","code":"model_not_found","type":"invalid_request_error"}}`))
		case "/chat/completions":
			chatCalled = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
  "id": "chatcmpl-compat",
  "model": "gpt-4o",
  "choices": [{
    "index": 0,
    "message": {"role": "assistant", "content": "chat fallback ok"},
    "finish_reason": "stop"
  }],
  "usage": {"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8}
}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	t.Setenv("OPENAI_COMPATIBLE_BASE_URL", srv.URL)
	t.Setenv("OPENAI_COMPATIBLE_API_KEY", "envkey")
	t.Setenv("OPENAI_COMPATIBLE_PROVIDER_QUIRKS", "")
	a, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	a.Client = srv.Client()

	resp, err := a.Complete(context.Background(), llm.Request{
		Model:    "gpt-4o",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got := strings.TrimSpace(resp.Text()); got != "chat fallback ok" {
		t.Fatalf("response text = %q, want chat fallback ok", got)
	}
	if !responsesCalled {
		t.Fatal("Responses API was not called before fallback")
	}
	if !chatCalled {
		t.Fatal("Chat Completions fallback was not called")
	}
}

func TestNewFromEnv_CompleteAutoFallbackPreservesOpenAICompatibleQuirks(t *testing.T) {
	var chatBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/responses":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"model not found","code":"model_not_found","type":"invalid_request_error"}}`))
		case "/chat/completions":
			if err := json.NewDecoder(r.Body).Decode(&chatBody); err != nil {
				t.Fatalf("decode chat body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
  "id": "chatcmpl-compat",
  "model": "gpt-4o",
  "choices": [{
    "index": 0,
    "message": {"role": "assistant", "content": "chat fallback ok"},
    "finish_reason": "stop"
  }]
}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	t.Setenv("OPENAI_COMPATIBLE_BASE_URL", srv.URL)
	t.Setenv("OPENAI_COMPATIBLE_API_KEY", "envkey")
	t.Setenv("OPENAI_COMPATIBLE_PROVIDER_QUIRKS", "openrouter")
	a, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	a.Client = srv.Client()

	effort := "max"
	_, err = a.Complete(context.Background(), llm.Request{
		Model:           "gpt-4o",
		Messages:        []llm.Message{llm.User("hi")},
		ReasoningEffort: &effort,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	obj, _ := chatBody["reasoning"].(map[string]any)
	if obj == nil || obj["effort"] != "xhigh" {
		t.Fatalf("chat fallback reasoning = %#v, want {effort: xhigh} (openrouter preset)", chatBody["reasoning"])
	}
}

func TestNewFromEnv_StreamAutoPrefersResponsesAPI(t *testing.T) {
	var responsesCalled bool
	var chatCalled bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/responses":
			responsesCalled = true
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintln(w, `event: response.output_text.delta`)
			_, _ = fmt.Fprintln(w, `data: {"type":"response.output_text.delta","delta":"responses stream"}`)
			_, _ = fmt.Fprintln(w)
			_, _ = fmt.Fprintln(w, `event: response.completed`)
			_, _ = fmt.Fprintln(w, `data: {"type":"response.completed","response":{"id":"resp_stream","model":"gpt-4o","output":[{"type":"message","content":[{"type":"output_text","text":"responses stream"}]}],"status":"completed","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)
			_, _ = fmt.Fprintln(w)
		case "/chat/completions":
			chatCalled = true
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	t.Setenv("OPENAI_COMPATIBLE_BASE_URL", srv.URL)
	t.Setenv("OPENAI_COMPATIBLE_API_KEY", "envkey")
	t.Setenv("OPENAI_COMPATIBLE_PROVIDER_QUIRKS", "")
	a, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	a.Client = srv.Client()

	st, err := a.Stream(context.Background(), llm.Request{
		Model:    "gpt-4o",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer st.Close() //nolint:errcheck

	var text strings.Builder
	var finish *llm.Response
	for ev := range st.Events() {
		if ev.Type == llm.StreamEventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
		if ev.Type == llm.StreamEventTextDelta {
			text.WriteString(ev.Delta)
		}
		if ev.Type == llm.StreamEventFinish {
			finish = ev.Response
		}
	}
	if text.String() != "responses stream" {
		t.Fatalf("stream text = %q, want responses stream", text.String())
	}
	if finish == nil || finish.Provider != "openai-compatible" {
		t.Fatalf("finish response = %+v, want openai-compatible provider", finish)
	}
	if !responsesCalled {
		t.Fatal("Responses API was not called")
	}
	if chatCalled {
		t.Fatal("Chat Completions should not be called when Responses succeeds")
	}
}

func TestNewFromEnv_StreamAutoFallsBackToChatCompletions(t *testing.T) {
	var responsesCalled bool
	var chatCalled bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/responses":
			responsesCalled = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"model not found","code":"model_not_found","type":"invalid_request_error"}}`))
		case "/chat/completions":
			chatCalled = true
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintln(w, `data: {"id":"chatcmpl-stream","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"chat stream"},"finish_reason":null}]}`)
			_, _ = fmt.Fprintln(w)
			_, _ = fmt.Fprintln(w, `data: {"id":"chatcmpl-stream","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`)
			_, _ = fmt.Fprintln(w)
			_, _ = fmt.Fprintln(w, `data: [DONE]`)
			_, _ = fmt.Fprintln(w)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	t.Setenv("OPENAI_COMPATIBLE_BASE_URL", srv.URL)
	t.Setenv("OPENAI_COMPATIBLE_API_KEY", "envkey")
	t.Setenv("OPENAI_COMPATIBLE_PROVIDER_QUIRKS", "")
	a, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	a.Client = srv.Client()

	st, err := a.Stream(context.Background(), llm.Request{
		Model:    "gpt-4o",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer st.Close() //nolint:errcheck

	var text strings.Builder
	for ev := range st.Events() {
		if ev.Type == llm.StreamEventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
		if ev.Type == llm.StreamEventTextDelta {
			text.WriteString(ev.Delta)
		}
	}
	if text.String() != "chat stream" {
		t.Fatalf("stream text = %q, want chat stream", text.String())
	}
	if !responsesCalled {
		t.Fatal("Responses API was not called before fallback")
	}
	if !chatCalled {
		t.Fatal("Chat Completions fallback was not called")
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

	// Verify tools were sent in request with correct structure.
	tools, ok := gotBody["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools: %#v", gotBody["tools"])
	}
	tool0, _ := tools[0].(map[string]any)
	if tool0["type"] != "function" {
		t.Fatalf("tool type: %v, want function", tool0["type"])
	}
	fn, _ := tool0["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Fatalf("function name: %v, want get_weather", fn["name"])
	}
	if fn["parameters"] == nil {
		t.Fatalf("function.parameters should be non-empty")
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
			if msg["content"] != "Sunny, 72F" {
				t.Fatalf("tool message content: %v, want Sunny, 72F", msg["content"])
			}
		}
	}
	if !foundToolMsg {
		t.Fatalf("no tool message in request")
	}
}

func TestBuildRequestBody_SanitizesMalformedHistoricalToolCallArguments(t *testing.T) {
	body, err := buildRequestBody(llm.Request{
		Model: "m",
		Messages: []llm.Message{{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{{
				Kind: llm.ContentToolCall,
				ToolCall: &llm.ToolCallData{
					ID:        "call_bad",
					Name:      "task_list",
					Arguments: json.RawMessage(`{"status": in_progress"}`),
					Type:      "function",
				},
			}},
		}},
	}, false, ModelCompat{})
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}

	msgs := body["messages"].([]map[string]any)
	calls := msgs[0]["tool_calls"].([]map[string]any)
	fn := calls[0]["function"].(map[string]any)
	args := fn["arguments"].(string)
	if args != "{}" {
		t.Fatalf("tool call arguments = %q, want {}", args)
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
	if llm.Kind(err) != llm.KindRateLimit {
		t.Fatalf("expected RateLimitError, got %T (%v)", err, err)
	}
}

func TestAdapter_Stream_YieldsTextDeltasAndFinish(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
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
			fmt.Fprintf(w, "data: %s\n\n", c) //nolint:errcheck
			flusher.Flush()
		}
		fmt.Fprintf(w, "data: [DONE]\n\n") //nolint:errcheck
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
	defer st.Close() //nolint:errcheck

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

	indexOf := func(target llm.StreamEventType) int {
		for i, k := range kinds {
			if k == target {
				return i
			}
		}
		return -1
	}
	startIdx := indexOf(llm.StreamEventStreamStart)
	finishIdx := indexOf(llm.StreamEventFinish)
	if startIdx < 0 {
		t.Fatalf("expected STREAM_START (kinds=%v)", kinds)
	}
	if finishIdx < 0 {
		t.Fatalf("expected FINISH (kinds=%v)", kinds)
	}
	if startIdx != 0 {
		t.Fatalf("STREAM_START should be first event, got index %d (kinds=%v)", startIdx, kinds)
	}
	if finishIdx != len(kinds)-1 {
		t.Fatalf("FINISH should be last event, got index %d of %d (kinds=%v)", finishIdx, len(kinds)-1, kinds)
	}
}

// TestAdapter_Stream_MidStreamReadFailure_SurfacesCause drives the adapter
// against a server that streams one delta then drops the connection mid-chunk,
// so llm.ParseSSE returns a read error. The adapter must surface a
// StreamEventError carrying that read error as its cause (errors.Unwrap), not
// drop it and leave the consumer to synthesize a generic "stream ended" (E3).
func TestAdapter_Stream_MidStreamReadFailure_SurfacesCause(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("ResponseWriter is not a Hijacker")
			return
		}
		conn, bufrw, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		// One chunked SSE delta, then drop the connection without the terminating
		// zero-length chunk so the client's body read fails mid-stream.
		_, _ = bufrw.WriteString("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n")
		sse := "data: " + `{"id":"x","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}` + "\n\n"
		_, _ = fmt.Fprintf(bufrw, "%x\r\n%s\r\n", len(sse), sse)
		_ = bufrw.Flush()
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	st, err := a.Stream(ctx, llm.Request{Model: "gpt-4o", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer st.Close() //nolint:errcheck

	var streamErr error
	for ev := range st.Events() {
		if ev.Type == llm.StreamEventError {
			streamErr = ev.Err
		}
	}
	if streamErr == nil {
		t.Fatal("expected a StreamEventError after the mid-stream read failure")
	}
	if errors.Unwrap(streamErr) == nil {
		t.Fatalf("surfaced stream error dropped the underlying read cause (E3 regression): %v", streamErr)
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
			fmt.Fprintf(w, "data: %s\n\n", c) //nolint:errcheck
			flusher.Flush()
		}
		fmt.Fprintf(w, "data: [DONE]\n\n") //nolint:errcheck
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
	defer st.Close() //nolint:errcheck

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

func TestHTTPErrorMapping_IncludesRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "rate limited"}}) //nolint:errcheck
	}))
	defer srv.Close()

	a := &Adapter{BaseURL: srv.URL, Client: srv.Client()}
	_, err := a.Complete(context.Background(), llm.Request{Model: "test", Messages: []llm.Message{llm.User("hi")}})
	if err == nil {
		t.Fatal("expected error")
	}
	if llm.Kind(err) != llm.KindRateLimit {
		t.Fatalf("expected RateLimitError, got %T: %v", err, err)
	}
	var le llm.Error
	errors.As(err, &le)
	if le.RetryAfter() == nil {
		t.Fatal("RetryAfter is nil, want 30s")
	}
	if *le.RetryAfter() != 30*time.Second {
		t.Errorf("RetryAfter = %v, want 30s", *le.RetryAfter())
	}
}

func TestComplete_PopulatesTotalTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
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

func TestComplete_ExtractsCachedTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"id": "chatcmpl-1", "model": "test",
			"choices": []any{map[string]any{
				"index":         0,
				"finish_reason": "stop",
				"message":       map[string]any{"role": "assistant", "content": "hi"},
			}},
			"usage": map[string]any{
				"prompt_tokens":         1000,
				"completion_tokens":     50,
				"total_tokens":          1050,
				"prompt_tokens_details": map[string]any{"cached_tokens": 800},
			},
		})
	}))
	defer srv.Close()

	a := &Adapter{BaseURL: srv.URL, Client: srv.Client()}
	resp, err := a.Complete(context.Background(), llm.Request{Model: "test", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.CacheReadTokens == nil {
		t.Fatalf("CacheReadTokens is nil, want 800")
	}
	if *resp.Usage.CacheReadTokens != 800 {
		t.Errorf("CacheReadTokens = %d, want 800", *resp.Usage.CacheReadTokens)
	}
}

func TestComplete_NormalizesInputTokens_SubtractsCached(t *testing.T) {
	// OpenAI-compat endpoints report prompt_tokens as total-including-cached.
	// The adapter must subtract cached_tokens so llm.Usage.InputTokens means
	// new uncached input only.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"id": "chatcmpl-1", "model": "test",
			"choices": []any{map[string]any{
				"index":         0,
				"finish_reason": "stop",
				"message":       map[string]any{"role": "assistant", "content": "hi"},
			}},
			"usage": map[string]any{
				"prompt_tokens":         10000,
				"completion_tokens":     500,
				"total_tokens":          10500,
				"prompt_tokens_details": map[string]any{"cached_tokens": 7000},
			},
		})
	}))
	defer srv.Close()

	a := &Adapter{BaseURL: srv.URL, Client: srv.Client()}
	resp, err := a.Complete(context.Background(), llm.Request{Model: "test", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.InputTokens != 3000 {
		t.Errorf("InputTokens: got %d, want 3000 (10000 - 7000 cached)", resp.Usage.InputTokens)
	}
	if resp.Usage.CacheReadTokens == nil || *resp.Usage.CacheReadTokens != 7000 {
		t.Errorf("CacheReadTokens: got %v, want 7000", resp.Usage.CacheReadTokens)
	}
}

func TestComplete_NoCachedTokensField_LeavesNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"id": "chatcmpl-1", "model": "test",
			"choices": []any{map[string]any{
				"index":         0,
				"finish_reason": "stop",
				"message":       map[string]any{"role": "assistant", "content": "hi"},
			}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
	}))
	defer srv.Close()

	a := &Adapter{BaseURL: srv.URL, Client: srv.Client()}
	resp, err := a.Complete(context.Background(), llm.Request{Model: "test", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.CacheReadTokens != nil {
		t.Errorf("CacheReadTokens = %v, want nil for response without prompt_tokens_details", resp.Usage.CacheReadTokens)
	}
}

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

func TestAdapterTimeout_Request_EnforcedOnComplete(t *testing.T) {
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-done
	}))
	t.Cleanup(func() {
		close(done)
		srv.Close()
	})

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
	if llm.Kind(err) == llm.KindTimeout {
		return // correct error type
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return
	}
	t.Errorf("expected RequestTimeoutError or DeadlineExceeded, got %T: %v", err, err)
}

func TestAdapterTimeout_Stream_AcceptsAdapterTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", `{"id":"chatcmpl-1","model":"test","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}`)
		if f != nil {
			f.Flush()
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", `{"id":"chatcmpl-1","model":"test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		if f != nil {
			f.Flush()
		}
		_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
		if f != nil {
			f.Flush()
		}
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "test", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{
		Model:    "test",
		Messages: []llm.Message{llm.User("hi")},
		AdapterTimeout: &llm.AdapterTimeout{
			Request:    30 * time.Second,
			StreamRead: 5 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close() //nolint:errcheck

	var gotFinish bool
	for ev := range stream.Events() {
		if ev.Type == llm.StreamEventFinish {
			gotFinish = true
		}
	}
	if !gotFinish {
		t.Fatal("expected FINISH event")
	}
}

func TestDefaultHeaders_SentOnCompleteRequests(t *testing.T) {
	var capturedHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{
		APIKey:  "test-key",
		BaseURL: srv.URL,
		Client:  srv.Client(),
		DefaultHeaders: map[string]string{
			"X-Custom-Header": "custom-value",
			"X-Another":       "another-value",
		},
	}
	_, err := a.Complete(context.Background(), llm.Request{
		Model:    "test",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if capturedHeaders.Get("X-Custom-Header") != "custom-value" {
		t.Errorf("X-Custom-Header = %q, want %q", capturedHeaders.Get("X-Custom-Header"), "custom-value")
	}
	if capturedHeaders.Get("X-Another") != "another-value" {
		t.Errorf("X-Another = %q, want %q", capturedHeaders.Get("X-Another"), "another-value")
	}
	// Provider-specific headers must still be present.
	if capturedHeaders.Get("Authorization") != "Bearer test-key" {
		t.Errorf("Authorization = %q, want %q", capturedHeaders.Get("Authorization"), "Bearer test-key")
	}
}

func TestDefaultHeaders_SentOnStreamRequests(t *testing.T) {
	var capturedHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header
		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: "+`{"id":"chatcmpl-1","model":"test","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if f != nil {
			f.Flush()
		}
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{
		APIKey:  "test-key",
		BaseURL: srv.URL,
		Client:  srv.Client(),
		DefaultHeaders: map[string]string{
			"X-Custom-Header": "custom-value",
		},
	}
	stream, err := a.Stream(context.Background(), llm.Request{
		Model:    "test",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close() //nolint:errcheck
	for range stream.Events() {
	}
	if capturedHeaders.Get("X-Custom-Header") != "custom-value" {
		t.Errorf("X-Custom-Header = %q, want %q", capturedHeaders.Get("X-Custom-Header"), "custom-value")
	}
	if capturedHeaders.Get("Authorization") != "Bearer test-key" {
		t.Errorf("Authorization = %q, want %q", capturedHeaders.Get("Authorization"), "Bearer test-key")
	}
}

func TestAdapter_Complete_ImageContent_IncludedInRequest(t *testing.T) {
	var sentBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &sentBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"id": "c1", "object": "chat.completion",
			"choices": []any{map[string]any{
				"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": "I see an image"},
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
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	msgs, _ := sentBody["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatal("no messages in request body")
	}
	userMsg, _ := msgs[0].(map[string]any)
	content, ok := userMsg["content"].([]any)
	if !ok {
		t.Fatalf("content should be an array for multimodal messages, got %T: %v", userMsg["content"], userMsg["content"])
	}
	if len(content) != 2 {
		t.Fatalf("expected 2 content parts (text + image), got %d", len(content))
	}
	textPart, _ := content[0].(map[string]any)
	if textPart["type"] != "text" {
		t.Fatalf("first part type: %v, want text", textPart["type"])
	}
	if textPart["text"] != "What's in this image?" {
		t.Fatalf("first part text: %v", textPart["text"])
	}
	imgPart, _ := content[1].(map[string]any)
	if imgPart["type"] != "image_url" {
		t.Fatalf("second part type: %v, want image_url", imgPart["type"])
	}
	imgURL, _ := imgPart["image_url"].(map[string]any)
	if imgURL["url"] != "https://example.com/img.png" {
		t.Fatalf("image url: %v", imgURL["url"])
	}
}

func TestAdapter_Complete_ImageContent_Base64(t *testing.T) {
	var sentBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &sentBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"id": "c1", "object": "chat.completion",
			"choices": []any{map[string]any{
				"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": "ok"},
			}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 1, "total_tokens": 11},
		})
	}))
	defer srv.Close()

	a := &Adapter{APIKey: "k", BaseURL: srv.URL}
	_, err := a.Complete(context.Background(), llm.Request{
		Model: "m",
		Messages: []llm.Message{{
			Role: llm.RoleUser,
			Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "describe"},
				{Kind: llm.ContentImage, Image: &llm.ImageData{Data: []byte{0x89, 0x50, 0x4E, 0x47}, MediaType: "image/png"}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	msgs, _ := sentBody["messages"].([]any)
	userMsg, _ := msgs[0].(map[string]any)
	content, ok := userMsg["content"].([]any)
	if !ok {
		t.Fatalf("content should be array, got %T", userMsg["content"])
	}
	imgPart, _ := content[1].(map[string]any)
	imgURL, _ := imgPart["image_url"].(map[string]any)
	url, _ := imgURL["url"].(string)
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Fatalf("expected data URI, got %q", url)
	}
	decoded, decErr := base64.StdEncoding.DecodeString(strings.TrimPrefix(url, "data:image/png;base64,"))
	if decErr != nil {
		t.Fatalf("base64 decode: %v", decErr)
	}
	if !bytes.Equal(decoded, []byte{0x89, 0x50, 0x4E, 0x47}) {
		t.Fatalf("image bytes: %v, want [137 80 78 71]", decoded)
	}
}

func TestAdapter_Complete_ImageContent_Detail(t *testing.T) {
	var sentBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &sentBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"id": "c1", "object": "chat.completion",
			"choices": []any{map[string]any{
				"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": "ok"},
			}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 1, "total_tokens": 11},
		})
	}))
	defer srv.Close()

	a := &Adapter{APIKey: "k", BaseURL: srv.URL}
	_, err := a.Complete(context.Background(), llm.Request{
		Model: "m",
		Messages: []llm.Message{{
			Role: llm.RoleUser,
			Content: []llm.ContentPart{
				{Kind: llm.ContentImage, Image: &llm.ImageData{URL: "https://example.com/img.png", Detail: "high"}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	msgs, _ := sentBody["messages"].([]any)
	userMsg, _ := msgs[0].(map[string]any)
	content, _ := userMsg["content"].([]any)
	imgPart, _ := content[0].(map[string]any)
	imgURL, _ := imgPart["image_url"].(map[string]any)
	if imgURL["detail"] != "high" {
		t.Fatalf("detail: %v, want high", imgURL["detail"])
	}
}

func TestAdapter_Complete_TextOnly_StaysString(t *testing.T) {
	// Text-only user messages should remain a plain string, not an array.
	var sentBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &sentBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"id": "c1", "object": "chat.completion",
			"choices": []any{map[string]any{
				"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": "hi"},
			}},
			"usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 1, "total_tokens": 6},
		})
	}))
	defer srv.Close()

	a := &Adapter{APIKey: "k", BaseURL: srv.URL}
	_, err := a.Complete(context.Background(), llm.Request{
		Model:    "m",
		Messages: []llm.Message{llm.User("hello")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	msgs, _ := sentBody["messages"].([]any)
	userMsg, _ := msgs[0].(map[string]any)
	// Text-only should be a string, not an array.
	if _, ok := userMsg["content"].(string); !ok {
		t.Fatalf("text-only content should be string, got %T", userMsg["content"])
	}
}

func TestAdapter_Complete_UnknownToolChoiceMode_ReturnsError(t *testing.T) {
	a := &Adapter{APIKey: "k", BaseURL: "http://unused"}
	_, err := a.Complete(context.Background(), llm.Request{
		Model:      "m",
		Messages:   []llm.Message{llm.User("hi")},
		Tools:      []llm.ToolDefinition{{Name: "t", Parameters: map[string]any{"type": "object"}}},
		ToolChoice: &llm.ToolChoice{Mode: "bogus"},
	})
	var unsupported *llm.UnsupportedToolChoiceError
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected UnsupportedToolChoiceError, got %T: %v", err, err)
	}
}

func TestAdapter_Complete_NamedToolChoice_EmptyName_ReturnsError(t *testing.T) {
	a := &Adapter{APIKey: "k", BaseURL: "http://unused"}
	_, err := a.Complete(context.Background(), llm.Request{
		Model:      "m",
		Messages:   []llm.Message{llm.User("hi")},
		Tools:      []llm.ToolDefinition{{Name: "t", Parameters: map[string]any{"type": "object"}}},
		ToolChoice: &llm.ToolChoice{Mode: "named", Name: ""},
	})
	if err == nil {
		t.Fatal("expected error for named mode with empty name")
	}
	var configErr *llm.ConfigurationError
	if !errors.As(err, &configErr) {
		t.Fatalf("expected ConfigurationError, got %T: %v", err, err)
	}
}

func TestDefaultHeaders_CannotOverrideProviderHeaders(t *testing.T) {
	var capturedHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{
		APIKey:  "real-key",
		BaseURL: srv.URL,
		Client:  srv.Client(),
		DefaultHeaders: map[string]string{
			"Authorization": "Bearer evil-key",
		},
	}
	_, err := a.Complete(context.Background(), llm.Request{
		Model:    "test",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Provider-specific Authorization must take precedence over DefaultHeaders.
	if capturedHeaders.Get("Authorization") != "Bearer real-key" {
		t.Errorf("Authorization = %q, want %q (provider auth must take precedence)", capturedHeaders.Get("Authorization"), "Bearer real-key")
	}
}

func TestAdapter_Complete_RateLimitHeaders_Parsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-ratelimit-remaining-requests", "99")
		w.Header().Set("x-ratelimit-limit-requests", "100")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
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
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.RateLimit == nil {
		t.Fatal("RateLimit is nil, want parsed rate limit info")
	}
	if resp.RateLimit.RequestsRemaining == nil || *resp.RateLimit.RequestsRemaining != 99 {
		t.Errorf("RequestsRemaining = %v, want 99", resp.RateLimit.RequestsRemaining)
	}
	if resp.RateLimit.RequestsLimit == nil || *resp.RateLimit.RequestsLimit != 100 {
		t.Errorf("RequestsLimit = %v, want 100", resp.RateLimit.RequestsLimit)
	}
}

func TestAdapter_Complete_ReasoningEffort_Propagated(t *testing.T) {
	var sentBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &sentBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
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
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if sentBody["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort = %v, want %q", sentBody["reasoning_effort"], "high")
	}
}

func TestAdapter_ListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"object": "list",
			"data": [
				{"id": "llama3.1:latest", "object": "model"},
				{"id": "codellama:latest", "object": "model"}
			]
		}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{BaseURL: srv.URL, Client: srv.Client()}
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2", len(models))
	}
	for _, m := range models {
		if m.Provider != "openai-compatible" {
			t.Errorf("model %s: provider = %q, want openai-compatible", m.ID, m.Provider)
		}
	}
}

func TestAdapter_Complete_Metadata_Propagated(t *testing.T) {
	var sentBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &sentBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
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
	_, err := a.Complete(context.Background(), llm.Request{
		Model: "m", Messages: []llm.Message{llm.User("hi")},
		Metadata: map[string]string{"user_id": "u123"},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	md, ok := sentBody["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata not map[string]any: %T %v", sentBody["metadata"], sentBody["metadata"])
	}
	if md["user_id"] != "u123" {
		t.Errorf("metadata[user_id] = %v, want %q", md["user_id"], "u123")
	}
}

// --- Reasoning content tests ---

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

	if len(resp.Message.Content) < 2 {
		t.Fatalf("expected at least 2 content parts, got %d", len(resp.Message.Content))
	}
	if resp.Message.Content[0].Kind != llm.ContentThinking {
		t.Fatalf("first part kind: %v, want thinking", resp.Message.Content[0].Kind)
	}
	if resp.Message.Content[0].Thinking == nil || resp.Message.Content[0].Thinking.Text != "Let me think step by step...\nFirst, I need to consider..." {
		t.Fatalf("thinking text mismatch: %+v", resp.Message.Content[0].Thinking)
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

func TestComplete_NoReasoningContent_OnlyText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "chatcmpl-nr1",
  "model": "gpt-4o",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "Hello"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 5, "completion_tokens": 1, "total_tokens": 6}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	resp, err := a.Complete(context.Background(), llm.Request{
		Model:    "gpt-4o",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(resp.Message.Content) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(resp.Message.Content))
	}
	if resp.Message.Content[0].Kind != llm.ContentText {
		t.Fatalf("part kind: %v, want text", resp.Message.Content[0].Kind)
	}
	if resp.ReasoningText() != "" {
		t.Fatalf("ReasoningText() should be empty, got %q", resp.ReasoningText())
	}
}

func TestComplete_ReasoningTokens_NativeFromCompletionDetails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "chatcmpl-rt1",
  "model": "kimi-k2.5",
  "choices": [{
    "index": 0,
    "message": {"role": "assistant", "reasoning_content": "thinking...", "content": "done"},
    "finish_reason": "stop"
  }],
  "usage": {
    "prompt_tokens": 10, "completion_tokens": 50, "total_tokens": 60,
    "completion_tokens_details": {"reasoning_tokens": 35}
  }
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

func TestComplete_ReasoningTokens_NilWhenProviderOmits(t *testing.T) {
	// completion_tokens from most OpenAI-compatible providers already
	// includes billed thinking tokens. Fabricating an estimate from
	// reasoning_content character count double-counts against output.
	// Leave ReasoningTokens nil unless the provider reports it natively.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 80 chars of reasoning_content, no completion_tokens_details.
		_, _ = w.Write([]byte(`{
  "id": "chatcmpl-rt2",
  "model": "kimi-k2.5",
  "choices": [{
    "index": 0,
    "message": {"role": "assistant", "reasoning_content": "01234567890123456789012345678901234567890123456789012345678901234567890123456789", "content": "done"},
    "finish_reason": "stop"
  }],
  "usage": {"prompt_tokens": 10, "completion_tokens": 50, "total_tokens": 60}
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
	if resp.Usage.ReasoningTokens != nil {
		t.Fatalf("ReasoningTokens should be nil (provider didn't report), got %d", *resp.Usage.ReasoningTokens)
	}
}

func TestComplete_RoundTrip_ThinkingSerializedAsReasoningContent(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "chatcmpl-rr1", "model": "m",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	_, err := a.Complete(context.Background(), llm.Request{
		Model: "m",
		Messages: []llm.Message{
			llm.User("question"),
			{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "my reasoning"}},
				{Kind: llm.ContentText, Text: "my answer"},
			}},
			llm.User("followup"),
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	msgs := gotBody["messages"].([]any)
	assistantMsg := msgs[1].(map[string]any)
	if assistantMsg["reasoning_content"] != "my reasoning" {
		t.Fatalf("reasoning_content: %v", assistantMsg["reasoning_content"])
	}
	if assistantMsg["content"] != "my answer" {
		t.Fatalf("content: %v", assistantMsg["content"])
	}
}

func TestComplete_RoundTrip_NoThinking_NoReasoningContent(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "chatcmpl-rr2", "model": "m",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	_, err := a.Complete(context.Background(), llm.Request{
		Model: "m",
		Messages: []llm.Message{
			llm.User("question"),
			llm.Assistant("plain answer"),
			llm.User("followup"),
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	msgs := gotBody["messages"].([]any)
	assistantMsg := msgs[1].(map[string]any)
	if _, has := assistantMsg["reasoning_content"]; has {
		t.Fatalf("reasoning_content should not be present, got: %v", assistantMsg["reasoning_content"])
	}
}

func TestStream_ReasoningContent_EmitsReasoningEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		chunks := []string{
			`{"id":"c1","model":"kimi-k2.5","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"Let me "},"finish_reason":null}]}`,
			`{"id":"c1","model":"kimi-k2.5","choices":[{"index":0,"delta":{"reasoning_content":"think..."},"finish_reason":null}]}`,
			`{"id":"c1","model":"kimi-k2.5","choices":[{"index":0,"delta":{"content":"The answer"},"finish_reason":null}]}`,
			`{"id":"c1","model":"kimi-k2.5","choices":[{"index":0,"delta":{"content":" is 42."},"finish_reason":null}]}`,
			`{"id":"c1","model":"kimi-k2.5","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":20,"total_tokens":25}}`,
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
		Messages: []llm.Message{llm.User("think")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer st.Close()

	var kinds []llm.StreamEventType
	var reasoningDeltas, textDeltas []string
	var finalResp *llm.Response
	for ev := range st.Events() {
		kinds = append(kinds, ev.Type)
		if ev.Type == llm.StreamEventReasoningDelta {
			reasoningDeltas = append(reasoningDeltas, ev.ReasoningDelta)
		}
		if ev.Type == llm.StreamEventTextDelta {
			textDeltas = append(textDeltas, ev.Delta)
		}
		if ev.Type == llm.StreamEventFinish && ev.Response != nil {
			finalResp = ev.Response
		}
	}

	if got := strings.Join(reasoningDeltas, ""); got != "Let me think..." {
		t.Fatalf("reasoning deltas: %q", got)
	}
	if got := strings.Join(textDeltas, ""); got != "The answer is 42." {
		t.Fatalf("text deltas: %q", got)
	}

	// Verify event ordering: REASONING_START before REASONING_DELTA, REASONING_END before TEXT_START.
	indexOf := func(target llm.StreamEventType) int {
		for i, k := range kinds {
			if k == target {
				return i
			}
		}
		return -1
	}
	if rs, rd := indexOf(llm.StreamEventReasoningStart), indexOf(llm.StreamEventReasoningDelta); rs < 0 || rd < 0 || rs >= rd {
		t.Fatalf("REASONING_START should precede REASONING_DELTA: %v", kinds)
	}
	if re, ts := indexOf(llm.StreamEventReasoningEnd), indexOf(llm.StreamEventTextStart); re < 0 || ts < 0 || re >= ts {
		t.Fatalf("REASONING_END should precede TEXT_START: %v", kinds)
	}

	// Final response should have both thinking and text.
	if finalResp == nil {
		t.Fatal("no final response")
	}
	if finalResp.ReasoningText() != "Let me think..." {
		t.Fatalf("final ReasoningText(): %q", finalResp.ReasoningText())
	}
	if finalResp.Text() != "The answer is 42." {
		t.Fatalf("final Text(): %q", finalResp.Text())
	}
}

func TestStream_ReasoningThenToolCalls_ReasoningEndBeforeToolStart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		chunks := []string{
			`{"id":"c2","model":"kimi-k2.5","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"I should call a tool"},"finish_reason":null}]}`,
			`{"id":"c2","model":"kimi-k2.5","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":""}}]},"finish_reason":null}]}`,
			`{"id":"c2","model":"kimi-k2.5","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"x\"}"}}]},"finish_reason":null}]}`,
			`{"id":"c2","model":"kimi-k2.5","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
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
		Messages: []llm.Message{llm.User("do something")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer st.Close()

	var kinds []llm.StreamEventType
	for ev := range st.Events() {
		kinds = append(kinds, ev.Type)
	}

	indexOf := func(target llm.StreamEventType) int {
		for i, k := range kinds {
			if k == target {
				return i
			}
		}
		return -1
	}

	// REASONING_END must appear before TOOL_CALL_START.
	re := indexOf(llm.StreamEventReasoningEnd)
	tc := indexOf(llm.StreamEventToolCallStart)
	if re < 0 || tc < 0 || re >= tc {
		t.Fatalf("REASONING_END (%d) should precede TOOL_CALL_START (%d): %v", re, tc, kinds)
	}
	// No TEXT_START should appear.
	if indexOf(llm.StreamEventTextStart) >= 0 {
		t.Fatalf("should not have TEXT_START when reasoning transitions to tool calls: %v", kinds)
	}
}

// --- ProviderQuirks tests ---

func TestQuirks_LockedParams_StrippedFromRequest(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "q1", "model": "m",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{
		APIKey:  "k",
		BaseURL: srv.URL,
		Client:  srv.Client(),
		Quirks: ProviderQuirks{
			LockTemperature:      true,
			LockTopP:             true,
			LockFrequencyPenalty: true,
			LockPresencePenalty:  true,
		},
	}
	temp := 0.7
	topP := 0.9
	_, err := a.Complete(context.Background(), llm.Request{
		Model:       "m",
		Messages:    []llm.Message{llm.User("hi")},
		Temperature: &temp,
		TopP:        &topP,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if _, has := gotBody["temperature"]; has {
		t.Fatalf("temperature should be stripped, got: %v", gotBody["temperature"])
	}
	if _, has := gotBody["top_p"]; has {
		t.Fatalf("top_p should be stripped, got: %v", gotBody["top_p"])
	}
}

func TestQuirks_ToolChoiceAutoOnly_ClampsRequired(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "q2", "model": "m",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{
		APIKey: "k", BaseURL: srv.URL, Client: srv.Client(),
		Quirks: ProviderQuirks{ToolChoiceAutoOnly: true},
	}
	_, err := a.Complete(context.Background(), llm.Request{
		Model:      "m",
		Messages:   []llm.Message{llm.User("hi")},
		Tools:      []llm.ToolDefinition{{Name: "foo"}},
		ToolChoice: &llm.ToolChoice{Mode: "required"},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if gotBody["tool_choice"] != "auto" {
		t.Fatalf("tool_choice should be clamped to auto, got: %v", gotBody["tool_choice"])
	}
}

func TestQuirks_ToolChoiceAutoOnly_PreservesAuto(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "q3", "model": "m",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{
		APIKey: "k", BaseURL: srv.URL, Client: srv.Client(),
		Quirks: ProviderQuirks{ToolChoiceAutoOnly: true},
	}
	_, err := a.Complete(context.Background(), llm.Request{
		Model:      "m",
		Messages:   []llm.Message{llm.User("hi")},
		Tools:      []llm.ToolDefinition{{Name: "foo"}},
		ToolChoice: &llm.ToolChoice{Mode: "auto"},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if gotBody["tool_choice"] != "auto" {
		t.Fatalf("tool_choice should remain auto, got: %v", gotBody["tool_choice"])
	}
}

func TestQuirks_MaxStopSequences_Truncates(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "q4", "model": "m",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{
		APIKey: "k", BaseURL: srv.URL, Client: srv.Client(),
		Quirks: ProviderQuirks{MaxStopSequences: 1},
	}
	_, err := a.Complete(context.Background(), llm.Request{
		Model:         "m",
		Messages:      []llm.Message{llm.User("hi")},
		StopSequences: []string{"stop1", "stop2", "stop3"},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	stops, ok := gotBody["stop"].([]any)
	if !ok {
		t.Fatalf("stop not array: %T", gotBody["stop"])
	}
	if len(stops) != 1 {
		t.Fatalf("stop should be truncated to 1, got %d: %v", len(stops), stops)
	}
}

func TestQuirks_StripEmptyContent_RemovesEmptyTextParts(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "q5", "model": "m",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{
		APIKey: "k", BaseURL: srv.URL, Client: srv.Client(),
		Quirks: ProviderQuirks{StripEmptyContent: true},
	}
	_, err := a.Complete(context.Background(), llm.Request{
		Model: "m",
		Messages: []llm.Message{
			{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: ""},
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID: "call_1", Name: "foo", Arguments: json.RawMessage(`{}`), Type: "function",
				}},
			}},
			{Role: llm.RoleTool, Content: []llm.ContentPart{
				{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
					ToolCallID: "call_1", Content: "result",
				}},
			}},
			llm.User("continue"),
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	msgs := gotBody["messages"].([]any)
	assistantMsg := msgs[0].(map[string]any)
	// With StripEmptyContent, the empty text part should be stripped entirely.
	// The assistant message should have tool_calls but the content key must be absent.
	if content, has := assistantMsg["content"]; has {
		t.Fatalf("content key should be absent after stripping, got: %v", content)
	}
}

func TestQuirks_NoJSONSchema_DowngradesToJsonObject(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "q6", "model": "m",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "{}"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{
		APIKey: "k", BaseURL: srv.URL, Client: srv.Client(),
		Quirks: ProviderQuirks{NoJSONSchema: true},
	}
	_, err := a.Complete(context.Background(), llm.Request{
		Model:    "m",
		Messages: []llm.Message{llm.User("hi")},
		ResponseFormat: &llm.ResponseFormat{
			Type:       "json_schema",
			JSONSchema: map[string]any{"type": "object"},
			Strict:     true,
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	rf, ok := gotBody["response_format"].(map[string]any)
	if !ok {
		t.Fatalf("response_format not map: %T", gotBody["response_format"])
	}
	if rf["type"] != "json_object" {
		t.Fatalf("response_format type should be json_object, got: %v", rf["type"])
	}
}

func TestQuirks_FinishReasonMap_MapsNonStandard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "q7", "model": "glm-5",
  "choices": [{
    "index": 0,
    "message": {"role": "assistant", "content": "filtered"},
    "finish_reason": "sensitive"
  }],
  "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
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
		Messages: []llm.Message{llm.User("test")},
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

func TestQuirksPreset_KimiK25(t *testing.T) {
	q := QuirksPreset("kimi-k2.5")
	if !q.LockTemperature || !q.LockTopP || !q.LockFrequencyPenalty || !q.LockPresencePenalty {
		t.Fatal("kimi-k2.5 should lock all four params")
	}
	if !q.ToolChoiceAutoOnly {
		t.Fatal("kimi-k2.5 should restrict tool_choice")
	}
	if !q.NoJSONSchema {
		t.Fatal("kimi-k2.5 should downgrade json_schema")
	}
}

func TestQuirksPreset_GLM5(t *testing.T) {
	q := QuirksPreset("glm-5")
	if !q.StripEmptyContent {
		t.Fatal("glm-5 should strip empty content")
	}
	if !q.ToolChoiceAutoOnly {
		t.Fatal("glm-5 should restrict tool_choice")
	}
	if q.MaxStopSequences != 1 {
		t.Fatalf("glm-5 MaxStopSequences: %d, want 1", q.MaxStopSequences)
	}
	if !q.NoJSONSchema {
		t.Fatal("glm-5 should downgrade json_schema")
	}
	if q.FinishReasonMap["sensitive"] != "content_filter" {
		t.Fatal("glm-5 should map sensitive to content_filter")
	}
	if q.ThinkingFormat != "zai" {
		t.Fatalf("glm-5 ThinkingFormat: %q, want zai", q.ThinkingFormat)
	}
}

func TestQuirksPreset_Unknown_ReturnsZeroValue(t *testing.T) {
	q := QuirksPreset("unknown")
	if q.LockTemperature || q.LockTopP || q.ToolChoiceAutoOnly || q.StripEmptyContent {
		t.Fatal("unknown preset should return zero-value quirks")
	}
}

func TestQuirksPreset_CaseInsensitive(t *testing.T) {
	names := []string{"Kimi-K2.5", "KIMI-K2.5", "kimi", "moonshot", "GLM-5", "glm", "zhipu"}
	for _, name := range names {
		q := QuirksPreset(name)
		if !q.ToolChoiceAutoOnly {
			t.Fatalf("QuirksPreset(%q) should have ToolChoiceAutoOnly", name)
		}
	}
}

func TestQuirks_FinishReasonMap_Streaming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		chunks := []string{
			`{"id":"c3","model":"glm-5","choices":[{"index":0,"delta":{"role":"assistant","content":"filtered"},"finish_reason":null}]}`,
			`{"id":"c3","model":"glm-5","choices":[{"index":0,"delta":{},"finish_reason":"sensitive"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
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
		Quirks: ProviderQuirks{
			FinishReasonMap: map[string]string{"sensitive": "content_filter"},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	st, err := a.Stream(ctx, llm.Request{
		Model:    "glm-5",
		Messages: []llm.Message{llm.User("test")},
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
		t.Fatal("no final response")
	}
	if finalResp.Finish.Reason != "content_filter" {
		t.Fatalf("finish reason: %q, want content_filter", finalResp.Finish.Reason)
	}
	if finalResp.Finish.Raw != "sensitive" {
		t.Fatalf("finish raw: %q, want sensitive", finalResp.Finish.Raw)
	}
}

func TestStream_ReasoningTokens_NilWhenProviderOmits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		chunks := []string{
			`{"id":"c4","model":"m","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"12345678901234567890"},"finish_reason":null}]}`,
			`{"id":"c4","model":"m","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":null}]}`,
			`{"id":"c4","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":5,"total_tokens":6}}`,
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
		Model:    "m",
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
		t.Fatal("no final response")
	}
	if finalResp.Usage.ReasoningTokens != nil {
		t.Fatalf("ReasoningTokens should be nil in stream (provider didn't report native count), got %d", *finalResp.Usage.ReasoningTokens)
	}
}

// --- reasoning_details tests (OpenRouter MiniMax format) ---

func TestComplete_ReasoningDetails_ParsedAsThinking(t *testing.T) {
	// OpenRouter returns reasoning as a reasoning_details array on the message,
	// not as reasoning_content. Verify it is parsed into thinking content parts.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "chatcmpl-mm1",
  "model": "minimax/minimax-m2.7",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "There are 3 r's in strawberry.",
      "reasoning_details": [
        {"type": "thinking", "thinking": "Let me count: s-t-r-a-w-b-e-r-r-y. r appears at positions 3, 8, 9."}
      ]
    },
    "finish_reason": "stop"
  }],
  "usage": {"prompt_tokens": 10, "completion_tokens": 50, "total_tokens": 60}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	resp, err := a.Complete(context.Background(), llm.Request{
		Model:    "minimax/minimax-m2.7",
		Messages: []llm.Message{llm.User("How many r's in strawberry?")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if len(resp.Message.Content) < 2 {
		t.Fatalf("expected at least 2 content parts, got %d", len(resp.Message.Content))
	}
	if resp.Message.Content[0].Kind != llm.ContentThinking {
		t.Fatalf("first part kind: %v, want thinking", resp.Message.Content[0].Kind)
	}
	if resp.Message.Content[0].Thinking == nil || resp.Message.Content[0].Thinking.Text != "Let me count: s-t-r-a-w-b-e-r-r-y. r appears at positions 3, 8, 9." {
		t.Fatalf("thinking text mismatch: %+v", resp.Message.Content[0].Thinking)
	}
	if resp.Message.Content[1].Kind != llm.ContentText {
		t.Fatalf("second part kind: %v, want text", resp.Message.Content[1].Kind)
	}
	if resp.ReasoningText() != "Let me count: s-t-r-a-w-b-e-r-r-y. r appears at positions 3, 8, 9." {
		t.Fatalf("ReasoningText(): %q", resp.ReasoningText())
	}
}

func TestComplete_ReasoningDetails_SuppressesReasoningEffort(t *testing.T) {
	// When provider options include a "reasoning" key, reasoning_effort should
	// NOT be emitted (it would conflict with the provider-specific format).
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "chatcmpl-mm2", "model": "minimax/minimax-m2.7",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	effort := "high"
	_, err := a.Complete(context.Background(), llm.Request{
		Model:           "minimax/minimax-m2.7",
		Messages:        []llm.Message{llm.User("think hard")},
		ReasoningEffort: &effort,
		ProviderOptions: map[string]any{
			"openai-compatible": map[string]any{
				"reasoning": map[string]any{"enabled": true},
			},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// reasoning_effort should be suppressed in favor of the provider-specific "reasoning" field.
	if _, has := gotBody["reasoning_effort"]; has {
		t.Fatalf("reasoning_effort should be suppressed when provider options contain 'reasoning', got: %v", gotBody["reasoning_effort"])
	}
	// "reasoning" from provider options should be present.
	reasoning, ok := gotBody["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("reasoning should be present from provider options, got: %v", gotBody["reasoning"])
	}
	if reasoning["enabled"] != true {
		t.Fatalf("reasoning.enabled: %v", reasoning["enabled"])
	}
}

func TestComplete_RoundTrip_ThinkingSerializedAsReasoningDetails(t *testing.T) {
	// When provider options contain "reasoning" (OpenRouter MiniMax format),
	// thinking data should be serialized as reasoning_details, not reasoning_content.
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "chatcmpl-mm3", "model": "minimax/minimax-m2.7",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	_, err := a.Complete(context.Background(), llm.Request{
		Model: "minimax/minimax-m2.7",
		Messages: []llm.Message{
			llm.User("question"),
			{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "my reasoning"}},
				{Kind: llm.ContentText, Text: "my answer"},
			}},
			llm.User("followup"),
		},
		ProviderOptions: map[string]any{
			"openai-compatible": map[string]any{
				"reasoning": map[string]any{"enabled": true},
			},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	msgs := gotBody["messages"].([]any)
	assistantMsg := msgs[1].(map[string]any)

	// Should use reasoning_details format, not reasoning_content.
	if _, has := assistantMsg["reasoning_content"]; has {
		t.Fatalf("reasoning_content should NOT be present when using reasoning_details format, got: %v", assistantMsg["reasoning_content"])
	}
	details, ok := assistantMsg["reasoning_details"].([]any)
	if !ok || len(details) == 0 {
		t.Fatalf("reasoning_details should be present, got: %v", assistantMsg["reasoning_details"])
	}
	detail := details[0].(map[string]any)
	if detail["type"] != "reasoning.text" || detail["text"] != "my reasoning" {
		t.Fatalf("reasoning_details[0]: %v", detail)
	}
	if assistantMsg["content"] != "my answer" {
		t.Fatalf("content: %v", assistantMsg["content"])
	}
}

func TestStream_ReasoningDetails_EmitsReasoningEvents(t *testing.T) {
	// OpenRouter streams reasoning_details as reasoning_content deltas for MiniMax.
	// But the final response should report them correctly.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		chunks := []string{
			`{"id":"c1","model":"minimax/minimax-m2.7","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"Let me "},"finish_reason":null}]}`,
			`{"id":"c1","model":"minimax/minimax-m2.7","choices":[{"index":0,"delta":{"reasoning_content":"count..."},"finish_reason":null}]}`,
			`{"id":"c1","model":"minimax/minimax-m2.7","choices":[{"index":0,"delta":{"content":"There are 3."},"finish_reason":null}]}`,
			`{"id":"c1","model":"minimax/minimax-m2.7","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":20,"total_tokens":25}}`,
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
		Model:    "minimax/minimax-m2.7",
		Messages: []llm.Message{llm.User("count r's")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer st.Close()

	var reasoningText, contentText strings.Builder
	for ev := range st.Events() {
		if ev.Type == llm.StreamEventReasoningDelta {
			reasoningText.WriteString(ev.ReasoningDelta)
		}
		if ev.Type == llm.StreamEventTextDelta {
			contentText.WriteString(ev.Delta)
		}
	}

	if reasoningText.String() != "Let me count..." {
		t.Fatalf("reasoning deltas: %q", reasoningText.String())
	}
	if contentText.String() != "There are 3." {
		t.Fatalf("text deltas: %q", contentText.String())
	}
}

// TestRescueClaudeXMLArgs exercises the rescue function against real-world
// corruption patterns observed in MiniMax M2.7 via OpenRouter. MiniMax
// sometimes reverts from JSON tool calling to Claude-style XML tool syntax
// mid-generation, which produces tool args like:
//
//	{"action":"update\">\n<parameter name=\"updates\">[{...}]"}
//
// These parse as JSON (one field with a long string value) but fail tool
// schema validation because the value for `action` is not a valid enum member.
// The rescue function detects the XML pattern inside the string value and
// lifts the embedded parameters to sibling fields.
func TestRescueClaudeXMLArgs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // JSON-encoded expected output (field order doesn't matter)
	}{
		{
			name: "clean JSON passes through",
			in:   `{"action":"append","tasks":[{"prompt":"hi"}]}`,
			want: `{"action":"append","tasks":[{"prompt":"hi"}]}`,
		},
		{
			name: "incomplete XML (no closing tag)",
			in:   `{"action":"update\">\n<parameter name=\"updates\">[{\"id\":1,\"status\":\"done\"}]"}`,
			want: `{"action":"update","updates":[{"id":1,"status":"done"}]}`,
		},
		{
			name: "complete XML (with closing tag)",
			in:   `{"action":"append\">\n<parameter name=\"tasks\">[{\"title\":\"t1\"}]</parameter>"}`,
			want: `{"action":"append","tasks":[{"title":"t1"}]}`,
		},
		{
			name: "multiple parameter blocks",
			in:   `{"action":"x\">\n<parameter name=\"a\">hello</parameter>\n<parameter name=\"b\">world</parameter>"}`,
			want: `{"a":"hello","action":"x","b":"world"}`,
		},
		{
			name: "json-encoded string becomes array (type confusion)",
			in:   `{"task":"do stuff","task_list":"[{\"title\":\"one\",\"prompt\":\"do one\"}]"}`,
			want: `{"task":"do stuff","task_list":[{"prompt":"do one","title":"one"}]}`,
		},
		{
			name: "genuine string starting with [ not parsed",
			in:   `{"message":"[INFO] starting up"}`,
			want: `{"message":"[INFO] starting up"}`,
		},
		{
			name: "single-quoted parameter name attribute",
			in:   `{"action":"update\">\n<parameter name='updates'>[{\"id\":1}]</parameter>"}`,
			want: `{"action":"update","updates":[{"id":1}]}`,
		},
		{
			name: "uppercase PARAMETER tag",
			in:   `{"action":"update\">\n<PARAMETER NAME=\"updates\">[{\"id\":2}]</PARAMETER>"}`,
			want: `{"action":"update","updates":[{"id":2}]}`,
		},
		{
			name: "extra attributes before name",
			in:   `{"action":"update\">\n<parameter type=\"array\" name=\"updates\">[{\"id\":3}]</parameter>"}`,
			want: `{"action":"update","updates":[{"id":3}]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := rescueClaudeXMLArgs(tc.in)
			var g, w any
			if err := json.Unmarshal([]byte(got), &g); err != nil {
				t.Fatalf("result is not valid JSON: %v\ngot: %s", err, got)
			}
			if err := json.Unmarshal([]byte(tc.want), &w); err != nil {
				t.Fatalf("want is not valid JSON: %v", err)
			}
			gj, _ := json.Marshal(g)
			wj, _ := json.Marshal(w)
			if !bytes.Equal(gj, wj) {
				t.Fatalf("\n got: %s\nwant: %s", gj, wj)
			}
		})
	}
}

// ================== Effort translation tests ==================

// openrouterWireEffort extracts the effort from OpenRouter's canonical
// reasoning object ({"reasoning": {"effort": ...}}), the preset's
// thinking_format="openrouter" wire shape (live-verified 2026-07-02:
// the full serf vocabulary incl. xhigh/minimal is accepted).
func openrouterWireEffort(t *testing.T, body map[string]any) string {
	t.Helper()
	if _, ok := body["reasoning_effort"]; ok {
		t.Fatal("openrouter preset must emit the reasoning object, not top-level reasoning_effort")
	}
	obj, ok := body["reasoning"].(map[string]any)
	if !ok {
		t.Fatal("expected reasoning object in body")
	}
	got, ok := obj["effort"].(string)
	if !ok {
		t.Fatal("expected reasoning.effort in body")
	}
	return got
}

func TestBuildRequestBody_TranslateMaxToXHigh_OpenRouter(t *testing.T) {
	// OpenRouter quirk translates "max" to "xhigh".
	effort := "max"
	req := llm.Request{
		Model:           "some-model",
		Messages:        []llm.Message{{Role: "user", Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi"}}}},
		ReasoningEffort: &effort,
	}
	quirks := QuirksPreset("openrouter")
	body, err := buildRequestBody(req, false, ModelCompat{Quirks: quirks})
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	if got := openrouterWireEffort(t, body); got != "xhigh" {
		t.Fatalf("expected reasoning.effort='xhigh', got %q", got)
	}
}

func TestBuildRequestBody_NoTranslation_OtherProviders(t *testing.T) {
	// Without the quirk, "max" passes through unchanged.
	effort := "max"
	req := llm.Request{
		Model:           "some-model",
		Messages:        []llm.Message{{Role: "user", Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi"}}}},
		ReasoningEffort: &effort,
	}
	quirks := ProviderQuirks{} // No translation quirk
	body, err := buildRequestBody(req, false, ModelCompat{Quirks: quirks})
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	got, ok := body["reasoning_effort"].(string)
	if !ok {
		t.Fatal("expected reasoning_effort in body")
	}
	if got != "max" {
		t.Fatalf("expected reasoning_effort='max' (unchanged), got %q", got)
	}
}

func TestBuildRequestBody_TranslateMaxToXHigh_CaseInsensitive(t *testing.T) {
	// The translation should be case-insensitive.
	for _, effort := range []string{"MAX", "Max", "mAx"} {
		t.Run(effort, func(t *testing.T) {
			e := effort
			req := llm.Request{
				Model:           "some-model",
				Messages:        []llm.Message{{Role: "user", Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi"}}}},
				ReasoningEffort: &e,
			}
			quirks := QuirksPreset("openrouter")
			body, err := buildRequestBody(req, false, ModelCompat{Quirks: quirks})
			if err != nil {
				t.Fatalf("buildRequestBody: %v", err)
			}
			if got := openrouterWireEffort(t, body); got != "xhigh" {
				t.Fatalf("expected reasoning.effort='xhigh', got %q", got)
			}
		})
	}
}

func TestBuildRequestBody_OtherEffortLevels_NoTranslation(t *testing.T) {
	// Non-"max" levels pass through unchanged even with the quirk.
	levels := []string{"low", "medium", "high", "xhigh"}
	quirks := QuirksPreset("openrouter")
	for _, level := range levels {
		t.Run(level, func(t *testing.T) {
			e := level
			req := llm.Request{
				Model:           "some-model",
				Messages:        []llm.Message{{Role: "user", Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi"}}}},
				ReasoningEffort: &e,
			}
			body, err := buildRequestBody(req, false, ModelCompat{Quirks: quirks})
			if err != nil {
				t.Fatalf("buildRequestBody: %v", err)
			}
			if got := openrouterWireEffort(t, body); got != level {
				t.Fatalf("expected reasoning.effort=%q, got %q", level, got)
			}
		})
	}
}

func TestNewForInstance_Name(t *testing.T) {
	a := NewForInstance(OpenAICompatInstanceParams{
		Name:    "work",
		BaseURL: "http://x",
		APIKey:  "k",
		Quirks:  QuirksPreset("kimi"),
	})
	if a.Name() != "work" {
		t.Fatalf("Name() = %q, want work", a.Name())
	}
}

func TestNewForInstance_QuirksApplied(t *testing.T) {
	a := NewForInstance(OpenAICompatInstanceParams{
		Name:    "work",
		BaseURL: "http://x",
		APIKey:  "k",
		Quirks:  QuirksPreset("kimi"),
	})
	if !a.Quirks.LockTemperature {
		t.Fatal("expected kimi quirks (LockTemperature) to be applied")
	}
}

func TestNewForInstance_EnvPathPreservesName(t *testing.T) {
	// The env factory still names the adapter "openai-compatible".
	t.Setenv("OPENAI_COMPATIBLE_BASE_URL", "http://env-test")
	t.Setenv("OPENAI_COMPATIBLE_API_KEY", "envkey")
	t.Setenv("OPENAI_COMPATIBLE_PROVIDER_QUIRKS", "")
	a, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	if a.Name() != "openai-compatible" {
		t.Fatalf("Name() = %q, want openai-compatible", a.Name())
	}
}

func TestAdapter_ListModels_ParsesContextLength(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"kimi-for-coding","context_length":262144}]}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{BaseURL: srv.URL, Client: srv.Client()}
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(models))
	}
	if models[0].ContextWindow != 262144 {
		t.Fatalf("ContextWindow = %d, want 262144 (provider context_length must be parsed)", models[0].ContextWindow)
	}
}

// Non-stream responses must parse encrypted reasoning_details onto the thinking
// part's EncryptedContent (OpenRouter Gemini/o-series opaque reasoning chain).
func TestComplete_EncryptedReasoningDetails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"id": "chatcmpl-1", "model": "m",
			"choices": []any{map[string]any{
				"index":         0,
				"finish_reason": "stop",
				"message": map[string]any{
					"role":    "assistant",
					"content": "answer",
					"reasoning_details": []any{
						map[string]any{"type": "reasoning.encrypted", "id": "rc_1", "data": "OPAQUE"},
					},
				},
			}},
		})
	}))
	defer srv.Close()

	a := &Adapter{BaseURL: srv.URL, Client: srv.Client()}
	resp, err := a.Complete(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	var enc string
	for _, p := range resp.Message.Content {
		if p.Kind == llm.ContentThinking && p.Thinking != nil {
			enc = p.Thinking.EncryptedContent
		}
	}
	if enc == "" || !strings.Contains(enc, "OPAQUE") {
		t.Fatalf("encrypted content = %q, want it to carry OPAQUE", enc)
	}
}

// Moonshot/Kimi may report usage on choices[0].usage instead of top-level usage.
func TestComplete_ChoiceLevelUsageFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"id": "chatcmpl-1", "model": "m",
			"choices": []any{map[string]any{
				"index":         0,
				"finish_reason": "stop",
				"message":       map[string]any{"role": "assistant", "content": "hi"},
				"usage":         map[string]any{"prompt_tokens": 7, "completion_tokens": 2, "total_tokens": 9},
			}},
		})
	}))
	defer srv.Close()

	a := &Adapter{BaseURL: srv.URL, Client: srv.Client()}
	resp, err := a.Complete(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.InputTokens != 7 || resp.Usage.OutputTokens != 2 {
		t.Fatalf("choice-level usage not picked up: %+v", resp.Usage)
	}
}

// When both top-level and choice-level usage are present, top-level wins.
func TestComplete_TopLevelUsageWinsOverChoice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"id": "chatcmpl-1", "model": "m",
			"choices": []any{map[string]any{
				"index":         0,
				"finish_reason": "stop",
				"message":       map[string]any{"role": "assistant", "content": "hi"},
				"usage":         map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
			}},
			"usage": map[string]any{"prompt_tokens": 7, "completion_tokens": 2, "total_tokens": 9},
		})
	}))
	defer srv.Close()

	a := &Adapter{BaseURL: srv.URL, Client: srv.Client()}
	resp, err := a.Complete(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.InputTokens != 7 || resp.Usage.OutputTokens != 2 {
		t.Fatalf("top-level usage should win: %+v", resp.Usage)
	}
}

// An openai + chat-completions instance without base_url targets OpenAI's own
// endpoint instead of sending requests to relative URLs.
func TestNewOpenAIChatCompletionsInstance_DefaultBaseURL(t *testing.T) {
	a, err := newOpenAIChatCompletionsInstance(providercfg.InstanceConfig{Name: "work", Type: "openai", APIStyle: providercfg.StyleChatCompletions}, "")
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if got := a.(*Adapter).BaseURL; got != "https://api.openai.com/v1" {
		t.Fatalf("BaseURL = %q, want the OpenAI default", got)
	}
	b, err := newOpenAIChatCompletionsInstance(providercfg.InstanceConfig{Name: "gw", Type: "openai", APIStyle: providercfg.StyleChatCompletions, BaseURL: "https://gw.example.com/v1"}, "")
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if got := b.(*Adapter).BaseURL; got != "https://gw.example.com/v1" {
		t.Fatalf("BaseURL = %q, want the configured gateway", got)
	}
}
