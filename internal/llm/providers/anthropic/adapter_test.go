package anthropic

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

func TestAdapter_Complete_MapsToMessagesAPI_AndSetsBetaHeaders(t *testing.T) {
	var gotBody map[string]any
	gotBeta := ""

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotBeta = r.Header.Get("anthropic-beta")
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-test",
  "content": [{"type":"text","text":"Hello"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 1, "output_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := a.Complete(ctx, llm.Request{
		Model: "claude-test",
		Messages: []llm.Message{
			llm.System("sys"),
			llm.Developer("dev"),
			llm.User("u1"),
			llm.Assistant("a1"),
			llm.ToolResultNamed("call1", "shell", "ok", false),
		},
		ProviderOptions: map[string]any{
			"anthropic": map[string]any{
				"beta_headers": "prompt-caching-2024-07-31",
			},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if strings.TrimSpace(resp.Text()) != "Hello" {
		t.Fatalf("resp text: %q", resp.Text())
	}
	if gotBeta != "prompt-caching-2024-07-31" {
		t.Fatalf("anthropic-beta header: %q", gotBeta)
	}
	if gotBody == nil {
		t.Fatalf("server did not capture request body")
	}
	sysBlocks, ok := gotBody["system"].([]any)
	if !ok || len(sysBlocks) == 0 {
		t.Fatalf("system blocks: %#v", gotBody["system"])
	}
	sb0, _ := sysBlocks[0].(map[string]any)
	if !strings.Contains(fmt.Sprint(sb0["text"]), "sys") || !strings.Contains(fmt.Sprint(sb0["text"]), "dev") {
		t.Fatalf("system text: %#v", sb0["text"])
	}
	if cc, _ := sb0["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Fatalf("expected cache_control on system block; got %#v", sb0["cache_control"])
	}
	if msgsAny, ok := gotBody["messages"].([]any); !ok || len(msgsAny) == 0 {
		t.Fatalf("messages: %#v", gotBody["messages"])
	}

	// Conversation prefix breakpoint should be set on the message immediately before the last user message.
	seenPrefixCC := false
	msgsAny, _ := gotBody["messages"].([]any)
	for _, mAny := range msgsAny {
		m, ok := mAny.(map[string]any)
		if !ok {
			continue
		}
		if m["role"] != "assistant" {
			continue
		}
		blocks, _ := m["content"].([]any)
		for _, bAny := range blocks {
			bm, ok := bAny.(map[string]any)
			if !ok {
				continue
			}
			if cc, ok := bm["cache_control"].(map[string]any); ok && cc["type"] == "ephemeral" {
				seenPrefixCC = true
			}
		}
	}
	if !seenPrefixCC {
		t.Fatalf("expected cache_control breakpoint on conversation prefix; messages=%#v", gotBody["messages"])
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

	_, err := a.Complete(ctx, llm.Request{Model: "claude-test", Messages: []llm.Message{llm.User("hi")}})
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
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
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

		write("content_block_start", `{"content_block":{"type":"text"}}`)
		write("content_block_delta", `{"delta":{"type":"text_delta","text":"Hel"}}`)
		write("content_block_delta", `{"delta":{"type":"text_delta","text":"lo"}}`)
		write("content_block_stop", `{}`)
		write("message_delta", `{"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}}`)
		write("message_stop", `{}`)
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{Model: "claude-test", Messages: []llm.Message{llm.User("hi")}})
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

func TestAdapter_Stream_TranslatesToolUseAndThinkingBlocks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
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

		write("content_block_start", `{"index":0,"content_block":{"type":"thinking","signature":"sig1"}}`)
		write("content_block_delta", `{"index":0,"delta":{"type":"thinking_delta","thinking":"Plan"}}`)
		write("content_block_stop", `{"index":0}`)

		write("content_block_start", `{"index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather"}}`)
		write("content_block_delta", `{"index":1,"delta":{"type":"input_json_delta","partial_json":"{\"n\":1}"}}`)
		write("content_block_stop", `{"index":1}`)

		write("message_delta", `{"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":2}}`)
		write("message_stop", `{}`)
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{Model: "claude-test", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()

	seenReasonStart := false
	seenReasonDelta := false
	seenReasonEnd := false
	seenToolStart := false
	seenToolDelta := false
	seenToolEnd := false
	var toolEnd llm.ToolCallData
	var finishResp *llm.Response

	for ev := range stream.Events() {
		switch ev.Type {
		case llm.StreamEventReasoningStart:
			seenReasonStart = true
		case llm.StreamEventReasoningDelta:
			if strings.TrimSpace(ev.ReasoningDelta) != "" {
				seenReasonDelta = true
			}
		case llm.StreamEventReasoningEnd:
			seenReasonEnd = true
		case llm.StreamEventToolCallStart:
			seenToolStart = true
		case llm.StreamEventToolCallDelta:
			seenToolDelta = true
		case llm.StreamEventToolCallEnd:
			seenToolEnd = true
			if ev.ToolCall != nil {
				toolEnd = *ev.ToolCall
			}
		case llm.StreamEventFinish:
			if ev.Response != nil {
				finishResp = ev.Response
			}
		}
	}

	if !seenReasonStart || !seenReasonDelta || !seenReasonEnd {
		t.Fatalf("reasoning events: start=%t delta=%t end=%t", seenReasonStart, seenReasonDelta, seenReasonEnd)
	}
	if !seenToolStart || !seenToolDelta || !seenToolEnd {
		t.Fatalf("tool call events: start=%t delta=%t end=%t", seenToolStart, seenToolDelta, seenToolEnd)
	}
	if toolEnd.ID != "toolu_1" || toolEnd.Name != "get_weather" {
		t.Fatalf("tool call end: %+v", toolEnd)
	}
	if strings.TrimSpace(string(toolEnd.Arguments)) != `{"n":1}` {
		t.Fatalf("tool call args: %q", string(toolEnd.Arguments))
	}
	if finishResp == nil {
		t.Fatalf("expected finish response")
	}
	foundThinking := false
	foundTool := false
	for _, p := range finishResp.Message.Content {
		if p.Kind == llm.ContentThinking && p.Thinking != nil {
			if strings.TrimSpace(p.Thinking.Text) == "Plan" && strings.TrimSpace(p.Thinking.Signature) == "sig1" {
				foundThinking = true
			}
		}
		if p.Kind == llm.ContentToolCall && p.ToolCall != nil {
			if p.ToolCall.ID == "toolu_1" && p.ToolCall.Name == "get_weather" {
				foundTool = true
			}
		}
	}
	if !foundThinking {
		t.Fatalf("expected thinking content part in finish response: %+v", finishResp.Message.Content)
	}
	if !foundTool {
		t.Fatalf("expected tool call content part in finish response: %+v", finishResp.Message.Content)
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
  "id": "msg_1",
  "model": "claude-test",
  "content": [{"type":"text","text":"ok"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 1, "output_tokens": 1}
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
	if _, err := a.Complete(ctx, llm.Request{Model: "claude-test", Messages: []llm.Message{msg}}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	msgs, ok := gotBody["messages"].([]any)
	if !ok || len(msgs) == 0 {
		t.Fatalf("messages: %#v", gotBody["messages"])
	}
	first, _ := msgs[0].(map[string]any)
	content, _ := first["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("content: %#v", first["content"])
	}

	seenURL := false
	seenBase64 := 0
	for _, bAny := range content {
		bm, ok := bAny.(map[string]any)
		if !ok {
			continue
		}
		if bm["type"] != "image" {
			continue
		}
		src, _ := bm["source"].(map[string]any)
		st, _ := src["type"].(string)
		switch st {
		case "url":
			if src["url"] == "https://example.com/x.png" {
				seenURL = true
			}
		case "base64":
			if strings.TrimSpace(fmt.Sprint(src["data"])) != "" {
				seenBase64++
			}
		}
	}
	if !seenURL || seenBase64 < 2 {
		t.Fatalf("expected url image + 2 base64 images; seenURL=%v seenBase64=%d content=%#v", seenURL, seenBase64, content)
	}
}

func TestAdapter_ThinkingBlocks_RoundTripIncludingRedacted(t *testing.T) {
	var gotBodies []map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		gotBodies = append(gotBodies, body)

		w.Header().Set("Content-Type", "application/json")
		if len(gotBodies) == 1 {
			_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-test",
  "content": [
    {"type":"thinking","thinking":"THINK","signature":"sig1"},
    {"type":"redacted_thinking","data":"opaque"},
    {"type":"text","text":"Hello"}
  ],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 1, "output_tokens": 2}
}`))
			return
		}
		_, _ = w.Write([]byte(`{
  "id": "msg_2",
  "model": "claude-test",
  "content": [{"type":"text","text":"ok"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 1, "output_tokens": 1}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp1, err := a.Complete(ctx, llm.Request{Model: "claude-test", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Complete 1: %v", err)
	}
	if got := len(resp1.Message.Content); got < 2 {
		t.Fatalf("expected thinking parts in response; got %+v", resp1.Message.Content)
	}

	_, err = a.Complete(ctx, llm.Request{Model: "claude-test", Messages: []llm.Message{llm.User("hi"), resp1.Message, llm.User("next")}})
	if err != nil {
		t.Fatalf("Complete 2: %v", err)
	}
	if len(gotBodies) < 2 {
		t.Fatalf("expected 2 captured bodies, got %d", len(gotBodies))
	}
	msgs, _ := gotBodies[1]["messages"].([]any)
	// Find assistant message blocks.
	var blocks []any
	for _, mAny := range msgs {
		m, ok := mAny.(map[string]any)
		if !ok {
			continue
		}
		if m["role"] == "assistant" {
			blocks, _ = m["content"].([]any)
		}
	}
	if len(blocks) == 0 {
		t.Fatalf("expected assistant message with content blocks; messages=%#v", msgs)
	}

	seenThinking := false
	seenRedacted := false
	for _, bAny := range blocks {
		bm, ok := bAny.(map[string]any)
		if !ok {
			continue
		}
		switch bm["type"] {
		case "thinking":
			if bm["thinking"] == "THINK" && bm["signature"] == "sig1" {
				seenThinking = true
			}
		case "redacted_thinking":
			if bm["data"] == "opaque" {
				seenRedacted = true
			}
		}
	}
	if !seenThinking || !seenRedacted {
		t.Fatalf("expected thinking + redacted_thinking blocks; seenThinking=%v seenRedacted=%v blocks=%#v", seenThinking, seenRedacted, blocks)
	}
}

func TestAdapter_Complete_ResponseFormat_JSONSchema_InjectedIntoSystem(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-test",
  "content": [{"type":"text","text":"{}"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 1, "output_tokens": 1}
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
		Model:    "claude-test",
		Messages: []llm.Message{llm.User("hi")},
		ResponseFormat: &llm.ResponseFormat{
			Type:       "json_schema",
			JSONSchema: schema,
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	sysBlocks, ok := gotBody["system"].([]any)
	if !ok || len(sysBlocks) == 0 {
		t.Fatalf("system blocks: %#v", gotBody["system"])
	}
	sb0, _ := sysBlocks[0].(map[string]any)
	if !strings.Contains(fmt.Sprint(sb0["text"]), "JSON Schema") {
		t.Fatalf("expected schema instructions in system; got %#v", sb0["text"])
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
  "id": "msg_1",
  "model": "claude-test",
  "content": [{"type":"text","text":"ok"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 1, "output_tokens": 1}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{
		Model:    "claude-test",
		Messages: []llm.Message{llm.User("hi")},
		ProviderOptions: map[string]any{
			"anthropic": map[string]any{
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

func TestAdapter_Complete_ToolChoice_MappedPerSpec(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-test",
  "content": [{"type":"text","text":"ok"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 1, "output_tokens": 1}
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
				if _, ok := body["tools"]; !ok {
					t.Fatalf("expected tools present")
				}
				tcAny, ok := body["tool_choice"].(map[string]any)
				if !ok || tcAny["type"] != "auto" {
					t.Fatalf("tool_choice: %#v", body["tool_choice"])
				}
			},
		},
		{
			name: "required",
			tc:   &llm.ToolChoice{Mode: "required"},
			want: func(t *testing.T, body map[string]any) {
				if _, ok := body["tools"]; !ok {
					t.Fatalf("expected tools present")
				}
				tcAny, ok := body["tool_choice"].(map[string]any)
				if !ok || tcAny["type"] != "any" {
					t.Fatalf("tool_choice: %#v", body["tool_choice"])
				}
			},
		},
		{
			name: "named",
			tc:   &llm.ToolChoice{Mode: "named", Name: "t1"},
			want: func(t *testing.T, body map[string]any) {
				if _, ok := body["tools"]; !ok {
					t.Fatalf("expected tools present")
				}
				tcAny, ok := body["tool_choice"].(map[string]any)
				if !ok || tcAny["type"] != "tool" || tcAny["name"] != "t1" {
					t.Fatalf("tool_choice: %#v", body["tool_choice"])
				}
			},
		},
		{
			name: "none",
			tc:   &llm.ToolChoice{Mode: "none"},
			want: func(t *testing.T, body map[string]any) {
				if _, ok := body["tools"]; ok {
					t.Fatalf("expected tools omitted for none; got %#v", body["tools"])
				}
				if _, ok := body["tool_choice"]; ok {
					t.Fatalf("expected tool_choice omitted for none; got %#v", body["tool_choice"])
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotBody = nil
			_, err := a.Complete(ctx, llm.Request{
				Model:      "claude-test",
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

func TestAdapter_Complete_ToolParameters_DefaultToEmptyObjectSchema(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-test",
  "content": [{"type":"text","text":"ok"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 1, "output_tokens": 1}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{
		Model:    "claude-test",
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
	schema, _ := t0["input_schema"].(map[string]any)
	if schema["type"] != "object" {
		t.Fatalf("input_schema.type: %#v", schema["type"])
	}
}

func TestAdapter_Complete_RejectsAudioAndDocumentParts(t *testing.T) {
	a := &Adapter{APIKey: "k", BaseURL: "http://example.com"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	msgAudio := llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentAudio, Audio: &llm.AudioData{URL: "https://example.com/a.wav"}}}}
	_, err := a.Complete(ctx, llm.Request{Model: "claude-test", Messages: []llm.Message{msgAudio}})
	if err == nil {
		t.Fatalf("expected error")
	}
	var ce *llm.ConfigurationError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConfigurationError, got %T (%v)", err, err)
	}

	msgDoc := llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentDocument, Document: &llm.DocumentData{URL: "https://example.com/a.pdf"}}}}
	_, err = a.Complete(ctx, llm.Request{Model: "claude-test", Messages: []llm.Message{msgDoc}})
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConfigurationError, got %T (%v)", err, err)
	}
}

func TestAdapter_PromptCaching_AutoCacheDefaultAndDisable(t *testing.T) {
	t.Run("default_enabled_injects_cache_control_and_beta", func(t *testing.T) {
		var gotBody map[string]any
		gotBeta := ""

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotBeta = r.Header.Get("anthropic-beta")
			b, _ := io.ReadAll(r.Body)
			_ = r.Body.Close()
			_ = json.Unmarshal(b, &gotBody)

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-test",
  "content": [{"type":"text","text":"ok"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 1, "output_tokens": 1}
}`))
		}))
		t.Cleanup(srv.Close)

		a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		_, err := a.Complete(ctx, llm.Request{
			Model: "claude-test",
			Messages: []llm.Message{
				llm.System("sys"),
				llm.User("u1"),
				llm.Assistant("a1"),
				llm.User("u2"),
			},
			Tools: []llm.ToolDefinition{
				{
					Name:        "t1",
					Description: "d",
					Parameters:  map[string]any{"type": "object"},
				},
			},
		})
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}

		if gotBeta != "prompt-caching-2024-07-31" {
			t.Fatalf("anthropic-beta: got %q want %q", gotBeta, "prompt-caching-2024-07-31")
		}

		sysBlocks, ok := gotBody["system"].([]any)
		if !ok || len(sysBlocks) == 0 {
			t.Fatalf("system blocks: %#v", gotBody["system"])
		}
		sb0, _ := sysBlocks[0].(map[string]any)
		if cc, _ := sb0["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
			t.Fatalf("expected cache_control on system block; got %#v", sb0["cache_control"])
		}

		toolsAny, ok := gotBody["tools"].([]any)
		if !ok || len(toolsAny) != 1 {
			t.Fatalf("tools: %#v", gotBody["tools"])
		}
		t0, _ := toolsAny[0].(map[string]any)
		if cc, _ := t0["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
			t.Fatalf("expected cache_control on tool def; got %#v", t0["cache_control"])
		}

		// Breakpoint on conversation prefix (message before last user message: assistant "a1").
		msgs, ok := gotBody["messages"].([]any)
		if !ok || len(msgs) == 0 {
			t.Fatalf("messages: %#v", gotBody["messages"])
		}
		seenPrefixCC := false
		for _, mAny := range msgs {
			m, ok := mAny.(map[string]any)
			if !ok {
				continue
			}
			if m["role"] != "assistant" {
				continue
			}
			blocks, _ := m["content"].([]any)
			for _, bAny := range blocks {
				bm, ok := bAny.(map[string]any)
				if !ok {
					continue
				}
				if cc, ok := bm["cache_control"].(map[string]any); ok && cc["type"] == "ephemeral" {
					seenPrefixCC = true
				}
			}
		}
		if !seenPrefixCC {
			t.Fatalf("expected cache_control breakpoint on conversation prefix; messages=%#v", gotBody["messages"])
		}
	})

	t.Run("disabled_does_not_inject_cache_control_or_beta", func(t *testing.T) {
		var gotBody map[string]any
		gotBeta := ""

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotBeta = r.Header.Get("anthropic-beta")
			b, _ := io.ReadAll(r.Body)
			_ = r.Body.Close()
			_ = json.Unmarshal(b, &gotBody)

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-test",
  "content": [{"type":"text","text":"ok"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 1, "output_tokens": 1}
}`))
		}))
		t.Cleanup(srv.Close)

		a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		_, err := a.Complete(ctx, llm.Request{
			Model: "claude-test",
			Messages: []llm.Message{
				llm.System("sys"),
				llm.User("u1"),
				llm.Assistant("a1"),
				llm.User("u2"),
			},
			Tools: []llm.ToolDefinition{
				{
					Name:        "t1",
					Description: "d",
					Parameters:  map[string]any{"type": "object"},
				},
			},
			ProviderOptions: map[string]any{
				"anthropic": map[string]any{
					"auto_cache":   false,
					"beta_headers": "x-test-beta",
				},
			},
		})
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}

		if gotBeta != "x-test-beta" {
			t.Fatalf("anthropic-beta: got %q want %q", gotBeta, "x-test-beta")
		}

		if _, ok := gotBody["system"].(string); !ok {
			t.Fatalf("expected system string when auto_cache=false; got %#v", gotBody["system"])
		}

		if toolsAny, ok := gotBody["tools"].([]any); ok && len(toolsAny) > 0 {
			t0, _ := toolsAny[0].(map[string]any)
			if _, ok := t0["cache_control"]; ok {
				t.Fatalf("unexpected cache_control on tool def when auto_cache=false: %#v", t0["cache_control"])
			}
		}

		msgs, _ := gotBody["messages"].([]any)
		for _, mAny := range msgs {
			m, ok := mAny.(map[string]any)
			if !ok {
				continue
			}
			blocks, _ := m["content"].([]any)
			for _, bAny := range blocks {
				bm, ok := bAny.(map[string]any)
				if !ok {
					continue
				}
				if _, ok := bm["cache_control"]; ok {
					t.Fatalf("unexpected cache_control in messages when auto_cache=false: %#v", bm["cache_control"])
				}
			}
		}
	})
}

func TestAdapter_Stream_ContextDeadline_EmitsRequestTimeoutError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
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

	st, err := a.Stream(ctx, llm.Request{Model: "claude-test", Messages: []llm.Message{llm.User("hi")}})
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

func TestAdapter_UsageCacheTokens_Mapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-test",
  "content": [{"type":"text","text":"ok"}],
  "stop_reason": "end_turn",
  "usage": {
    "input_tokens": 1,
    "output_tokens": 2,
    "cache_read_input_tokens": 30,
    "cache_creation_input_tokens": 20
  }
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := a.Complete(ctx, llm.Request{Model: "claude-test", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Usage.CacheReadTokens == nil || *resp.Usage.CacheReadTokens != 30 {
		t.Fatalf("cache_read_tokens: %#v", resp.Usage.CacheReadTokens)
	}
	if resp.Usage.CacheWriteTokens == nil || *resp.Usage.CacheWriteTokens != 20 {
		t.Fatalf("cache_write_tokens: %#v", resp.Usage.CacheWriteTokens)
	}
}

func TestAdapter_Complete_DefaultMaxTokens_Is4096(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-test",
  "content": [{"type":"text","text":"ok"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 1, "output_tokens": 1}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{
		Model:    "claude-test",
		Messages: []llm.Message{llm.User("hi")},
		// No MaxTokens set - should default to 4096.
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotBody == nil {
		t.Fatalf("server did not capture request body")
	}
	mt, ok := gotBody["max_tokens"].(float64)
	if !ok {
		t.Fatalf("max_tokens not found or not a number: %#v", gotBody["max_tokens"])
	}
	if int(mt) != 4096 {
		t.Fatalf("max_tokens: got %d want 4096", int(mt))
	}
}

func TestAdapter_Complete_FinishReason_Normalized(t *testing.T) {
	cases := []struct {
		name       string
		stopReason string
		wantReason string
		wantRaw    string
	}{
		{"end_turn", "end_turn", "stop", "end_turn"},
		{"stop_sequence", "stop_sequence", "stop", "stop_sequence"},
		{"max_tokens", "max_tokens", "length", "max_tokens"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{
  "id": "msg_1",
  "model": "claude-test",
  "content": [{"type":"text","text":"ok"}],
  "stop_reason": %q,
  "usage": {"input_tokens": 1, "output_tokens": 1}
}`, tc.stopReason)
			}))
			t.Cleanup(srv.Close)

			a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			resp, err := a.Complete(ctx, llm.Request{Model: "claude-test", Messages: []llm.Message{llm.User("hi")}})
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

func TestAdapter_Complete_FinishReason_ToolUse_Normalized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-test",
  "content": [{"type":"tool_use","id":"t1","name":"get_weather","input":{"n":1}}],
  "stop_reason": "tool_use",
  "usage": {"input_tokens": 1, "output_tokens": 1}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := a.Complete(ctx, llm.Request{Model: "claude-test", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Finish.Reason != "tool_calls" {
		t.Fatalf("Finish.Reason = %q, want %q", resp.Finish.Reason, "tool_calls")
	}
	if resp.Finish.Raw != "tool_use" {
		t.Fatalf("Finish.Raw = %q, want %q", resp.Finish.Raw, "tool_use")
	}
}

func TestAdapter_Complete_ParallelToolCalls_ParsedCorrectly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-test",
  "content": [
    {"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"NYC"}},
    {"type":"tool_use","id":"toolu_2","name":"get_time","input":{"tz":"UTC"}}
  ],
  "stop_reason": "tool_use",
  "usage": {"input_tokens": 10, "output_tokens": 20}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := a.Complete(ctx, llm.Request{Model: "claude-test", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	calls := resp.ToolCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(calls))
	}
	if calls[0].ID != "toolu_1" || calls[0].Name != "get_weather" {
		t.Fatalf("call[0]: %+v", calls[0])
	}
	if calls[1].ID != "toolu_2" || calls[1].Name != "get_time" {
		t.Fatalf("call[1]: %+v", calls[1])
	}

	// Verify arguments are preserved
	var args0 map[string]any
	if err := json.Unmarshal(calls[0].Arguments, &args0); err != nil {
		t.Fatalf("unmarshal call[0] args: %v", err)
	}
	if args0["city"] != "NYC" {
		t.Fatalf("call[0] args: %v", args0)
	}

	var args1 map[string]any
	if err := json.Unmarshal(calls[1].Arguments, &args1); err != nil {
		t.Fatalf("unmarshal call[1] args: %v", err)
	}
	if args1["tz"] != "UTC" {
		t.Fatalf("call[1] args: %v", args1)
	}
}

func TestAdapter_Complete_WebSearch_AddsServerTool(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-test",
  "content": [{"type":"text","text":"ok"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 1, "output_tokens": 1}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{
		Model:    "claude-test",
		Messages: []llm.Message{llm.User("search for auth docs")},
		Tools: []llm.ToolDefinition{{
			Name:       "shell",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
		}},
		ToolChoice: &llm.ToolChoice{Mode: "auto"},
		WebSearch:  true,
		ProviderOptions: map[string]any{
			"anthropic": map[string]any{"auto_cache": false},
		},
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

	wsTool, _ := toolsAny[1].(map[string]any)
	if wsTool["type"] != "web_search_20250305" {
		t.Fatalf("ws tool type: got %v", wsTool["type"])
	}
	if wsTool["name"] != "web_search" {
		t.Fatalf("ws tool name: got %v", wsTool["name"])
	}
}

func TestAdapter_Complete_WebSearch_ParsesServerToolUseAndResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-test",
  "content": [
    {"type": "text", "text": "I'll search for that."},
    {
      "type": "server_tool_use",
      "id": "srvtoolu_abc",
      "name": "web_search",
      "input": {"query": "Go error handling best practices"}
    },
    {
      "type": "web_search_tool_result",
      "tool_use_id": "srvtoolu_abc",
      "content": [
        {
          "type": "web_search_result",
          "url": "https://go.dev/blog/error-handling",
          "title": "Error Handling in Go",
          "encrypted_content": "ENCRYPTED_DATA_HERE",
          "page_age": "2024-01-15"
        }
      ]
    },
    {"type": "text", "text": "Based on search results, here is the info."}
  ],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 100, "output_tokens": 200}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := a.Complete(ctx, llm.Request{
		Model:     "claude-test",
		Messages:  []llm.Message{llm.User("search")},
		WebSearch: true,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if len(resp.Message.Content) != 4 {
		t.Fatalf("content parts: got %d want 4", len(resp.Message.Content))
	}

	if resp.Message.Content[0].Kind != llm.ContentText {
		t.Fatalf("part[0] kind: %q", resp.Message.Content[0].Kind)
	}

	ws1 := resp.Message.Content[1]
	if ws1.Kind != llm.ContentWebSearch {
		t.Fatalf("part[1] kind: got %q want web_search", ws1.Kind)
	}
	if ws1.WebSearch == nil {
		t.Fatalf("part[1] web_search is nil")
	}
	if ws1.WebSearch.Query != "Go error handling best practices" {
		t.Fatalf("query: got %q", ws1.WebSearch.Query)
	}

	ws2 := resp.Message.Content[2]
	if ws2.Kind != llm.ContentWebSearch {
		t.Fatalf("part[2] kind: got %q want web_search", ws2.Kind)
	}
	if ws2.WebSearch == nil {
		t.Fatalf("part[2] web_search is nil")
	}
	if !strings.Contains(string(ws2.WebSearch.Raw), "ENCRYPTED_DATA_HERE") {
		t.Fatalf("encrypted_content not preserved in Raw: %s", ws2.WebSearch.Raw)
	}

	if resp.Message.Content[3].Kind != llm.ContentText {
		t.Fatalf("part[3] kind: %q", resp.Message.Content[3].Kind)
	}
}

func TestToAnthropicMessages_WebSearch_ReplayedAsBlocks(t *testing.T) {
	serverToolUseRaw := json.RawMessage(`{"type":"server_tool_use","id":"srvtoolu_abc","name":"web_search","input":{"query":"test"}}`)
	resultRaw := json.RawMessage(`{"type":"web_search_tool_result","tool_use_id":"srvtoolu_abc","content":[{"type":"web_search_result","url":"https://example.com","encrypted_content":"ENC_DATA"}]}`)

	msgs := []llm.Message{
		llm.User("search something"),
		{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "I'll search."},
				{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{Query: "test", Raw: serverToolUseRaw}},
				{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{Raw: resultRaw}},
				{Kind: llm.ContentText, Text: "Found it."},
			},
		},
		llm.User("thanks"),
	}

	_, messages, err := toAnthropicMessages(msgs)
	if err != nil {
		t.Fatalf("toAnthropicMessages: %v", err)
	}

	var assistantBlocks []any
	for _, msgAny := range messages {
		if msgAny["role"] == "assistant" {
			switch c := msgAny["content"].(type) {
			case []map[string]any:
				for _, b := range c {
					assistantBlocks = append(assistantBlocks, b)
				}
			case []any:
				assistantBlocks = c
			}
			break
		}
	}
	if assistantBlocks == nil {
		t.Fatalf("no assistant message found")
	}

	if len(assistantBlocks) != 4 {
		t.Fatalf("assistant blocks: got %d want 4", len(assistantBlocks))
	}

	block1, _ := assistantBlocks[1].(map[string]any)
	if block1["type"] != "server_tool_use" {
		t.Fatalf("block[1] type: %v", block1["type"])
	}
	if block1["id"] != "srvtoolu_abc" {
		t.Fatalf("block[1] id: %v", block1["id"])
	}

	block2, _ := assistantBlocks[2].(map[string]any)
	if block2["type"] != "web_search_tool_result" {
		t.Fatalf("block[2] type: %v", block2["type"])
	}
	content, _ := block2["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("block[2] content empty")
	}
	result, _ := content[0].(map[string]any)
	if result["encrypted_content"] != "ENC_DATA" {
		t.Fatalf("encrypted_content: got %v", result["encrypted_content"])
	}
}

func TestAdapter_Integration_WebSearch(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}

	a, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := a.Complete(ctx, llm.Request{
		Model:    "claude-sonnet-4-5-20250929",
		Messages: []llm.Message{llm.User("Search the web and tell me: what is the current population of Tokyo?")},
		Tools: []llm.ToolDefinition{{
			Name:        "shell",
			Description: "run a shell command",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}},
		}},
		ToolChoice: &llm.ToolChoice{Mode: "auto"},
		WebSearch:  true,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if strings.TrimSpace(resp.Text()) == "" {
		t.Fatalf("expected non-empty text response")
	}

	// Verify we got web search content parts back (server_tool_use + web_search_tool_result).
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

func newAnthropicStreamServer(t *testing.T, lines []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		for _, line := range lines {
			_, _ = io.WriteString(w, line+"\n")
		}
		if f != nil {
			f.Flush()
		}
	}))
}

func TestStream_CapturesIDAndModel(t *testing.T) {
	srv := newAnthropicStreamServer(t, []string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_123","model":"claude-3-5-sonnet-20241022","usage":{"input_tokens":10}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	})
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	stream, err := a.Stream(context.Background(), llm.Request{
		Model:    "claude-3-5-sonnet-20241022",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	var resp *llm.Response
	for ev := range stream.Events() {
		if ev.Response != nil {
			resp = ev.Response
		}
	}
	if resp == nil {
		t.Fatal("no response in finish event")
	}
	if resp.ID != "msg_123" {
		t.Fatalf("ID = %q, want %q", resp.ID, "msg_123")
	}
	if resp.Model != "claude-3-5-sonnet-20241022" {
		t.Fatalf("Model = %q, want actual model from message_start", resp.Model)
	}
}

func TestStream_CapturesCacheTokens(t *testing.T) {
	srv := newAnthropicStreamServer(t, []string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-3-5-sonnet-20241022","usage":{"input_tokens":100,"cache_read_input_tokens":80,"cache_creation_input_tokens":20}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"cached"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	})
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	stream, err := a.Stream(context.Background(), llm.Request{
		Model:    "claude-3-5-sonnet-20241022",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	var resp *llm.Response
	for ev := range stream.Events() {
		if ev.Response != nil {
			resp = ev.Response
		}
	}
	if resp == nil {
		t.Fatal("no response")
	}
	if resp.Usage.CacheReadTokens == nil || *resp.Usage.CacheReadTokens != 80 {
		t.Fatalf("CacheReadTokens = %v, want 80", resp.Usage.CacheReadTokens)
	}
	if resp.Usage.CacheWriteTokens == nil || *resp.Usage.CacheWriteTokens != 20 {
		t.Fatalf("CacheWriteTokens = %v, want 20", resp.Usage.CacheWriteTokens)
	}
}

func TestStream_IncludesRaw(t *testing.T) {
	srv := newAnthropicStreamServer(t, []string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_raw","model":"claude-test","usage":{"input_tokens":5}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	})
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	stream, err := a.Stream(context.Background(), llm.Request{
		Model:    "claude-test",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	var resp *llm.Response
	for ev := range stream.Events() {
		if ev.Response != nil {
			resp = ev.Response
		}
	}
	if resp == nil {
		t.Fatal("no response")
	}
	if resp.Raw == nil {
		t.Fatal("Raw is nil, expected message_start raw message")
	}
	if id, _ := resp.Raw["id"].(string); id != "msg_raw" {
		t.Fatalf("Raw[id] = %q, want %q", id, "msg_raw")
	}
}

func TestStream_HandlesWebSearchBlocks(t *testing.T) {
	srv := newAnthropicStreamServer(t, []string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_ws","model":"claude-test","usage":{"input_tokens":10}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Searching..."}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"Go error handling"}}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":1}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[{"type":"web_search_result","url":"https://go.dev","title":"Go Docs","encrypted_content":"ENC"}]}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":2}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":3,"content_block":{"type":"text"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":"Here is the info."}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":3}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	})
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	stream, err := a.Stream(context.Background(), llm.Request{
		Model:    "claude-test",
		Messages: []llm.Message{llm.User("search")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	var resp *llm.Response
	for ev := range stream.Events() {
		if ev.Response != nil {
			resp = ev.Response
		}
	}
	if resp == nil {
		t.Fatal("no response")
	}

	// Should have 4 content parts: text, server_tool_use (web_search), web_search_tool_result (web_search), text
	if len(resp.Message.Content) != 4 {
		t.Fatalf("content parts = %d, want 4; parts = %+v", len(resp.Message.Content), resp.Message.Content)
	}

	ws1 := resp.Message.Content[1]
	if ws1.Kind != llm.ContentWebSearch {
		t.Fatalf("part[1] kind = %q, want web_search", ws1.Kind)
	}
	if ws1.WebSearch == nil || ws1.WebSearch.Query != "Go error handling" {
		t.Fatalf("part[1] query = %v", ws1.WebSearch)
	}

	ws2 := resp.Message.Content[2]
	if ws2.Kind != llm.ContentWebSearch {
		t.Fatalf("part[2] kind = %q, want web_search", ws2.Kind)
	}
	if ws2.WebSearch == nil || len(ws2.WebSearch.Raw) == 0 {
		t.Fatalf("part[2] raw is empty")
	}
	if !strings.Contains(string(ws2.WebSearch.Raw), "ENC") {
		t.Fatalf("part[2] raw should contain encrypted content: %s", ws2.WebSearch.Raw)
	}
}

func TestComplete_EstimatesReasoningTokens(t *testing.T) {
	thinkingText := "Let me think about this step by step..."
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"id":    "msg_1",
			"model": "claude-3-5-sonnet-20241022",
			"content": []any{
				map[string]any{"type": "thinking", "thinking": thinkingText, "signature": "sig_abc"},
				map[string]any{"type": "text", "text": "The answer is 42."},
			},
			"stop_reason": "end_turn",
			"usage":       map[string]any{"input_tokens": 10, "output_tokens": 20},
		}
		b, _ := json.Marshal(resp)
		w.Write(b)
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	resp, err := a.Complete(context.Background(), llm.Request{
		Model:    "claude-3-5-sonnet-20241022",
		Messages: []llm.Message{llm.User("think hard")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.ReasoningTokens == nil {
		t.Fatal("ReasoningTokens should be estimated, got nil")
	}
	expected := len(thinkingText) / 4
	if *resp.Usage.ReasoningTokens != expected {
		t.Fatalf("ReasoningTokens = %d, want %d (len=%d / 4)", *resp.Usage.ReasoningTokens, expected, len(thinkingText))
	}
}

func TestStream_EstimatesReasoningTokens(t *testing.T) {
	srv := newAnthropicStreamServer(t, []string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_t","model":"claude-test","usage":{"input_tokens":10}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","signature":"sig1"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think step by step about this problem..."}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"42"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":1}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	})
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	stream, err := a.Stream(context.Background(), llm.Request{
		Model:    "claude-test",
		Messages: []llm.Message{llm.User("think")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	var resp *llm.Response
	for ev := range stream.Events() {
		if ev.Response != nil {
			resp = ev.Response
		}
	}
	if resp == nil {
		t.Fatal("no response")
	}
	if resp.Usage.ReasoningTokens == nil {
		t.Fatal("ReasoningTokens should be estimated in streaming, got nil")
	}
	if *resp.Usage.ReasoningTokens <= 0 {
		t.Fatalf("ReasoningTokens = %d, want > 0", *resp.Usage.ReasoningTokens)
	}
}

func TestComplete_ParsesRetryAfterHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(429)
		fmt.Fprint(w, `{"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`)
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	_, err := a.Complete(context.Background(), llm.Request{
		Model:    "claude-3-5-sonnet-20241022",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var e llm.Error
	if !errors.As(err, &e) {
		t.Fatalf("expected llm.Error, got %T", err)
	}
	if !e.Retryable() {
		t.Fatal("429 should be retryable")
	}
	ra := e.RetryAfter()
	if ra == nil {
		t.Fatal("RetryAfter should be set from header")
	}
	if *ra != 30*time.Second {
		t.Fatalf("RetryAfter = %v, want 30s", *ra)
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
		fmt.Fprint(w, `{"id":"msg_1","model":"claude-test","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	resp, err := a.Complete(context.Background(), llm.Request{
		Model:    "claude-test",
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

func TestStream_IncludesWebSearchTool(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		write := func(event, data string) {
			_, _ = io.WriteString(w, "event: "+event+"\n")
			_, _ = io.WriteString(w, "data: "+data+"\n\n")
			if f != nil {
				f.Flush()
			}
		}
		write("content_block_start", `{"index":0,"content_block":{"type":"text"}}`)
		write("content_block_delta", `{"index":0,"delta":{"type":"text_delta","text":"hi"}}`)
		write("content_block_stop", `{"index":0}`)
		write("message_delta", `{"stop_reason":"end_turn","usage":{"output_tokens":1}}`)
		write("message_stop", `{}`)
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{
		Model:    "claude-3-5-sonnet-20241022",
		Messages: []llm.Message{llm.User("search the web")},
		Tools: []llm.ToolDefinition{{
			Name:       "shell",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
		}},
		WebSearch: true,
		ProviderOptions: map[string]any{
			"anthropic": map[string]any{"auto_cache": false},
		},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()

	// Drain events
	for range stream.Events() {
	}

	toolsAny, ok := gotBody["tools"].([]any)
	if !ok {
		t.Fatalf("expected tools array, got: %v", gotBody["tools"])
	}

	found := false
	for _, tool := range toolsAny {
		tm, _ := tool.(map[string]any)
		if tm["type"] == "web_search_20250305" {
			found = true
		}
	}
	if !found {
		t.Fatalf("web_search_20250305 tool not found in Stream() tools: %v", toolsAny)
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
		Model:    "claude-3-5-sonnet-20241022",
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
		Model:    "claude-test",
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

func TestDefaultHeaders_SentOnCompleteRequests(t *testing.T) {
	var capturedHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","model":"claude-test","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
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
		Model:    "claude-test",
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
	if capturedHeaders.Get("x-api-key") != "test-key" {
		t.Errorf("x-api-key = %q, want %q", capturedHeaders.Get("x-api-key"), "test-key")
	}
	if capturedHeaders.Get("anthropic-version") != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want %q", capturedHeaders.Get("anthropic-version"), "2023-06-01")
	}
}

func TestDefaultHeaders_SentOnStreamRequests(t *testing.T) {
	var capturedHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header
		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		write := func(event, data string) {
			_, _ = io.WriteString(w, "event: "+event+"\n")
			_, _ = io.WriteString(w, "data: "+data+"\n\n")
			if f != nil {
				f.Flush()
			}
		}
		write("content_block_start", `{"index":0,"content_block":{"type":"text"}}`)
		write("content_block_delta", `{"index":0,"delta":{"type":"text_delta","text":"hi"}}`)
		write("content_block_stop", `{"index":0}`)
		write("message_delta", `{"stop_reason":"end_turn","usage":{"output_tokens":1}}`)
		write("message_stop", `{}`)
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
		Model:    "claude-test",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	for range stream.Events() {
	}
	if capturedHeaders.Get("X-Custom-Header") != "custom-value" {
		t.Errorf("X-Custom-Header = %q, want %q", capturedHeaders.Get("X-Custom-Header"), "custom-value")
	}
	if capturedHeaders.Get("x-api-key") != "test-key" {
		t.Errorf("x-api-key = %q, want %q", capturedHeaders.Get("x-api-key"), "test-key")
	}
}

func TestDefaultHeaders_CannotOverrideProviderHeaders(t *testing.T) {
	var capturedHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","model":"claude-test","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{
		APIKey:  "real-key",
		BaseURL: srv.URL,
		Client:  srv.Client(),
		DefaultHeaders: map[string]string{
			"x-api-key": "evil-key",
		},
	}
	_, err := a.Complete(context.Background(), llm.Request{
		Model:    "claude-test",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Provider-specific x-api-key must take precedence over DefaultHeaders.
	if capturedHeaders.Get("x-api-key") != "real-key" {
		t.Errorf("x-api-key = %q, want %q (provider auth must take precedence)", capturedHeaders.Get("x-api-key"), "real-key")
	}
}

func TestAdapter_Complete_ToolResultStructuredContent_MarshaledAsJSON(t *testing.T) {
	// When tool result content is a map (not a string), the adapter must
	// JSON-marshal it rather than using fmt.Sprint which produces Go debug format.
	var sentBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &sentBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
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
	t.Cleanup(srv.Close)

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
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Find the tool_result block in the sent messages.
	msgs, _ := sentBody["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatal("expected messages in sent body")
	}
	lastMsg, _ := msgs[len(msgs)-1].(map[string]any)
	content, _ := lastMsg["content"].([]any)
	if len(content) == 0 {
		t.Fatal("expected content blocks in last message")
	}
	block, _ := content[0].(map[string]any)
	contentStr, _ := block["content"].(string)

	// Must be valid JSON, not Go fmt.Sprint output like "map[temperature:72 unit:F]".
	if strings.Contains(contentStr, "map[") {
		t.Errorf("content should be JSON, not Go fmt.Sprint output; got %q", contentStr)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(contentStr), &parsed); err != nil {
		t.Fatalf("content should be valid JSON; got %q, error: %v", contentStr, err)
	}
	if temp, _ := parsed["temperature"].(float64); temp != 72 {
		t.Errorf("temperature = %v, want 72", parsed["temperature"])
	}
	if unit, _ := parsed["unit"].(string); unit != "F" {
		t.Errorf("unit = %q, want %q", parsed["unit"], "F")
	}
}

func contentKinds(parts []llm.ContentPart) []string {
	var kinds []string
	for _, p := range parts {
		kinds = append(kinds, string(p.Kind))
	}
	return kinds
}
