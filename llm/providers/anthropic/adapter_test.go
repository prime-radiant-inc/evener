package anthropic

import (
	"context"
	"encoding/base64"
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

	"primeradiant.com/serf/llm"
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
	// Beta header should pass through from providerOptions (no auto-injection of prompt-caching).
	if gotBeta != "prompt-caching-2024-07-31" {
		t.Fatalf("anthropic-beta header: got %q want pass-through of provider option", gotBeta)
	}
	if gotBody == nil {
		t.Fatalf("server did not capture request body")
	}

	// Top-level cache_control for automatic caching.
	if cc, ok := gotBody["cache_control"].(map[string]any); !ok || cc["type"] != "ephemeral" {
		t.Fatalf("expected top-level cache_control; got %#v", gotBody["cache_control"])
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

	// No manual conversation prefix breakpoints — automatic caching handles this.
	msgsAny, _ := gotBody["messages"].([]any)
	for _, mAny := range msgsAny {
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
				t.Fatalf("unexpected manual cache_control breakpoint in messages; automatic caching should handle this: %#v", bm)
			}
		}
	}
}

// Anthropic rejects forced tool_choice ("any"/"tool") when extended thinking is
// enabled ("Thinking may not be enabled when tool_choice forces tool use"). When
// reasoning effort turns thinking on, a forced tool_choice must be downgraded to
// "auto" so the request is accepted.
func TestAdapter_BuildRequestBody_DowngradesForcedToolChoiceWhenThinking(t *testing.T) {
	effort := "high"
	body, err := (&Adapter{}).buildRequestBody(llm.Request{
		Model:           "some-thinking-model", // not in catalog → legacy budget path
		Messages:        []llm.Message{llm.User("hi")},
		Tools:           []llm.ToolDefinition{{Name: "noop", Description: "x", Parameters: map[string]any{"type": "object"}}},
		ToolChoice:      &llm.ToolChoice{Mode: "required"},
		ReasoningEffort: &effort,
	})
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	if _, ok := body["thinking"]; !ok {
		t.Fatal("expected thinking to be enabled for ReasoningEffort=high")
	}
	tc, _ := body["tool_choice"].(map[string]any)
	if tc == nil || tc["type"] != "auto" {
		t.Fatalf("tool_choice = %#v, want {type: auto} (forced choice downgraded under thinking)", body["tool_choice"])
	}
}

// Without thinking, a forced tool_choice is preserved ("any") — we only downgrade
// when we need to.
func TestAdapter_BuildRequestBody_KeepsForcedToolChoiceWithoutThinking(t *testing.T) {
	body, err := (&Adapter{}).buildRequestBody(llm.Request{
		Model:      "some-model",
		Messages:   []llm.Message{llm.User("hi")},
		Tools:      []llm.ToolDefinition{{Name: "noop", Description: "x", Parameters: map[string]any{"type": "object"}}},
		ToolChoice: &llm.ToolChoice{Mode: "required"},
		// no ReasoningEffort → no thinking
	})
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	if _, ok := body["thinking"]; ok {
		t.Fatal("did not expect thinking without ReasoningEffort")
	}
	tc, _ := body["tool_choice"].(map[string]any)
	if tc == nil || tc["type"] != "any" {
		t.Fatalf("tool_choice = %#v, want {type: any} (forcing preserved without thinking)", body["tool_choice"])
	}
}

// Anthropic requires max_tokens > thinking.budget_tokens. A provider-options
// max_tokens floor (e.g. the Anthropic profile's 16384) must not clobber the
// budget-adjusted value down below the budget, or the request 400s.
func TestAdapter_BuildRequestBody_MaxTokensExceedsThinkingBudget(t *testing.T) {
	effort := "high"
	body, err := (&Adapter{}).buildRequestBody(llm.Request{
		Model:           "some-thinking-model", // legacy budget path
		Messages:        []llm.Message{llm.User("hi")},
		ReasoningEffort: &effort,
		ProviderOptions: map[string]any{"anthropic": map[string]any{"max_tokens": 1000}},
	})
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	th, _ := body["thinking"].(map[string]any)
	budget, _ := th["budget_tokens"].(int)
	if budget <= 0 {
		t.Fatalf("expected a thinking budget, got thinking=%#v", body["thinking"])
	}
	mt, _ := body["max_tokens"].(int)
	if mt <= budget {
		t.Fatalf("max_tokens=%d must exceed thinking budget=%d (provider-opt floor must not clobber it below budget)", mt, budget)
	}
}

func TestAdapter_BuildRequestBody_ServiceTier(t *testing.T) {
	body, err := (&Adapter{}).buildRequestBody(llm.Request{
		Model:       "claude-test",
		Messages:    []llm.Message{llm.User("hi")},
		ServiceTier: " standard_only ",
	})
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	if got := body["service_tier"]; got != "standard_only" {
		t.Fatalf("service_tier = %#v, want standard_only", got)
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
	if llm.Kind(err) != llm.KindAuthentication {
		t.Fatalf("expected AuthenticationError, got %T (%v)", err, err)
	}
	var ae llm.Error
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

func TestAdapter_CountInputTokens_UsesMessagesCountTokensAPI(t *testing.T) {
	var gotBody map[string]any
	gotBeta := ""

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages/count_tokens" {
			t.Fatalf("request = %s %s, want POST /v1/messages/count_tokens", r.Method, r.URL.Path)
		}
		gotBeta = r.Header.Get("anthropic-beta")
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens":123}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	got, err := a.CountInputTokens(ctx, llm.Request{
		Model: "claude-test",
		Messages: []llm.Message{
			llm.System("sys"),
			llm.User("hello"),
		},
		ProviderOptions: map[string]any{
			"anthropic": map[string]any{
				"beta_headers": "token-counting-beta",
			},
		},
	})
	if err != nil {
		t.Fatalf("CountInputTokens: %v", err)
	}
	if got.Tokens != 123 || !got.Exact || got.Source != llm.TokenCountSourceProvider {
		t.Fatalf("CountInputTokens = %+v, want exact provider count", got)
	}
	if got.Provider != "anthropic" || got.Model != "claude-test" {
		t.Fatalf("provider/model = %q/%q, want anthropic/claude-test", got.Provider, got.Model)
	}
	if gotBeta != "token-counting-beta" {
		t.Fatalf("anthropic-beta = %q, want token-counting-beta", gotBeta)
	}
	if gotBody["model"] != "claude-test" {
		t.Fatalf("model = %#v, want claude-test", gotBody["model"])
	}
	if _, ok := gotBody["messages"].([]any); !ok {
		t.Fatalf("messages missing from body: %#v", gotBody)
	}
	if _, ok := gotBody["system"].([]any); !ok {
		t.Fatalf("system missing from body: %#v", gotBody)
	}
	if got.Raw == nil || got.Raw["input_tokens"] == nil {
		t.Fatalf("raw count response missing input_tokens: %#v", got.Raw)
	}
}

