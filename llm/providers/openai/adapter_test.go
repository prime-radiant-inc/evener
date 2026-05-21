package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	authopenai "primeradiant.com/serf/internal/auth/openai"
	"primeradiant.com/serf/internal/auth/openai/oaitest"
	"primeradiant.com/serf/llm"
)

func TestAdapter_Complete_MapsToResponsesAPI(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "resp_1",
  "model": "gpt-5.2",
  "output": [
    {"type": "message", "content": [{"type":"output_text", "text":"Hello"}]}
  ],
  "usage": {"input_tokens": 1, "output_tokens": 2, "total_tokens": 3}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	reasoning := "low"
	resp, err := a.Complete(ctx, llm.Request{
		Model: "gpt-5.2",
		Messages: []llm.Message{
			llm.System("sys"),
			llm.Developer("dev"),
			llm.User("u1"),
			llm.Assistant("a1"),
			llm.ToolResultNamed("call1", "shell", map[string]any{"ok": true}, false),
		},
		Tools: []llm.ToolDefinition{{
			Name:        "shell",
			Description: "run shell",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		}},
		ReasoningEffort: &reasoning,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if strings.TrimSpace(resp.Text()) != "Hello" {
		t.Fatalf("resp text: %q", resp.Text())
	}

	// Assert request mapping.
	if gotBody == nil {
		t.Fatalf("server did not capture request body")
	}
	if gotBody["model"] != "gpt-5.2" {
		t.Fatalf("model: %v", gotBody["model"])
	}
	if instr, _ := gotBody["instructions"].(string); !strings.Contains(instr, "sys") || !strings.Contains(instr, "dev") {
		t.Fatalf("instructions: %q", instr)
	}
	if reasoningAny, ok := gotBody["reasoning"].(map[string]any); !ok || reasoningAny["effort"] != "low" {
		t.Fatalf("reasoning: %#v", gotBody["reasoning"])
	}
	if toolsAny, ok := gotBody["tools"].([]any); !ok || len(toolsAny) != 1 {
		t.Fatalf("tools: %#v", gotBody["tools"])
	}
	if inputAny, ok := gotBody["input"].([]any); !ok || len(inputAny) == 0 {
		t.Fatalf("input: %#v", gotBody["input"])
	}
}

func TestAdapter_Complete_ToolChoice_MappedPerSpec(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "resp_1",
  "model": "gpt-5.2",
  "output": [{"type": "message", "content": [{"type":"output_text", "text":"ok"}]}],
  "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	toolDef := llm.ToolDefinition{Name: "shell", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}

	cases := []struct {
		name string
		tc   *llm.ToolChoice
		want func(t *testing.T, v any)
	}{
		{
			name: "auto",
			tc:   &llm.ToolChoice{Mode: "auto"},
			want: func(t *testing.T, v any) {
				if v != "auto" {
					t.Fatalf("tool_choice: got %#v want %q", v, "auto")
				}
			},
		},
		{
			name: "none",
			tc:   &llm.ToolChoice{Mode: "none"},
			want: func(t *testing.T, v any) {
				if v != "none" {
					t.Fatalf("tool_choice: got %#v want %q", v, "none")
				}
			},
		},
		{
			name: "required",
			tc:   &llm.ToolChoice{Mode: "required"},
			want: func(t *testing.T, v any) {
				if v != "required" {
					t.Fatalf("tool_choice: got %#v want %q", v, "required")
				}
			},
		},
		{
			name: "named",
			tc:   &llm.ToolChoice{Mode: "named", Name: "shell"},
			want: func(t *testing.T, v any) {
				m, ok := v.(map[string]any)
				if !ok {
					t.Fatalf("tool_choice: %#v", v)
				}
				if m["type"] != "function" {
					t.Fatalf("tool_choice.type: %#v", m["type"])
				}
				fn, _ := m["function"].(map[string]any)
				if fn["name"] != "shell" {
					t.Fatalf("tool_choice.function.name: %#v", fn["name"])
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotBody = nil
			_, err := a.Complete(ctx, llm.Request{
				Model:      "gpt-5.2",
				Messages:   []llm.Message{llm.User("hi")},
				Tools:      []llm.ToolDefinition{toolDef},
				ToolChoice: tc.tc,
			})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if gotBody == nil {
				t.Fatalf("server did not capture request body")
			}
			tc.want(t, gotBody["tool_choice"])
		})
	}
}

func TestAdapter_Complete_CommunicateTool_UsesNonStrictSchema(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "resp_1",
  "model": "gpt-5.2",
  "output": [{"type": "message", "content": [{"type":"output_text", "text":"ok"}]}],
  "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	strictFalse := false
	_, err := a.Complete(ctx, llm.Request{
		Model:    "gpt-5.2",
		Messages: []llm.Message{llm.User("hi")},
		Tools: []llm.ToolDefinition{{
			Name:        "communicate",
			Description: "Send a user-facing message.",
			Strict:      &strictFalse,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"message": map[string]any{"type": "string"},
					"output": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"data": map[string]any{
								"type":                 "object",
								"additionalProperties": true,
							},
						},
					},
				},
				"required": []string{"message", "output"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	toolsAny, ok := gotBody["tools"].([]any)
	if !ok || len(toolsAny) != 1 {
		t.Fatalf("tools: %#v", gotBody["tools"])
	}
	tool, ok := toolsAny[0].(map[string]any)
	if !ok {
		t.Fatalf("tool: %#v", toolsAny[0])
	}
	if strict, ok := tool["strict"].(bool); !ok || strict {
		t.Fatalf("tool.strict=%#v, want false", tool["strict"])
	}
	params, ok := tool["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("tool.parameters=%#v", tool["parameters"])
	}
	props, _ := params["properties"].(map[string]any)
	output, _ := props["output"].(map[string]any)
	outProps, _ := output["properties"].(map[string]any)
	data, _ := outProps["data"].(map[string]any)
	if data["additionalProperties"] != true {
		t.Fatalf("data.additionalProperties=%#v, want true", data["additionalProperties"])
	}
}

func TestAdapter_Complete_Usage_MapsReasoningAndCacheTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "resp_1",
  "model": "gpt-5.2",
  "output": [{"type": "message", "content": [{"type":"output_text", "text":"ok"}]}],
  "usage": {
    "input_tokens": 1,
    "output_tokens": 2,
    "total_tokens": 3,
    "input_tokens_details": {"cached_tokens": 10},
    "output_tokens_details": {"reasoning_tokens": 7}
  }
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := a.Complete(ctx, llm.Request{Model: "gpt-5.2", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Usage.CacheReadTokens == nil || *resp.Usage.CacheReadTokens != 10 {
		t.Fatalf("cache_read_tokens: %#v", resp.Usage.CacheReadTokens)
	}
	if resp.Usage.ReasoningTokens == nil || *resp.Usage.ReasoningTokens != 7 {
		t.Fatalf("reasoning_tokens: %#v", resp.Usage.ReasoningTokens)
	}
}

func TestAdapter_Complete_ToolParameters_DefaultToEmptyObjectSchema(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "resp_1",
  "model": "gpt-5.2",
  "output": [{"type": "message", "content": [{"type":"output_text", "text":"ok"}]}],
  "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{
		Model:    "gpt-5.2",
		Messages: []llm.Message{llm.User("hi")},
		Tools:    []llm.ToolDefinition{{Name: "t1"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	tools, _ := gotBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools: %#v", gotBody["tools"])
	}
	t0, _ := tools[0].(map[string]any)
	params, _ := t0["parameters"].(map[string]any)
	if params["type"] != "object" {
		t.Fatalf("parameters.type: %#v", params["type"])
	}
}

func TestAdapter_Complete_RejectsAudioParts(t *testing.T) {
	a := &Adapter{APIKey: "k", BaseURL: "http://example.com"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	msgAudio := llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentAudio, Audio: &llm.AudioData{URL: "https://example.com/a.wav"}}}}
	_, err := a.Complete(ctx, llm.Request{Model: "gpt-5.2", Messages: []llm.Message{msgAudio}})
	if err == nil {
		t.Fatalf("expected error")
	}
	var ce *llm.ConfigurationError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConfigurationError, got %T (%v)", err, err)
	}
}

func TestAdapter_Complete_DocumentParts(t *testing.T) {
	// Verify that document parts (PDFs) are sent as input_file content.
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-1","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"PDF content"}]}],"usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pdfData := []byte("%PDF-1.4 test content")
	msg := llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: "Describe this document"},
		{Kind: llm.ContentDocument, Document: &llm.DocumentData{
			Data:      pdfData,
			MediaType: "application/pdf",
			FileName:  "invoice.pdf",
		}},
	}}
	resp, err := a.Complete(ctx, llm.Request{Model: "gpt-5.2", Messages: []llm.Message{msg}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Text() != "PDF content" {
		t.Fatalf("got text %q, want 'PDF content'", resp.Text())
	}

	// Verify the request body contains an input_file entry.
	input, ok := gotBody["input"].([]any)
	if !ok || len(input) == 0 {
		t.Fatal("expected input array in request body")
	}
	// Find the user message item.
	var content []any
	for _, item := range input {
		m, _ := item.(map[string]any)
		if m["role"] == "user" {
			content, _ = m["content"].([]any)
		}
	}
	if len(content) < 2 {
		t.Fatalf("expected at least 2 content parts, got %d", len(content))
	}
	// Find the input_file part.
	var filePart map[string]any
	for _, c := range content {
		cm, _ := c.(map[string]any)
		if cm["type"] == "input_file" {
			filePart = cm
		}
	}
	if filePart == nil {
		t.Fatal("expected input_file content part for document")
	}
	if filePart["filename"] != "invoice.pdf" {
		t.Fatalf("filename = %q, want 'invoice.pdf'", filePart["filename"])
	}
	fileData, _ := filePart["file_data"].(string)
	if !strings.HasPrefix(fileData, "data:application/pdf;base64,") {
		t.Fatalf("file_data should be a data URI, got: %q", fileData[:min(len(fileData), 50)])
	}
}

func TestAdapter_Complete_HTTPErrorMapping_IncludesRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{Model: "gpt-5.2", Messages: []llm.Message{llm.User("hi")}})
	if err == nil {
		t.Fatalf("expected error")
	}
	var rl *llm.RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("expected RateLimitError, got %T (%v)", err, err)
	}
	if rl.StatusCode() != 429 {
		t.Fatalf("status_code: %d", rl.StatusCode())
	}
	if rl.RetryAfter() == nil || *rl.RetryAfter() != 2*time.Second {
		t.Fatalf("retry_after: %v", rl.RetryAfter())
	}
}

