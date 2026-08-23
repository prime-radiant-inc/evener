package openai

import (
	"strings"
	"testing"
)

// TestExtractRecordedResponse_EmptyBody covers the empty body error path.
func TestExtractRecordedResponse_EmptyBody(t *testing.T) {
	if _, err := ExtractRecordedResponse([]byte("   "), "gpt-5.2"); err == nil {
		t.Fatal("empty body should error")
	}
}

// TestExtractRecordedResponse_InvalidJSON covers the JSON decode error path.
func TestExtractRecordedResponse_InvalidJSON(t *testing.T) {
	if _, err := ExtractRecordedResponse([]byte("{invalid"), "gpt-5.2"); err == nil {
		t.Fatal("invalid JSON should error")
	}
}

// TestExtractRecordedChatCompletionsResponse_EmptyBody covers the empty body
// error path.
func TestExtractRecordedChatCompletionsResponse_EmptyBody(t *testing.T) {
	if _, err := ExtractRecordedChatCompletionsResponse([]byte("  "), "gpt-5.2"); err == nil {
		t.Fatal("empty body should error")
	}
}

// TestIsChatCompletionsSSE_EmptyData covers the empty data path in the SSE
// scanner (line 86-87).
func TestIsChatCompletionsSSE_EmptyData(t *testing.T) {
	// An SSE body with only empty data events should return false.
	body := []byte("data: \n\ndata: \n\n")
	if isChatCompletionsSSE(body) {
		t.Fatal("isChatCompletionsSSE with only empty data should return false")
	}
}

// TestIsChatCompletionsSSE_DoneMarker covers the [DONE] marker path (lines 89-91).
func TestIsChatCompletionsSSE_DoneMarker(t *testing.T) {
	// An SSE body with [DONE] should return true.
	body := []byte("data: [DONE]\n\n")
	if !isChatCompletionsSSE(body) {
		t.Fatal("isChatCompletionsSSE with [DONE] should return true")
	}
}

// TestIsChatCompletionsSSE_ChoicesInData covers the choices detection path.
func TestIsChatCompletionsSSE_ChoicesInData(t *testing.T) {
	body := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	if !isChatCompletionsSSE(body) {
		t.Fatal("isChatCompletionsSSE with choices in data should return true")
	}
}

// TestIsChatCompletionsSSE_NonChoicesSSE returns false for a Responses-API SSE.
func TestIsChatCompletionsSSE_NonChoicesSSE(t *testing.T) {
	body := []byte("data: {\"type\":\"response.created\"}\n\n")
	if isChatCompletionsSSE(body) {
		t.Fatal("isChatCompletionsSSE with Responses API event should return false")
	}
}

// TestExtractResponsesFromSSE_EmptyDataEvent covers the len(ev.Data)==0 path.
func TestExtractResponsesFromSSE_EmptyDataEvent(t *testing.T) {
	// SSE with an empty data event followed by a valid response.completed.
	body := "data: \n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"model\":\"m\",\"output\":[]}}\n\n"
	resp, err := ExtractRecordedResponse([]byte(body), "m")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	if resp.ID != "r1" {
		t.Fatalf("ID = %q, want r1", resp.ID)
	}
}

// TestExtractResponsesFromSSE_InvalidJSONPayload covers the decodeSSEPayload
// false return path (line 120-121).
func TestExtractResponsesFromSSE_InvalidJSONPayload(t *testing.T) {
	// An invalid JSON payload should be skipped, not cause an error.
	body := "data: {not json}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"model\":\"m\",\"output\":[]}}\n\n"
	resp, err := ExtractRecordedResponse([]byte(body), "m")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	if resp.ID != "r1" {
		t.Fatalf("ID = %q, want r1", resp.ID)
	}
}

// TestExtractResponsesFromSSE_FallbackToEventName covers the typ=="" path
// where the event name is used as the type (line 124-125).
func TestExtractResponsesFromSSE_FallbackToEventName(t *testing.T) {
	var b strings.Builder
	b.WriteString("event: response.completed\n")
	b.WriteString("data: {\"response\":{\"id\":\"r1\",\"model\":\"m\",\"output\":[]}}\n\n")
	resp, err := ExtractRecordedResponse([]byte(b.String()), "m")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	if resp.ID != "r1" {
		t.Fatalf("ID = %q, want r1", resp.ID)
	}
}

// TestExtractResponsesFromSSE_FunctionCallArgumentsDone covers the
// response.function_call_arguments.done event (line 134).
func TestExtractResponsesFromSSE_FunctionCallArgumentsDone(t *testing.T) {
	var b strings.Builder
	write := func(event, data string) {
		b.WriteString("event: ")
		b.WriteString(event)
		b.WriteString("\ndata: ")
		b.WriteString(data)
		b.WriteString("\n\n")
	}
	write("response.output_item.added", `{"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_1","id":"item_1","name":"write_file"}}`)
	write("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","call_id":"call_1","delta":"{\"path\""}`)
	write("response.function_call_arguments.done", `{"type":"response.function_call_arguments.done","call_id":"call_1","arguments":"{\"path\":\"x\"}"}`)
	write("response.output_item.done", `{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","id":"item_1","name":"write_file","arguments":"{\"path\":\"x\"}"}}`)
	write("response.completed", `{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.2","output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)

	resp, err := ExtractRecordedResponse([]byte(b.String()), "gpt-5.2")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("ToolCalls() = %d, want 1", len(calls))
	}
}