func TestAdapter_CountInputTokens_HTTPErrorMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages/count_tokens" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.CountInputTokens(ctx, llm.Request{Model: "claude-test", Messages: []llm.Message{llm.User("hi")}})
	if err == nil {
		t.Fatalf("expected error")
	}
	if llm.Kind(err) != llm.KindRateLimit {
		t.Fatalf("Kind = %v, want %v (err=%v)", llm.Kind(err), llm.KindRateLimit, err)
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
	defer stream.Close() //nolint:errcheck

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

// When extended thinking streams multiple separate thinking blocks, the
// incremental reasoning deltas must be separated by a blank line so the live
// "thinking" view stays readable (mirrors the OpenAI summary-part behavior).
// The request must also enable extended thinking when reasoning effort is set.
func TestAdapter_Stream_EmitsReasoningDeltas_SectionBreakBetweenBlocks(t *testing.T) {
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

		// First thinking block.
		write("content_block_start", `{"index":0,"content_block":{"type":"thinking"}}`)
		write("content_block_delta", `{"index":0,"delta":{"type":"thinking_delta","thinking":"Let me "}}`)
		write("content_block_delta", `{"index":0,"delta":{"type":"thinking_delta","thinking":"think."}}`)
		write("content_block_stop", `{"index":0}`)
		// Second, separate thinking block.
		write("content_block_start", `{"index":1,"content_block":{"type":"thinking"}}`)
		write("content_block_delta", `{"index":1,"delta":{"type":"thinking_delta","thinking":"Then verify."}}`)
		write("content_block_stop", `{"index":1}`)
		// Answer text.
		write("content_block_start", `{"index":2,"content_block":{"type":"text"}}`)
		write("content_block_delta", `{"index":2,"delta":{"type":"text_delta","text":"Answer"}}`)
		write("content_block_stop", `{"index":2}`)

		write("message_delta", `{"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}}`)
		write("message_stop", `{}`)
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	effort := "high"
	stream, err := a.Stream(ctx, llm.Request{Model: "claude-test", ReasoningEffort: &effort, Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close() //nolint:errcheck

	var reasoning strings.Builder
	for ev := range stream.Events() {
		if ev.Type == llm.StreamEventReasoningDelta {
			reasoning.WriteString(ev.ReasoningDelta)
		}
	}
	// The two separate thinking blocks are joined by a blank line.
	if got := reasoning.String(); got != "Let me think.\n\nThen verify." {
		t.Fatalf("reasoning stream = %q, want %q", got, "Let me think.\n\nThen verify.")
	}

	// Extended thinking must be requested when reasoning effort is set, or the
	// API streams no thinking at all.
	th, _ := gotBody["thinking"].(map[string]any)
	if th == nil || th["type"] != "enabled" {
		t.Fatalf("request thinking = %#v, want {type: enabled, budget_tokens: N}", gotBody["thinking"])
	}
	if llm.IntFromAny(th["budget_tokens"]) <= 0 {
		t.Fatalf("request thinking budget_tokens = %#v, want > 0", th["budget_tokens"])
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
	sysText := fmt.Sprint(sb0["text"])
	if !strings.Contains(sysText, "JSON Schema") {
		t.Fatalf("expected schema instructions in system; got %#v", sb0["text"])
	}
	// Verify the schema was actually serialized into the system prompt, not just
	// the "JSON Schema:" header. A mutation omitting string(b) would still pass
	// the check above while sending clients no schema at all.
	if !strings.Contains(sysText, "name") {
		t.Fatalf("expected schema field names serialized in system prompt; got %#v", sb0["text"])
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

func TestAdapter_Complete_ToolParameters_DropsTopLevelCombinators(t *testing.T) {
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
		Tools: []llm.ToolDefinition{{
			Name: "t1",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action": map[string]any{"type": "string"},
				},
				"oneOf": []any{
					map[string]any{"properties": map[string]any{"action": map[string]any{"const": "status"}}},
				},
				"anyOf": []any{
					map[string]any{"properties": map[string]any{"action": map[string]any{"const": "result"}}},
				},
				"allOf": []any{
					map[string]any{"properties": map[string]any{"action": map[string]any{"const": "result"}}},
				},
			},
		}},
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
	if _, ok := schema["oneOf"]; ok {
		t.Fatalf("expected input_schema.oneOf to be removed; got: %#v", schema["oneOf"])
	}
	if _, ok := schema["anyOf"]; ok {
		t.Fatalf("expected input_schema.anyOf to be removed; got: %#v", schema["anyOf"])
	}
	if _, ok := schema["allOf"]; ok {
		t.Fatalf("expected input_schema.allOf to be removed; got: %#v", schema["allOf"])
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

func TestAdapter_PromptCaching_AutomaticCaching(t *testing.T) {
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

	// No prompt-caching beta header (caching is GA).
	if gotBeta != "" {
		t.Fatalf("anthropic-beta: got %q, want empty (caching is GA, no beta header needed)", gotBeta)
	}

	// Top-level cache_control for automatic caching.
	cc, ok := gotBody["cache_control"].(map[string]any)
	if !ok || cc["type"] != "ephemeral" {
		t.Fatalf("expected top-level cache_control; got %#v", gotBody["cache_control"])
	}

	// System block has explicit cache_control breakpoint.
	sysBlocks, ok := gotBody["system"].([]any)
	if !ok || len(sysBlocks) == 0 {
		t.Fatalf("system blocks: %#v", gotBody["system"])
	}
	sb0, _ := sysBlocks[0].(map[string]any)
	if scc, _ := sb0["cache_control"].(map[string]any); scc["type"] != "ephemeral" {
		t.Fatalf("expected cache_control on system block; got %#v", sb0["cache_control"])
	}

	// Last tool has explicit cache_control breakpoint.
	toolsAny, ok := gotBody["tools"].([]any)
	if !ok || len(toolsAny) != 1 {
		t.Fatalf("tools: %#v", gotBody["tools"])
	}
	t0, _ := toolsAny[0].(map[string]any)
	if tcc, _ := t0["cache_control"].(map[string]any); tcc["type"] != "ephemeral" {
		t.Fatalf("expected cache_control on tool def; got %#v", t0["cache_control"])
	}

	// No manual conversation prefix breakpoints in messages.
	msgs, ok := gotBody["messages"].([]any)
	if !ok || len(msgs) == 0 {
		t.Fatalf("messages: %#v", gotBody["messages"])
	}
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
				t.Fatalf("unexpected manual cache_control in messages: %#v", bm)
			}
		}
	}
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
	if llm.Kind(sawErr) != llm.KindTimeout {
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
	requireLiveAnthropic(t)

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
	defer stream.Close() //nolint:errcheck

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
	defer stream.Close() //nolint:errcheck

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
	defer stream.Close() //nolint:errcheck

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
	defer stream.Close() //nolint:errcheck

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

func TestComplete_CacheCreation_SplitsByTTL(t *testing.T) {
	// Anthropic reports cache_creation breakdown when extended-cache-ttl
	// is requested. The adapter must route 5m and 1h writes to their
	// respective fields so downstream pricing uses the correct rate.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"id":    "msg_1",
			"model": "claude-opus-4-5",
			"content": []any{
				map[string]any{"type": "text", "text": "hi"},
			},
			"stop_reason": "end_turn",
			"usage": map[string]any{
				"input_tokens":                2000,
				"output_tokens":               100,
				"cache_read_input_tokens":     0,
				"cache_creation_input_tokens": 3000, // aggregate
				"cache_creation": map[string]any{
					"ephemeral_5m_input_tokens": 1200,
					"ephemeral_1h_input_tokens": 1800,
				},
			},
		}
		b, _ := json.Marshal(resp)
		w.Write(b) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	resp, err := a.Complete(context.Background(), llm.Request{
		Model:    "claude-opus-4-5",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.CacheWriteTokens == nil || *resp.Usage.CacheWriteTokens != 1200 {
		t.Errorf("CacheWriteTokens (5m): got %v, want 1200", resp.Usage.CacheWriteTokens)
	}
	if resp.Usage.CacheWrite1hTokens == nil || *resp.Usage.CacheWrite1hTokens != 1800 {
		t.Errorf("CacheWrite1hTokens: got %v, want 1800", resp.Usage.CacheWrite1hTokens)
	}
}

func TestComplete_CacheCreation_FallbackWhenNoBreakdown(t *testing.T) {
	// Legacy/default behavior: no `cache_creation` breakdown, only the
	// aggregate field. Must route entirely to CacheWriteTokens (5m TTL).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"id":          "msg_1",
			"model":       "claude-opus-4-5",
			"content":     []any{map[string]any{"type": "text", "text": "hi"}},
			"stop_reason": "end_turn",
			"usage": map[string]any{
				"input_tokens":                2000,
				"output_tokens":               100,
				"cache_creation_input_tokens": 3000,
			},
		}
		b, _ := json.Marshal(resp)
		w.Write(b) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	resp, err := a.Complete(context.Background(), llm.Request{
		Model:    "claude-opus-4-5",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.CacheWriteTokens == nil || *resp.Usage.CacheWriteTokens != 3000 {
		t.Errorf("CacheWriteTokens fallback: got %v, want 3000", resp.Usage.CacheWriteTokens)
	}
	if resp.Usage.CacheWrite1hTokens != nil {
		t.Errorf("CacheWrite1hTokens should be nil without breakdown, got %d", *resp.Usage.CacheWrite1hTokens)
	}
}

func TestComplete_ReasoningEstimated_FromThinkingChars(t *testing.T) {
	// Anthropic output_tokens already includes billed thinking tokens, so
	// ReasoningTokens must stay nil (would double-count if added).
	// But we DO surface a rough char-based estimate on the separate
	// ReasoningTokensEstimated field, for display only — never billed.
	thinkingText := "0123456789abcdefghij01234567" // 28 chars / 4 = 7
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"id":    "msg_1",
			"model": "claude-3-5-sonnet-20241022",
			"content": []any{
				map[string]any{"type": "thinking", "thinking": thinkingText, "signature": "sig_abc"},
				map[string]any{"type": "text", "text": "answer"},
			},
			"stop_reason": "end_turn",
			"usage":       map[string]any{"input_tokens": 10, "output_tokens": 20},
		}
		b, _ := json.Marshal(resp)
		w.Write(b) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	resp, err := a.Complete(context.Background(), llm.Request{
		Model:    "claude-3-5-sonnet-20241022",
		Messages: []llm.Message{llm.User("think")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.ReasoningTokens != nil {
		t.Errorf("ReasoningTokens (native) should remain nil, got %d", *resp.Usage.ReasoningTokens)
	}
	if resp.Usage.ReasoningTokensEstimated == nil {
		t.Fatal("ReasoningTokensEstimated should be populated from thinking chars")
	}
	if *resp.Usage.ReasoningTokensEstimated != 7 {
		t.Errorf("estimated reasoning: got %d, want 7 (chars/4)", *resp.Usage.ReasoningTokensEstimated)
	}
}

func TestComplete_ReasoningTokens_NilWhenProviderOmits(t *testing.T) {
	// Without any thinking blocks in the response, both the native field
	// (ReasoningTokens, from provider usage) and the estimated field
	// (ReasoningTokensEstimated, from char-counting thinking content) stay nil.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"id":          "msg_1",
			"model":       "claude-3-5-sonnet-20241022",
			"content":     []any{map[string]any{"type": "text", "text": "The answer is 42."}},
			"stop_reason": "end_turn",
			"usage":       map[string]any{"input_tokens": 10, "output_tokens": 20},
		}
		b, _ := json.Marshal(resp)
		w.Write(b) //nolint:errcheck
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
	if resp.Usage.ReasoningTokens != nil {
		t.Fatalf("ReasoningTokens should be nil (provider didn't report), got %d", *resp.Usage.ReasoningTokens)
	}
	if resp.Usage.ReasoningTokensEstimated != nil {
		t.Fatalf("ReasoningTokensEstimated should be nil (no thinking blocks to estimate from), got %d", *resp.Usage.ReasoningTokensEstimated)
	}
}

func TestStream_ReasoningTokens_NilWhenProviderOmits(t *testing.T) {
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
	defer stream.Close() //nolint:errcheck

	var resp *llm.Response
	for ev := range stream.Events() {
		if ev.Response != nil {
			resp = ev.Response
		}
	}
	if resp == nil {
		t.Fatal("no response")
	}
	if resp.Usage.ReasoningTokens != nil {
		t.Fatalf("ReasoningTokens should be nil in streaming (not natively reported), got %d", *resp.Usage.ReasoningTokens)
	}
}

func TestComplete_ParsesRetryAfterHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`) //nolint:errcheck
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
		fmt.Fprint(w, `{"id":"msg_1","model":"claude-test","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`) //nolint:errcheck
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
	defer stream.Close() //nolint:errcheck

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
		write := func(event, data string) {
			_, _ = io.WriteString(w, "event: "+event+"\n")
			_, _ = io.WriteString(w, "data: "+data+"\n\n")
			if f != nil {
				f.Flush()
			}
		}
		write("message_start", `{"type":"message_start","message":{"id":"msg_1","model":"claude-test","usage":{"input_tokens":1,"output_tokens":0}}}`)
		write("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		write("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`)
		write("content_block_stop", `{"type":"content_block_stop","index":0}`)
		write("message_delta", `{"type":"message_delta","stop_reason":"end_turn","usage":{"output_tokens":1}}`)
		write("message_stop", `{"type":"message_stop"}`)
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "test", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{
		Model:    "claude-test",
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
	defer stream.Close() //nolint:errcheck
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

func TestComplete_UsageRaw_ContainsProviderData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-test",
  "content": [{"type":"text","text":"hi"}],
  "stop_reason": "end_turn",
  "usage": {
    "input_tokens": 10, "output_tokens": 5,
    "cache_read_input_tokens": 3
  }
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	resp, err := a.Complete(context.Background(), llm.Request{
		Model:    "claude-test",
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
	if _, ok := resp.Usage.Raw["cache_read_input_tokens"]; !ok {
		t.Fatalf("Usage.Raw missing cache_read_input_tokens key; got %v", resp.Usage.Raw)
	}
}

func TestAdapter_Complete_WebSearchOnly_IncludesWebSearchTool(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "msg_1", "type": "message", "role": "assistant", "model": "test",
  "content": [{"type":"text","text":"search result"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 10, "output_tokens": 5}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "test-key", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{
		Model:     "test",
		Messages:  []llm.Message{llm.User("search the web")},
		WebSearch: true,
		// Note: no Tools
		ProviderOptions: map[string]any{
			"anthropic": map[string]any{"auto_cache": false},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	toolsAny, ok := gotBody["tools"].([]any)
	if !ok {
		t.Fatalf("tools must be present in request body; got %#v", gotBody["tools"])
	}
	if len(toolsAny) != 1 {
		t.Fatalf("tools count: got %d want 1", len(toolsAny))
	}
	wsTool, _ := toolsAny[0].(map[string]any)
	if wsTool["type"] != "web_search_20250305" {
		t.Fatalf("ws tool type: got %v want web_search_20250305", wsTool["type"])
	}
}

func TestComplete_ReasoningEffort_MappedToThinking(t *testing.T) {
	cases := []struct {
		effort     string
		wantBudget float64
	}{
		{"low", 1024},
		{"medium", 8192},
		{"high", 32768},
	}
	for _, tc := range cases {
		t.Run(tc.effort, func(t *testing.T) {
			var gotBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(b, &gotBody)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{
					"id": "msg_1", "type": "message", "role": "assistant", "model": "test",
					"content": [{"type": "text", "text": "thought"}],
					"stop_reason": "end_turn",
					"usage": {"input_tokens": 10, "output_tokens": 5}
				}`))
			}))
			t.Cleanup(srv.Close)

			a := &Adapter{APIKey: "test-key", BaseURL: srv.URL}
			effort := tc.effort
			_, err := a.Complete(context.Background(), llm.Request{
				Model:           "test",
				Messages:        []llm.Message{llm.User("think hard")},
				ReasoningEffort: &effort,
				ProviderOptions: map[string]any{
					"anthropic": map[string]any{"auto_cache": false},
				},
			})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}

			thinking, ok := gotBody["thinking"].(map[string]any)
			if !ok {
				t.Fatalf("thinking parameter must be present; body keys: %v", gotBody)
			}
			if thinking["type"] != "enabled" {
				t.Fatalf("thinking.type = %v, want enabled", thinking["type"])
			}
			budget, ok := thinking["budget_tokens"].(float64)
			if !ok {
				t.Fatalf("budget_tokens type = %T, want float64", thinking["budget_tokens"])
			}
			if budget != tc.wantBudget {
				t.Fatalf("budget_tokens = %v, want %v", budget, tc.wantBudget)
			}
		})
	}
}