func TestAdapter_Complete_HTTPErrorMapping_AuthenticationError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{Model: "gpt-5.2", Messages: []llm.Message{llm.User("hi")}})
	if err == nil {
		t.Fatalf("expected error")
	}
	var ae *llm.AuthenticationError
	if !errors.As(err, &ae) {
		t.Fatalf("expected AuthenticationError, got %T (%v)", err, err)
	}
	if ae.StatusCode() != 401 {
		t.Fatalf("status_code: %d", ae.StatusCode())
	}
	if ae.Retryable() {
		t.Fatalf("expected non-retryable auth error")
	}
}

func TestAdapter_Stream_YieldsTextDeltasAndFinish(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)

		write := func(event string, data string) {
			_, _ = io.WriteString(w, "event: "+event+"\n")
			_, _ = io.WriteString(w, "data: "+data+"\n\n")
			if f != nil {
				f.Flush()
			}
		}

		write("response.output_text.delta", `{"type":"response.output_text.delta","delta":"Hel"}`)
		write("response.output_text.delta", `{"type":"response.output_text.delta","delta":"lo"}`)
		write("response.completed", `{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.2","output":[{"type":"message","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{Model: "gpt-5.2", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close() //nolint:errcheck

	var deltas []string
	var kinds []llm.StreamEventType
	var finish *llm.Response
	for ev := range stream.Events() {
		kinds = append(kinds, ev.Type)
		if ev.Type == llm.StreamEventTextDelta {
			deltas = append(deltas, ev.Delta)
		}
		if ev.Type == llm.StreamEventFinish && ev.Response != nil {
			finish = ev.Response
		}
	}
	if strings.Join(deltas, "") != "Hello" {
		t.Fatalf("deltas: %q", strings.Join(deltas, ""))
	}
	if finish == nil || strings.TrimSpace(finish.Text()) != "Hello" {
		t.Fatalf("finish response: %+v", finish)
	}

	if gotBody == nil {
		t.Fatalf("server did not capture request body")
	}
	if v, _ := gotBody["stream"].(bool); !v {
		t.Fatalf("expected stream=true in request body; got %#v", gotBody["stream"])
	}

	// Basic ordering check: STREAM_START before deltas; FINISH present.
	if len(kinds) == 0 || kinds[0] != llm.StreamEventStreamStart {
		t.Fatalf("first event: got %v want %v (kinds=%v)", kinds, llm.StreamEventStreamStart, kinds)
	}
	foundTextStart := false
	foundTextEnd := false
	foundFinish := false
	for _, k := range kinds {
		if k == llm.StreamEventTextStart {
			foundTextStart = true
		}
		if k == llm.StreamEventTextEnd {
			foundTextEnd = true
		}
		if k == llm.StreamEventFinish {
			foundFinish = true
		}
	}
	if !foundTextStart || !foundTextEnd {
		t.Fatalf("expected TEXT_START and TEXT_END events (kinds=%v)", kinds)
	}
	if !foundFinish {
		t.Fatalf("expected FINISH event (kinds=%v)", kinds)
	}
}

func TestAdapter_Stream_CloseClosesResponsesStream(t *testing.T) {
	requestDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
		w.Header().Set("Content-Type", "text/event-stream")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
		close(requestDone)
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{Model: "gpt-5.2", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	closed := make(chan error, 1)
	go func() { closed <- stream.Close() }()

	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Close hung without closing the underlying Responses stream")
	}

	select {
	case <-requestDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("server request context was not canceled by stream Close")
	}
}

func TestAdapter_Stream_TranslatesToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)

		write := func(event string, data string) {
			_, _ = io.WriteString(w, "event: "+event+"\n")
			_, _ = io.WriteString(w, "data: "+data+"\n\n")
			if f != nil {
				f.Flush()
			}
		}

		write("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","call_id":"call_1","name":"get_weather","delta":"{\"n\":1}"}`)
		write("response.output_item.done", `{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"n\":1}"}}`)
		write("response.completed", `{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.2","output":[{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"n\":1}"}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{Model: "gpt-5.2", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close() //nolint:errcheck

	starts := 0
	deltas := 0
	ends := 0
	var startID, endID, name string
	var endArgs string
	var finishResp *llm.Response

	for ev := range stream.Events() {
		switch ev.Type {
		case llm.StreamEventToolCallStart:
			starts++
			if ev.ToolCall != nil {
				startID = ev.ToolCall.ID
				name = ev.ToolCall.Name
			}
		case llm.StreamEventToolCallDelta:
			deltas++
		case llm.StreamEventToolCallEnd:
			ends++
			if ev.ToolCall != nil {
				endID = ev.ToolCall.ID
				if name == "" {
					name = ev.ToolCall.Name
				}
				endArgs = string(ev.ToolCall.Arguments)
			}
		case llm.StreamEventFinish:
			if ev.Response != nil {
				finishResp = ev.Response
			}
		}
	}

	if starts != 1 || deltas < 1 || ends != 1 {
		t.Fatalf("tool call events: got starts=%d deltas=%d ends=%d", starts, deltas, ends)
	}
	if startID != "call_1" || endID != "call_1" {
		t.Fatalf("call ids: start=%q end=%q", startID, endID)
	}
	if name != "get_weather" {
		t.Fatalf("tool name: %q", name)
	}
	if strings.TrimSpace(endArgs) != `{"n":1}` {
		t.Fatalf("tool args: %q", endArgs)
	}
	if finishResp == nil {
		t.Fatalf("expected finish response")
	}
	calls := finishResp.ToolCalls()
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "get_weather" {
		t.Fatalf("finish tool calls: %+v", calls)
	}
}

func TestAdapter_Complete_ImageInput_URL_Data_AndFilePath(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "resp_1",
  "model": "gpt-5.2",
  "output": [{"type": "message", "content": [{"type":"output_text", "text":"ok"}]}],
  "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	imgPath := filepath.Join(dir, "img.png")
	_ = os.WriteFile(imgPath, []byte{0x89, 0x50, 0x4e, 0x47}, 0o644)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	msg := llm.Message{
		Role: llm.RoleUser,
		Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "see"},
			{Kind: llm.ContentImage, Image: &llm.ImageData{URL: "https://example.com/x.png"}},
			{Kind: llm.ContentImage, Image: &llm.ImageData{MediaType: "image/png", Data: []byte{0x01, 0x02, 0x03}}},
			{Kind: llm.ContentImage, Image: &llm.ImageData{URL: imgPath}},
		},
	}
	if _, err := a.Complete(ctx, llm.Request{Model: "gpt-5.2", Messages: []llm.Message{msg}}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	inputAny, ok := gotBody["input"].([]any)
	if !ok || len(inputAny) == 0 {
		t.Fatalf("input: %#v", gotBody["input"])
	}
	// Find first message item and inspect content.
	var content []any
	for _, it := range inputAny {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if m["type"] == "message" && m["role"] == "user" {
			if c, ok := m["content"].([]any); ok {
				content = c
			}
		}
	}
	if len(content) == 0 {
		t.Fatalf("missing message content in input: %#v", inputAny)
	}

	seenURL := false
	seenData := false
	seenFile := false
	for _, cAny := range content {
		c, ok := cAny.(map[string]any)
		if !ok {
			continue
		}
		if c["type"] != "input_image" {
			continue
		}
		u, _ := c["image_url"].(string)
		switch {
		case strings.HasPrefix(u, "https://example.com/"):
			seenURL = true
		case strings.HasPrefix(u, "data:image/png;base64,"):
			// Covers both raw data and file-path expansion.
			if seenData {
				seenFile = true
			} else {
				seenData = true
			}
		}
	}
	if !seenURL || !seenData || !seenFile {
		t.Fatalf("expected url+data+file images; seenURL=%v seenData=%v seenFile=%v content=%#v", seenURL, seenData, seenFile, content)
	}
}

func TestImageInput_DetailHintPassedThrough(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &captured) //nolint:errcheck
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
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
	t.Cleanup(srv.Close)

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

func TestAdapter_Complete_ResponseFormat_JSONSchema(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "resp_1",
  "model": "gpt-5.2",
  "output": [{"type": "message", "content": [{"type":"output_text", "text":"{}"}]}],
  "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string"},
		},
		"required": []string{"name"},
	}
	_, err := a.Complete(ctx, llm.Request{
		Model:    "gpt-5.2",
		Messages: []llm.Message{llm.User("hi")},
		ResponseFormat: &llm.ResponseFormat{
			Type:       "json_schema",
			JSONSchema: schema,
			Strict:     true,
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	text, _ := gotBody["text"].(map[string]any)
	if text == nil {
		t.Fatalf("text: %#v", gotBody["text"])
	}
	rf, ok := text["format"].(map[string]any)
	if !ok || rf == nil {
		t.Fatalf("text.format: %#v", text["format"])
	}
	if rf["type"] != "json_schema" {
		t.Fatalf("text.format.type: %#v", rf["type"])
	}
	if rf["name"] == "" {
		t.Fatalf("text.format.name: %#v", rf["name"])
	}
	if _, ok := rf["schema"].(map[string]any); !ok {
		t.Fatalf("text.format.schema: %#v", rf["schema"])
	}
}

func TestAdapter_Stream_ContextDeadline_EmitsRequestTimeoutError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	st, err := a.Stream(ctx, llm.Request{Model: "gpt-5.2", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer st.Close() //nolint:errcheck

	var sawErr error
	for ev := range st.Events() {
		if ev.Type == llm.StreamEventError && ev.Err != nil {
			sawErr = ev.Err
		}
	}
	if sawErr == nil {
		t.Fatalf("expected stream error")
	}
	var rte *llm.RequestTimeoutError
	if !errors.As(sawErr, &rte) {
		t.Fatalf("expected RequestTimeoutError, got %T (%v)", sawErr, sawErr)
	}
}

func TestAdapter_ProviderOptions_PassThrough(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "resp_1",
  "model": "gpt-5.2",
  "output": [{"type": "message", "content": [{"type":"output_text", "text":"ok"}]}],
  "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{
		Model:    "gpt-5.2",
		Messages: []llm.Message{llm.User("hi")},
		ProviderOptions: map[string]any{
			"openai": map[string]any{
				"parallel_tool_calls": true,
			},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got, _ := gotBody["parallel_tool_calls"].(bool); !got {
		t.Fatalf("parallel_tool_calls: %#v", gotBody["parallel_tool_calls"])
	}
}

func TestAdapter_Complete_WebSearch_AddsToolToRequest(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "resp_1",
  "model": "gpt-5.2",
  "output": [{"type": "message", "content": [{"type":"output_text", "text":"ok"}]}],
  "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{
		Model:    "gpt-5.2",
		Messages: []llm.Message{llm.User("search for go errors")},
		Tools: []llm.ToolDefinition{{
			Name:       "shell",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
		}},
		WebSearch: true,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	toolsAny, ok := gotBody["tools"].([]any)
	if !ok {
		t.Fatalf("tools not array: %#v", gotBody["tools"])
	}
	if len(toolsAny) != 2 {
		t.Fatalf("tools count: got %d want 2", len(toolsAny))
	}
	lastTool, _ := toolsAny[len(toolsAny)-1].(map[string]any)
	if lastTool["type"] != "web_search" {
		t.Fatalf("last tool type: got %v want web_search", lastTool["type"])
	}
}

func TestAdapter_Complete_WebSearch_DisabledByDefault(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "resp_1",
  "model": "gpt-5.2",
  "output": [{"type": "message", "content": [{"type":"output_text", "text":"ok"}]}],
  "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{
		Model:    "gpt-5.2",
		Messages: []llm.Message{llm.User("hello")},
		Tools: []llm.ToolDefinition{{
			Name:       "shell",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
		}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	toolsAny, ok := gotBody["tools"].([]any)
	if !ok {
		t.Fatalf("tools not array: %#v", gotBody["tools"])
	}
	if len(toolsAny) != 1 {
		t.Fatalf("tools count: got %d want 1 (no web_search)", len(toolsAny))
	}
}

func TestAdapter_Complete_WebSearch_ParsesWebSearchCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "resp_1",
  "model": "gpt-5.2",
  "output": [
    {
      "type": "web_search_call",
      "id": "ws_abc123",
      "status": "completed",
      "action": {"type": "search", "query": "Go generics tutorial"}
    },
    {
      "type": "message",
      "content": [{"type":"output_text", "text":"Here is what I found about Go generics."}]
    }
  ],
  "usage": {"input_tokens": 10, "output_tokens": 20, "total_tokens": 30}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := a.Complete(ctx, llm.Request{
		Model:     "gpt-5.2",
		Messages:  []llm.Message{llm.User("search for Go generics")},
		WebSearch: true,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if len(resp.Message.Content) != 2 {
		t.Fatalf("content parts: got %d want 2", len(resp.Message.Content))
	}

	ws := resp.Message.Content[0]
	if ws.Kind != llm.ContentWebSearch {
		t.Fatalf("part[0] kind: got %q want %q", ws.Kind, llm.ContentWebSearch)
	}
	if ws.WebSearch == nil {
		t.Fatalf("part[0] web_search is nil")
	}
	if ws.WebSearch.Query != "Go generics tutorial" {
		t.Fatalf("query: got %q", ws.WebSearch.Query)
	}
	if len(ws.WebSearch.Raw) == 0 {
		t.Fatalf("raw is empty")
	}

	txt := resp.Message.Content[1]
	if txt.Kind != llm.ContentText {
		t.Fatalf("part[1] kind: got %q want %q", txt.Kind, llm.ContentText)
	}
	if !strings.Contains(txt.Text, "Go generics") {
		t.Fatalf("text: %q", txt.Text)
	}

	if resp.Finish.Reason != "stop" {
		t.Fatalf("finish: got %q want stop", resp.Finish.Reason)
	}
}

func TestToResponsesInput_WebSearch_ReplayedAsItem(t *testing.T) {
	raw := json.RawMessage(`{"type":"web_search_call","id":"ws_abc","status":"completed","action":{"type":"search","query":"test"}}`)
	msgs := []llm.Message{
		llm.User("search something"),
		{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{Query: "test", Raw: raw}},
				{Kind: llm.ContentText, Text: "Here are the results."},
			},
		},
		llm.User("thanks"),
	}

	_, items, err := toResponsesInput(msgs, "gpt-5.4")
	if err != nil {
		t.Fatalf("toResponsesInput: %v", err)
	}

	found := false
	for _, itemAny := range items {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		if item["type"] == "web_search_call" {
			found = true
			if item["id"] != "ws_abc" {
				t.Fatalf("web_search_call id: %v", item["id"])
			}
			break
		}
	}
	if !found {
		t.Fatalf("web_search_call item not found in input items: %v", items)
	}
}

func TestAdapter_Integration_PhaseAnnotation(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set")
	}
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	a, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := a.Complete(ctx, llm.Request{
		Model:    "gpt-5.3-codex",
		Messages: []llm.Message{llm.User("What is 2+2? Answer in one word.")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// gpt-5.3-codex should emit phase annotations on message items.
	var phases []string
	for _, p := range resp.Message.Content {
		if p.Kind == llm.ContentText {
			phases = append(phases, p.Phase)
			t.Logf("text part: phase=%q text=%.60s", p.Phase, p.Text)
		}
	}
	if len(phases) == 0 {
		t.Fatalf("expected at least one text content part")
	}
	// Verify at least one part has a non-empty phase.
	hasPhase := false
	for _, ph := range phases {
		if ph != "" {
			hasPhase = true
		}
	}
	if !hasPhase {
		t.Fatalf("expected at least one phase annotation from gpt-5.3-codex; got phases=%v", phases)
	}

	// Verify phase survives round-trip serialization.
	nextMsgs := []llm.Message{
		llm.User("initial"),
		resp.Message,
		llm.User("follow-up"),
	}
	_, items, err := toResponsesInput(nextMsgs, "gpt-5.4")
	if err != nil {
		t.Fatalf("toResponsesInput: %v", err)
	}
	var replayedPhases []string
	for _, itemAny := range items {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		if item["role"] == "assistant" && item["type"] == "message" {
			if ph, ok := item["phase"].(string); ok {
				replayedPhases = append(replayedPhases, ph)
			}
		}
	}
	t.Logf("replayed phases: %v", replayedPhases)
	if len(replayedPhases) == 0 {
		t.Fatalf("phase not replayed in toResponsesInput; items=%v", items)
	}
}

func TestAdapter_Integration_WebSearch(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set")
	}

	a, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	resp, err := a.Complete(ctx, llm.Request{
		Model:    "gpt-5-mini-2025-08-07",
		Messages: []llm.Message{llm.User("Search the web and tell me: what is the current population of Tokyo?")},
		Tools: []llm.ToolDefinition{{
			Name:        "shell",
			Description: "run a shell command",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}},
		}},
		WebSearch: true,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if strings.TrimSpace(resp.Text()) == "" {
		t.Fatalf("expected non-empty text response")
	}

	// Verify we got web search content parts back.
	var wsCount int
	for _, p := range resp.Message.Content {
		if p.Kind == llm.ContentWebSearch {
			wsCount++
			if p.WebSearch == nil {
				t.Fatalf("ContentWebSearch part has nil WebSearch data")
			}
			t.Logf("web_search query=%q raw_len=%d", p.WebSearch.Query, len(p.WebSearch.Raw))
		}
	}
	if wsCount == 0 {
		t.Fatalf("expected at least one ContentWebSearch part in response; got content kinds: %v", contentKinds(resp.Message.Content))
	}
	t.Logf("response text (truncated): %.200s", resp.Text())
}

func TestStream_IncludesWebSearchTool(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: response.completed\n")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"r1","model":"gpt-5.2","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`+"\n\n")
		if f != nil {
			f.Flush()
		}
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{
		Model:     "gpt-5.2",
		Messages:  []llm.Message{llm.User("search the web")},
		WebSearch: true,
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close() //nolint:errcheck

	for range stream.Events() {
	}

	tools, ok := gotBody["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("expected tools with web_search, got: %v", gotBody["tools"])
	}
	found := false
	for _, tool := range tools {
		tm, _ := tool.(map[string]any)
		if tm["type"] == "web_search" {
			found = true
		}
	}
	if !found {
		t.Fatalf("web_search tool not found in tools: %v", tools)
	}
}

