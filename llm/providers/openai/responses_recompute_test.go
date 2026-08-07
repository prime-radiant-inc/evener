package openai

import (
	"strings"
	"testing"
)

// syntheticResponsesSSE builds a raw Responses-API SSE body mirroring the
// affected wire shape (see responses_recording_test.go): a function_call and
// a text message item arrive via response.output_item.done events, but the
// terminal response.completed payload's "output" is empty. This is the exact
// stored-body shape apilog --recompute must re-extract counts from.
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

func TestExtractRecordedChatCompletionsResponse_SSE(t *testing.T) {
	var b strings.Builder
	write := func(data string) {
		b.WriteString("data: ")
		b.WriteString(data)
		b.WriteString("\n\n")
	}
	write(`{"model":"gpt-5.2","choices":[{"delta":{"content":"Hel"}}]}`)
	write(`{"model":"gpt-5.2","choices":[{"delta":{"content":"lo"}}]}`)
	write(`{"model":"gpt-5.2","choices":[{"finish_reason":"stop","delta":{}}]}`)
	write(`[DONE]`)

	resp, err := ExtractRecordedChatCompletionsResponse([]byte(b.String()), "gpt-5.2")
	if err != nil {
		t.Fatalf("ExtractRecordedChatCompletionsResponse: %v", err)
	}
	if resp.Text() != "Hello" {
		t.Fatalf("Text() = %q, want %q", resp.Text(), "Hello")
	}
}

func TestExtractRecordedChatCompletionsResponse_RejectsJSON(t *testing.T) {
	body := []byte(`{"id":"chatcmpl_1","model":"gpt-5.2","choices":[{"finish_reason":"stop","message":{"content":"hi"}}]}`)

	if _, err := ExtractRecordedChatCompletionsResponse(body, "gpt-5.2"); err == nil {
		t.Fatal("ExtractRecordedChatCompletionsResponse accepted a JSON body; want an error (this family has no non-streamed live parser)")
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