// TestExtractResponsesFromSSE_ResponseCompletedNoResponseField covers the
// rawResp == nil fallback (line 140-141).
func TestExtractResponsesFromSSE_ResponseCompletedNoResponseField(t *testing.T) {
	// response.completed with no "response" key — the payload itself is used.
	body := "data: {\"type\":\"response.completed\",\"id\":\"r1\",\"model\":\"m\",\"output\":[]}\n\n"
	resp, err := ExtractRecordedResponse([]byte(body), "m")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	if resp.ID != "r1" {
		t.Fatalf("ID = %q, want r1", resp.ID)
	}
}

// TestExtractResponsesFromSSE_NoResponseCompleted covers the missing
// response.completed event error (line 150-151).
func TestExtractResponsesFromSSE_NoResponseCompleted(t *testing.T) {
	body := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\"}}\n\n"
	if _, err := ExtractRecordedResponse([]byte(body), "m"); err == nil {
		t.Fatal("SSE without response.completed should error")
	}
}

// TestExtractChatCompletionsFromSSE_EmptyData covers the data=="" path.
func TestExtractChatCompletionsFromSSE_EmptyData(t *testing.T) {
	var b strings.Builder
	b.WriteString("data: \n\n")
	b.WriteString("data: {\"model\":\"gpt-5.2\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	b.WriteString("data: [DONE]\n\n")
	resp, err := ExtractRecordedChatCompletionsResponse([]byte(b.String()), "gpt-5.2")
	if err != nil {
		t.Fatalf("ExtractRecordedChatCompletionsResponse: %v", err)
	}
	if resp.Text() != "hi" {
		t.Fatalf("Text() = %q, want hi", resp.Text())
	}
}

// TestExtractChatCompletionsFromSSE_InvalidChunk covers the JSON unmarshal
// error path (line 177-178).
func TestExtractChatCompletionsFromSSE_InvalidChunk(t *testing.T) {
	var b strings.Builder
	b.WriteString("data: {not valid json}\n\n")
	b.WriteString("data: {\"model\":\"gpt-5.2\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
	b.WriteString("data: [DONE]\n\n")
	resp, err := ExtractRecordedChatCompletionsResponse([]byte(b.String()), "gpt-5.2")
	if err != nil {
		t.Fatalf("ExtractRecordedChatCompletionsResponse: %v", err)
	}
	if resp.Text() != "ok" {
		t.Fatalf("Text() = %q, want ok", resp.Text())
	}
}

// TestExtractChatCompletionsFromSSE_NoChoices covers the len(choices)==0 path.
func TestExtractChatCompletionsFromSSE_NoChoices(t *testing.T) {
	var b strings.Builder
	b.WriteString("data: {\"model\":\"gpt-5.2\",\"choices\":[]}\n\n")
	b.WriteString("data: {\"model\":\"gpt-5.2\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	b.WriteString("data: [DONE]\n\n")
	resp, err := ExtractRecordedChatCompletionsResponse([]byte(b.String()), "gpt-5.2")
	if err != nil {
		t.Fatalf("ExtractRecordedChatCompletionsResponse: %v", err)
	}
	if resp.Text() != "hi" {
		t.Fatalf("Text() = %q, want hi", resp.Text())
	}
}

// TestExtractChatCompletionsFromSSE_ToolCallDelta covers the tool call delta
// handling path (line 189-190).
func TestExtractChatCompletionsFromSSE_ToolCallDelta(t *testing.T) {
	var b strings.Builder
	b.WriteString("data: {\"model\":\"gpt-5.2\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"write_file\",\"arguments\":\"{\\\"path\\\":\\\"x\\\"}\"}}]}}]}\n\n")
	b.WriteString("data: [DONE]\n\n")
	resp, err := ExtractRecordedChatCompletionsResponse([]byte(b.String()), "gpt-5.2")
	if err != nil {
		t.Fatalf("ExtractRecordedChatCompletionsResponse: %v", err)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 || calls[0].Name != "write_file" {
		t.Fatalf("ToolCalls() = %+v, want 1 call named write_file", calls)
	}
}

// TestExtractChatCompletionsFromSSE_NoDone covers the missing [DONE] error
// (line 197-198).
func TestExtractChatCompletionsFromSSE_NoDone(t *testing.T) {
	body := "data: {\"model\":\"gpt-5.2\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"
	if _, err := ExtractRecordedChatCompletionsResponse([]byte(body), "gpt-5.2"); err == nil {
		t.Fatal("SSE without [DONE] should error")
	}
}

// TestExtractChatCompletionsFromSSE_ModelFallback covers the r.Model=="" path
// where requestedModel is used (line 202-203).
func TestExtractChatCompletionsFromSSE_ModelFallback(t *testing.T) {
	// A chunk with no model field — the requestedModel should be used.
	var b strings.Builder
	b.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	b.WriteString("data: [DONE]\n\n")
	resp, err := ExtractRecordedChatCompletionsResponse([]byte(b.String()), "fallback-model")
	if err != nil {
		t.Fatalf("ExtractRecordedChatCompletionsResponse: %v", err)
	}
	if resp.Model != "fallback-model" {
		t.Fatalf("Model = %q, want fallback-model", resp.Model)
	}
}

// TestDecodeJSONObject_Invalid covers the JSON decode error (line 212-213).
func TestDecodeJSONObject_Invalid(t *testing.T) {
	if _, err := decodeJSONObject([]byte("{invalid")); err == nil {
		t.Fatal("decodeJSONObject with invalid JSON should error")
	}
}

// TestDecodeSSEPayload_Invalid covers the JSON decode error (line 222-223).
func TestDecodeSSEPayload_Invalid(t *testing.T) {
	if _, ok := decodeSSEPayload([]byte("{invalid")); ok {
		t.Fatal("decodeSSEPayload with invalid JSON should return false")
	}
}