func TestComplete_ReasoningEffort_None_NoThinking(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "test",
			"content": [{"type": "text", "text": "ok"}],
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 10, "output_tokens": 5}
		}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "test-key", BaseURL: srv.URL}
	effort := "none"
	_, err := a.Complete(context.Background(), llm.Request{
		Model:           "test",
		Messages:        []llm.Message{llm.User("hi")},
		ReasoningEffort: &effort,
		ProviderOptions: map[string]any{
			"anthropic": map[string]any{"auto_cache": false},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, ok := gotBody["thinking"]; ok {
		t.Fatalf("thinking should not be set for effort=none, got %v", gotBody["thinking"])
	}
}

func TestComplete_ReasoningEffort_AdjustsMaxTokens(t *testing.T) {
	cases := []struct {
		effort        string
		maxTokens     *int
		wantMaxTokens float64
	}{
		// Default maxTokens (4096) < medium budget (8192): adjusted to budget + default.
		{"medium", nil, 8192 + 4096},
		// Explicit maxTokens (1024) < medium budget (8192): adjusted to budget + explicit.
		{"medium", ptrInt(1024), 8192 + 1024},
		// Explicit maxTokens (10000) > medium budget (8192): no adjustment.
		{"medium", ptrInt(10000), 10000},
		// Low budget (1024) < default maxTokens (4096): no adjustment.
		{"low", nil, 4096},
	}
	for _, tc := range cases {
		name := tc.effort
		if tc.maxTokens != nil {
			name += fmt.Sprintf("_max%d", *tc.maxTokens)
		}
		t.Run(name, func(t *testing.T) {
			var gotBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(b, &gotBody)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{
					"id": "msg_1", "type": "message", "role": "assistant", "model": "test",
					"content": [{"type": "text", "text": "ok"}],
					"stop_reason": "end_turn",
					"usage": {"input_tokens": 10, "output_tokens": 5}
				}`))
			}))
			t.Cleanup(srv.Close)

			a := &Adapter{APIKey: "test-key", BaseURL: srv.URL}
			effort := tc.effort
			req := llm.Request{
				Model:           "test",
				Messages:        []llm.Message{llm.User("think")},
				ReasoningEffort: &effort,
				MaxTokens:       tc.maxTokens,
				ProviderOptions: map[string]any{
					"anthropic": map[string]any{"auto_cache": false},
				},
			}
			_, err := a.Complete(context.Background(), req)
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			gotMax, ok := gotBody["max_tokens"].(float64)
			if !ok {
				t.Fatalf("max_tokens type = %T", gotBody["max_tokens"])
			}
			if gotMax != tc.wantMaxTokens {
				t.Fatalf("max_tokens = %v, want %v", gotMax, tc.wantMaxTokens)
			}
		})
	}
}

func TestStream_ReasoningEffort_MappedToThinking(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"test\",\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		_, _ = fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n")
		_, _ = fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		if f != nil {
			f.Flush()
		}
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "test-key", BaseURL: srv.URL}
	effort := "high"
	stream, err := a.Stream(context.Background(), llm.Request{
		Model:           "test",
		Messages:        []llm.Message{llm.User("think")},
		ReasoningEffort: &effort,
		ProviderOptions: map[string]any{
			"anthropic": map[string]any{"auto_cache": false},
		},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close() //nolint:errcheck
	for range stream.Events() {
	}

	thinking, ok := gotBody["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking parameter must be present; body keys: %v", gotBody)
	}
	if thinking["type"] != "enabled" {
		t.Fatalf("thinking.type = %v, want enabled", thinking["type"])
	}
	budget, ok := thinking["budget_tokens"].(float64)
	if !ok {
		t.Fatalf("budget_tokens type = %T, want float64", thinking["budget_tokens"])
	}
	if budget != 32768 {
		t.Fatalf("budget_tokens = %v, want 32768", budget)
	}
}

func contentKinds(parts []llm.ContentPart) []string {
	var kinds []string
	for _, p := range parts {
		kinds = append(kinds, string(p.Kind))
	}
	return kinds
}

func TestToAnthropicMessages_ToolCallInput_NeverNull(t *testing.T) {
	// Anthropic requires tool_use input to be a dictionary, never null.
	tests := []struct {
		name string
		args json.RawMessage
	}{
		{"valid args", json.RawMessage(`{"city":"Paris"}`)},
		{"empty object", json.RawMessage(`{}`)},
		{"nil args", nil},
		{"empty bytes", json.RawMessage(``)},
		{"malformed args", json.RawMessage(`{"status": in_progress"}`)},
		{"non-object args", json.RawMessage(`["status"]`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgs := []llm.Message{
				llm.User("hi"),
				{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{
						{
							Kind: llm.ContentToolCall,
							ToolCall: &llm.ToolCallData{
								ID:        "toolu_1",
								Name:      "get_weather",
								Arguments: tt.args,
								Type:      "function",
							},
						},
					},
				},
			}

			_, messages, err := toAnthropicMessages(msgs)
			if err != nil {
				t.Fatalf("toAnthropicMessages: %v", err)
			}

			// Find the assistant message and check the tool_use input field.
			for _, msg := range messages {
				if msg["role"] != "assistant" {
					continue
				}
				blocks, _ := msg["content"].([]map[string]any)
				for _, block := range blocks {
					if block["type"] != "tool_use" {
						continue
					}
					input := block["input"]
					if input == nil {
						t.Fatalf("tool_use input is nil; Anthropic requires a dictionary")
					}
					// Must be a map (dictionary), not a string or other type.
					if _, ok := input.(map[string]any); !ok {
						t.Fatalf("tool_use input is %T, want map[string]any", input)
					}
				}
			}
		})
	}
}

func TestStream_ToolCall_ArgsNotCorrupted(t *testing.T) {
	// Regression: content_block_start sends input:{} as placeholder. If captured,
	// it prefixes actual args from input_json_delta, producing invalid JSON.
	sseLines := []string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","model":"claude-test","content":[],"usage":{"input_tokens":10,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"Paris\"}"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":20}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range sseLines {
			fmt.Fprintln(w, line)
		}
	}))
	defer srv.Close()

	a := &Adapter{BaseURL: srv.URL, APIKey: "test"}

	ctx := context.Background()
	st, err := a.Stream(ctx, llm.Request{Model: "claude-test", Messages: []llm.Message{llm.User("weather in Paris")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	acc := llm.NewStreamAccumulator()
	for ev := range st.Events() {
		acc.Process(ev)
	}
	_ = st.Close()

	resp := acc.Response()
	if resp == nil {
		t.Fatal("no response")
	}

	calls := resp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].Name != "get_weather" {
		t.Fatalf("tool call name: %s", calls[0].Name)
	}

	// The key assertion: Arguments must be valid JSON {"city":"Paris"},
	// NOT corrupted with a leading {} from the content_block_start placeholder.
	var args map[string]any
	if err := json.Unmarshal(calls[0].Arguments, &args); err != nil {
		t.Fatalf("unmarshal tool args %q: %v", string(calls[0].Arguments), err)
	}
	if args["city"] != "Paris" {
		t.Fatalf("tool args: %v", args)
	}

	// Also verify args don't start with the placeholder {}.
	argsStr := string(calls[0].Arguments)
	if strings.HasPrefix(argsStr, "{}") {
		t.Fatalf("tool args corrupted with placeholder: %s", argsStr)
	}
}

func TestAdapter_ListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("x-api-key") != "test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"data": [
				{"id": "claude-sonnet-4-6-20250514", "display_name": "Claude Sonnet 4.6", "type": "model"},
				{"id": "claude-haiku-4-5-20251001", "display_name": "Claude Haiku 4.5", "type": "model"}
			],
			"has_more": false
		}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "test-key", BaseURL: srv.URL, Client: srv.Client()}
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	// 2 API models + 1 synthetic [1m] variant (sonnet, not haiku).
	if len(models) != 3 {
		ids := make([]string, len(models))
		for i, m := range models {
			ids[i] = m.ID
		}
		t.Fatalf("got %d models, want 3: %v", len(models), ids)
	}
	// Sorted alphabetically by ID
	if models[0].ID != "claude-haiku-4-5-20251001" {
		t.Errorf("models[0].ID = %q, want claude-haiku-4-5-20251001 (sorted)", models[0].ID)
	}
	if models[0].DisplayName != "Claude Haiku 4.5" {
		t.Errorf("models[0].DisplayName = %q, want 'Claude Haiku 4.5'", models[0].DisplayName)
	}
	for _, m := range models {
		if m.Provider != "anthropic" {
			t.Errorf("model %s: provider = %q, want anthropic", m.ID, m.Provider)
		}
	}
}

