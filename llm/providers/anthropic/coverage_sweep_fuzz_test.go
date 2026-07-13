package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	_ "unsafe"

	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

//go:linkname sweepRawBodyEnabled primeradiant.com/serf/llm.rawBodyEnabled
var sweepRawBodyEnabled bool

type sweepRoundTripFunc func(*http.Request) (*http.Response, error)

func (f sweepRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func sweepResponse(r *http.Request, status int, body string) (*http.Response, error) {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}, nil
}

func anthropicCoverageSweep(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	if _, err := NewForInstance(AnthropicInstanceParams{}); err == nil {
		t.Fatal("empty API key was accepted")
	}
	a, err := NewForInstance(AnthropicInstanceParams{
		Name: "named", APIKey: " key ", BaseURL: " https://anthropic.test/// ",
		Headers: map[string]string{"X-Sweep": "yes"},
	})
	if err != nil || a.Name() != "named" || a.BaseURL != "https://anthropic.test" {
		t.Fatalf("NewForInstance normalization: adapter=%+v err=%v", a, err)
	}
	def, err := NewForInstance(AnthropicInstanceParams{APIKey: "k"})
	if err != nil || def.Name() != "anthropic" || def.BaseURL != "https://api.anthropic.com" {
		t.Fatalf("NewForInstance defaults: adapter=%+v err=%v", def, err)
	}

	t.Setenv(envvars.AnthropicAPIKey.Name, "")
	_, _ = llm.NewFromEnv()
	t.Setenv(envvars.AnthropicAPIKey.Name, "env-key")
	t.Setenv(envvars.AnthropicBaseURL.Name, "https://env.anthropic.test/")
	if _, err := llm.NewFromEnv(); err != nil {
		t.Fatalf("registered env factory with base URL: %v", err)
	}
	fromEnv, err := NewFromEnv()
	if err != nil || fromEnv.Name() != "anthropic" || fromEnv.BaseURL != "https://env.anthropic.test" {
		t.Fatalf("NewFromEnv: adapter=%+v err=%v", fromEnv, err)
	}
	t.Setenv(envvars.AnthropicBaseURL.Name, "")
	if _, err := llm.NewFromEnv(); err != nil {
		t.Fatalf("registered env factory with default base URL: %v", err)
	}
	client, err := llm.NewFromProviders(providercfg.Config{Instances: []providercfg.InstanceConfig{{
		Name: "configured", Type: "anthropic", APIKey: "k", BaseURL: "https://configured.test",
	}}})
	if err != nil || client == nil {
		t.Fatalf("registered instance factory: client=%v err=%v", client, err)
	}

	hreq, _ := http.NewRequest(http.MethodPost, "https://example.test", nil)
	a.setAnthropicHeaders(hreq, map[string]any{"anthropic": map[string]any{"beta_headers": []any{" one ", 2, "", "two"}}})
	if hreq.Header.Get("X-Sweep") != "yes" || hreq.Header.Get("anthropic-beta") != "one,two" || hreq.Header.Get("x-api-key") != " key " {
		t.Fatalf("headers not preserved: %v", hreq.Header)
	}

	if intFromAny(3) != 3 || intFromAny(int64(4)) != 4 || intFromAny(json.Number("5")) != 5 || intFromAny(struct{}{}) != 0 {
		t.Fatal("intFromAny conversion mismatch")
	}

	requestCoverageSweep(t, a)
	responseCoverageSweep(t)
	transportCoverageSweep(ctx, t)
	streamCoverageSweep(ctx, t)
}

