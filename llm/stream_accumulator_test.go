package llm

import (
	"encoding/json"
	"testing"
)

func TestStreamAccumulator_FinishWithResponse_UsesIt(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	acc := NewStreamAccumulator()
	acc.Process(StreamEvent{Type: StreamEventStreamStart})
	acc.Process(StreamEvent{Type: StreamEventTextDelta, TextID: "t", Delta: "ignored"})

	r := Response{Provider: "openai", Model: "m", Message: Assistant("Hello"), Finish: FinishReason{Reason: "stop"}}
	acc.Process(StreamEvent{Type: StreamEventFinish, Response: &r, FinishReason: &r.Finish, Usage: &r.Usage})

	got := acc.Response()
	if got == nil {
		t.Fatalf("expected response")
	}
	if got.Provider != "openai" || got.Model != "m" || got.Text() != "Hello" {
		t.Fatalf("response: %+v", *got)
	}
}

func TestStreamAccumulator_NoFinishResponse_BuildsFromText(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	acc := NewStreamAccumulator()
	acc.Process(StreamEvent{Type: StreamEventStreamStart})
	acc.Process(StreamEvent{Type: StreamEventTextStart, TextID: "t1"})
	acc.Process(StreamEvent{Type: StreamEventTextDelta, TextID: "t1", Delta: "Hel"})
	acc.Process(StreamEvent{Type: StreamEventTextDelta, TextID: "t1", Delta: "lo"})
	acc.Process(StreamEvent{Type: StreamEventTextEnd, TextID: "t1"})

	if pr := acc.PartialResponse(); pr == nil || pr.Text() != "Hello" {
		if pr == nil {
			t.Fatalf("expected partial response, got nil")
		}
		t.Fatalf("partial text: %q", pr.Text())
	}

	f := FinishReason{Reason: "stop"}
	u := Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3}
	acc.Process(StreamEvent{Type: StreamEventFinish, FinishReason: &f, Usage: &u})

	got := acc.Response()
	if got == nil {
		t.Fatalf("expected response")
	}
	if got.Text() != "Hello" {
		t.Fatalf("text: %q", got.Text())
	}
	if got.Finish.Reason != "stop" {
		t.Fatalf("finish: %+v", got.Finish)
	}
	if got.Usage.TotalTokens != 3 {
		t.Fatalf("usage: %+v", got.Usage)
	}
}

func TestStreamAccumulator_FinishWithMetadataOnlyResponse_PreservesAccumulatedContent(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	acc := NewStreamAccumulator()
	acc.Process(StreamEvent{Type: StreamEventStreamStart})
	acc.Process(StreamEvent{Type: StreamEventTextStart, TextID: "t1"})
	acc.Process(StreamEvent{Type: StreamEventTextDelta, TextID: "t1", Delta: "Hello"})
	acc.Process(StreamEvent{Type: StreamEventTextEnd, TextID: "t1"})
	acc.Process(StreamEvent{Type: StreamEventToolCallEnd, ToolCall: &ToolCallData{
		ID: "call_streamed", Name: "f", Arguments: json.RawMessage(`{}`),
	}})

	final := Response{
		ID:       "resp_1",
		Provider: "openai",
		Model:    "gpt-5.4",
		Raw:      map[string]any{"id": "resp_1"},
	}
	StampEndpointURL(&final, "https://serving.invalid/fixture-31", NewAPILogCredentialMaterial(nil, nil, "fixture-31"))
	acc.Process(StreamEvent{Type: StreamEventFinish, Response: &final})

	got := acc.Response()
	if got == nil {
		t.Fatalf("expected response")
	}
	if got.ID != "resp_1" {
		t.Fatalf("ID = %q, want resp_1", got.ID)
	}
	if got.Provider != "openai" || got.Model != "gpt-5.4" {
		t.Fatalf("provider/model = %q/%q", got.Provider, got.Model)
	}
	if got.Text() != "Hello" {
		t.Fatalf("text = %q, want Hello", got.Text())
	}
	if got.Raw["id"] != "resp_1" {
		t.Fatalf("raw = %#v", got.Raw)
	}
	if got.ReasoningEndpoint() == "" || got.ReasoningEndpoint() != final.ReasoningEndpoint() {
		t.Fatal("metadata-only terminal lost the streamed call's serving origin")
	}
	if calls := got.ToolCalls(); len(calls) != 1 || calls[0].ID != "call_streamed" {
		t.Fatalf("metadata-only terminal lost the streamed call: %+v", calls)
	}
}

