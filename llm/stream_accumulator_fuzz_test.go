package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// FuzzStreamAccumulator folds an arbitrary sequence of StreamEvents into a
// StreamAccumulator and inspects the assembled Response. Process/buildResponse/
// responseHasContent/copyResponseMetadata bridge every streaming provider back to
// a buffered Response; only fixed unit sequences exercised them (0% fuzz).
//
// Oracles:
//   - Process never panics over any event order and is a no-op on a nil receiver.
//   - the assembled text equals the concatenation of all non-empty text deltas in
//     arrival order (no delta is dropped or reordered), and the reasoning text
//     equals the concatenation of all reasoning deltas.
//   - after a FINISH event with no embedded Response, Response() is non-nil with
//     at least one content part (the documented "always at least one part"
//     guarantee), and PartialResponse never aliases internal state.
func FuzzStreamAccumulator(f *testing.F) {
	f.Add("hello| world|", "think|ing", "call1", true)
	f.Add("", "", "", false)
	f.Add("a|b|c", "", "id", true)

	f.Fuzz(func(t *testing.T, textPipes, reasonPipes, toolID string, finish bool) {
		var nilAcc *StreamAccumulator
		nilAcc.Process(StreamEvent{Type: StreamEventTextDelta, Delta: "x"}) // must be a no-op
		if nilAcc.Response() != nil || nilAcc.PartialResponse() != nil || nilAcc.buildResponse() != nil {
			t.Fatal("nil accumulator returned a response")
		}

		acc := NewStreamAccumulator()
		implicit := NewStreamAccumulator()
		implicit.Process(StreamEvent{Type: StreamEventTextDelta, Delta: "implicit"})
		_ = NewStreamAccumulator().PartialResponse()
		acc.Process(StreamEvent{Type: StreamEventTextStart})
		acc.Process(StreamEvent{Type: StreamEventTextStart, TextID: "t0"})
		acc.Process(StreamEvent{Type: StreamEventTextStart, TextID: "t0"})
		acc.Process(StreamEvent{Type: StreamEventTextDelta, TextID: "implicit", Delta: "x"})
		acc.Process(StreamEvent{Type: StreamEventReasoningDelta, ReasoningDelta: "r"})
		acc.Process(StreamEvent{Type: StreamEventError})

		var wantText, wantReason strings.Builder
		wantReason.WriteString("r")
		for d := range strings.SplitSeq(textPipes, "|") {
			acc.Process(StreamEvent{Type: StreamEventTextDelta, TextID: "t0", Delta: d})
			wantText.WriteString(d)
		}
		wantText.WriteString("x")
		for d := range strings.SplitSeq(reasonPipes, "|") {
			acc.Process(StreamEvent{Type: StreamEventReasoningDelta, ReasoningDelta: d})
			wantReason.WriteString(d)
		}
		if strings.TrimSpace(toolID) != "" {
			acc.Process(StreamEvent{Type: StreamEventToolCallStart})
			acc.Process(StreamEvent{Type: StreamEventToolCallStart, ToolCall: &ToolCallData{ID: toolID, Name: "fn"}})
			acc.Process(StreamEvent{Type: StreamEventToolCallDelta, ToolCall: &ToolCallData{ID: toolID, Arguments: []byte(`{"a":1}`)}})
			acc.Process(StreamEvent{Type: StreamEventToolCallEnd, ToolCall: &ToolCallData{ID: toolID}})
		}
		acc.Process(StreamEvent{Type: StreamEventToolCallDelta, ToolCall: &ToolCallData{ID: "late", ItemID: "item", Name: "late_fn", Type: "function", Arguments: json.RawMessage(`{}`)}})
		acc.Process(StreamEvent{Type: StreamEventToolCallDelta, ToolCall: &ToolCallData{ID: "late", ItemID: "item2", Name: "late_fn2", Type: "custom"}})
		acc.Process(StreamEvent{Type: StreamEventToolCallStart, ToolCall: &ToolCallData{ID: "fill-delta"}})
		acc.Process(StreamEvent{Type: StreamEventToolCallDelta, ToolCall: &ToolCallData{ID: "fill-delta", ItemID: "filled", Name: "filled", Type: "function"}})
		acc.Process(StreamEvent{Type: StreamEventToolCallEnd, ToolCall: &ToolCallData{ID: "ended", ItemID: "end-item", Name: "end_fn", Type: "function", Arguments: json.RawMessage(`[]`)}})
		acc.Process(StreamEvent{Type: StreamEventToolCallEnd, ToolCall: &ToolCallData{ID: "ended", ItemID: "end-item2", Name: "end_fn2", Type: "custom"}})
		acc.Process(StreamEvent{Type: StreamEventToolCallStart, ToolCall: &ToolCallData{ID: "fill-end"}})
		acc.Process(StreamEvent{Type: StreamEventToolCallEnd, ToolCall: &ToolCallData{ID: "fill-end", ItemID: "filled", Name: "filled", Type: "function"}})

		// Partial reflects text seen so far without aliasing internal state.
		if p := acc.PartialResponse(); p != nil {
			if got := p.Message.Text(); got != wantText.String() {
				t.Fatalf("partial text %q != accumulated %q", got, wantText.String())
			}
		}

		if !finish {
			if acc.Response() != nil {
				t.Fatalf("Response() non-nil before FINISH")
			}
			return
		}

		meta := Response{
			ID: "response-id", Model: "model", Provider: "provider", Raw: map[string]any{"raw": true},
			Finish: FinishReason{Reason: FinishReasonStop}, Usage: Usage{InputTokens: 1},
			Warnings: []Warning{{Message: "warning"}}, RateLimit: &RateLimitInfo{},
		}
		acc.Process(StreamEvent{Type: StreamEventFinish, Response: &meta})
		resp := acc.Response()
		if resp == nil {
			t.Fatalf("Response() nil after FINISH")
		}
		if len(resp.Message.Content) == 0 {
			t.Fatalf("assembled response has zero content parts")
		}
		if got := resp.Message.Text(); got != wantText.String() {
			t.Fatalf("assembled text %q != expected %q", got, wantText.String())
		}
		if got := resp.ReasoningText(); got != wantReason.String() {
			t.Fatalf("assembled reasoning %q != expected %q", got, wantReason.String())
		}
		if strings.TrimSpace(toolID) != "" {
			if len(resp.ToolCalls()) < 1 || resp.ToolCalls()[0].ID != toolID {
				t.Fatalf("tool call not preserved: %+v", resp.ToolCalls())
			}
		}
		full := Response{Message: Assistant("authoritative")}
		acc.Process(StreamEvent{Type: StreamEventFinish, Response: &full})
		if got := acc.Response(); got == nil || got.Text() != "authoritative" {
			t.Fatalf("content-bearing finish response not preserved: %+v", got)
		}
		empty := NewStreamAccumulator()
		empty.Process(StreamEvent{Type: StreamEventFinish, FinishReason: &FinishReason{Reason: FinishReasonStop}, Usage: &Usage{TotalTokens: 1}})
		_ = responseHasContent(Response{Message: Message{Content: []ContentPart{{Kind: ContentText}, {Kind: ContentText}}}})
		copyResponseMetadata(&Response{}, nil)
	})
}