func TestAdapter_ListModels_Pagination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("x-api-key") != "test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		afterID := r.URL.Query().Get("after_id")
		w.Header().Set("Content-Type", "application/json")

		switch afterID {
		case "":
			// First page: return 2 models, signal more available.
			w.Write([]byte(`{
				"data": [
					{"id": "claude-haiku-4-5-20251001", "display_name": "Claude Haiku 4.5", "type": "model"},
					{"id": "claude-opus-4-6-20260205", "display_name": "Claude Opus 4.6", "type": "model"}
				],
				"has_more": true,
				"first_id": "claude-haiku-4-5-20251001",
				"last_id": "claude-opus-4-6-20260205"
			}`))
		case "claude-opus-4-6-20260205":
			// Second page: return 1 more model, no more pages.
			w.Write([]byte(`{
				"data": [
					{"id": "claude-sonnet-4-6-20260205", "display_name": "Claude Sonnet 4.6", "type": "model"}
				],
				"has_more": false,
				"first_id": "claude-sonnet-4-6-20260205",
				"last_id": "claude-sonnet-4-6-20260205"
			}`))
		default:
			t.Errorf("unexpected after_id: %q", afterID)
			w.Write([]byte(`{"data": [], "has_more": false}`))
		}
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "test-key", BaseURL: srv.URL, Client: srv.Client()}
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	// 3 API models + 2 synthetic [1m] variants (opus + sonnet, not haiku).
	if len(models) != 5 {
		allIDs := make([]string, len(models))
		for i, m := range models {
			allIDs[i] = m.ID
		}
		t.Fatalf("got %d models, want 5 (should paginate + generate 1M variants): %v", len(models), allIDs)
	}
	// All models present and sorted.
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	want := []string{
		"claude-haiku-4-5-20251001",
		"claude-opus-4-6-20260205",
		"claude-opus-4-6-20260205[1m]",
		"claude-sonnet-4-6-20260205",
		"claude-sonnet-4-6-20260205[1m]",
	}
	for i, id := range ids {
		if id != want[i] {
			t.Errorf("models[%d].ID = %q, want %q", i, id, want[i])
		}
	}
}

