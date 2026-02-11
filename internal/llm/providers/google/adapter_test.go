package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestAdapter_Complete_MapsToGeminiGenerateContent(t *testing.T) {
	var gotBody map[string]any
	gotKey := ""
	gotPath := ""

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.URL.Query().Get("key")
		gotPath = r.URL.Path
		if r.Method != http.MethodPost || !strings.Contains(r.URL.Path, ":generateContent") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "candidates": [{"content": {"parts": [{"text":"Hello"}]}, "finishReason":"STOP"}],
  "usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 2, "totalTokenCount": 3}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := a.Complete(ctx, llm.Request{
		Model: "gemini-test",
		Messages: []llm.Message{
			llm.System("sys"),
			llm.Developer("dev"),
			llm.User("u1"),
		},
		Tools: []llm.ToolDefinition{{
			Name:        "shell",
			Description: "run shell",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if strings.TrimSpace(resp.Text()) != "Hello" {
		t.Fatalf("resp text: %q", resp.Text())
	}
	if gotKey != "k" {
		t.Fatalf("key param: %q", gotKey)
	}
	if !strings.Contains(gotPath, "/v1beta/models/") {
		t.Fatalf("path: %q", gotPath)
	}

	// Request mapping basics.
	if gotBody == nil {
		t.Fatalf("server did not capture request body")
	}
	if _, ok := gotBody["contents"].([]any); !ok {
		t.Fatalf("contents: %#v", gotBody["contents"])
	}
	if sysAny, ok := gotBody["systemInstruction"].(map[string]any); !ok || sysAny == nil {
		t.Fatalf("systemInstruction: %#v", gotBody["systemInstruction"])
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
  "candidates": [{"content": {"parts": [{"text":"ok"}]}, "finishReason":"STOP"}],
  "usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	toolDef := llm.ToolDefinition{Name: "t1", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}

	cases := []struct {
		name string
		tc   *llm.ToolChoice
		want func(t *testing.T, body map[string]any)
	}{
		{
			name: "auto",
			tc:   &llm.ToolChoice{Mode: "auto"},
			want: func(t *testing.T, body map[string]any) {
				tcAny, _ := body["toolConfig"].(map[string]any)
				fc, _ := tcAny["functionCallingConfig"].(map[string]any)
				if fc["mode"] != "AUTO" {
					t.Fatalf("toolConfig.functionCallingConfig.mode: %#v", fc["mode"])
				}
			},
		},
		{
			name: "none",
			tc:   &llm.ToolChoice{Mode: "none"},
			want: func(t *testing.T, body map[string]any) {
				tcAny, _ := body["toolConfig"].(map[string]any)
				fc, _ := tcAny["functionCallingConfig"].(map[string]any)
				if fc["mode"] != "NONE" {
					t.Fatalf("toolConfig.functionCallingConfig.mode: %#v", fc["mode"])
				}
			},
		},
		{
			name: "required",
			tc:   &llm.ToolChoice{Mode: "required"},
			want: func(t *testing.T, body map[string]any) {
				tcAny, _ := body["toolConfig"].(map[string]any)
				fc, _ := tcAny["functionCallingConfig"].(map[string]any)
				if fc["mode"] != "ANY" {
					t.Fatalf("toolConfig.functionCallingConfig.mode: %#v", fc["mode"])
				}
			},
		},
		{
			name: "named",
			tc:   &llm.ToolChoice{Mode: "named", Name: "t1"},
			want: func(t *testing.T, body map[string]any) {
				tcAny, _ := body["toolConfig"].(map[string]any)
				fc, _ := tcAny["functionCallingConfig"].(map[string]any)
				if fc["mode"] != "ANY" {
					t.Fatalf("toolConfig.functionCallingConfig.mode: %#v", fc["mode"])
				}
				allow, _ := fc["allowedFunctionNames"].([]any)
				if len(allow) != 1 || allow[0] != "t1" {
					t.Fatalf("allowedFunctionNames: %#v", fc["allowedFunctionNames"])
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotBody = nil
			_, err := a.Complete(ctx, llm.Request{
				Model:      "gemini-test",
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
			tc.want(t, gotBody)
		})
	}
}

func TestAdapter_Complete_Usage_MapsReasoningAndCacheTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "candidates": [{"content": {"parts": [{"text":"ok"}]}, "finishReason":"STOP"}],
  "usageMetadata": {
    "promptTokenCount": 1,
    "candidatesTokenCount": 2,
    "totalTokenCount": 3,
    "cachedContentTokenCount": 10,
    "thoughtsTokenCount": 7
  }
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := a.Complete(ctx, llm.Request{Model: "gemini-test", Messages: []llm.Message{llm.User("hi")}})
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
  "candidates": [{"content": {"parts": [{"text":"ok"}]}, "finishReason":"STOP"}],
  "usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{
		Model:    "gemini-test",
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
	fds, _ := t0["functionDeclarations"].([]any)
	if len(fds) != 1 {
		t.Fatalf("functionDeclarations: %#v", t0["functionDeclarations"])
	}
	fd0, _ := fds[0].(map[string]any)
	params, _ := fd0["parameters"].(map[string]any)
	if params["type"] != "object" {
		t.Fatalf("parameters.type: %#v", params["type"])
	}
}

func TestAdapter_Complete_StripsAdditionalPropertiesFromSchemas(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "candidates": [{"content": {"parts": [{"text":"ok"}]}, "finishReason":"STOP"}],
  "usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{
		Model:    "gemini-test",
		Messages: []llm.Message{llm.User("hi")},
		ResponseFormat: &llm.ResponseFormat{
			Type: "json_schema",
			JSONSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{"a": map[string]any{"type": "string"}},
			},
		},
		Tools: []llm.ToolDefinition{{
			Name: "t1",
			Parameters: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"x": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"properties":           map[string]any{"y": map[string]any{"type": "string"}},
					},
				},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotBody == nil {
		t.Fatalf("server did not capture request body")
	}

	// Tool schemas: additionalProperties must be stripped (Gemini Schema proto rejects it).
	tools, _ := gotBody["tools"].([]any)
	t0, _ := tools[0].(map[string]any)
	fds, _ := t0["functionDeclarations"].([]any)
	fd0, _ := fds[0].(map[string]any)
	params, _ := fd0["parameters"].(map[string]any)
	if _, ok := params["additionalProperties"]; ok {
		t.Fatalf("unexpected additionalProperties in tool parameters: %#v", params["additionalProperties"])
	}
	props, _ := params["properties"].(map[string]any)
	x, _ := props["x"].(map[string]any)
	if _, ok := x["additionalProperties"]; ok {
		t.Fatalf("unexpected nested additionalProperties in tool parameters: %#v", x["additionalProperties"])
	}

	// Response schema: additionalProperties must also be stripped.
	genCfg, _ := gotBody["generationConfig"].(map[string]any)
	rs, _ := genCfg["responseSchema"].(map[string]any)
	if _, ok := rs["additionalProperties"]; ok {
		t.Fatalf("unexpected additionalProperties in responseSchema: %#v", rs["additionalProperties"])
	}
}

func TestAdapter_Complete_RejectsAudioAndDocumentParts(t *testing.T) {
	a := &Adapter{APIKey: "k", BaseURL: "http://example.com"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	msgAudio := llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentAudio, Audio: &llm.AudioData{URL: "https://example.com/a.wav"}}}}
	_, err := a.Complete(ctx, llm.Request{Model: "gemini-test", Messages: []llm.Message{msgAudio}})
	if err == nil {
		t.Fatalf("expected error")
	}
	var ce *llm.ConfigurationError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConfigurationError, got %T (%v)", err, err)
	}

	msgDoc := llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentDocument, Document: &llm.DocumentData{URL: "https://example.com/a.pdf"}}}}
	_, err = a.Complete(ctx, llm.Request{Model: "gemini-test", Messages: []llm.Message{msgDoc}})
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConfigurationError, got %T (%v)", err, err)
	}
}

