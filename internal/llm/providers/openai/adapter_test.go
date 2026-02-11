package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/internal/llm"
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

func TestAdapter_Complete_RejectsAudioAndDocumentParts(t *testing.T) {
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

	msgDoc := llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentDocument, Document: &llm.DocumentData{URL: "https://example.com/a.pdf"}}}}
	_, err = a.Complete(ctx, llm.Request{Model: "gpt-5.2", Messages: []llm.Message{msgDoc}})
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConfigurationError, got %T (%v)", err, err)
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
	defer stream.Close()

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
	defer stream.Close()

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

	rf, ok := gotBody["response_format"].(map[string]any)
	if !ok || rf == nil {
		t.Fatalf("response_format: %#v", gotBody["response_format"])
	}
	if rf["type"] != "json_schema" {
		t.Fatalf("response_format.type: %#v", rf["type"])
	}
	if _, ok := rf["json_schema"].(map[string]any); !ok {
		t.Fatalf("response_format.json_schema: %#v", rf["json_schema"])
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
	defer st.Close()

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

	_, items, err := toResponsesInput(msgs)
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
	defer stream.Close()

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
		"id":    "r1",
		"model": "gpt-5.2",
		"status": "incomplete",
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
	defer stream.Close()
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

func contentKinds(parts []llm.ContentPart) []string {
	var kinds []string
	for _, p := range parts {
		kinds = append(kinds, string(p.Kind))
	}
	return kinds
}
