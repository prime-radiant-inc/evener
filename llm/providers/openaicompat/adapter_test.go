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