func requestCoverageSweep(t *testing.T, a *Adapter) {
	t.Helper()
	badRF := llm.ResponseFormat{Type: "json_schema", JSONSchema: map[string]any{"bad": make(chan int)}}
	if _, err := a.buildRequestBody(llm.Request{ResponseFormat: &badRF}); err == nil {
		t.Fatal("unmarshalable response schema was accepted")
	}
	for _, tc := range []*llm.ToolChoice{{Mode: "named"}, {Mode: "unsupported"}} {
		if _, err := a.buildRequestBody(llm.Request{Tools: []llm.ToolDefinition{{Name: "x"}}, ToolChoice: tc}); err == nil {
			t.Fatalf("invalid tool choice was accepted: %+v", tc)
		}
	}
	jsonRF := llm.ResponseFormat{Type: "json_schema"}
	service := "standard_only"
	effort := "high"
	_, err := a.buildRequestBody(llm.Request{
		Model: "claude-sonnet-5", ServiceTier: service, ReasoningEffort: &effort,
		ResponseFormat: &jsonRF,
	})
	if err != nil {
		t.Fatalf("claude 5 request: %v", err)
	}
	_, err = a.buildRequestBody(llm.Request{
		Model: "claude-opus-4-5", ReasoningEffort: &effort,
		ProviderOptions: map[string]any{"anthropic": map[string]any{"beta_headers": "skip"}},
	})
	if err != nil {
		t.Fatalf("hybrid thinking request: %v", err)
	}
	for _, opts := range []map[string]any{
		{"anthropic": "wrong"},
		{"anthropic": map[string]any{}},
		{"anthropic": map[string]any{"beta_headers": 9}},
	} {
		_ = betaHeaderFromProviderOptions(opts)
	}
	if block, err := anthropicImageBlock(llm.ContentPart{Image: &llm.ImageData{}}); err != nil || block != nil {
		t.Fatalf("empty image block: block=%v err=%v", block, err)
	}
	unknownImage := t.TempDir() + "/image.unknown"
	if err := os.WriteFile(unknownImage, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	if block, err := anthropicImageBlock(llm.ContentPart{Image: &llm.ImageData{URL: unknownImage}}); err != nil || block == nil {
		t.Fatalf("unknown-extension local image: block=%v err=%v", block, err)
	}

	missing := t.TempDir() + "/missing.jpg"
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentImage}, {Kind: llm.ContentImage, Image: &llm.ImageData{URL: missing}}}},
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentImage}, {Kind: llm.ContentToolCall}, {Kind: llm.ContentThinking}, {Kind: llm.ContentRedThinking}, {Kind: llm.ContentWebSearch}}},
		{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentText}, {Kind: llm.ContentToolResult}}},
	}
	if _, _, err := toAnthropicMessages(msgs); err == nil {
		t.Fatal("missing image file was accepted")
	}

	msgs[0] = llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentImage, Image: &llm.ImageData{Data: []byte("png")}},
		{Kind: llm.ContentImage, Image: &llm.ImageData{URL: "https://example.test/i.png"}},
	}}
	msgs[1] = llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: "assistant"},
		{Kind: llm.ContentImage},
		{Kind: llm.ContentImage, Image: &llm.ImageData{Data: []byte("assistant")}},
		{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call", Name: "tool", Arguments: []byte("null")}},
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "think", Signature: "reasoning_content"}},
		{Kind: llm.ContentRedThinking, Thinking: &llm.ThinkingData{Text: "secret"}},
		{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{Raw: json.RawMessage(`{"type":"server_tool_use"}`)}},
	}}
	msgs[2] = llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call", Content: map[string]any{"ok": true}, ImageData: []byte("img")}},
	}}
	_, got, err := toAnthropicMessages(msgs)
	if err != nil || len(got) != 3 {
		t.Fatalf("rich message translation: len=%d err=%v", len(got), err)
	}

	for _, kind := range []llm.ContentKind{llm.ContentAudio, llm.ContentDocument} {
		_, _, err := toAnthropicMessages([]llm.Message{{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: kind}}}})
		if err == nil {
			t.Fatalf("assistant content %s was accepted", kind)
		}
	}

	chanReq := llm.Request{ProviderOptions: map[string]any{"anthropic": map[string]any{"bad": make(chan int)}}}
	if _, err := a.Complete(context.Background(), chanReq); err == nil {
		t.Fatal("unmarshalable provider option reached transport")
	}
	badContent := llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentAudio}}}}}
	for name, invoke := range map[string]func() error{
		"complete": func() error { _, err := a.Complete(context.Background(), badContent); return err },
		"count":    func() error { _, err := a.CountInputTokens(context.Background(), badContent); return err },
		"stream":   func() error { _, err := a.Stream(context.Background(), badContent); return err },
	} {
		if err := invoke(); err == nil {
			t.Fatalf("%s accepted unsupported content", name)
		}
	}
	for name, invoke := range map[string]func() error{
		"count":  func() error { _, err := a.CountInputTokens(context.Background(), chanReq); return err },
		"stream": func() error { _, err := a.Stream(context.Background(), chanReq); return err },
	} {
		if err := invoke(); err == nil {
			t.Fatalf("%s accepted unmarshalable provider option", name)
		}
	}
}