func TestComplete_PopulatesRateLimitInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-ratelimit-remaining-requests", "99")
		w.Header().Set("x-ratelimit-limit-requests", "100")
		w.Header().Set("x-ratelimit-remaining-tokens", "9999")
		w.Header().Set("x-ratelimit-limit-tokens", "10000")
		w.Header().Set("x-ratelimit-reset-requests", "2026-02-10T12:00:00Z")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"r1","model":"gpt-5.2","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	resp, err := a.Complete(context.Background(), llm.Request{
		Model:    "gpt-5.2",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.RateLimit == nil {
		t.Fatal("RateLimit is nil")
	}
	if resp.RateLimit.RequestsRemaining == nil || *resp.RateLimit.RequestsRemaining != 99 {
		t.Fatalf("RequestsRemaining = %v", resp.RateLimit.RequestsRemaining)
	}
	if resp.RateLimit.TokensLimit == nil || *resp.RateLimit.TokensLimit != 10000 {
		t.Fatalf("TokensLimit = %v", resp.RateLimit.TokensLimit)
	}
}

func TestNewFromEnv_ReadsOrgAndProjectID(t *testing.T) {
	// Isolate from any stored OAuth / OpenAI env vars on the dev machine so
	// the test's explicit OPENAI_* values win deterministically.
	oaitest.IsolateOpenAIAuth(t)
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("OPENAI_ORG_ID", "org-123")
	t.Setenv("OPENAI_PROJECT_ID", "proj-456")

	a, err := NewFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if a.OrgID != "org-123" {
		t.Fatalf("OrgID = %q", a.OrgID)
	}
	if a.ProjectID != "proj-456" {
		t.Fatalf("ProjectID = %q", a.ProjectID)
	}
}