func TestBuildRequestBody_Strips1MSuffix(t *testing.T) {
	a := &Adapter{APIKey: "k", BaseURL: "http://localhost"}
	body, err := a.buildRequestBody(llm.Request{
		Model:    "claude-opus-4-6[1m]",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	model, _ := body["model"].(string)
	if model != "claude-opus-4-6" {
		t.Fatalf("model in body: got %q, want %q", model, "claude-opus-4-6")
	}
}

func TestBuildRequestBody_NoSuffix_Unchanged(t *testing.T) {
	a := &Adapter{APIKey: "k", BaseURL: "http://localhost"}
	body, err := a.buildRequestBody(llm.Request{
		Model:    "claude-opus-4-6",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	model, _ := body["model"].(string)
	if model != "claude-opus-4-6" {
		t.Fatalf("model in body: got %q, want %q", model, "claude-opus-4-6")
	}
}

func TestListModels_Generates1MVariants(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"data": [
				{"id": "claude-opus-4-6-20260205", "display_name": "Claude Opus 4.6"},
				{"id": "claude-sonnet-4-6-20260205", "display_name": "Claude Sonnet 4.6"},
				{"id": "claude-haiku-4-5-20251001", "display_name": "Claude Haiku 4.5"}
			],
			"has_more": false
		}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	// Should have original 3 + 2 synthetic [1m] variants (opus + sonnet, not haiku).
	if len(models) != 5 {
		ids := make([]string, len(models))
		for i, m := range models {
			ids[i] = m.ID
		}
		t.Fatalf("got %d models, want 5: %v", len(models), ids)
	}

	// Check that [1m] variants exist for opus and sonnet.
	idSet := map[string]bool{}
	for _, m := range models {
		idSet[m.ID] = true
	}
	if !idSet["claude-opus-4-6-20260205[1m]"] {
		t.Error("missing claude-opus-4-6-20260205[1m]")
	}
	if !idSet["claude-sonnet-4-6-20260205[1m]"] {
		t.Error("missing claude-sonnet-4-6-20260205[1m]")
	}
	// Haiku should NOT have a 1M variant.
	if idSet["claude-haiku-4-5-20251001[1m]"] {
		t.Error("haiku should not have a [1m] variant")
	}

	// Check display name for a 1M variant.
	for _, m := range models {
		if m.ID == "claude-opus-4-6-20260205[1m]" {
			if !strings.Contains(m.DisplayName, "1M context") {
				t.Errorf("1M variant display name: %q", m.DisplayName)
			}
		}
	}
}

func TestAdapter_Integration_PromptCaching(t *testing.T) {
	requireLiveAnthropic(t)

	a, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	// System prompt must exceed the minimum cacheable token count.
	// Haiku 4.5 requires 4096 tokens; at ~4 chars/token we need ~20K chars.
	var sb strings.Builder
	sb.WriteString("You are a helpful assistant.\n\n")
	paragraph := "This is a test of the Anthropic prompt caching feature. " +
		"The system prompt must be long enough to exceed the minimum token " +
		"threshold for caching to activate. Prompt caching allows the API to " +
		"reuse previously processed context across requests, reducing latency " +
		"and cost for subsequent turns in a conversation. When caching is " +
		"active, the API response includes cache_read_input_tokens and " +
		"cache_creation_input_tokens in the usage object.\n\n"
	for sb.Len() < 20000 {
		sb.WriteString(paragraph)
	}
	systemPrompt := sb.String()

	tools := []llm.ToolDefinition{{
		Name:        "shell",
		Description: "Run a shell command and return its output.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "the shell command to run"},
			},
			"required": []string{"command"},
		},
	}}

	req := llm.Request{
		Model: "claude-haiku-4-5-20251001",
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: systemPrompt}}},
			llm.User("Say hello in exactly one word."),
		},
		Tools:      tools,
		ToolChoice: &llm.ToolChoice{Mode: "auto"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	derefOr := func(p *int, fallback int) int {
		if p == nil {
			return fallback
		}
		return *p
	}

	// Turn 1: cache write (creates the cache entry).
	resp1, err := a.Complete(ctx, req)
	if err != nil {
		t.Fatalf("Complete (turn 1): %v", err)
	}
	t.Logf("turn 1 usage: input=%d output=%d cache_write=%d cache_read=%d raw=%v",
		resp1.Usage.InputTokens, resp1.Usage.OutputTokens,
		derefOr(resp1.Usage.CacheWriteTokens, -1),
		derefOr(resp1.Usage.CacheReadTokens, -1),
		resp1.Usage.Raw)

	if resp1.Usage.CacheWriteTokens == nil || *resp1.Usage.CacheWriteTokens == 0 {
		t.Fatalf("turn 1: expected cache_creation_input_tokens > 0, got %d",
			derefOr(resp1.Usage.CacheWriteTokens, 0))
	}

	// Turn 2: same system + tools, different user message → cache read.
	req.Messages = []llm.Message{
		{Role: llm.RoleSystem, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: systemPrompt}}},
		llm.User("Say goodbye in exactly one word."),
	}

	resp2, err := a.Complete(ctx, req)
	if err != nil {
		t.Fatalf("Complete (turn 2): %v", err)
	}
	t.Logf("turn 2 usage: input=%d output=%d cache_write=%d cache_read=%d raw=%v",
		resp2.Usage.InputTokens, resp2.Usage.OutputTokens,
		derefOr(resp2.Usage.CacheWriteTokens, -1),
		derefOr(resp2.Usage.CacheReadTokens, -1),
		resp2.Usage.Raw)

	if resp2.Usage.CacheReadTokens == nil || *resp2.Usage.CacheReadTokens == 0 {
		t.Fatalf("turn 2: expected cache_read_input_tokens > 0, got %d",
			derefOr(resp2.Usage.CacheReadTokens, 0))
	}
}

