package llm

import (
	"encoding/json"
	"testing"
)

func TestStreamAccumulator_FinishWithResponse_UsesIt(t *testing.T) {
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
	acc := NewStreamAccumulator()
	acc.Process(StreamEvent{Type: StreamEventStreamStart})
	acc.Process(StreamEvent{Type: StreamEventTextStart, TextID: "t1"})
	acc.Process(StreamEvent{Type: StreamEventTextDelta, TextID: "t1", Delta: "Hello"})
	acc.Process(StreamEvent{Type: StreamEventTextEnd, TextID: "t1"})

	final := Response{
		ID:       "resp_1",
		Provider: "openai",
		Model:    "gpt-5.4",
		Raw:      map[string]any{"id": "resp_1"},
	}
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
}

func TestStreamAccumulator_FinishWithContentResponsePreservesFinalToolCallItemID(t *testing.T) {
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

func TestStreamAccumulator_ToolCallEndOverridesAccumulatedArgumentsAndName(t *testing.T) {
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

func TestStreamAccumulator_ReasoningAndToolCallsTogether(t *testing.T) {
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
