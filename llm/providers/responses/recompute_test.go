package responses

import (
	"strings"
	"testing"
)

// syntheticResponsesSSE builds a raw Responses-API SSE body mirroring the
// affected wire shape: a function_call and a text message item arrive via
// response.output_item.done events, but the terminal response.completed
// payload's "output" is empty. This is the exact stored-body shape apilog
// --recompute must re-extract counts from.
func syntheticResponsesSSE() string {
	var b strings.Builder
	write := func(event, data string) {
		b.WriteString("event: ")
		b.WriteString(event)
		b.WriteString("\ndata: ")
		b.WriteString(data)
		b.WriteString("\n\n")
	}
	write("response.created", `{"type":"response.created","response":{"id":"resp_1"}}`)
	write("response.output_item.added", `{"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_1","id":"item_1","name":"write_file"}}`)
	write("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","call_id":"call_1","delta":"{\"path\":\"x\"}"}`)
	write("response.output_item.done", `{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","id":"item_1","name":"write_file","arguments":"{\"path\":\"x\"}"}}`)
	write("response.output_text.delta", `{"type":"response.output_text.delta","delta":"Hello"}`)
	write("response.output_item.done", `{"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"Hello"}]}}`)
	write("response.completed", `{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.2","output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)
	return b.String()
}

func TestExtractRecordedResponse_ResponsesSSE_TerminalWinsWhenNonEmpty(t *testing.T) {
	var b strings.Builder
	write := func(event, data string) {
		b.WriteString("event: ")
		b.WriteString(event)
		b.WriteString("\ndata: ")
		b.WriteString(data)
		b.WriteString("\n\n")
	}
	write("response.output_item.added", `{"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_1","id":"item_1","name":"write_file"}}`)
	write("response.output_item.done", `{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","id":"item_1","name":"write_file","arguments":"{\"path\":\"x\"}"}}`)
	write("response.completed", `{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.2","output":[{"type":"message","content":[{"type":"output_text","text":"Different terminal text"}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)

	resp, err := ExtractRecordedResponse([]byte(b.String()), "gpt-5.2")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	if strings.TrimSpace(resp.Text()) != "Different terminal text" {
		t.Fatalf("Text() = %q, want terminal payload's text", resp.Text())
	}
	if calls := resp.ToolCalls(); len(calls) != 0 {
		t.Fatalf("ToolCalls() = %d, want 0: %+v", len(calls), calls)
	}
}

func TestExtractRecordedResponse_ResponsesJSON(t *testing.T) {
	body := []byte(`{"id":"resp_1","model":"gpt-5.2","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hi there"}]}]}`)

	resp, err := ExtractRecordedResponse(body, "gpt-5.2")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	if strings.TrimSpace(resp.Text()) != "hi there" {
		t.Fatalf("Text() = %q, want %q", resp.Text(), "hi there")
	}
}

func TestExtractRecordedResponse_RejectsChatCompletionsJSON(t *testing.T) {
	body := []byte(`{"id":"chatcmpl_1","model":"gpt-5.2","choices":[{"finish_reason":"tool_calls","message":{"content":"","tool_calls":[{"id":"call_1","function":{"name":"write_file","arguments":"{\"path\":\"x\"}"}}]}}]}`)

	if _, err := ExtractRecordedResponse(body, "gpt-5.2"); err == nil {
		t.Fatal("ExtractRecordedResponse accepted a Chat Completions JSON body; want an error directing callers elsewhere")
	}
}

func TestExtractRecordedResponse_RejectsChatCompletionsSSE(t *testing.T) {
	var b strings.Builder
	write := func(data string) {
		b.WriteString("data: ")
		b.WriteString(data)
		b.WriteString("\n\n")
	}
	write(`{"model":"gpt-5.2","choices":[{"delta":{"content":"Hello"}}]}`)
	write(`[DONE]`)

	if _, err := ExtractRecordedResponse([]byte(b.String()), "gpt-5.2"); err == nil {
		t.Fatal("ExtractRecordedResponse accepted a Chat Completions SSE body; want an error directing callers elsewhere")
	}
}

func TestExtractRecordedResponse_ResponsesSSE_SynthesizesFromAccumulatedItems(t *testing.T) {
	body := []byte(syntheticResponsesSSE())

	resp, err := ExtractRecordedResponse(body, "gpt-5.2")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	if strings.TrimSpace(resp.Text()) != "Hello" {
		t.Fatalf("Text() = %q, want %q", resp.Text(), "Hello")
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("ToolCalls() = %d, want 1 (%+v)", len(calls), calls)
	}
	if calls[0].Name != "write_file" {
		t.Fatalf("tool call name = %q, want write_file", calls[0].Name)
	}
}

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

// TestIsChatCompletionsSSE_EmptyData covers the empty data path in the SSE
// scanner.
func TestIsChatCompletionsSSE_EmptyData(t *testing.T) {
	// An SSE body with only empty data events should return false.
	body := []byte("data: \n\ndata: \n\n")
	if isChatCompletionsSSE(body) {
		t.Fatal("isChatCompletionsSSE with only empty data should return false")
	}
}

// TestIsChatCompletionsSSE_DoneMarker covers the [DONE] marker path.
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
// false return path.
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
// where the event name is used as the type.
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
// response.function_call_arguments.done event.
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
// rawResp == nil fallback.
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
// response.completed event error.
func TestExtractResponsesFromSSE_NoResponseCompleted(t *testing.T) {
	body := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\"}}\n\n"
	if _, err := ExtractRecordedResponse([]byte(body), "m"); err == nil {
		t.Fatal("SSE without response.completed should error")
	}
}

// TestDecodeJSONObject_Invalid covers the JSON decode error.
func TestDecodeJSONObject_Invalid(t *testing.T) {
	if _, err := decodeJSONObject([]byte("{invalid")); err == nil {
		t.Fatal("decodeJSONObject with invalid JSON should error")
	}
}

// TestDecodeSSEPayload_Invalid covers the JSON decode error.
func TestDecodeSSEPayload_Invalid(t *testing.T) {
	if _, ok := decodeSSEPayload([]byte("{invalid")); ok {
		t.Fatal("decodeSSEPayload with invalid JSON should return false")
	}
}
