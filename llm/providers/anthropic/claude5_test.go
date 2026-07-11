package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

// ================== Claude 5+ request shaping ==================

func TestIsClaude5OrNewer(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"claude-sonnet-5", true},
		{"claude-fable-5", true},
		{"claude-sonnet-5-20260901", true},
		{"claude-fable-6", true},
		{"claude-opus-4-6", false},
		{"claude-sonnet-4-5", false},
		{"claude-3-5-sonnet-20241022", false},
		{"claude-haiku-4-5-20251001", false},
		{"gpt-5.5", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isClaude5OrNewer(tc.model); got != tc.want {
			t.Errorf("isClaude5OrNewer(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}

func TestBuildRequestBody_Claude5_AdaptiveWithDisplay(t *testing.T) {
	// Claude 5 models must take the adaptive path (budget_tokens 400s) and
	// request displayed thinking (display defaults to "omitted" on Claude 5).
	a := &Adapter{APIKey: "test", BaseURL: "https://api.anthropic.com"}
	effort := "high"
	for _, model := range []string{"claude-sonnet-5", "claude-fable-5", "claude-fable-5-20260901"} {
		req := llm.Request{
			Model:           model,
			Messages:        []llm.Message{llm.User("hi")},
			ReasoningEffort: &effort,
		}
		body, err := a.buildRequestBody(req)
		if err != nil {
			t.Fatalf("%s: buildRequestBody: %v", model, err)
		}
		thinking, ok := body["thinking"].(map[string]any)
		if !ok {
			t.Fatalf("%s: expected thinking block", model)
		}
		if thinking["type"] != "adaptive" {
			t.Fatalf("%s: expected thinking.type=adaptive, got %v", model, thinking["type"])
		}
		if _, hasBudget := thinking["budget_tokens"]; hasBudget {
			t.Fatalf("%s: claude 5 must never send budget_tokens", model)
		}
		if thinking["display"] != "summarized" {
			t.Fatalf("%s: expected thinking.display=summarized, got %v", model, thinking["display"])
		}
		oc, ok := body["output_config"].(map[string]any)
		if !ok {
			t.Fatalf("%s: expected output_config", model)
		}
		if oc["effort"] == "" {
			t.Fatalf("%s: expected output_config.effort", model)
		}
	}
}

func TestBuildRequestBody_Claude5_NoEffort_StillAdaptiveDisplay(t *testing.T) {
	a := &Adapter{APIKey: "test", BaseURL: "https://api.anthropic.com"}
	req := llm.Request{
		Model:    "claude-fable-5",
		Messages: []llm.Message{llm.User("hi")},
	}
	body, err := a.buildRequestBody(req)
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	thinking, ok := body["thinking"].(map[string]any)
	if !ok {
		t.Fatal("expected thinking block for claude-fable-5")
	}
	if thinking["type"] != "adaptive" || thinking["display"] != "summarized" {
		t.Fatalf("expected adaptive+summarized, got %v", thinking)
	}
	if _, ok := body["output_config"]; ok {
		t.Fatal("should not emit output_config when no effort specified")
	}
}

func TestBuildRequestBody_OlderAdaptiveModels_NoDisplayField(t *testing.T) {
	// Opus 4.6 / Sonnet 4.6 requests must stay byte-identical: no display field.
	a := &Adapter{APIKey: "test", BaseURL: "https://api.anthropic.com"}
	for _, model := range []string{"claude-opus-4-6", "claude-sonnet-4-6"} {
		req := llm.Request{
			Model:    model,
			Messages: []llm.Message{llm.User("hi")},
		}
		body, err := a.buildRequestBody(req)
		if err != nil {
			t.Fatalf("%s: buildRequestBody: %v", model, err)
		}
		thinking, ok := body["thinking"].(map[string]any)
		if !ok {
			t.Fatalf("%s: expected thinking block", model)
		}
		if _, has := thinking["display"]; has {
			t.Fatalf("%s: must not send display field, got %v", model, thinking)
		}
	}
}

func TestBuildRequestBody_Claude5_OmitsSamplingParams(t *testing.T) {
	// Sonnet 5 400s on non-default temperature/top_p; Fable removed them.
	a := &Adapter{APIKey: "test", BaseURL: "https://api.anthropic.com"}
	temp := 0.7
	topP := 0.9
	req := llm.Request{
		Model:       "claude-sonnet-5",
		Messages:    []llm.Message{llm.User("hi")},
		Temperature: &temp,
		TopP:        &topP,
	}
	body, err := a.buildRequestBody(req)
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	if _, has := body["temperature"]; has {
		t.Error("claude 5 request must omit temperature")
	}
	if _, has := body["top_p"]; has {
		t.Error("claude 5 request must omit top_p")
	}
	if _, has := body["top_k"]; has {
		t.Error("claude 5 request must omit top_k")
	}

	// Older models keep sending sampling params (unchanged behavior).
	req.Model = "claude-sonnet-4-5"
	body, err = a.buildRequestBody(req)
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	if body["temperature"] != temp {
		t.Errorf("claude-sonnet-4-5 should keep temperature, got %v", body["temperature"])
	}
	if body["top_p"] != topP {
		t.Errorf("claude-sonnet-4-5 should keep top_p, got %v", body["top_p"])
	}
}

func TestBuildRequestBody_Claude5_1MSuffixStripped(t *testing.T) {
	// A hypothetical [1m]-suffixed claude 5 model still takes the claude 5 path.
	a := &Adapter{APIKey: "test", BaseURL: "https://api.anthropic.com"}
	req := llm.Request{
		Model:    "claude-sonnet-5[1m]",
		Messages: []llm.Message{llm.User("hi")},
	}
	body, err := a.buildRequestBody(req)
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	if body["model"] != "claude-sonnet-5" {
		t.Fatalf("model = %v", body["model"])
	}
	thinking, _ := body["thinking"].(map[string]any)
	if thinking == nil || thinking["display"] != "summarized" {
		t.Fatalf("expected claude 5 adaptive+summarized thinking, got %v", thinking)
	}
}

// ================== Claude 5+ response surface ==================

func TestFromAnthropicResponse_RefusalStopDetails(t *testing.T) {
	raw := map[string]any{
		"id":    "msg_r1",
		"model": "claude-fable-5",
		"content": []any{
			map[string]any{"type": "text", "text": "I can't help with that."},
		},
		"stop_reason": "refusal",
		"stop_details": map[string]any{
			"type":        "refusal",
			"category":    "cyber",
			"explanation": "request involved malware development",
		},
		"usage": map[string]any{"input_tokens": float64(10), "output_tokens": float64(5)},
	}
	r := fromAnthropicResponse(raw, "claude-fable-5")
	if r.Finish.Reason != llm.FinishReasonContentFilter {
		t.Fatalf("Finish.Reason = %q, want content_filter", r.Finish.Reason)
	}
	if r.Finish.Raw != "refusal" {
		t.Fatalf("Finish.Raw = %q, want refusal", r.Finish.Raw)
	}
	if len(r.Warnings) != 1 {
		t.Fatalf("want 1 refusal warning, got %v", r.Warnings)
	}
	w := r.Warnings[0]
	if w.Code != "refusal" {
		t.Errorf("warning code = %q, want refusal", w.Code)
	}
	if !strings.Contains(w.Message, "cyber") || !strings.Contains(w.Message, "malware development") {
		t.Errorf("warning message missing category/explanation: %q", w.Message)
	}
}

func TestFromAnthropicResponse_RefusalNullCategory(t *testing.T) {
	raw := map[string]any{
		"id":          "msg_r2",
		"model":       "claude-sonnet-5",
		"content":     []any{map[string]any{"type": "text", "text": "no"}},
		"stop_reason": "refusal",
		"stop_details": map[string]any{
			"type":        "refusal",
			"category":    nil,
			"explanation": "declined",
		},
	}
	r := fromAnthropicResponse(raw, "claude-sonnet-5")
	if r.Finish.Reason != llm.FinishReasonContentFilter || r.Finish.Raw != "refusal" {
		t.Fatalf("Finish = %+v", r.Finish)
	}
	if len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0].Message, "declined") {
		t.Fatalf("Warnings = %v", r.Warnings)
	}
}

func TestFromAnthropicResponse_NullStopDetailsAndUnknownBlocks(t *testing.T) {
	// stop_details is null for non-refusal stop reasons; fallback blocks and
	// usage.iterations are new surfaces the parser must tolerate.
	raw := map[string]any{
		"id":    "msg_f1",
		"model": "claude-fable-5",
		"content": []any{
			map[string]any{"type": "fallback", "from": map[string]any{"model": "claude-fable-5"}, "to": map[string]any{"model": "claude-sonnet-5"}},
			map[string]any{"type": "text", "text": "hello"},
		},
		"stop_reason":  "end_turn",
		"stop_details": nil,
		"usage": map[string]any{
			"input_tokens":  float64(7),
			"output_tokens": float64(3),
			"iterations": []any{
				map[string]any{"model": "claude-fable-5", "input_tokens": float64(7)},
			},
		},
	}
	r := fromAnthropicResponse(raw, "claude-fable-5")
	if r.Finish.Reason != llm.FinishReasonStop {
		t.Fatalf("Finish.Reason = %q, want stop", r.Finish.Reason)
	}
	if r.Text() != "hello" {
		t.Fatalf("Text = %q", r.Text())
	}
	if len(r.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", r.Warnings)
	}
	if r.Usage.InputTokens != 7 || r.Usage.OutputTokens != 3 {
		t.Fatalf("Usage = %+v", r.Usage)
	}
}

func TestStream_RefusalStopDetailsAndFallbackBlock(t *testing.T) {
	srv := newAnthropicStreamServer(t, []string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_r3","model":"claude-fable-5","usage":{"input_tokens":10}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"fallback","from":{"model":"claude-fable-5"},"to":{"model":"claude-sonnet-5"}}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"I can't help."}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":1}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"refusal","stop_details":{"type":"refusal","category":"bio","explanation":"bio risk"}},"usage":{"output_tokens":4,"iterations":[{"model":"claude-fable-5"}]}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	})
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	stream, err := a.Stream(context.Background(), llm.Request{
		Model:    "claude-fable-5",
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
	if resp.Finish.Reason != llm.FinishReasonContentFilter || resp.Finish.Raw != "refusal" {
		t.Fatalf("Finish = %+v", resp.Finish)
	}
	if resp.Text() != "I can't help." {
		t.Fatalf("Text = %q", resp.Text())
	}
	if len(resp.Warnings) != 1 {
		t.Fatalf("want 1 refusal warning, got %v", resp.Warnings)
	}
	if !strings.Contains(resp.Warnings[0].Message, "bio") || !strings.Contains(resp.Warnings[0].Message, "bio risk") {
		t.Fatalf("warning = %v", resp.Warnings[0])
	}
}

// ================== No synthetic [1m] variant for Claude 5 ==================

func TestAdapter_ListModels_NoSynthetic1MForClaude5(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": [
				{"id": "claude-sonnet-5", "display_name": "Claude Sonnet 5", "type": "model"},
				{"id": "claude-fable-5", "display_name": "Claude Fable 5", "type": "model"},
				{"id": "claude-sonnet-4-6-20250514", "display_name": "Claude Sonnet 4.6", "type": "model"}
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
	ids := map[string]bool{}
	for _, m := range models {
		ids[m.ID] = true
	}
	// Claude 5 models are 1M-context natively; no synthetic variants.
	if ids["claude-sonnet-5[1m]"] || ids["claude-fable-5[1m]"] {
		t.Fatalf("claude 5 models must not get synthetic [1m] variants: %v", ids)
	}
	// Sonnet 4.6 still gets one (unchanged behavior).
	if !ids["claude-sonnet-4-6-20250514[1m]"] {
		t.Fatalf("missing claude-sonnet-4-6 [1m] variant: %v", ids)
	}
}