// TestNewFromEnv_PrefersStoredOAuthOverAPIKey verifies the new priority order:
// when both OPENAI_API_KEY and a stored OAuth record are present, the adapter
// uses the OAuth path (ChatGPT/Codex backend), not the env API key.
func TestNewFromEnv_PrefersStoredOAuthOverAPIKey(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	t.Setenv("OPENAI_API_KEY", "sk-env-should-be-ignored")
	t.Setenv("OPENAI_CHATGPT_BASE_URL", "https://chatgpt.example.test")
	userStateDir := authopenai.DefaultStateDir()
	if err := authopenai.SaveAuth(userStateDir, authopenai.AuthRecord{
		Version:      1,
		Provider:     "openai",
		Source:       authopenai.AuthSourceOAuth,
		ObtainedAt:   time.Now().Add(-time.Minute).UTC(),
		TokenType:    "Bearer",
		Scope:        "openid profile email offline_access",
		AccessToken:  "oauth-token",
		RefreshToken: "refresh-token",
		Expiry:       time.Now().Add(time.Hour).UTC(),
		AccountID:    "acct_oauth",
	}); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}

	a, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	if a.APIKey != "oauth-token" {
		t.Fatalf("APIKey = %q, want OAuth bearer token", a.APIKey)
	}
	if a.ResponsesPath != "/backend-api/codex/responses" {
		t.Fatalf("ResponsesPath = %q, want codex responses path", a.ResponsesPath)
	}
	if a.ChatGPTAccountID != "acct_oauth" {
		t.Fatalf("ChatGPTAccountID = %q, want %q", a.ChatGPTAccountID, "acct_oauth")
	}
}

func TestNewFromEnv_UsesStoredOAuthTransportWhenAPIKeyAbsent(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	xdgStateHome := os.Getenv("XDG_STATE_HOME")
	userStateDir := authopenai.DefaultStateDir()
	projectStateDir := filepath.Join(xdgStateHome, "serf", "projects", "repo")
	t.Setenv("SERF_STATE_DIR", projectStateDir)
	if err := authopenai.SaveAuth(userStateDir, authopenai.AuthRecord{
		Version:      1,
		Provider:     "openai",
		Source:       "oauth",
		ObtainedAt:   time.Now().Add(-time.Minute).UTC(),
		TokenType:    "Bearer",
		Scope:        "openid profile email offline_access",
		AccessToken:  "oauth-token",
		RefreshToken: "refresh-token",
		Expiry:       time.Now().Add(time.Hour).UTC(),
		AccountID:    "acct_123",
		WorkspaceID:  "ws_123",
	}); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}

	t.Setenv("OPENAI_CHATGPT_BASE_URL", "https://chatgpt.example.test")
	a, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	if a.APIKey != "oauth-token" {
		t.Fatalf("APIKey = %q", a.APIKey)
	}
	if a.BaseURL != "https://chatgpt.example.test" {
		t.Fatalf("BaseURL = %q", a.BaseURL)
	}
	if a.ResponsesPath != "/backend-api/codex/responses" {
		t.Fatalf("ResponsesPath = %q", a.ResponsesPath)
	}
	if a.ChatGPTAccountID != "acct_123" {
		t.Fatalf("ChatGPTAccountID = %q", a.ChatGPTAccountID)
	}
}

func TestEnvFactoryUsesUserScopedOAuthWithProjectStateDir(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	userStateDir := authopenai.DefaultStateDir()
	projectStateDir := filepath.Join(t.TempDir(), "project-state")
	if err := authopenai.SaveAuth(userStateDir, authopenai.AuthRecord{
		Version:      1,
		Provider:     "openai",
		Source:       authopenai.AuthSourceOAuth,
		ObtainedAt:   time.Now().Add(-time.Minute).UTC(),
		TokenType:    "Bearer",
		Scope:        "openid profile email offline_access",
		AccessToken:  "oauth-token",
		RefreshToken: "refresh-token",
		Expiry:       time.Now().Add(time.Hour).UTC(),
		AccountID:    "acct_user_state",
	}); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}

	c, err := llm.NewFromEnv(llm.WithStateDir(projectStateDir))
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	if c.DefaultProvider() != "openai" {
		t.Fatalf("DefaultProvider = %q, want openai", c.DefaultProvider())
	}
}