func TestStreamAccumulator_FinishWithContentResponsePreservesFinalToolCallItemID(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	acc := NewStreamAccumulator()
	acc.Process(StreamEvent{Type: StreamEventStreamStart})
	acc.Process(StreamEvent{Type: StreamEventToolCallStart, ToolCall: &ToolCallData{
		ID: "call_streamed", ItemID: "fc_streamed", Name: "delegate", Type: "function",
	}})
	acc.Process(StreamEvent{Type: StreamEventToolCallDelta, ToolCall: &ToolCallData{
		ID: "call_streamed", ItemID: "fc_streamed", Arguments: json.RawMessage(`{"task":"streamed"}`), Type: "function",
	}})

	final := Response{
		ID:       "resp_1",
		Provider: "openai",
		Model:    "gpt-5.5",
		Message: Message{Role: RoleAssistant, Content: []ContentPart{{
			Kind: ContentToolCall,
			ToolCall: &ToolCallData{
				ID:        "call_final",
				ItemID:    "fc_final",
				Name:      "delegate",
				Arguments: json.RawMessage(`{"task":"final"}`),
				Type:      "function",
			},
		}}},
		Finish: FinishReason{Reason: "tool_calls"},
	}
	acc.Process(StreamEvent{Type: StreamEventFinish, Response: &final, FinishReason: &final.Finish})

	got := acc.Response()
	if got == nil {
		t.Fatalf("expected response")
	}
	calls := got.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(calls))
	}
	if calls[0].ID != "call_final" {
		t.Fatalf("tool call id = %q, want call_final", calls[0].ID)
	}
	if calls[0].ItemID != "fc_final" {
		t.Fatalf("tool item id = %q, want fc_final", calls[0].ItemID)
	}
}

func TestStreamAccumulator_ReasoningEvents_AccumulatedInResponse(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	acc := NewStreamAccumulator()
	acc.Process(StreamEvent{Type: StreamEventStreamStart})
	acc.Process(StreamEvent{Type: StreamEventReasoningStart})
	acc.Process(StreamEvent{Type: StreamEventReasoningDelta, ReasoningDelta: "Let me think"})
	acc.Process(StreamEvent{Type: StreamEventReasoningDelta, ReasoningDelta: " about this."})
	acc.Process(StreamEvent{Type: StreamEventReasoningEnd})
	acc.Process(StreamEvent{Type: StreamEventTextStart, TextID: "t1"})
	acc.Process(StreamEvent{Type: StreamEventTextDelta, TextID: "t1", Delta: "Result"})
	acc.Process(StreamEvent{Type: StreamEventTextEnd, TextID: "t1"})
	f := FinishReason{Reason: "stop"}
	acc.Process(StreamEvent{Type: StreamEventFinish, FinishReason: &f})

	resp := acc.Response()
	if resp == nil {
		t.Fatalf("expected response, got nil")
	}
	if got := resp.Text(); got != "Result" {
		t.Fatalf("text: got %q, want %q", got, "Result")
	}
	if got := resp.ReasoningText(); got != "Let me think about this." {
		t.Fatalf("reasoning: got %q, want %q", got, "Let me think about this.")
	}
}