func TestAdapter_Complete_ToolResultWithImageData(t *testing.T) {
	var sentBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &sentBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-test",
  "content": [{"type":"text","text":"I see an image"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 10, "output_tokens": 5}
}`))
	}))
	t.Cleanup(srv.Close)

	imgBytes := []byte{0x89, 0x50, 0x4e, 0x47} // PNG magic bytes

	// Build a tool result message with image data.
	toolResultMsg := llm.ToolResult("call_img", "Image file: photo.png (PNG, 4 bytes)", false)
	toolResultMsg.Content[0].ToolResult.ImageData = imgBytes
	toolResultMsg.Content[0].ToolResult.ImageMediaType = "image/png"

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{
		Model: "claude-test",
		Messages: []llm.Message{
			llm.User("Read this image"),
			{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
				Kind:     llm.ContentToolCall,
				ToolCall: &llm.ToolCallData{ID: "call_img", Name: "read_file", Arguments: json.RawMessage(`{"path":"photo.png"}`)},
			}}},
			toolResultMsg,
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Find the tool_result block in the sent request body.
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
	if block["type"] != "tool_result" {
		t.Fatalf("block type: got %v want tool_result", block["type"])
	}
	if block["tool_use_id"] != "call_img" {
		t.Fatalf("tool_use_id: got %v want call_img", block["tool_use_id"])
	}

	// Content must be an array (not a string) when image data is present.
	contentArr, ok := block["content"].([]any)
	if !ok {
		t.Fatalf("tool_result content should be an array when image data is present; got %T: %v", block["content"], block["content"])
	}
	if len(contentArr) != 2 {
		t.Fatalf("tool_result content array: got %d items want 2", len(contentArr))
	}

	// First element: text block.
	textBlock, _ := contentArr[0].(map[string]any)
	if textBlock["type"] != "text" {
		t.Fatalf("content[0] type: got %v want text", textBlock["type"])
	}
	if textBlock["text"] != "Image file: photo.png (PNG, 4 bytes)" {
		t.Fatalf("content[0] text: got %v", textBlock["text"])
	}

	// Second element: image block.
	imgBlock, _ := contentArr[1].(map[string]any)
	if imgBlock["type"] != "image" {
		t.Fatalf("content[1] type: got %v want image", imgBlock["type"])
	}
	src, _ := imgBlock["source"].(map[string]any)
	if src["type"] != "base64" {
		t.Fatalf("image source type: got %v want base64", src["type"])
	}
	if src["media_type"] != "image/png" {
		t.Fatalf("image media_type: got %v want image/png", src["media_type"])
	}
	wantB64 := base64.StdEncoding.EncodeToString(imgBytes)
	if src["data"] != wantB64 {
		t.Fatalf("image data: got %v want %v", src["data"], wantB64)
	}
}

func TestAdapter_Complete_ToolResultWithImageData_DefaultMediaType(t *testing.T) {
	var sentBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &sentBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-test",
  "content": [{"type":"text","text":"ok"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 10, "output_tokens": 5}
}`))
	}))
	t.Cleanup(srv.Close)

	imgBytes := []byte{0x89, 0x50, 0x4e, 0x47}

	// Build a tool result message with image data but NO media type set.
	toolResultMsg := llm.ToolResult("call_img2", "screenshot.png", false)
	toolResultMsg.Content[0].ToolResult.ImageData = imgBytes
	// ImageMediaType left empty — should default to "image/png".

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{
		Model: "claude-test",
		Messages: []llm.Message{
			llm.User("Read this"),
			{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
				Kind:     llm.ContentToolCall,
				ToolCall: &llm.ToolCallData{ID: "call_img2", Name: "read_file", Arguments: json.RawMessage(`{}`)},
			}}},
			toolResultMsg,
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	msgs, _ := sentBody["messages"].([]any)
	lastMsg, _ := msgs[len(msgs)-1].(map[string]any)
	content, _ := lastMsg["content"].([]any)
	block, _ := content[0].(map[string]any)
	contentArr, ok := block["content"].([]any)
	if !ok {
		t.Fatalf("tool_result content should be an array; got %T", block["content"])
	}
	imgBlock, _ := contentArr[1].(map[string]any)
	src, _ := imgBlock["source"].(map[string]any)
	if src["media_type"] != "image/png" {
		t.Fatalf("default media_type: got %v want image/png", src["media_type"])
	}
}