// TestStream_EmptyResponsesStream_FallsBackToChatCompletions verifies that when
// the Responses API returns 200 OK but closes the stream with zero events (the
// silent failure mode for models that don't support /v1/responses), the adapter
// automatically falls back to /v1/chat/completions and returns the response.
func TestStream_EmptyResponsesStream_FallsBackToChatCompletions(t *testing.T) {
	// Responses endpoint: returns 200 then closes immediately with no SSE events.
	// Chat completions endpoint: returns a proper streaming response.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			// 200 OK but empty body (simulates silent failure for unsupported models).
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			// Write nothing — stream closes immediately.
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			// Proper streaming response.
			chunks := []string{
				`data: {"id":"cc1","model":"gpt-4.1-mini","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}`,
				`data: {"id":"cc1","model":"gpt-4.1-mini","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}`,
				`data: {"id":"cc1","model":"gpt-4.1-mini","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
				`data: [DONE]`,
			}
			for _, c := range chunks {
				_, _ = fmt.Fprintln(w, c)
				_, _ = fmt.Fprintln(w)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "test-key", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{
		Model:    "gpt-4.1-mini",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()

	var gotText strings.Builder
	var gotFinish bool
	for ev := range stream.Events() {
		switch ev.Type {
		case llm.StreamEventError:
			t.Fatalf("unexpected error event: %v", ev.Err)
		case llm.StreamEventTextDelta:
			gotText.WriteString(ev.Delta)
		case llm.StreamEventFinish:
			gotFinish = true
		}
	}

	if !gotFinish {
		t.Fatal("expected finish event, got none")
	}
	if gotText.String() != "Hello world" {
		t.Fatalf("text: got %q want %q", gotText.String(), "Hello world")
	}
}

// TestStream_SuccessfulResponsesStream_NoFallback verifies that when the
// Responses API works correctly, the fallback path is never triggered.
func TestStream_SuccessfulResponsesStream_NoFallback(t *testing.T) {
	chatCompletionsCalled := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			events := []string{
				`event: response.output_text.delta` + "\ndata: " + `{"type":"response.output_text.delta","delta":"Hi there"}`,
				`event: response.completed` + "\ndata: " + `{"type":"response.completed","response":{"id":"r1","model":"gpt-5","output":[{"type":"message","content":[{"type":"output_text","text":"Hi there"}]}],"status":"completed","usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`,
			}
			for _, e := range events {
				_, _ = fmt.Fprintln(w, e)
				_, _ = fmt.Fprintln(w)
			}
		case "/v1/chat/completions":
			chatCompletionsCalled = true
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "test-key", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{
		Model:    "gpt-5",
		Messages: []llm.Message{llm.User("hello")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()

	var gotFinish bool
	var gotText strings.Builder
	for ev := range stream.Events() {
		if ev.Type == llm.StreamEventError {
			t.Fatalf("unexpected error: %v", ev.Err)
		}
		if ev.Type == llm.StreamEventTextDelta {
			gotText.WriteString(ev.Delta)
		}
		if ev.Type == llm.StreamEventFinish {
			gotFinish = true
		}
	}

	if !gotFinish {
		t.Fatal("expected finish event")
	}
	if chatCompletionsCalled {
		t.Fatal("chat completions should not be called when Responses API succeeds")
	}
}

// TestStream_BothEndpointsFail_ClearCombinedError verifies that when both the
// Responses API (empty stream) and Chat Completions fail, the error message
// names the model and both endpoints — never silent.
func TestStream_BothEndpointsFail_ClearCombinedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			// Empty stream — silent failure.
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
		case "/v1/chat/completions":
			// Chat completions also fails.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"error":{"message":"model not supported on this endpoint","code":"unsupported_model","type":"invalid_request_error"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "test-key", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{
		Model:    "gpt-4.1-mini",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()

	var gotErr error
	for ev := range stream.Events() {
		if ev.Type == llm.StreamEventError {
			gotErr = ev.Err
		}
	}

	if gotErr == nil {
		t.Fatal("expected combined error, got nil")
	}
	errMsg := gotErr.Error()
	if !strings.Contains(errMsg, "gpt-4.1-mini") {
		t.Errorf("error should mention model name, got: %s", errMsg)
	}
	if !strings.Contains(errMsg, "/v1/responses") {
		t.Errorf("error should mention /v1/responses, got: %s", errMsg)
	}
	if !strings.Contains(errMsg, "/v1/chat/completions") {
		t.Errorf("error should mention /v1/chat/completions, got: %s", errMsg)
	}
}

// TestStream_ResponsesAPI_404_FallsBackToChatCompletions verifies that a 404
// from the Responses API (model-not-found on this endpoint) triggers fallback.
func TestStream_ResponsesAPI_404_FallsBackToChatCompletions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"This model is only supported in v1/responses and not in v1/chat/completions","code":"model_not_found","type":"invalid_request_error"}}`))
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			chunks := []string{
				`data: {"id":"cc2","model":"gpt-4.1-mini","choices":[{"index":0,"delta":{"content":"fallback response"},"finish_reason":null}]}`,
				`data: {"id":"cc2","model":"gpt-4.1-mini","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
				`data: [DONE]`,
			}
			for _, c := range chunks {
				_, _ = fmt.Fprintln(w, c)
				_, _ = fmt.Fprintln(w)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "test-key", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{
		Model:    "gpt-4.1-mini",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()

	var gotText strings.Builder
	var gotFinish bool
	for ev := range stream.Events() {
		if ev.Type == llm.StreamEventError {
			t.Fatalf("unexpected error: %v", ev.Err)
		}
		if ev.Type == llm.StreamEventTextDelta {
			gotText.WriteString(ev.Delta)
		}
		if ev.Type == llm.StreamEventFinish {
			gotFinish = true
		}
	}

	if !gotFinish {
		t.Fatal("expected finish event from fallback")
	}
	if gotText.String() != "fallback response" {
		t.Fatalf("text from fallback: got %q want %q", gotText.String(), "fallback response")
	}
}

func TestStream_ResponsesAPI_404_FallbackPreservesChatRequestSemantics(t *testing.T) {
	var chatBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"model not found","code":"model_not_found","type":"invalid_request_error"}}`))
		case "/v1/chat/completions":
			if err := json.NewDecoder(r.Body).Decode(&chatBody); err != nil {
				t.Fatalf("decode chat body: %v", err)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintln(w, `data: {"id":"cc","model":"gpt-4.1-mini","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`)
			_, _ = fmt.Fprintln(w)
			_, _ = fmt.Fprintln(w, `data: {"id":"cc","model":"gpt-4.1-mini","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
			_, _ = fmt.Fprintln(w)
			_, _ = fmt.Fprintln(w, `data: [DONE]`)
			_, _ = fmt.Fprintln(w)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "test-key", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	localImage := filepath.Join(t.TempDir(), "local.png")
	if err := os.WriteFile(localImage, []byte("png bytes"), 0o600); err != nil {
		t.Fatalf("write local image: %v", err)
	}

	stream, err := a.Stream(ctx, llm.Request{
		Model: "gpt-4.1-mini",
		Messages: []llm.Message{{
			Role: llm.RoleUser,
			Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "describe"},
				{Kind: llm.ContentImage, Image: &llm.ImageData{URL: localImage}},
				{Kind: llm.ContentDocument, Document: &llm.DocumentData{
					Data:     []byte("%PDF"),
					FileName: "brief.pdf",
				}},
			},
		}},
		Metadata:  map[string]string{"trace": "abc"},
		WebSearch: true,
		ResponseFormat: &llm.ResponseFormat{
			Type:       "json_schema",
			JSONSchema: map[string]any{"type": "object"},
			Strict:     true,
		},
		ProviderOptions: map[string]any{"openai": map[string]any{"parallel_tool_calls": false}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close() //nolint:errcheck
	for ev := range stream.Events() {
		if ev.Type == llm.StreamEventError {
			t.Fatalf("unexpected stream error: %v", ev.Err)
		}
	}
	if chatBody == nil {
		t.Fatal("chat fallback was not called")
	}
	if chatBody["metadata"].(map[string]any)["trace"] != "abc" {
		t.Fatalf("metadata not preserved: %#v", chatBody["metadata"])
	}
	if got, ok := chatBody["parallel_tool_calls"].(bool); !ok || got {
		t.Fatalf("provider option parallel_tool_calls = %v, want false", got)
	}
	rf := chatBody["response_format"].(map[string]any)
	if rf["type"] != "json_schema" {
		t.Fatalf("response_format=%#v", rf)
	}
	jsonSchema := rf["json_schema"].(map[string]any)
	if jsonSchema["strict"] != true {
		t.Fatalf("response_format json_schema=%#v, want strict", jsonSchema)
	}
	tools := chatBody["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["type"] != "web_search" {
		t.Fatalf("tools=%#v, want web_search", tools)
	}
	messages := chatBody["messages"].([]any)
	content := messages[0].(map[string]any)["content"].([]any)
	if len(content) != 3 || content[1].(map[string]any)["type"] != "image_url" {
		t.Fatalf("user multimodal content=%#v", content)
	}
	imageURL := content[1].(map[string]any)["image_url"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(imageURL, "data:image/png;base64,") {
		t.Fatalf("image_url=%q, want local image data URI", imageURL)
	}
	file := content[2].(map[string]any)
	if file["type"] != "file" || file["file"].(map[string]any)["filename"] != "brief.pdf" {
		t.Fatalf("document content=%#v", file)
	}
}

func TestAdapter_Complete_OAuthTransportUsesCodexEndpointAndAccountHeader(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotAccount string
	var gotStream bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccount = r.Header.Get("ChatGPT-Account-ID")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotStream, _ = body["stream"].(bool)
		if !gotStream {
			w.WriteHeader(http.StatusBadRequest)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"detail":"Stream must be set to true"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.output_text.delta\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
		_, _ = io.WriteString(w, "event: response.completed\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"model\":\"gpt-5.2\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{
		APIKey:           "oauth-token",
		BaseURL:          srv.URL,
		ResponsesPath:    "/backend-api/codex/responses",
		ChatGPTAccountID: "acct_123",
		Client:           srv.Client(),
	}
	_, err := a.Complete(context.Background(), llm.Request{
		Model:    "gpt-5.2",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotPath != "/backend-api/codex/responses" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer oauth-token" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotAccount != "acct_123" {
		t.Fatalf("ChatGPT-Account-ID = %q", gotAccount)
	}
	if !gotStream {
		t.Fatal("stream = false, want true for OAuth transport")
	}
}

func TestAdapter_Complete_OAuthTransportOmitsMaxOutputTokens(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/codex/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.output_text.delta\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
		_, _ = io.WriteString(w, "event: response.completed\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"model\":\"gpt-5.5\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{
		APIKey:           "oauth-token",
		BaseURL:          srv.URL,
		ResponsesPath:    "/backend-api/codex/responses",
		ChatGPTAccountID: "acct_123",
		Client:           srv.Client(),
	}
	maxTokens := 80
	_, err := a.Complete(context.Background(), llm.Request{
		Model:     "gpt-5.5",
		Messages:  []llm.Message{llm.User("hi")},
		MaxTokens: &maxTokens,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, ok := gotBody["max_output_tokens"]; ok {
		t.Fatalf("max_output_tokens should be omitted for Codex backend: %#v", gotBody)
	}
	if gotBody["stream"] != true {
		t.Fatalf("stream = %#v, want true", gotBody["stream"])
	}
}

func TestAdapter_Complete_OAuthTransportPreservesStreamedToolCallsWhenCompletedOutputIsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.output_item.added\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"status\":\"in_progress\",\"arguments\":\"\",\"call_id\":\"call_1\",\"name\":\"task_list\"}}\n\n")
		_, _ = io.WriteString(w, "event: response.function_call_arguments.delta\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{\\\"action\\\":\\\"update\\\"}\",\"item_id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"task_list\"}\n\n")
		_, _ = io.WriteString(w, "event: response.output_item.done\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"status\":\"completed\",\"arguments\":\"{\\\"action\\\":\\\"update\\\"}\",\"call_id\":\"call_1\",\"name\":\"task_list\"}}\n\n")
		_, _ = io.WriteString(w, "event: response.completed\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"model\":\"gpt-5.5\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{
		APIKey:           "oauth-token",
		BaseURL:          srv.URL,
		ResponsesPath:    "/backend-api/codex/responses",
		ChatGPTAccountID: "acct_123",
		Client:           srv.Client(),
	}
	resp, err := a.Complete(context.Background(), llm.Request{
		Model:    "gpt-5.5",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(resp.ToolCalls()) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.ToolCalls()))
	}
	if resp.ToolCalls()[0].Name != "task_list" {
		t.Fatalf("tool name = %q, want %q", resp.ToolCalls()[0].Name, "task_list")
	}
}

func TestAdapter_Complete_OAuthTransportTracksItemIDAndFragmentedToolArguments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.output_item.added\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"status\":\"in_progress\",\"arguments\":\"\",\"call_id\":\"call_1\",\"name\":\"task_list\"}}\n\n")
		_, _ = io.WriteString(w, "event: response.function_call_arguments.delta\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{\\\"\",\"item_id\":\"fc_1\"}\n\n")
		_, _ = io.WriteString(w, "event: response.function_call_arguments.delta\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"action\",\"item_id\":\"fc_1\"}\n\n")
		_, _ = io.WriteString(w, "event: response.function_call_arguments.delta\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"\\\":\\\"update\\\"}\",\"item_id\":\"fc_1\"}\n\n")
		_, _ = io.WriteString(w, "event: response.function_call_arguments.done\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.function_call_arguments.done\",\"arguments\":\"{\\\"action\\\":\\\"update\\\"}\",\"item_id\":\"fc_1\"}\n\n")
		_, _ = io.WriteString(w, "event: response.output_item.done\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"status\":\"completed\",\"arguments\":\"{\\\"action\\\":\\\"update\\\"}\",\"call_id\":\"call_1\",\"name\":\"task_list\"}}\n\n")
		_, _ = io.WriteString(w, "event: response.completed\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"model\":\"gpt-5.5\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{
		APIKey:           "oauth-token",
		BaseURL:          srv.URL,
		ResponsesPath:    "/backend-api/codex/responses",
		ChatGPTAccountID: "acct_123",
		Client:           srv.Client(),
	}
	resp, err := a.Complete(context.Background(), llm.Request{
		Model:    "gpt-5.5",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(resp.ToolCalls()) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.ToolCalls()))
	}
	if resp.ToolCalls()[0].ID != "call_1" {
		t.Fatalf("tool id = %q, want %q", resp.ToolCalls()[0].ID, "call_1")
	}
	if resp.ToolCalls()[0].Name != "task_list" {
		t.Fatalf("tool name = %q, want %q", resp.ToolCalls()[0].Name, "task_list")
	}
	if got := string(resp.ToolCalls()[0].Arguments); got != `{"action":"update"}` {
		t.Fatalf("tool args = %q, want %q", got, `{"action":"update"}`)
	}
}

func TestComplete_SendsOrgAndProjectHeaders(t *testing.T) {
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"r1","model":"gpt-5.2","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client(), OrgID: "org-123", ProjectID: "proj-456"}
	_, err := a.Complete(context.Background(), llm.Request{
		Model:    "gpt-5.2",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotHeaders.Get("OpenAI-Organization") != "org-123" {
		t.Fatalf("OpenAI-Organization header = %q", gotHeaders.Get("OpenAI-Organization"))
	}
	if gotHeaders.Get("OpenAI-Project") != "proj-456" {
		t.Fatalf("OpenAI-Project header = %q", gotHeaders.Get("OpenAI-Project"))
	}
}

func TestFromResponses_IncompleteStatus(t *testing.T) {
	raw := map[string]any{
		"id":                 "r1",
		"model":              "gpt-5.2",
		"status":             "incomplete",
		"incomplete_details": map[string]any{"reason": "max_output_tokens"},
		"output": []any{
			map[string]any{"type": "message", "content": []any{
				map[string]any{"type": "output_text", "text": "partial"},
			}},
		},
		"usage": map[string]any{"input_tokens": float64(10), "output_tokens": float64(100), "total_tokens": float64(110)},
	}
	r := fromResponses(raw, "gpt-5.2")
	if r.Finish.Reason != "length" {
		t.Fatalf("Finish.Reason = %q, want %q", r.Finish.Reason, "length")
	}
}

func TestComplete_PassesStopSequences(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"r1","model":"gpt-5.2","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	_, err := a.Complete(context.Background(), llm.Request{
		Model:         "gpt-5.2",
		Messages:      []llm.Message{llm.User("hi")},
		StopSequences: []string{"STOP", "END"},
	})
	if err != nil {
		t.Fatal(err)
	}
	stop, ok := gotBody["stop"].([]any)
	if !ok || len(stop) != 2 {
		t.Fatalf("stop = %v", gotBody["stop"])
	}
}

func TestStream_PassesStopSequences(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: response.completed\n")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"r1","model":"gpt-5.2","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`+"\n\n")
		if f != nil {
			f.Flush()
		}
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{
		Model:         "gpt-5.2",
		Messages:      []llm.Message{llm.User("hi")},
		StopSequences: []string{"STOP", "END"},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close() //nolint:errcheck
	for range stream.Events() {
	}

	stop, ok := gotBody["stop"].([]any)
	if !ok || len(stop) != 2 {
		t.Fatalf("stop = %v", gotBody["stop"])
	}
}

func TestComplete_WrapsContextCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := a.Complete(ctx, llm.Request{
		Model:    "gpt-5.2",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var abortErr *llm.AbortError
	if !errors.As(err, &abortErr) {
		t.Fatalf("expected AbortError, got %T: %v", err, err)
	}
}

func TestComplete_ProviderOptions_PassedThrough(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"r1","model":"gpt-5.2","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	_, err := a.Complete(context.Background(), llm.Request{
		Model:    "gpt-5.2",
		Messages: []llm.Message{llm.User("hi")},
		ProviderOptions: map[string]any{
			"openai": map[string]any{
				"custom_field": "custom_value",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["custom_field"] != "custom_value" {
		t.Fatalf("custom_field not passed through: %v", gotBody)
	}
}

func TestParseUsage_ReasoningTokensDistinctFromOutputTokens(t *testing.T) {
	// Test via a full response to verify parseUsage behavior indirectly.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "r1",
  "model": "gpt-5.2",
  "output": [{"type": "message", "content": [{"type": "output_text", "text": "hi"}]}],
  "usage": {
    "input_tokens": 100,
    "output_tokens": 50,
    "total_tokens": 150,
    "output_tokens_details": {"reasoning_tokens": 30}
  }
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	resp, err := a.Complete(context.Background(), llm.Request{
		Model:    "gpt-5.2",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.OutputTokens != 50 {
		t.Fatalf("OutputTokens = %d, want 50", resp.Usage.OutputTokens)
	}
	if resp.Usage.ReasoningTokens == nil || *resp.Usage.ReasoningTokens != 30 {
		t.Fatalf("ReasoningTokens = %v, want 30", resp.Usage.ReasoningTokens)
	}
	// Reasoning tokens must NOT inflate OutputTokens
	if resp.Usage.OutputTokens == 50+30 {
		t.Fatal("OutputTokens includes reasoning tokens — they should be distinct")
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
		Model:    "gpt-5.2",
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
		_, _ = io.WriteString(w, "event: response.completed\n")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"r1","model":"gpt-5.2","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`+"\n\n")
		if f != nil {
			f.Flush()
		}
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "test", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{
		Model:    "gpt-5.2",
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
		_, _ = w.Write([]byte(`{"id":"r1","model":"gpt-5.2","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
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
		Model:    "gpt-5.2",
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
		_, _ = io.WriteString(w, "event: response.completed\n")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"r1","model":"gpt-5.2","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`+"\n\n")
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
		Model:    "gpt-5.2",
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

func TestDefaultHeaders_CannotOverrideProviderHeaders(t *testing.T) {
	var capturedHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"r1","model":"gpt-5.2","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
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
		Model:    "gpt-5.2",
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

func TestComplete_ToolResultIsError_SentToAPI(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "resp_1",
  "model": "gpt-5.2",
  "output": [{"type": "message", "content": [{"type":"output_text", "text":"ok"}]}],
  "usage": {"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{
		Model: "gpt-5.2",
		Messages: []llm.Message{
			llm.User("call tool"),
			{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
				Kind:     llm.ContentToolCall,
				ToolCall: &llm.ToolCallData{ID: "call_1", Name: "failing_tool", Arguments: json.RawMessage(`{}`)},
			}}},
			llm.ToolResult("call_1", "connection refused", true),
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Find the function_call_output item in the sent request body.
	input, ok := gotBody["input"].([]any)
	if !ok {
		t.Fatalf("input not array: %#v", gotBody["input"])
	}
	var found bool
	for _, item := range input {
		m, _ := item.(map[string]any)
		if m["type"] == "function_call_output" {
			if _, present := m["is_error"]; present {
				t.Fatalf("OpenAI Responses input must not include is_error on function_call_output items (API rejects unknown params)")
			}
			outStr, ok := m["output"].(string)
			if !ok {
				t.Fatalf("function_call_output.output not string: %#v", m["output"])
			}
			var wrapped map[string]any
			if err := json.Unmarshal([]byte(outStr), &wrapped); err != nil {
				t.Fatalf("expected wrapped JSON output for error tool result, got %q: %v", outStr, err)
			}
			if wrapped["is_error"] != true {
				t.Fatalf("wrapped output is_error=%v, want true; wrapped=%#v", wrapped["is_error"], wrapped)
			}
			if wrapped["content"] != "connection refused" {
				t.Fatalf("wrapped output content=%v, want %q; wrapped=%#v", wrapped["content"], "connection refused", wrapped)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("must find a function_call_output item in input: %#v", input)
	}
}

func TestComplete_ToolResultIsError_OmittedWhenFalse(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "resp_1",
  "model": "gpt-5.2",
  "output": [{"type": "message", "content": [{"type":"output_text", "text":"ok"}]}],
  "usage": {"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{
		Model: "gpt-5.2",
		Messages: []llm.Message{
			llm.User("call tool"),
			{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
				Kind:     llm.ContentToolCall,
				ToolCall: &llm.ToolCallData{ID: "call_1", Name: "good_tool", Arguments: json.RawMessage(`{}`)},
			}}},
			llm.ToolResult("call_1", "success", false),
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	input, ok := gotBody["input"].([]any)
	if !ok {
		t.Fatalf("input not array: %#v", gotBody["input"])
	}
	for _, item := range input {
		m, _ := item.(map[string]any)
		if m["type"] == "function_call_output" {
			if _, present := m["is_error"]; present {
				t.Fatalf("OpenAI Responses input must not include is_error on function_call_output items (API rejects unknown params)")
			}
			if got := m["output"]; got != "success" {
				t.Fatalf("function_call_output.output=%#v, want %q", got, "success")
			}
		}
	}
}

func TestToResponsesInput_ToolResultWithImage(t *testing.T) {
	imgBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a} // PNG header
	msgs := []llm.Message{
		llm.User("read this file"),
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
			Kind:     llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{ID: "call_img", Name: "read_file", Arguments: json.RawMessage(`{"path":"img.png"}`)},
		}}},
		{Role: llm.RoleTool, ToolCallID: "call_img", Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID:     "call_img",
				Content:        "image content below",
				ImageData:      imgBytes,
				ImageMediaType: "image/png",
			},
		}}},
	}

	_, items, err := toResponsesInput(msgs, "gpt-5.4")
	if err != nil {
		t.Fatalf("toResponsesInput: %v", err)
	}

	// Find the function_call_output and input_image items.
	var foundOutput, foundImage bool
	var imageURL string
	for _, itemAny := range items {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		switch item["type"] {
		case "function_call_output":
			if item["call_id"] == "call_img" {
				foundOutput = true
			}
		case "input_image":
			foundImage = true
			imageURL, _ = item["image_url"].(string)
		}
	}

	if !foundOutput {
		t.Fatalf("expected function_call_output item with call_id=call_img; items=%v", items)
	}
	if !foundImage {
		t.Fatalf("expected input_image item after function_call_output; items=%v", items)
	}

	// Verify the data URI format.
	wantPrefix := "data:image/png;base64,"
	if !strings.HasPrefix(imageURL, wantPrefix) {
		t.Fatalf("image_url should start with %q, got %q", wantPrefix, imageURL)
	}

	// Verify the data URI matches what DataURI would produce.
	wantURI := llm.DataURI("image/png", imgBytes)
	if imageURL != wantURI {
		t.Fatalf("image_url = %q, want %q", imageURL, wantURI)
	}
}

func TestBuildChatCompletionsBodyRejectsToolResultImage(t *testing.T) {
	req := llm.Request{
		Model: "gpt-4.1-mini",
		Messages: []llm.Message{{
			Role: llm.RoleTool,
			Content: []llm.ContentPart{{
				Kind: llm.ContentToolResult,
				ToolResult: &llm.ToolResultData{
					ToolCallID:     "call_img",
					Content:        "screenshot",
					ImageData:      []byte{0x89, 0x50, 0x4e, 0x47},
					ImageMediaType: "image/png",
				},
			}},
		}},
	}
	_, err := buildChatCompletionsBody(req, true)
	if err == nil {
		t.Fatal("buildChatCompletionsBody accepted tool-result image")
	}
	if !strings.Contains(err.Error(), "tool-result images") {
		t.Fatalf("error=%v, want tool-result image explanation", err)
	}
}

func TestToResponsesInput_ToolResultWithImage_DefaultMediaType(t *testing.T) {
	imgBytes := []byte{0x89, 0x50, 0x4e, 0x47}
	msgs := []llm.Message{
		llm.User("read this"),
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
			Kind:     llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{ID: "call_2", Name: "read_file", Arguments: json.RawMessage(`{}`)},
		}}},
		{Role: llm.RoleTool, ToolCallID: "call_2", Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID: "call_2",
				Content:    "image",
				ImageData:  imgBytes,
				// ImageMediaType intentionally omitted — should default to image/png.
			},
		}}},
	}

	_, items, err := toResponsesInput(msgs, "gpt-5.4")
	if err != nil {
		t.Fatalf("toResponsesInput: %v", err)
	}

	var imageURL string
	for _, itemAny := range items {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		if item["type"] == "input_image" {
			imageURL, _ = item["image_url"].(string)
		}
	}
	if imageURL == "" {
		t.Fatalf("expected input_image item; items=%v", items)
	}
	wantPrefix := "data:image/png;base64,"
	if !strings.HasPrefix(imageURL, wantPrefix) {
		t.Fatalf("image_url should default to image/png; got %q", imageURL)
	}
}

func TestToResponsesInput_ToolResultWithoutImage_NoInputImage(t *testing.T) {
	msgs := []llm.Message{
		llm.User("run this"),
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
			Kind:     llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{ID: "call_3", Name: "shell", Arguments: json.RawMessage(`{}`)},
		}}},
		llm.ToolResult("call_3", "done", false),
	}

	_, items, err := toResponsesInput(msgs, "gpt-5.4")
	if err != nil {
		t.Fatalf("toResponsesInput: %v", err)
	}

	for _, itemAny := range items {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		if item["type"] == "input_image" {
			t.Fatalf("should not emit input_image for tool result without image data; items=%v", items)
		}
	}
}