func responseCoverageSweep(t *testing.T) {
	t.Helper()
	raw := map[string]any{
		"id": "msg", "model": "actual", "stop_reason": "refusal",
		"stop_details": map[string]any{"type": "refusal", "category": "policy", "explanation": "no"},
		"content": []any{
			"skip",
			map[string]any{"type": "text", "text": "hello"},
			map[string]any{"type": "tool_use", "id": "call", "name": "tool", "input": map[string]any{"x": 1}},
			map[string]any{"type": "thinking", "thinking": "abcd", "signature": "sig"},
			map[string]any{"type": "redacted_thinking", "data": "secret"},
			map[string]any{"type": "server_tool_use", "input": map[string]any{"query": "q"}},
			map[string]any{"type": "web_search_tool_result", "content": []any{}},
			map[string]any{"type": "unknown"},
		},
		"usage": map[string]any{
			"input_tokens": 1, "output_tokens": 2, "cache_read_input_tokens": 3,
			"cache_creation": map[string]any{"ephemeral_5m_input_tokens": 4, "ephemeral_1h_input_tokens": 5},
		},
	}
	r := fromAnthropicResponse(raw, "requested")
	if r.ID != "msg" || r.Model != "actual" || len(r.ToolCalls()) != 1 || r.Usage.TotalTokens != 15 || len(r.Warnings) != 1 {
		t.Fatalf("rich response translation: %+v", r)
	}
	r2 := fromAnthropicResponse(map[string]any{"usage": map[string]any{"cache_creation_input_tokens": 7}}, "requested")
	if r2.Usage.CacheWriteTokens == nil || *r2.Usage.CacheWriteTokens != 7 {
		t.Fatalf("usage fallback missing: %+v", r2.Usage)
	}
	if refusalWarning(map[string]any{"type": "other"}) != nil {
		t.Fatal("non-refusal produced warning")
	}
}

func transportCoverageSweep(ctx context.Context, t *testing.T) {
	t.Helper()
	fail := sweepRoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("transport down") })
	for _, invoke := range []func(*Adapter) error{
		func(a *Adapter) error { _, err := a.Complete(ctx, llm.Request{}); return err },
		func(a *Adapter) error { _, err := a.CountInputTokens(ctx, llm.Request{}); return err },
		func(a *Adapter) error { _, err := a.Stream(ctx, llm.Request{}); return err },
		func(a *Adapter) error { _, err := a.ListModels(ctx); return err },
	} {
		if err := invoke(&Adapter{BaseURL: "https://anthropic.test", Client: &http.Client{Transport: fail}}); err == nil {
			t.Fatal("transport error was swallowed")
		}
	}

	countRT := sweepRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		resp, err := sweepResponse(r, http.StatusOK, `{"input_tokens":9}`)
		resp.Header.Set("Content-Type", "application/json")
		return resp, err
	})
	count, err := (&Adapter{BaseURL: "https://anthropic.test", Client: &http.Client{Transport: countRT}}).CountInputTokens(ctx, llm.Request{Model: "m"})
	if err != nil || count.Tokens != 9 {
		t.Fatalf("count success: count=%+v err=%v", count, err)
	}
	errorRT := sweepRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		resp, err := sweepResponse(r, http.StatusTooManyRequests, `{"error":{"message":"busy"}}`)
		resp.Header.Set("Retry-After", "1")
		return resp, err
	})
	for _, invoke := range []func(*Adapter) error{
		func(a *Adapter) error { _, err := a.CountInputTokens(ctx, llm.Request{}); return err },
		func(a *Adapter) error { _, err := a.Stream(ctx, llm.Request{}); return err },
	} {
		if err := invoke(&Adapter{BaseURL: "https://anthropic.test", Client: &http.Client{Transport: errorRT}}); err == nil {
			t.Fatal("HTTP error was swallowed")
		}
	}

	oldRaw := sweepRawBodyEnabled
	sweepRawBodyEnabled = true
	t.Cleanup(func() { sweepRawBodyEnabled = oldRaw })
	completeRT := sweepRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return sweepResponse(r, http.StatusOK, `{"content":[{"type":"text","text":"ok"}]}`)
	})
	complete, err := (&Adapter{BaseURL: "https://anthropic.test", Client: &http.Client{Transport: completeRT}}).Complete(ctx, llm.Request{})
	if err != nil || complete.RawRequestBody == "" || complete.RawResponseBody == "" {
		t.Fatalf("raw complete capture: response=%+v err=%v", complete, err)
	}

	invalidComplete := &Adapter{BaseURL: ":"}
	if _, err := invalidComplete.Complete(ctx, llm.Request{}); err == nil {
		t.Fatal("invalid complete URL accepted")
	}
	invalidCount := &Adapter{BaseURL: ":"}
	if _, err := invalidCount.CountInputTokens(ctx, llm.Request{}); err == nil {
		t.Fatal("invalid count URL accepted")
	}
	invalidStream := &Adapter{BaseURL: ":"}
	if _, err := invalidStream.Stream(ctx, llm.Request{}); err == nil {
		t.Fatal("invalid stream URL accepted")
	}
	invalidModels := &Adapter{BaseURL: ":"}
	if _, err := invalidModels.ListModels(ctx); err == nil {
		t.Fatal("invalid models URL accepted")
	}
	if invalidComplete.Client == nil || invalidCount.Client == nil || invalidStream.Client == nil || invalidModels.Client == nil {
		t.Fatal("default client was not initialized")
	}

	page := 0
	modelsRT := sweepRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		page++
		if page == 1 {
			return sweepResponse(r, http.StatusOK, `{"data":[{"id":"claude-opus-4-1"}],"has_more":true,"last_id":"next"}`)
		}
		if !strings.Contains(r.URL.RawQuery, "after_id=next") {
			t.Fatalf("pagination query missing: %s", r.URL)
		}
		return sweepResponse(r, http.StatusOK, `{"data":[],"has_more":false}`)
	})
	models, err := (&Adapter{BaseURL: "https://anthropic.test", DefaultHeaders: map[string]string{"X-Test": "v"}, Client: &http.Client{Transport: modelsRT}}).ListModels(ctx)
	if err != nil || len(models) != 2 {
		t.Fatalf("pagination/model variant: models=%+v err=%v", models, err)
	}

}