func TestAdapter_Complete_WebSearchWithTools_NoDuplicateNames(t *testing.T) {
	// When WebSearch is true and function tools are present, the adapter
	// injects a server-side web_search tool. If the caller also passes a
	// function tool named "web_search", the API rejects with
	// "Tool names must be unique." This test verifies the adapter strips
	// the function-type web_search to prevent the collision.
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "msg_1", "type": "message", "role": "assistant", "model": "test",
  "content": [{"type":"text","text":"ok"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 1, "output_tokens": 1}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Simulate a tool list that includes a function-type web_search alongside
	// other function tools, plus WebSearch=true which triggers the adapter to
	// inject its own server-side web_search.
	_, err := a.Complete(ctx, llm.Request{
		Model:    "claude-test",
		Messages: []llm.Message{llm.User("search for docs")},
		Tools: []llm.ToolDefinition{
			{Name: "shell", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}},
			{Name: "web_search", Description: "function-type shim", Parameters: map[string]any{
				"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}},
			}},
		},
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

	// Check for duplicate names.
	seen := make(map[string]bool)
	for _, tAny := range toolsAny {
		tm, _ := tAny.(map[string]any)
		name, _ := tm["name"].(string)
		if name == "" {
			continue
		}
		if seen[name] {
			t.Errorf("duplicate tool name %q in Anthropic request body", name)
		}
		seen[name] = true
	}

	// The server-side web_search should be present.
	if !seen["web_search"] {
		t.Error("server-side web_search tool not found in request body")
	}
}

func ptrInt(i int) *int { return &i }

// ================== Catalog-driven adaptive/hybrid thinking tests ==================

func TestBuildRequestBody_AdaptiveThinking_Opus46(t *testing.T) {
	// Test opus-4-6 with effort => adaptive thinking + output_config.
	a := &Adapter{APIKey: "test", BaseURL: "https://api.anthropic.com"}
	effort := "medium"
	req := llm.Request{
		Model:           "claude-opus-4-6",
		Messages:        []llm.Message{{Role: "user", Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi"}}}},
		ReasoningEffort: &effort,
	}
	body, err := a.buildRequestBody(req)
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}

	// Should emit adaptive thinking.
	thinking, ok := body["thinking"].(map[string]any)
	if !ok {
		t.Fatal("expected thinking block")
	}
	if thinking["type"] != "adaptive" {
		t.Fatalf("expected thinking.type=adaptive, got %v", thinking["type"])
	}
	if _, hasBudget := thinking["budget_tokens"]; hasBudget {
		t.Fatal("adaptive thinking should not have budget_tokens")
	}

	// Should emit output_config.effort.
	outputConfig, ok := body["output_config"].(map[string]any)
	if !ok {
		t.Fatal("expected output_config block for opus-4-6")
	}
	if outputConfig["effort"] != "medium" {
		t.Fatalf("expected output_config.effort=medium, got %v", outputConfig["effort"])
	}
}

func TestBuildRequestBody_AdaptiveThinking_NoEffort(t *testing.T) {
	// Test opus-4-6 without effort => adaptive thinking only, no output_config.
	a := &Adapter{APIKey: "test", BaseURL: "https://api.anthropic.com"}
	req := llm.Request{
		Model:    "claude-opus-4-6",
		Messages: []llm.Message{{Role: "user", Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi"}}}},
		// No ReasoningEffort
	}
	body, err := a.buildRequestBody(req)
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}

	// Should still emit adaptive thinking.
	thinking, ok := body["thinking"].(map[string]any)
	if !ok {
		t.Fatal("expected thinking block for opus-4-6")
	}
	if thinking["type"] != "adaptive" {
		t.Fatalf("expected thinking.type=adaptive, got %v", thinking["type"])
	}

	// No output_config when no effort specified.
	if _, ok := body["output_config"]; ok {
		t.Fatal("should not emit output_config when no effort specified")
	}
}

func TestBuildRequestBody_HybridThinking_Opus45(t *testing.T) {
	// Test opus-4-5 with effort => manual thinking + output_config (hybrid).
	a := &Adapter{APIKey: "test", BaseURL: "https://api.anthropic.com"}
	effort := "medium"
	req := llm.Request{
		Model:           "claude-opus-4-5",
		Messages:        []llm.Message{{Role: "user", Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi"}}}},
		ReasoningEffort: &effort,
	}
	body, err := a.buildRequestBody(req)
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}

	// Should emit manual thinking with budget.
	thinking, ok := body["thinking"].(map[string]any)
	if !ok {
		t.Fatal("expected thinking block")
	}
	if thinking["type"] != "enabled" {
		t.Fatalf("expected thinking.type=enabled, got %v", thinking["type"])
	}
	budget, ok := thinking["budget_tokens"].(int)
	if !ok || budget <= 0 {
		t.Fatalf("expected budget_tokens > 0, got %v", thinking["budget_tokens"])
	}

	// Should also emit output_config.effort (hybrid).
	outputConfig, ok := body["output_config"].(map[string]any)
	if !ok {
		t.Fatal("expected output_config block for opus-4-5 (hybrid)")
	}
	if outputConfig["effort"] != "medium" {
		t.Fatalf("expected output_config.effort=medium, got %v", outputConfig["effort"])
	}
}

func TestBuildRequestBody_LegacyThinking_Sonnet45(t *testing.T) {
	// Test sonnet-4-5 with effort => manual thinking only, no output_config.
	a := &Adapter{APIKey: "test", BaseURL: "https://api.anthropic.com"}
	effort := "medium"
	req := llm.Request{
		Model:           "claude-sonnet-4-5",
		Messages:        []llm.Message{{Role: "user", Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi"}}}},
		ReasoningEffort: &effort,
	}
	body, err := a.buildRequestBody(req)
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}

	// Should emit manual thinking with budget.
	thinking, ok := body["thinking"].(map[string]any)
	if !ok {
		t.Fatal("expected thinking block")
	}
	if thinking["type"] != "enabled" {
		t.Fatalf("expected thinking.type=enabled, got %v", thinking["type"])
	}
	budget, ok := thinking["budget_tokens"].(int)
	if !ok || budget <= 0 {
		t.Fatalf("expected budget_tokens > 0, got %v", thinking["budget_tokens"])
	}

	// Should NOT emit output_config for older models.
	if _, ok := body["output_config"]; ok {
		t.Fatal("sonnet-4-5 should not emit output_config")
	}
}

func TestBuildRequestBody_EffortClamping_Opus45(t *testing.T) {
	// Test opus-4-5 with effort "max" => should clamp to "high" since it only supports [low, medium, high].
	a := &Adapter{APIKey: "test", BaseURL: "https://api.anthropic.com"}
	effort := "max"
	req := llm.Request{
		Model:           "claude-opus-4-5",
		Messages:        []llm.Message{{Role: "user", Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi"}}}},
		ReasoningEffort: &effort,
	}
	body, err := a.buildRequestBody(req)
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}

	// output_config should have clamped effort to "high".
	outputConfig, ok := body["output_config"].(map[string]any)
	if !ok {
		t.Fatal("expected output_config block")
	}
	if outputConfig["effort"] != "high" {
		t.Fatalf("expected effort clamped to 'high', got %v", outputConfig["effort"])
	}

	// budget should also be for "high", not "max".
	thinking, ok := body["thinking"].(map[string]any)
	if !ok {
		t.Fatal("expected thinking block")
	}
	budgetHigh := llm.ReasoningBudget("high")
	budgetMax := llm.ReasoningBudget("max")
	budget, _ := thinking["budget_tokens"].(int)
	if budget != budgetHigh {
		t.Fatalf("expected budget_tokens=%d (high), got %d", budgetHigh, budget)
	}
	if budget == budgetMax {
		t.Fatal("budget_tokens should not be max value after clamping")
	}
}

func TestClampEffort(t *testing.T) {
	tests := []struct {
		name      string
		requested string
		supported []string
		want      string
	}{
		{
			name:      "empty supported returns as-is",
			requested: "max",
			supported: nil,
			want:      "max",
		},
		{
			name:      "supported level passes through",
			requested: "medium",
			supported: []string{"low", "medium", "high"},
			want:      "medium",
		},
		{
			name:      "max clamped to high",
			requested: "max",
			supported: []string{"low", "medium", "high"},
			want:      "high",
		},
		{
			name:      "max allowed when supported",
			requested: "max",
			supported: []string{"low", "medium", "high", "max"},
			want:      "max",
		},
		{
			name:      "high clamped to medium",
			requested: "high",
			supported: []string{"low", "medium"},
			want:      "medium",
		},
		{
			name:      "xhigh clamped down to high when model tops out at high",
			requested: "xhigh",
			supported: []string{"low", "medium", "high"},
			want:      "high",
		},
		{
			name:      "unknown level passes through",
			requested: "turbo",
			supported: []string{"low", "medium", "high"},
			want:      "turbo",
		},
		{
			name:      "minimal raised to lowest supported",
			requested: "minimal",
			supported: []string{"low", "medium", "high", "max"},
			want:      "low",
		},
		{
			name:      "exact match at the bottom rank is not raised further",
			requested: "minimal",
			supported: []string{"minimal", "low", "medium"},
			want:      "minimal",
		},
		{
			name:      "max maps to the model's xhigh spelling at the top rank",
			requested: "max",
			supported: []string{"low", "medium", "high", "xhigh"},
			want:      "xhigh",
		},
		{
			name:      "case insensitive",
			requested: "MAX",
			supported: []string{"Low", "Medium", "High"},
			want:      "high",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clampEffort(tt.requested, tt.supported)
			if got != tt.want {
				t.Errorf("clampEffort(%q, %v) = %q, want %q", tt.requested, tt.supported, got, tt.want)
			}
		})
	}
}

func TestNewForInstance_Anthropic_Name(t *testing.T) {
	a, err := NewForInstance(AnthropicInstanceParams{
		Name:   "work",
		APIKey: "sk-x",
	})
	if err != nil {
		t.Fatalf("NewForInstance: %v", err)
	}
	if a.Name() != "work" {
		t.Fatalf("Name() = %q, want %q", a.Name(), "work")
	}
}

func TestNewFromEnv_Anthropic_Name(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-env")
	a, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	if a.Name() != "anthropic" {
		t.Fatalf("Name() = %q, want %q", a.Name(), "anthropic")
	}
}