func TestComplete_UsageRaw_ContainsProviderData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "resp_1",
  "model": "gpt-5.2",
  "output": [{"type": "message", "content": [{"type":"output_text", "text":"hi"}]}],
  "usage": {
    "input_tokens": 10, "output_tokens": 5, "total_tokens": 15,
    "output_tokens_details": {"reasoning_tokens": 2}
  }
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	resp, err := a.Complete(context.Background(), llm.Request{
		Model:    "gpt-5.2",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(resp.Usage.Raw) == 0 {
		t.Fatal("Usage.Raw must contain provider data, got empty map")
	}
	if _, ok := resp.Usage.Raw["input_tokens"]; !ok {
		t.Fatalf("Usage.Raw missing input_tokens key; got %v", resp.Usage.Raw)
	}
	if _, ok := resp.Usage.Raw["output_tokens_details"]; !ok {
		t.Fatalf("Usage.Raw missing output_tokens_details key; got %v", resp.Usage.Raw)
	}
}

func TestComplete_NormalizesInputTokens_SubtractsCached(t *testing.T) {
	// OpenAI Responses API reports input_tokens as total-including-cached.
	// llm.Usage's invariant is that InputTokens means "new uncached input."
	// Assert the adapter subtracts cached_tokens before storing.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "resp_1",
  "model": "gpt-5.2",
  "output": [{"type": "message", "content": [{"type":"output_text", "text":"ok"}]}],
  "usage": {
    "input_tokens": 10000,
    "output_tokens": 500,
    "total_tokens": 10500,
    "input_tokens_details": {"cached_tokens": 7000}
  }
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	resp, err := a.Complete(context.Background(), llm.Request{Model: "gpt-5.2", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	// After normalization: InputTokens = 10000 - 7000 = 3000 (new uncached only).
	if resp.Usage.InputTokens != 3000 {
		t.Errorf("InputTokens: got %d, want 3000 (10000 - 7000 cached)", resp.Usage.InputTokens)
	}
	if resp.Usage.CacheReadTokens == nil || *resp.Usage.CacheReadTokens != 7000 {
		t.Errorf("CacheReadTokens: got %v, want 7000", resp.Usage.CacheReadTokens)
	}
	if resp.Usage.OutputTokens != 500 {
		t.Errorf("OutputTokens: got %d, want 500", resp.Usage.OutputTokens)
	}
}

func TestAdapter_ListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"object": "list",
			"data": [
				{"id": "gpt-4o", "object": "model", "owned_by": "openai"},
				{"id": "gpt-4o-mini", "object": "model", "owned_by": "openai"},
				{"id": "o3", "object": "model", "owned_by": "openai"},
				{"id": "text-embedding-3-small", "object": "model", "owned_by": "openai"},
				{"id": "dall-e-3", "object": "model", "owned_by": "openai"},
				{"id": "tts-1", "object": "model", "owned_by": "openai"},
				{"id": "gpt-4o-mini-tts", "object": "model", "owned_by": "openai"},
				{"id": "whisper-1", "object": "model", "owned_by": "openai"},
				{"id": "gpt-4o-audio-preview", "object": "model", "owned_by": "openai"},
				{"id": "gpt-4o-realtime-preview", "object": "model", "owned_by": "openai"},
				{"id": "gpt-4o-transcribe", "object": "model", "owned_by": "openai"},
				{"id": "gpt-image-1", "object": "model", "owned_by": "openai"},
				{"id": "omni-moderation-latest", "object": "model", "owned_by": "openai"},
				{"id": "sora-2", "object": "model", "owned_by": "openai"},
				{"id": "ft:gpt-4o:my-org:custom:id", "object": "model", "owned_by": "user"}
			]
		}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "test-key", BaseURL: srv.URL, Client: srv.Client()}
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	ids := make(map[string]bool)
	for _, m := range models {
		ids[m.ID] = true
	}

	// Chat models should be present.
	for _, want := range []string{"gpt-4o", "gpt-4o-mini", "o3", "ft:gpt-4o:my-org:custom:id"} {
		if !ids[want] {
			t.Errorf("missing expected chat model %q", want)
		}
	}

	// Non-chat models should be filtered out.
	filtered := []string{
		"text-embedding-3-small", "dall-e-3", "tts-1", "gpt-4o-mini-tts",
		"whisper-1", "gpt-4o-audio-preview", "gpt-4o-realtime-preview",
		"gpt-4o-transcribe", "gpt-image-1", "omni-moderation-latest", "sora-2",
	}
	for _, bad := range filtered {
		if ids[bad] {
			t.Errorf("should filter out %q", bad)
		}
	}
	for _, m := range models {
		if m.Provider != "openai" {
			t.Errorf("model %s: provider = %q, want openai", m.ID, m.Provider)
		}
	}
	for i := 1; i < len(models); i++ {
		if models[i].ID < models[i-1].ID {
			t.Errorf("models not sorted: %s before %s", models[i-1].ID, models[i].ID)
		}
	}
}