func TestStreamAccumulator_ToolCallEvents_AccumulatedInResponse(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	acc := NewStreamAccumulator()
	acc.Process(StreamEvent{Type: StreamEventStreamStart})
	acc.Process(StreamEvent{Type: StreamEventToolCallStart, ToolCall: &ToolCallData{
		ID: "call_1", Name: "get_weather", Type: "function",
	}})
	acc.Process(StreamEvent{Type: StreamEventToolCallDelta, ToolCall: &ToolCallData{
		ID: "call_1", Arguments: json.RawMessage(`{"city":`),
	}})
	acc.Process(StreamEvent{Type: StreamEventToolCallDelta, ToolCall: &ToolCallData{
		ID: "call_1", Arguments: json.RawMessage(`"SF"}`),
	}})
	acc.Process(StreamEvent{Type: StreamEventToolCallEnd, ToolCall: &ToolCallData{ID: "call_1"}})
	f := FinishReason{Reason: "tool_calls"}
	acc.Process(StreamEvent{Type: StreamEventFinish, FinishReason: &f})

	resp := acc.Response()
	if resp == nil {
		t.Fatalf("expected response, got nil")
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("tool calls: got %d, want 1", len(calls))
	}
	if calls[0].ID != "call_1" {
		t.Fatalf("tool call ID: got %q, want %q", calls[0].ID, "call_1")
	}
	if calls[0].Name != "get_weather" {
		t.Fatalf("tool call name: got %q, want %q", calls[0].Name, "get_weather")
	}
	if got := string(calls[0].Arguments); got != `{"city":"SF"}` {
		t.Fatalf("tool call arguments: got %q, want %q", got, `{"city":"SF"}`)
	}
}