func TestAdapter_Complete_HTTPErrorMapping_ServerErrorWithRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"unavailable"}}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{Model: "gemini-test", Messages: []llm.Message{llm.User("hi")}})
	if err == nil {
		t.Fatalf("expected error")
	}
	var se *llm.ServerError
	if !errors.As(err, &se) {
		t.Fatalf("expected ServerError, got %T (%v)", err, err)
	}
	if !se.Retryable() {
		t.Fatalf("expected retryable server error")
	}
	if se.RetryAfter() == nil || *se.RetryAfter() != 1*time.Second {
		t.Fatalf("retry_after: %v", se.RetryAfter())
	}
}

func TestAdapter_Complete_GRPCStatusMapping(t *testing.T) {
	// Gemini returns gRPC status codes in error response bodies. When the HTTP
	// status is ambiguous (e.g. 400), the gRPC status provides more specific
	// classification. When HTTP status is already specific (e.g. 429), the
	// existing mapping is correct.
	cases := []struct {
		name       string
		httpStatus int
		grpcStatus string
		message    string
		checkErr   func(t *testing.T, err error)
	}{
		{
			name:       "RESOURCE_EXHAUSTED via 429 maps to RateLimitError",
			httpStatus: 429,
			grpcStatus: "RESOURCE_EXHAUSTED",
			message:    "quota exceeded",
			checkErr: func(t *testing.T, err error) {
				var target *llm.RateLimitError
				if !errors.As(err, &target) {
					t.Fatalf("expected RateLimitError, got %T (%v)", err, err)
				}
			},
		},
		{
			name:       "NOT_FOUND via 404 maps to NotFoundError",
			httpStatus: 404,
			grpcStatus: "NOT_FOUND",
			message:    "model not found",
			checkErr: func(t *testing.T, err error) {
				var target *llm.NotFoundError
				if !errors.As(err, &target) {
					t.Fatalf("expected NotFoundError, got %T (%v)", err, err)
				}
			},
		},
		{
			name:       "INVALID_ARGUMENT via 400 maps to InvalidRequestError",
			httpStatus: 400,
			grpcStatus: "INVALID_ARGUMENT",
			message:    "invalid argument",
			checkErr: func(t *testing.T, err error) {
				var target *llm.InvalidRequestError
				if !errors.As(err, &target) {
					t.Fatalf("expected InvalidRequestError, got %T (%v)", err, err)
				}
			},
		},
		{
			name:       "PERMISSION_DENIED via 403 maps to AccessDeniedError",
			httpStatus: 403,
			grpcStatus: "PERMISSION_DENIED",
			message:    "permission denied",
			checkErr: func(t *testing.T, err error) {
				var target *llm.AccessDeniedError
				if !errors.As(err, &target) {
					t.Fatalf("expected AccessDeniedError, got %T (%v)", err, err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.httpStatus)
				_, _ = w.Write([]byte(fmt.Sprintf(
					`{"error":{"code":%d,"message":"%s","status":"%s"}}`,
					tc.httpStatus, tc.message, tc.grpcStatus,
				)))
			}))
			t.Cleanup(srv.Close)

			a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			_, err := a.Complete(ctx, llm.Request{Model: "gemini-test", Messages: []llm.Message{llm.User("hi")}})
			if err == nil {
				t.Fatalf("expected error")
			}
			tc.checkErr(t, err)
		})
	}
}