func TestAdapter_ListModels_OAuthTransportUsesCodexModelsEndpoint(t *testing.T) {
	var gotPath string
	var gotQuery string
	var gotAuth string
	var gotAccount string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		gotAccount = r.Header.Get("ChatGPT-Account-ID")
		if r.Method != http.MethodGet || r.URL.Path != "/backend-api/codex/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"models": [
				{"slug": "gpt-5.3-codex", "display_name": "GPT-5.3 Codex"},
				{"slug": "gpt-image-1", "display_name": "Image"}
			]
		}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{
		APIKey:           "oauth-token",
		BaseURL:          srv.URL,
		ResponsesPath:    defaultCodexResponses,
		ChatGPTAccountID: "acct_123",
		Client:           srv.Client(),
	}
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if gotPath != "/backend-api/codex/models" {
		t.Fatalf("path = %q, want /backend-api/codex/models", gotPath)
	}
	values, err := url.ParseQuery(gotQuery)
	if err != nil {
		t.Fatalf("parse query %q: %v", gotQuery, err)
	}
	if values.Get("client_version") != "0.0.0" {
		t.Fatalf("client_version = %q, want 0.0.0", values.Get("client_version"))
	}
	if gotAuth != "Bearer oauth-token" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotAccount != "acct_123" {
		t.Fatalf("ChatGPT-Account-ID = %q", gotAccount)
	}
	if len(models) != 1 || models[0].ID != "gpt-5.3-codex" || models[0].DisplayName != "GPT-5.3 Codex" || models[0].Provider != "openai" {
		t.Fatalf("models=%+v", models)
	}
}

func TestFromResponses_PreservesPhase(t *testing.T) {
	raw := map[string]any{
		"id":    "r1",
		"model": "gpt-5.3-codex",
		"output": []any{
			map[string]any{
				"type":  "message",
				"phase": "commentary",
				"content": []any{
					map[string]any{"type": "output_text", "text": "Let me think about this..."},
				},
			},
			map[string]any{
				"type":  "message",
				"phase": "final_answer",
				"content": []any{
					map[string]any{"type": "output_text", "text": "The answer is 42."},
				},
			},
		},
		"usage": map[string]any{"input_tokens": float64(10), "output_tokens": float64(20), "total_tokens": float64(30)},
	}

	r := fromResponses(raw, "gpt-5.3-codex")
	if len(r.Message.Content) != 2 {
		t.Fatalf("content parts: got %d want 2", len(r.Message.Content))
	}
	if r.Message.Content[0].Phase != "commentary" {
		t.Fatalf("part[0] phase: got %q want %q", r.Message.Content[0].Phase, "commentary")
	}
	if r.Message.Content[1].Phase != "final_answer" {
		t.Fatalf("part[1] phase: got %q want %q", r.Message.Content[1].Phase, "final_answer")
	}
}

func TestFromResponses_NullPhase(t *testing.T) {
	raw := map[string]any{
		"id":    "r1",
		"model": "gpt-5.2",
		"output": []any{
			map[string]any{
				"type": "message",
				"content": []any{
					map[string]any{"type": "output_text", "text": "Hello"},
				},
			},
		},
		"usage": map[string]any{"input_tokens": float64(1), "output_tokens": float64(2), "total_tokens": float64(3)},
	}

	r := fromResponses(raw, "gpt-5.2")
	if len(r.Message.Content) != 1 {
		t.Fatalf("content parts: got %d want 1", len(r.Message.Content))
	}
	if r.Message.Content[0].Phase != "" {
		t.Fatalf("phase: got %q want empty string", r.Message.Content[0].Phase)
	}
}

func TestToResponsesInput_PhaseReplayed(t *testing.T) {
	msgs := []llm.Message{
		llm.User("hello"),
		{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "Thinking...", Phase: "commentary"},
				{Kind: llm.ContentText, Text: "The answer.", Phase: "final_answer"},
			},
		},
	}

	_, items, err := toResponsesInput(msgs, "gpt-5.4")
	if err != nil {
		t.Fatalf("toResponsesInput: %v", err)
	}

	// Should produce: user message, then two separate assistant message items (one per phase).
	var assistantItems []map[string]any
	for _, itemAny := range items {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		if item["role"] == "assistant" && item["type"] == "message" {
			assistantItems = append(assistantItems, item)
		}
	}
	if len(assistantItems) != 2 {
		t.Fatalf("assistant message items: got %d want 2; items=%v", len(assistantItems), items)
	}
	if assistantItems[0]["phase"] != "commentary" {
		t.Fatalf("item[0] phase: got %v want %q", assistantItems[0]["phase"], "commentary")
	}
	if assistantItems[1]["phase"] != "final_answer" {
		t.Fatalf("item[1] phase: got %v want %q", assistantItems[1]["phase"], "final_answer")
	}
}

