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

	"primeradiant.com/serf/llm"
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
		w.WriteHeader(429)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "rate limited"}}) //nolint:errcheck
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
	var rte *llm.RequestTimeoutError
	if errors.As(err, &rte) {
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
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
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

func TestStream_ReasoningContent_EmitsReasoningEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		chunks := []string{
			`{"id":"chatcmpl-1","model":"kimi-k2.5","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"Let me "},"finish_reason":null}]}`,
			`{"id":"chatcmpl-1","model":"kimi-k2.5","choices":[{"index":0,"delta":{"reasoning_content":"think..."},"finish_reason":null}]}`,
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

	if strings.Join(reasoningDeltas, "") != "Let me think..." {
		t.Fatalf("reasoning deltas: %q", strings.Join(reasoningDeltas, ""))
	}
	if strings.Join(textDeltas, "") != "The answer is 42." {
		t.Fatalf("text deltas: %q", strings.Join(textDeltas, ""))
	}

	// Verify event ordering.
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

	// Verify final response includes thinking content.
	if finalResp == nil {
		t.Fatal("no FINISH response")
	}
	if finalResp.ReasoningText() != "Let me think..." {
		t.Fatalf("final ReasoningText(): %q", finalResp.ReasoningText())
	}
	if finalResp.Text() != "The answer is 42." {
		t.Fatalf("final Text(): %q", finalResp.Text())
	}
}

func TestStream_ReasoningContent_ThenToolCall_EmitsReasoningEndBeforeToolStart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		chunks := []string{
			`{"id":"chatcmpl-1","model":"kimi-k2.5","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"I need to call the API."},"finish_reason":null}]}`,
			`{"id":"chatcmpl-1","model":"kimi-k2.5","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}`,
			`{"id":"chatcmpl-1","model":"kimi-k2.5","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":\"SF\"}"}}]},"finish_reason":null}]}`,
			`{"id":"chatcmpl-1","model":"kimi-k2.5","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`,
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
		Messages: []llm.Message{llm.User("weather in SF?")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer st.Close()

	var kinds []llm.StreamEventType
	var finalResp *llm.Response
	for ev := range st.Events() {
		kinds = append(kinds, ev.Type)
		if ev.Type == llm.StreamEventFinish && ev.Response != nil {
			finalResp = ev.Response
		}
	}

	// REASONING_END should appear before TOOL_CALL_START.
	var reasoningEndIdx, toolCallStartIdx int
	reasoningEndIdx = -1
	toolCallStartIdx = -1
	for i, k := range kinds {
		if k == llm.StreamEventReasoningEnd {
			reasoningEndIdx = i
		}
		if k == llm.StreamEventToolCallStart && toolCallStartIdx == -1 {
			toolCallStartIdx = i
		}
	}
	if reasoningEndIdx == -1 {
		t.Fatalf("expected REASONING_END event, kinds: %v", kinds)
	}
	if toolCallStartIdx == -1 {
		t.Fatalf("expected TOOL_CALL_START event, kinds: %v", kinds)
	}
	if reasoningEndIdx >= toolCallStartIdx {
		t.Fatalf("REASONING_END (%d) should come before TOOL_CALL_START (%d)", reasoningEndIdx, toolCallStartIdx)
	}

	// No TEXT_START or TEXT_DELTA events.
	for _, k := range kinds {
		if k == llm.StreamEventTextStart || k == llm.StreamEventTextDelta {
			t.Fatalf("unexpected text event: %v", k)
		}
	}

	// Final response should have ContentThinking + ContentToolCall (no ContentText).
	if finalResp == nil {
		t.Fatal("no FINISH response")
	}
	if finalResp.ReasoningText() != "I need to call the API." {
		t.Fatalf("ReasoningText(): %q", finalResp.ReasoningText())
	}
	if len(finalResp.ToolCalls()) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(finalResp.ToolCalls()))
	}
}

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

func TestComplete_NoThinking_OmitsReasoningContent(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "chatcmpl-nr2",
  "model": "gpt-4o",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "OK"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 5, "completion_tokens": 1, "total_tokens": 6}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	_, err := a.Complete(context.Background(), llm.Request{
		Model: "gpt-4o",
		Messages: []llm.Message{
			llm.User("hi"),
			llm.Assistant("hey"),
			llm.User("what?"),
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	msgs, _ := gotBody["messages"].([]any)
	assistantMsg, _ := msgs[1].(map[string]any)
	if _, ok := assistantMsg["reasoning_content"]; ok {
		t.Fatalf("reasoning_content should be absent, got %v", assistantMsg["reasoning_content"])
	}
}

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
	reasoning := strings.Repeat("abcd", 20) // 80 chars -> 80/4 = 20 estimated tokens
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