func TestAdapter_Stream_YieldsTextDeltasAndFinish(t *testing.T) {
	var gotBody map[string]any
	gotKey := ""
	gotPath := ""
	gotAlt := ""

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.URL.Query().Get("key")
		gotAlt = r.URL.Query().Get("alt")
		gotPath = r.URL.Path
		if r.Method != http.MethodPost || !strings.Contains(r.URL.Path, ":streamGenerateContent") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		write := func(data string) {
			_, _ = io.WriteString(w, "data: "+data+"\n\n")
			if f != nil {
				f.Flush()
			}
		}

		write(`{"candidates":[{"content":{"parts":[{"text":"Hel"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)
		write(`{"candidates":[{"content":{"parts":[{"text":"lo"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}`)
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{Model: "gemini-test", Messages: []llm.Message{llm.User("hi")}})
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
	if gotKey != "k" {
		t.Fatalf("key param: %q", gotKey)
	}
	if gotAlt != "sse" {
		t.Fatalf("alt param: %q", gotAlt)
	}
	if !strings.Contains(gotPath, "/v1beta/models/") {
		t.Fatalf("path: %q", gotPath)
	}
	if gotBody == nil {
		t.Fatalf("server did not capture request body")
	}
	if _, ok := gotBody["contents"].([]any); !ok {
		t.Fatalf("contents: %#v", gotBody["contents"])
	}

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

func TestAdapter_Stream_TranslatesFunctionCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.Contains(r.URL.Path, ":streamGenerateContent") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		write := func(data string) {
			_, _ = io.WriteString(w, "data: "+data+"\n\n")
			if f != nil {
				f.Flush()
			}
		}

		write(`{"candidates":[{"content":{"parts":[{"thoughtSignature":"sig-1","functionCall":{"name":"get_weather","args":{"n":1}}}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)
		write(`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}`)
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{Model: "gemini-test", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()

	var startID, endID string
	var startSig, endSig string
	var endArgs string
	var finishResp *llm.Response
	var kinds []llm.StreamEventType
	for ev := range stream.Events() {
		kinds = append(kinds, ev.Type)
		switch ev.Type {
		case llm.StreamEventToolCallStart:
			if ev.ToolCall != nil {
				startID = ev.ToolCall.ID
				startSig = ev.ToolCall.ThoughtSignature
			}
		case llm.StreamEventToolCallEnd:
			if ev.ToolCall != nil {
				endID = ev.ToolCall.ID
				endSig = ev.ToolCall.ThoughtSignature
				endArgs = string(ev.ToolCall.Arguments)
			}
		case llm.StreamEventFinish:
			if ev.Response != nil {
				finishResp = ev.Response
			}
		}
	}
	if strings.TrimSpace(startID) == "" || strings.TrimSpace(endID) == "" || startID != endID {
		t.Fatalf("expected stable synthetic call id; start=%q end=%q", startID, endID)
	}
	if strings.TrimSpace(endArgs) != `{"n":1}` {
		t.Fatalf("tool args: %q", endArgs)
	}
	if startSig != "sig-1" || endSig != "sig-1" {
		t.Fatalf("thought signature: start=%q end=%q", startSig, endSig)
	}
	if finishResp == nil {
		t.Fatalf("expected finish response")
	}
	calls := finishResp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("finish tool calls: %+v", calls)
	}
	if calls[0].ID != startID || calls[0].Name != "get_weather" {
		t.Fatalf("finish tool call: %+v", calls[0])
	}
	if strings.TrimSpace(string(calls[0].Arguments)) != `{"n":1}` {
		t.Fatalf("finish tool call args: %q", string(calls[0].Arguments))
	}
	if calls[0].ThoughtSignature != "sig-1" {
		t.Fatalf("finish tool call thought signature: %q", calls[0].ThoughtSignature)
	}
	foundStart := false
	foundEnd := false
	for _, k := range kinds {
		if k == llm.StreamEventToolCallStart {
			foundStart = true
		}
		if k == llm.StreamEventToolCallEnd {
			foundEnd = true
		}
	}
	if !foundStart || !foundEnd {
		t.Fatalf("expected TOOL_CALL_START and TOOL_CALL_END events (kinds=%v)", kinds)
	}
}

func TestAdapter_Complete_ReplaysFunctionCallThoughtSignature(t *testing.T) {
	var secondBody map[string]any
	callN := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callN++
		if r.Method != http.MethodPost || !strings.Contains(r.URL.Path, ":generateContent") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if callN == 2 {
			b, _ := io.ReadAll(r.Body)
			_ = r.Body.Close()
			_ = json.Unmarshal(b, &secondBody)
		}

		w.Header().Set("Content-Type", "application/json")
		if callN == 1 {
			_, _ = w.Write([]byte(`{
  "candidates": [{"content": {"parts": [{"thoughtSignature":"sig-replay-1","functionCall":{"name":"list_dir","args":{"path":"."}}}]}}],
  "usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2}
}`))
			return
		}
		_, _ = w.Write([]byte(`{
  "candidates": [{"content": {"parts": [{"text":"done"}]}, "finishReason":"STOP"}],
  "usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp1, err := a.Complete(ctx, llm.Request{
		Model:    "gemini-test",
		Messages: []llm.Message{llm.User("hi")},
		Tools: []llm.ToolDefinition{{
			Name:       "list_dir",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
		}},
	})
	if err != nil {
		t.Fatalf("first Complete: %v", err)
	}
	calls := resp1.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("first tool calls: %+v", calls)
	}
	if calls[0].ThoughtSignature != "sig-replay-1" {
		t.Fatalf("captured thought signature: %q", calls[0].ThoughtSignature)
	}

	toolResult := llm.ToolResultNamed(calls[0].ID, calls[0].Name, map[string]any{"entries": []string{"a", "b"}}, false)
	_, err = a.Complete(ctx, llm.Request{
		Model: "gemini-test",
		Messages: []llm.Message{
			llm.User("hi"),
			resp1.Message,
			toolResult,
		},
		Tools: []llm.ToolDefinition{{
			Name:       "list_dir",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
		}},
	})
	if err != nil {
		t.Fatalf("second Complete: %v", err)
	}
	if secondBody == nil {
		t.Fatalf("second request body not captured")
	}

	foundSig := ""
	contents, _ := secondBody["contents"].([]any)
	for _, cAny := range contents {
		c, _ := cAny.(map[string]any)
		parts, _ := c["parts"].([]any)
		for _, pAny := range parts {
			p, _ := pAny.(map[string]any)
			if _, ok := p["functionCall"].(map[string]any); !ok {
				continue
			}
			if sig, _ := p["thoughtSignature"].(string); sig != "" {
				foundSig = sig
			}
		}
	}
	if foundSig != "sig-replay-1" {
		t.Fatalf("replayed thought signature: got %q want %q", foundSig, "sig-replay-1")
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
  "candidates": [{"content": {"parts": [{"text":"ok"}]}, "finishReason":"STOP"}],
  "usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2}
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
			{Kind: llm.ContentImage, Image: &llm.ImageData{URL: "https://example.com/x.png", MediaType: "image/png"}},
			{Kind: llm.ContentImage, Image: &llm.ImageData{MediaType: "image/png", Data: []byte{0x01, 0x02, 0x03}}},
			{Kind: llm.ContentImage, Image: &llm.ImageData{URL: imgPath}},
		},
	}
	if _, err := a.Complete(ctx, llm.Request{Model: "gemini-test", Messages: []llm.Message{msg}}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	contents, ok := gotBody["contents"].([]any)
	if !ok || len(contents) == 0 {
		t.Fatalf("contents: %#v", gotBody["contents"])
	}
	first, _ := contents[0].(map[string]any)
	parts, _ := first["parts"].([]any)
	if len(parts) == 0 {
		t.Fatalf("parts: %#v", first["parts"])
	}

	seenURL := false
	seenInline := 0
	for _, pAny := range parts {
		p, ok := pAny.(map[string]any)
		if !ok {
			continue
		}
		if fd, ok := p["fileData"].(map[string]any); ok {
			if fd["fileUri"] == "https://example.com/x.png" {
				seenURL = true
			}
		}
		if _, ok := p["inlineData"].(map[string]any); ok {
			seenInline++
		}
	}
	if !seenURL || seenInline < 2 {
		t.Fatalf("expected url image + 2 inline images; seenURL=%v seenInline=%d parts=%#v", seenURL, seenInline, parts)
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
  "candidates": [{"content": {"parts": [{"text":"{}"}]}, "finishReason":"STOP"}],
  "usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2}
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
		Model:    "gemini-test",
		Messages: []llm.Message{llm.User("hi")},
		ResponseFormat: &llm.ResponseFormat{
			Type:       "json_schema",
			JSONSchema: schema,
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	genCfg, ok := gotBody["generationConfig"].(map[string]any)
	if !ok || genCfg == nil {
		t.Fatalf("generationConfig: %#v", gotBody["generationConfig"])
	}
	if genCfg["responseMimeType"] != "application/json" {
		t.Fatalf("responseMimeType: %#v", genCfg["responseMimeType"])
	}
	if _, ok := genCfg["responseSchema"].(map[string]any); !ok {
		t.Fatalf("responseSchema: %#v", genCfg["responseSchema"])
	}
}

func TestAdapter_Stream_ContextDeadline_EmitsRequestTimeoutError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.Contains(r.URL.Path, ":streamGenerateContent") {
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

	st, err := a.Stream(ctx, llm.Request{Model: "gemini-test", Messages: []llm.Message{llm.User("hi")}})
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
  "candidates": [{"content": {"parts": [{"text":"ok"}]}, "finishReason":"STOP"}],
  "usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{
		Model:    "gemini-test",
		Messages: []llm.Message{llm.User("hi")},
		ProviderOptions: map[string]any{
			"google": map[string]any{
				"x-test-opt": 123,
			},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got, want := gotBody["x-test-opt"], float64(123); got != want {
		t.Fatalf("x-test-opt: got %#v want %#v", got, want)
	}
}

func TestAdapter_Complete_WebSearch_SkippedWithFunctionTools(t *testing.T) {
	// Gemini does not support google_search combined with functionDeclarations.
	// When both are requested, only functionDeclarations should be sent.
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "candidates": [{
    "content": {"parts": [{"text": "ok"}], "role": "model"},
    "finishReason": "STOP"
  }],
  "usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{
		Model:    "gemini-2.5-flash",
		Messages: []llm.Message{llm.User("search for docs")},
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

	// Should have only 1 entry: functionDeclarations (google_search excluded)
	if len(toolsAny) != 1 {
		t.Fatalf("tools count: got %d want 1 (google_search should be excluded)", len(toolsAny))
	}

	entry0, _ := toolsAny[0].(map[string]any)
	if _, ok := entry0["functionDeclarations"]; !ok {
		t.Fatalf("first tools entry missing functionDeclarations: %#v", entry0)
	}
}

func TestAdapter_Complete_WebSearch_SentWithoutFunctionTools(t *testing.T) {
	// When WebSearch is true and no function tools, google_search should be sent.
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "candidates": [{
    "content": {"parts": [{"text": "ok"}], "role": "model"},
    "finishReason": "STOP"
  }],
  "usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{
		Model:     "gemini-2.5-flash",
		Messages:  []llm.Message{llm.User("search for docs")},
		WebSearch: true,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	toolsAny, ok := gotBody["tools"].([]any)
	if !ok {
		t.Fatalf("tools not array: %#v", gotBody["tools"])
	}

	if len(toolsAny) != 1 {
		t.Fatalf("tools count: got %d want 1", len(toolsAny))
	}

	entry0, _ := toolsAny[0].(map[string]any)
	if _, ok := entry0["google_search"]; !ok {
		t.Fatalf("tools entry missing google_search: %#v", entry0)
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
  "candidates": [{
    "content": {"parts": [{"text": "ok"}], "role": "model"},
    "finishReason": "STOP"
  }],
  "usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{
		Model:    "gemini-2.5-flash",
		Messages: []llm.Message{llm.User("hello")},
		Tools: []llm.ToolDefinition{{
			Name:       "shell",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
		}},
		// WebSearch: false (default)
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	toolsAny, ok := gotBody["tools"].([]any)
	if !ok {
		t.Fatalf("tools not array: %#v", gotBody["tools"])
	}
	if len(toolsAny) != 1 {
		t.Fatalf("tools count: got %d want 1 (no google_search)", len(toolsAny))
	}
}

func TestAdapter_Complete_WebSearch_ParsesGroundingMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "candidates": [{
    "content": {"parts": [{"text": "Spain won Euro 2024."}], "role": "model"},
    "finishReason": "STOP",
    "groundingMetadata": {
      "webSearchQueries": ["Euro 2024 winner", "who won euro 2024"],
      "groundingChunks": [
        {"web": {"uri": "https://example.com/euro", "title": "Euro Results"}}
      ],
      "groundingSupports": [
        {"segment": {"startIndex": 0, "endIndex": 20, "text": "Spain won Euro 2024."}, "groundingChunkIndices": [0], "confidenceScores": [0.95]}
      ]
    }
  }],
  "usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 20, "totalTokenCount": 30}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := a.Complete(ctx, llm.Request{
		Model:     "gemini-2.5-flash",
		Messages:  []llm.Message{llm.User("who won euro 2024")},
		WebSearch: true,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Should have 2 content parts: text + web_search (grounding metadata)
	if len(resp.Message.Content) != 2 {
		t.Fatalf("content parts: got %d want 2", len(resp.Message.Content))
	}

	txt := resp.Message.Content[0]
	if txt.Kind != llm.ContentText || !strings.Contains(txt.Text, "Spain") {
		t.Fatalf("part[0]: kind=%q text=%q", txt.Kind, txt.Text)
	}

	ws := resp.Message.Content[1]
	if ws.Kind != llm.ContentWebSearch {
		t.Fatalf("part[1] kind: got %q want web_search", ws.Kind)
	}
	if ws.WebSearch == nil {
		t.Fatalf("part[1] web_search is nil")
	}
	if !strings.Contains(ws.WebSearch.Query, "Euro 2024") {
		t.Fatalf("query: got %q", ws.WebSearch.Query)
	}
	if len(ws.WebSearch.Raw) == 0 {
		t.Fatalf("raw is empty")
	}
}

func TestAdapter_Complete_FinishReason_Normalized(t *testing.T) {
	cases := []struct {
		name         string
		finishReason string
		wantReason   string
		wantRaw      string
	}{
		{"STOP", "STOP", "stop", "STOP"},
		{"MAX_TOKENS", "MAX_TOKENS", "length", "MAX_TOKENS"},
		{"SAFETY", "SAFETY", "content_filter", "SAFETY"},
		{"RECITATION", "RECITATION", "content_filter", "RECITATION"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{
  "candidates": [{"content": {"parts": [{"text":"ok"}]}, "finishReason":%q}],
  "usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2}
}`, tc.finishReason)
			}))
			t.Cleanup(srv.Close)

			a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			resp, err := a.Complete(ctx, llm.Request{Model: "gemini-test", Messages: []llm.Message{llm.User("hi")}})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if resp.Finish.Reason != tc.wantReason {
				t.Fatalf("Finish.Reason = %q, want %q", resp.Finish.Reason, tc.wantReason)
			}
			if resp.Finish.Raw != tc.wantRaw {
				t.Fatalf("Finish.Raw = %q, want %q", resp.Finish.Raw, tc.wantRaw)
			}
		})
	}
}

func TestAdapter_Integration_WebSearch(t *testing.T) {
	if os.Getenv("GEMINI_API_KEY") == "" && os.Getenv("GOOGLE_API_KEY") == "" {
		t.Skip("GEMINI_API_KEY not set")
	}

	a, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Gemini does not support google_search combined with functionDeclarations,
	// so we test web search without function tools.
	resp, err := a.Complete(ctx, llm.Request{
		Model:     "gemini-2.5-flash",
		Messages:  []llm.Message{llm.User("Search the web and tell me: what is the current population of Tokyo?")},
		WebSearch: true,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if strings.TrimSpace(resp.Text()) == "" {
		t.Fatalf("expected non-empty text response")
	}

	// Verify we got web search content parts (groundingMetadata).
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

func TestAdapter_Integration_WebSearch_WithFunctionTools(t *testing.T) {
	if os.Getenv("GEMINI_API_KEY") == "" && os.Getenv("GOOGLE_API_KEY") == "" {
		t.Skip("GEMINI_API_KEY not set")
	}

	a, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	funcTool := llm.ToolDefinition{
		Name:        "shell",
		Description: "Run a shell command",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}},
	}

	// First call: function tools + WebSearch:true. The adapter skips google_search
	// due to the Gemini API limitation, so this should succeed without error.
	resp1, err := a.Complete(ctx, llm.Request{
		Model:     "gemini-2.5-flash",
		Messages:  []llm.Message{llm.User("What is 2+2?")},
		Tools:     []llm.ToolDefinition{funcTool},
		WebSearch: true,
	})
	if err != nil {
		t.Fatalf("First call (function tools + WebSearch): %v", err)
	}
	t.Logf("First call response (truncated): %.200s", resp1.Text())

	// Second call: WebSearch:true with NO function tools. This simulates the
	// web_search tool's grounding call — the adapter sends google_search and
	// the response should include grounding metadata.
	resp2, err := a.Complete(ctx, llm.Request{
		Model:     "gemini-2.5-flash",
		Messages:  []llm.Message{llm.User("Search the web: what is the current population of Tokyo?")},
		WebSearch: true,
	})
	if err != nil {
		t.Fatalf("Second call (WebSearch only): %v", err)
	}

	if strings.TrimSpace(resp2.Text()) == "" {
		t.Fatalf("expected non-empty text response from grounding call")
	}

	var wsCount int
	for _, p := range resp2.Message.Content {
		if p.Kind == llm.ContentWebSearch {
			wsCount++
			if p.WebSearch == nil {
				t.Fatalf("ContentWebSearch part has nil WebSearch data")
			}
			t.Logf("web_search query=%q raw_len=%d", p.WebSearch.Query, len(p.WebSearch.Raw))
		}
	}
	if wsCount == 0 {
		t.Fatalf("expected at least one ContentWebSearch part; got content kinds: %v", contentKinds(resp2.Message.Content))
	}
	t.Logf("Grounding response (truncated): %.200s", resp2.Text())
}

func contentKinds(parts []llm.ContentPart) []string {
	var kinds []string
	for _, p := range parts {
		kinds = append(kinds, string(p.Kind))
	}
	return kinds
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
		Model:    "gemini-test",
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

func TestComplete_WrapsContextDeadlineExceeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond) // ensure deadline expires

	_, err := a.Complete(ctx, llm.Request{
		Model:    "gemini-test",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var rte *llm.RequestTimeoutError
	if !errors.As(err, &rte) {
		t.Fatalf("expected RequestTimeoutError, got %T: %v", err, err)
	}
}

func TestStream_IncludesWebSearchTool_NoFunctionTools(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: "+`{"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`+"\n\n")
		if f != nil {
			f.Flush()
		}
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{
		Model:     "gemini-2.0-flash",
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
		t.Fatalf("expected tools with google_search, got: %v", gotBody["tools"])
	}
	found := false
	for _, tool := range tools {
		tm, _ := tool.(map[string]any)
		if _, ok := tm["google_search"]; ok {
			found = true
		}
	}
	if !found {
		t.Fatalf("google_search tool not found in tools: %v", tools)
	}
}

func TestStream_WebSearch_SkippedWithFunctionTools(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: "+`{"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`+"\n\n")
		if f != nil {
			f.Flush()
		}
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{
		Model:    "gemini-2.0-flash",
		Messages: []llm.Message{llm.User("search the web")},
		Tools: []llm.ToolDefinition{{
			Name:       "shell",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
		}},
		WebSearch: true,
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()
	for range stream.Events() {
	}

	tools, ok := gotBody["tools"].([]any)
	if !ok {
		t.Fatalf("tools not array: %#v", gotBody["tools"])
	}
	// Should have only 1 entry: functionDeclarations (google_search excluded)
	if len(tools) != 1 {
		t.Fatalf("tools count: got %d want 1 (google_search should be excluded)", len(tools))
	}
	entry0, _ := tools[0].(map[string]any)
	if _, ok := entry0["functionDeclarations"]; !ok {
		t.Fatalf("first tools entry missing functionDeclarations: %#v", entry0)
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
		fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	resp, err := a.Complete(context.Background(), llm.Request{
		Model:    "gemini-test",
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
	if resp.RateLimit.RequestsLimit == nil || *resp.RateLimit.RequestsLimit != 100 {
		t.Fatalf("RequestsLimit = %v", resp.RateLimit.RequestsLimit)
	}
	if resp.RateLimit.TokensRemaining == nil || *resp.RateLimit.TokensRemaining != 9999 {
		t.Fatalf("TokensRemaining = %v", resp.RateLimit.TokensRemaining)
	}
	if resp.RateLimit.TokensLimit == nil || *resp.RateLimit.TokensLimit != 10000 {
		t.Fatalf("TokensLimit = %v", resp.RateLimit.TokensLimit)
	}
	if resp.RateLimit.ResetAt != "2026-02-10T12:00:00Z" {
		t.Fatalf("ResetAt = %q", resp.RateLimit.ResetAt)
	}
}

func TestComplete_NoRateLimitHeaders_ReturnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	resp, err := a.Complete(context.Background(), llm.Request{
		Model:    "gemini-test",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.RateLimit != nil {
		t.Fatalf("RateLimit should be nil when no headers present, got %+v", resp.RateLimit)
	}
}