func TestToResponsesInput_EmptyPhase_SingleItem(t *testing.T) {
	msgs := []llm.Message{
		llm.User("hello"),
		{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "Part one."},
				{Kind: llm.ContentText, Text: "Part two."},
			},
		},
	}

	_, items, err := toResponsesInput(msgs, "gpt-5.4")
	if err != nil {
		t.Fatalf("toResponsesInput: %v", err)
	}

	// With empty phase, all text parts should be in a single message item (backward compat).
	var assistantItems []map[string]any
	for _, itemAny := range items {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		if item["role"] == "assistant" && item["type"] == "message" {
			assistantItems = append(assistantItems, item)
		}
	}
	if len(assistantItems) != 1 {
		t.Fatalf("assistant message items: got %d want 1 (single item for empty phase); items=%v", len(assistantItems), items)
	}
	// Should NOT have a phase key when empty.
	if _, hasPhase := assistantItems[0]["phase"]; hasPhase {
		t.Fatalf("empty-phase message should not have phase key; got %v", assistantItems[0]["phase"])
	}
	content, _ := assistantItems[0]["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content entries: got %d want 2", len(content))
	}
}

func TestPhaseRoundTrip(t *testing.T) {
	// Simulate API response with phase annotations.
	raw := map[string]any{
		"id":    "r1",
		"model": "gpt-5.3-codex",
		"output": []any{
			map[string]any{
				"type":  "message",
				"phase": "commentary",
				"content": []any{
					map[string]any{"type": "output_text", "text": "Reasoning here."},
				},
			},
			map[string]any{
				"type":  "message",
				"phase": "final_answer",
				"content": []any{
					map[string]any{"type": "output_text", "text": "Final result."},
				},
			},
		},
		"usage": map[string]any{"input_tokens": float64(10), "output_tokens": float64(20), "total_tokens": float64(30)},
	}

	// Parse response.
	r := fromResponses(raw, "gpt-5.3-codex")

	// Re-serialize the assistant message as input for next turn.
	nextMsgs := []llm.Message{
		llm.User("initial"),
		r.Message,
		llm.User("follow-up"),
	}
	_, items, err := toResponsesInput(nextMsgs, "gpt-5.4")
	if err != nil {
		t.Fatalf("toResponsesInput: %v", err)
	}

	// Find the two assistant message items.
	var phases []string
	for _, itemAny := range items {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		if item["role"] == "assistant" && item["type"] == "message" {
			phase, _ := item["phase"].(string)
			phases = append(phases, phase)
		}
	}
	if len(phases) != 2 {
		t.Fatalf("round-trip assistant items: got %d want 2; items=%v", len(phases), items)
	}
	if phases[0] != "commentary" || phases[1] != "final_answer" {
		t.Fatalf("round-trip phases: got %v want [commentary final_answer]", phases)
	}
}

// TestFromResponses_EmptyTextWithPhase verifies that when the API returns a
// message with phase="final_answer" but empty text (gpt-5.3-codex's empty
// response behavior), the phase is still preserved in the parsed ContentPart.
func TestFromResponses_EmptyTextWithPhase(t *testing.T) {
	raw := map[string]any{
		"id":    "r1",
		"model": "gpt-5.3-codex",
		"output": []any{
			map[string]any{
				"type":  "message",
				"phase": "final_answer",
				"content": []any{
					map[string]any{"type": "output_text", "text": ""},
				},
			},
		},
		"usage": map[string]any{"input_tokens": float64(100), "output_tokens": float64(4), "total_tokens": float64(104)},
	}

	r := fromResponses(raw, "gpt-5.3-codex")
	// Should have one content part preserving the phase, even with empty text.
	if len(r.Message.Content) != 1 {
		t.Fatalf("content parts: got %d want 1", len(r.Message.Content))
	}
	if r.Message.Content[0].Phase != "final_answer" {
		t.Fatalf("phase: got %q want %q", r.Message.Content[0].Phase, "final_answer")
	}
}

// TestEmptyPhaseRoundTrip verifies that an empty-text final_answer survives
// serialization back to the API. The model needs to see that it previously
// entered final_answer mode (even with no content) so it can course-correct.
func TestEmptyPhaseRoundTrip(t *testing.T) {
	// Parse an empty final_answer response.
	raw := map[string]any{
		"id":    "r1",
		"model": "gpt-5.3-codex",
		"output": []any{
			map[string]any{
				"type":  "message",
				"phase": "final_answer",
				"content": []any{
					map[string]any{"type": "output_text", "text": ""},
				},
			},
		},
		"usage": map[string]any{"input_tokens": float64(100), "output_tokens": float64(4), "total_tokens": float64(104)},
	}
	r := fromResponses(raw, "gpt-5.3-codex")

	// Serialize back.
	nextMsgs := []llm.Message{
		llm.User("initial task"),
		r.Message,
		llm.User("continue working"),
	}
	_, items, err := toResponsesInput(nextMsgs, "gpt-5.4")
	if err != nil {
		t.Fatalf("toResponsesInput: %v", err)
	}

	// Find assistant message items.
	var assistantItems []map[string]any
	for _, itemAny := range items {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		if item["role"] == "assistant" && item["type"] == "message" {
			assistantItems = append(assistantItems, item)
		}
	}
	if len(assistantItems) != 1 {
		t.Fatalf("assistant message items: got %d want 1; items=%v", len(assistantItems), items)
	}
	if assistantItems[0]["phase"] != "final_answer" {
		t.Fatalf("phase: got %v want %q", assistantItems[0]["phase"], "final_answer")
	}
}

func contentKinds(parts []llm.ContentPart) []string {
	var kinds []string
	for _, p := range parts {
		kinds = append(kinds, string(p.Kind))
	}
	return kinds
}

// TestAdapter_Complete_StampsEndpointURL_ResponsesPath verifies that the
// /v1/responses URL the adapter actually dialed is stashed on resp.Raw so the
// APILogger can promote it to the api_call transcript.
func TestAdapter_Complete_StampsEndpointURL_ResponsesPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "resp_1",
  "model": "gpt-5.2",
  "output": [
    {"type": "message", "content": [{"type":"output_text", "text":"ok"}]}
  ],
  "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := a.Complete(ctx, llm.Request{
		Model:    "gpt-5.2",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	want := srv.URL + "/v1/responses"
	got, _ := resp.Raw["endpoint_url"].(string)
	if got != want {
		t.Fatalf("Raw[endpoint_url] = %q, want %q", got, want)
	}
}

// TestAdapter_Stream_StampsEndpointURL_ResponsesPath verifies that the SSE
// streaming responses path also stamps the dialed URL onto the terminal
// Response carried by the Finish event.
func TestAdapter_Stream_StampsEndpointURL_ResponsesPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		events := []string{
			`event: response.output_text.delta` + "\ndata: " + `{"type":"response.output_text.delta","delta":"Hi"}`,
			`event: response.completed` + "\ndata: " + `{"type":"response.completed","response":{"id":"r1","model":"gpt-5","output":[{"type":"message","content":[{"type":"output_text","text":"Hi"}]}],"status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
		}
		for _, e := range events {
			_, _ = fmt.Fprintln(w, e)
			_, _ = fmt.Fprintln(w)
		}
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{Model: "gpt-5", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()

	var finalResp *llm.Response
	for ev := range stream.Events() {
		if ev.Type == llm.StreamEventFinish && ev.Response != nil {
			finalResp = ev.Response
		}
	}
	if finalResp == nil {
		t.Fatal("no finish-with-response event")
	}
	want := srv.URL + "/v1/responses"
	got, _ := finalResp.Raw["endpoint_url"].(string)
	if got != want {
		t.Fatalf("Raw[endpoint_url] = %q, want %q", got, want)
	}
}

// TestAdapter_Stream_StampsEndpointURL_ChatCompletionsFallback verifies that
// when the Responses API returns no events and the adapter falls back to
// /v1/chat/completions, the stamped URL reflects the chat-completions path
// (the one actually used), not the failed primary path.
func TestAdapter_Stream_StampsEndpointURL_ChatCompletionsFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			// Empty stream — triggers fallback.
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			chunks := []string{
				`data: {"id":"cc1","model":"gpt-4.1-mini","choices":[{"index":0,"delta":{"role":"assistant","content":"Hi"},"finish_reason":null}]}`,
				`data: {"id":"cc1","model":"gpt-4.1-mini","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`,
				`data: [DONE]`,
			}
			for _, c := range chunks {
				_, _ = fmt.Fprintln(w, c)
				_, _ = fmt.Fprintln(w)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{Model: "gpt-4.1-mini", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()

	var finalResp *llm.Response
	for ev := range stream.Events() {
		if ev.Type == llm.StreamEventError {
			t.Fatalf("unexpected error: %v", ev.Err)
		}
		if ev.Type == llm.StreamEventFinish && ev.Response != nil {
			finalResp = ev.Response
		}
	}
	if finalResp == nil {
		t.Fatal("no finish-with-response event")
	}
	want := srv.URL + "/v1/chat/completions"
	got, _ := finalResp.Raw["endpoint_url"].(string)
	if got != want {
		t.Fatalf("Raw[endpoint_url] = %q, want %q (must be chat completions, not failed responses path)", got, want)
	}
}

// TestAdapter_Complete_StampsEndpointURL_CodexBackend verifies that when the
// adapter is configured for the ChatGPT OAuth/Codex backend, the stamped URL
// reflects the codex responses path so QA can distinguish OAuth-routed
// traffic from /v1/responses API-key traffic.
func TestAdapter_Complete_StampsEndpointURL_CodexBackend(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/codex/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		events := []string{
			`event: response.output_text.delta` + "\ndata: " + `{"type":"response.output_text.delta","delta":"Hi"}`,
			`event: response.completed` + "\ndata: " + `{"type":"response.completed","response":{"id":"r1","model":"gpt-5","output":[{"type":"message","content":[{"type":"output_text","text":"Hi"}]}],"status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
		}
		for _, e := range events {
			_, _ = fmt.Fprintln(w, e)
			_, _ = fmt.Fprintln(w)
		}
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{
		APIKey:           "oauth-token",
		BaseURL:          srv.URL,
		ResponsesPath:    "/backend-api/codex/responses",
		ChatGPTAccountID: "acct-1",
		Client:           srv.Client(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := a.Complete(ctx, llm.Request{Model: "gpt-5", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	want := srv.URL + "/backend-api/codex/responses"
	got, _ := resp.Raw["endpoint_url"].(string)
	if got != want {
		t.Fatalf("Raw[endpoint_url] = %q, want %q", got, want)
	}
}