func streamCoverageSweep(ctx context.Context, t *testing.T) {
	t.Helper()
	sse := strings.Join([]string{
		`event: message_start\ndata: {"type":"message_start","message":{"id":"msg","model":"actual","usage":{"input_tokens":1}}}\n\n`,
		`event: content_block_start\ndata: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call","name":"tool"}}\n\n`,
		`event: content_block_delta\ndata: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"x\":1}"}}\n\n`,
		`event: content_block_stop\ndata: {"type":"content_block_stop","index":0}\n\n`,
		`event: content_block_delta\ndata: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":" "}}\n\n`,
		`event: content_block_stop\ndata: {"type":"content_block_stop","index":0}\n\n`,
		`event: content_block_stop\ndata: {"type":"content_block_stop","index":0}\n\n`,
		`event: content_block_start\ndata: {"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":"a"}}\n\n`,
		`event: content_block_delta\ndata: {"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","text":"b"}}\n\n`,
		`event: content_block_delta\ndata: {"type":"content_block_delta","index":1,"delta":{"type":"signature_delta","signature":"sig"}}\n\n`,
		`event: content_block_stop\ndata: {"type":"content_block_stop","index":1}\n\n`,
		`event: content_block_start\ndata: {"type":"content_block_start","index":2,"content_block":{"type":"redacted_thinking","data":"secret"}}\n\n`,
		`event: content_block_start\ndata: {"type":"content_block_start","index":3,"content_block":{"type":"server_tool_use","id":"srv","name":"web_search","input":{"query":"q"}}}\n\n`,
		`event: content_block_start\ndata: {"type":"content_block_start","index":4,"content_block":{"type":"web_search_tool_result","content":[]}}\n\n`,
		`event: content_block_stop\ndata: {"type":"content_block_stop","index":99}\n\n`,
		`event: message_delta\ndata: {"type":"message_delta","delta":{"stop_reason":"refusal","stop_details":{"type":"refusal","category":"policy"}},"usage":{"output_tokens":2,"input_tokens":3}}\n\n`,
		`event: mystery\ndata: {"type":"mystery","value":1}\n\n`,
		`event: message_stop\ndata: {"type":"message_stop"}\n\n`,
	}, "")
	sse = strings.ReplaceAll(sse, `\n`, "\n")
	rt := sweepRoundTripFunc(func(r *http.Request) (*http.Response, error) { return sweepResponse(r, http.StatusOK, sse) })
	a := &Adapter{BaseURL: "https://anthropic.test", Client: &http.Client{Transport: rt}}
	stream, err := a.Stream(ctx, llm.Request{Model: "requested"})
	if err != nil {
		t.Fatalf("stream setup: %v", err)
	}
	var finish *llm.Response
	for ev := range stream.Events() {
		if ev.Response != nil {
			finish = ev.Response
		}
	}
	_ = stream.Close()
	if finish == nil || finish.Model != "actual" || len(finish.ToolCalls()) != 1 || len(finish.Warnings) != 1 {
		t.Fatalf("stream terminal response: %+v", finish)
	}
}
