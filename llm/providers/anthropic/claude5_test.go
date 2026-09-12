package anthropic

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

// ================== Claude 5+ response and stream shapes ==================

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
	sse := strings.Join([]string{
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
		``,
	}, "\n")
	srv, _ := protoServer(t, func(*http.Request) (int, string) { return 200, sse })
	stream, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), protoReq(""), protoLive(srv))
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