func TestStreamAccumulator_ToolCallThoughtSignature(t *testing.T) {
	t.Parallel()
	acc := NewStreamAccumulator()
	acc.Process(StreamEvent{Type: StreamEventToolCallStart, ToolCall: &ToolCallData{
		ID: "call_gemini", Name: "read_file", Type: "function", ThoughtSignature: "sig-1",
	}})
	acc.Process(StreamEvent{Type: StreamEventToolCallEnd, ToolCall: &ToolCallData{
		ID: "call_gemini", Arguments: json.RawMessage(`{"path":"x"}`), ThoughtSignature: "sig-1",
	}})
	f := FinishReason{Reason: "tool_calls"}
	acc.Process(StreamEvent{Type: StreamEventFinish, FinishReason: &f})

	resp := acc.Response()
	if resp == nil {
		t.Fatal("expected response")
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 || calls[0].ThoughtSignature != "sig-1" {
		t.Fatalf("tool calls = %+v, want one call with the signature", calls)
	}
}

func TestStreamAccumulator_ToolCallEndOverridesAccumulatedArgumentsAndName(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	acc := NewStreamAccumulator()
	acc.Process(StreamEvent{Type: StreamEventStreamStart})
	acc.Process(StreamEvent{Type: StreamEventToolCallStart, ToolCall: &ToolCallData{
		ID: "call_1", Type: "function",
	}})
	acc.Process(StreamEvent{Type: StreamEventToolCallDelta, ToolCall: &ToolCallData{
		ID: "call_1", Arguments: json.RawMessage(`{"ac`),
	}})
	acc.Process(StreamEvent{Type: StreamEventToolCallDelta, ToolCall: &ToolCallData{
		ID: "call_1", Arguments: json.RawMessage(`tion":"u`),
	}})
	acc.Process(StreamEvent{Type: StreamEventToolCallEnd, ToolCall: &ToolCallData{
		ID:        "call_1",
		Name:      "task_list",
		Type:      "function",
		Arguments: json.RawMessage(`{"action":"update"}`),
	}})
	f := FinishReason{Reason: "tool_calls"}
	acc.Process(StreamEvent{Type: StreamEventFinish, FinishReason: &f})

	resp := acc.Response()
	if resp == nil {
		t.Fatalf("expected response, got nil")
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("tool calls: got %d, want 1", len(calls))
	}
	if calls[0].Name != "task_list" {
		t.Fatalf("tool call name: got %q, want %q", calls[0].Name, "task_list")
	}
	if got := string(calls[0].Arguments); got != `{"action":"update"}` {
		t.Fatalf("tool call arguments: got %q, want %q", got, `{"action":"update"}`)
	}
}

func TestStreamAccumulator_MultipleToolCalls(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	acc := NewStreamAccumulator()
	acc.Process(StreamEvent{Type: StreamEventStreamStart})

	// First tool call
	acc.Process(StreamEvent{Type: StreamEventToolCallStart, ToolCall: &ToolCallData{
		ID: "call_1", Name: "read_file", Type: "function",
	}})
	acc.Process(StreamEvent{Type: StreamEventToolCallDelta, ToolCall: &ToolCallData{
		ID: "call_1", Arguments: json.RawMessage(`{"path":"a.go"}`),
	}})
	acc.Process(StreamEvent{Type: StreamEventToolCallEnd, ToolCall: &ToolCallData{ID: "call_1"}})

	// Second tool call
	acc.Process(StreamEvent{Type: StreamEventToolCallStart, ToolCall: &ToolCallData{
		ID: "call_2", Name: "read_file", Type: "function",
	}})
	acc.Process(StreamEvent{Type: StreamEventToolCallDelta, ToolCall: &ToolCallData{
		ID: "call_2", Arguments: json.RawMessage(`{"path":"b.go"}`),
	}})
	acc.Process(StreamEvent{Type: StreamEventToolCallEnd, ToolCall: &ToolCallData{ID: "call_2"}})

	f := FinishReason{Reason: "tool_calls"}
	acc.Process(StreamEvent{Type: StreamEventFinish, FinishReason: &f})

	resp := acc.Response()
	if resp == nil {
		t.Fatalf("expected response, got nil")
	}
	calls := resp.ToolCalls()
	if len(calls) != 2 {
		t.Fatalf("tool calls: got %d, want 2", len(calls))
	}
	if calls[0].ID != "call_1" {
		t.Fatalf("first tool call ID: got %q, want %q", calls[0].ID, "call_1")
	}
	if calls[1].ID != "call_2" {
		t.Fatalf("second tool call ID: got %q, want %q", calls[1].ID, "call_2")
	}
	if got := string(calls[0].Arguments); got != `{"path":"a.go"}` {
		t.Fatalf("first tool call args: got %q, want %q", got, `{"path":"a.go"}`)
	}
	if got := string(calls[1].Arguments); got != `{"path":"b.go"}` {
		t.Fatalf("second tool call args: got %q, want %q", got, `{"path":"b.go"}`)
	}
}

func TestStreamAccumulator_PartialResponse_RebuildsOncePerDirtyRound(t *testing.T) {
	t.Parallel()
	acc := NewStreamAccumulator()
	acc.Process(StreamEvent{Type: StreamEventStreamStart})
	acc.Process(StreamEvent{Type: StreamEventTextStart, TextID: "t1"})

	for range 5 {
		acc.Process(StreamEvent{Type: StreamEventTextDelta, TextID: "t1", Delta: "a"})
	}

	if acc.buildCount != 0 {
		t.Fatalf("buildCount after 5 deltas with no PartialResponse call = %d, want 0 (rebuild must be lazy)", acc.buildCount)
	}

	before := acc.buildCount
	pr := acc.PartialResponse()
	if pr == nil || pr.Text() != "aaaaa" {
		t.Fatalf("partial = %+v, want text %q", pr, "aaaaa")
	}
	if got := acc.buildCount - before; got != 1 {
		t.Fatalf("builds performed by PartialResponse() = %d, want exactly 1", got)
	}

	// A second call with no intervening Process() must not trigger another build.
	before = acc.buildCount
	if pr2 := acc.PartialResponse(); pr2 == nil || pr2.Text() != "aaaaa" {
		t.Fatalf("second partial = %+v, want text %q", pr2, "aaaaa")
	}
	if got := acc.buildCount - before; got != 0 {
		t.Fatalf("builds performed by repeated PartialResponse() = %d, want 0", got)
	}
}

func TestStreamAccumulator_ReasoningAndToolCallsTogether(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	acc := NewStreamAccumulator()
	acc.Process(StreamEvent{Type: StreamEventStreamStart})

	// Reasoning
	acc.Process(StreamEvent{Type: StreamEventReasoningStart})
	acc.Process(StreamEvent{Type: StreamEventReasoningDelta, ReasoningDelta: "I need to check the weather."})
	acc.Process(StreamEvent{Type: StreamEventReasoningEnd})

	// Text
	acc.Process(StreamEvent{Type: StreamEventTextStart, TextID: "t1"})
	acc.Process(StreamEvent{Type: StreamEventTextDelta, TextID: "t1", Delta: "Checking weather..."})
	acc.Process(StreamEvent{Type: StreamEventTextEnd, TextID: "t1"})

	// Tool call
	acc.Process(StreamEvent{Type: StreamEventToolCallStart, ToolCall: &ToolCallData{
		ID: "call_1", Name: "get_weather", Type: "function",
	}})
	acc.Process(StreamEvent{Type: StreamEventToolCallDelta, ToolCall: &ToolCallData{
		ID: "call_1", Arguments: json.RawMessage(`{"city":"NYC"}`),
	}})
	acc.Process(StreamEvent{Type: StreamEventToolCallEnd, ToolCall: &ToolCallData{ID: "call_1"}})

	f := FinishReason{Reason: "tool_calls"}
	acc.Process(StreamEvent{Type: StreamEventFinish, FinishReason: &f})

	resp := acc.Response()
	if resp == nil {
		t.Fatalf("expected response, got nil")
	}
	if got := resp.Text(); got != "Checking weather..." {
		t.Fatalf("text: got %q, want %q", got, "Checking weather...")
	}
	if got := resp.ReasoningText(); got != "I need to check the weather." {
		t.Fatalf("reasoning: got %q, want %q", got, "I need to check the weather.")
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("tool calls: got %d, want 1", len(calls))
	}
	if calls[0].Name != "get_weather" {
		t.Fatalf("tool call name: got %q, want %q", calls[0].Name, "get_weather")
	}
}

// TestStreamAccumulatorToolCallDeltaWithoutStart covers the arms where a
// ToolCallDelta and ToolCallEnd arrive for tool-call IDs that were never
// opened with a ToolCallStart, so the accumulator must create the record and
// fill its name/item/type/arguments lazily.
func TestStreamAccumulatorToolCallDeltaWithoutStart(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	acc := NewStreamAccumulator()
	acc.Process(StreamEvent{Type: StreamEventTextStart})
	acc.Process(StreamEvent{Type: StreamEventTextDelta, Delta: "hello"})

	acc.Process(StreamEvent{Type: StreamEventToolCallDelta, ToolCall: &ToolCallData{
		ID: "call-a", ItemID: "item-a", Name: "search", Type: "function", Arguments: []byte(`{"q":`),
	}})
	acc.Process(StreamEvent{Type: StreamEventToolCallEnd, ToolCall: &ToolCallData{
		ID: "call-b", ItemID: "item-b", Name: "fetch", Type: "function", Arguments: []byte(`{}`),
	}})
	acc.Process(StreamEvent{Type: StreamEventFinish})

	resp := acc.Response()
	if resp == nil {
		t.Fatal("Response() = nil after finish")
	}
	calls := resp.ToolCalls()
	if len(calls) != 2 {
		t.Fatalf("ToolCalls() = %d, want 2", len(calls))
	}
	if calls[0].Name != "search" || calls[1].Name != "fetch" {
		t.Fatalf("tool call names = %q, %q", calls[0].Name, calls[1].Name)
	}
	if acc.PartialResponse() == nil {
		t.Fatal("PartialResponse() = nil, want accumulated partial")
	}
}

// TestStreamAccumulatorFinishRebuildsEmptyResponse covers the FINISH arm where
// the provider Response carries no content, so the accumulator rebuilds it from
// accumulated deltas and copies the provider metadata across.
func TestStreamAccumulatorFinishRebuildsEmptyResponse(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	acc := NewStreamAccumulator()
	acc.Process(StreamEvent{Type: StreamEventTextDelta, TextID: "t0", Delta: "body"})
	acc.Process(StreamEvent{Type: StreamEventFinish, Response: &Response{
		Model:     "gpt-x",
		Provider:  "openai",
		Warnings:  []Warning{{Message: "deprecated", Code: "warn"}},
		RateLimit: &RateLimitInfo{},
	}})

	resp := acc.Response()
	if resp == nil {
		t.Fatal("Response() = nil after finish")
	}
	if resp.Model != "gpt-x" || resp.Provider != "openai" {
		t.Fatalf("metadata not copied: %+v", resp)
	}
	if len(resp.Warnings) != 1 || resp.RateLimit == nil {
		t.Fatalf("warnings/ratelimit not copied: %+v", resp)
	}
	if resp.Text() != "body" {
		t.Fatalf("rebuilt text = %q, want body", resp.Text())
	}
}

// TestStreamAccumulator_ToolCallEndDoesNotRewriteAHeldPartial covers the
// ownership rule the projected partial depends on: a partial handed to a caller
// owns its bytes. A ToolCallEnd that supplies different arguments than the
// deltas accumulated must not rewrite the array a held partial shares, or the
// caller's snapshot changes under it.
//
// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
func TestStreamAccumulator_ToolCallEndDoesNotRewriteAHeldPartial(t *testing.T) {
	acc := NewStreamAccumulator()
	acc.Process(StreamEvent{Type: StreamEventToolCallStart, ToolCall: &ToolCallData{
		ID: "c1", Name: "t1", Type: "function",
	}})
	acc.Process(StreamEvent{Type: StreamEventToolCallDelta, ToolCall: &ToolCallData{
		ID: "c1", Arguments: json.RawMessage(`{"streamed":`),
	}})
	acc.Process(StreamEvent{Type: StreamEventToolCallDelta, ToolCall: &ToolCallData{
		ID: "c1", Arguments: json.RawMessage(`true}`),
	}})

	// Hold a partial mid-argument-stream, the way a caller reading
	// PartialResponse between deltas does.
	held := acc.PartialResponse()
	if held == nil {
		t.Fatal("PartialResponse() = nil mid-stream")
	}
	heldCalls := held.ToolCalls()
	if len(heldCalls) != 1 {
		t.Fatalf("held partial has %d tool calls, want 1", len(heldCalls))
	}
	heldArgs := string(heldCalls[0].Arguments)
	if heldArgs != `{"streamed":true}` {
		t.Fatalf("held partial arguments = %q, want %q", heldArgs, `{"streamed":true}`)
	}

	// A ToolCallEnd carrying different arguments arrives after the read.
	acc.Process(StreamEvent{Type: StreamEventToolCallEnd, ToolCall: &ToolCallData{
		ID: "c1", Name: "t1", Type: "function", Arguments: json.RawMessage(`{"final":"value"}`),
	}})

	if got := string(heldCalls[0].Arguments); got != heldArgs {
		t.Fatalf("held partial arguments changed from %q to %q: the ToolCallEnd rewrote the array the snapshot shares", heldArgs, got)
	}
	fresh := acc.PartialResponse()
	if fresh == nil {
		t.Fatal("PartialResponse() = nil after ToolCallEnd")
	}
	freshCalls := fresh.ToolCalls()
	if len(freshCalls) != 1 {
		t.Fatalf("fresh partial has %d tool calls, want 1", len(freshCalls))
	}
	if got := string(freshCalls[0].Arguments); got != `{"final":"value"}` {
		t.Fatalf("fresh partial arguments = %q, want the ToolCallEnd value %q", got, `{"final":"value"}`)
	}
}
