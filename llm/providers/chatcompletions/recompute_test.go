package chatcompletions

import (
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

// TestExtractRecordedResponse_JSON pins that recompute reuses
// fromChatCompletionResponse — the live non-streamed parser for the
// openai_chat_completions family — rather than a second hand-rolled
// decoder: reasoning_content (a field a narrower struct could easily drop)
// must round-trip.
func TestExtractRecordedResponse_JSON(t *testing.T) {
	body := []byte(`{"id":"chatcmpl_1","model":"","choices":[{"finish_reason":"tool_calls","message":{"content":"","reasoning_content":"thinking...","tool_calls":[{"id":"call_1","function":{"name":"write_file","arguments":"{\"path\":\"x\"}"}}]}}]}`)

	resp, err := ExtractRecordedResponse(body, "fallback-model")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 || calls[0].Name != "write_file" {
		t.Fatalf("ToolCalls() = %+v, want one write_file call", calls)
	}
	if resp.Model != "fallback-model" {
		t.Fatalf("Model = %q, want fallback to requestedModel when the body omits its own", resp.Model)
	}
	foundReasoning := false
	for _, p := range resp.Message.Content {
		if p.Kind == llm.ContentThinking && p.Thinking != nil && p.Thinking.Text == "thinking..." {
			foundReasoning = true
		}
	}
	if !foundReasoning {
		t.Fatalf("reasoning_content did not round-trip through the shared parser: %+v", resp.Message.Content)
	}
}

func TestExtractRecordedResponse_SSE(t *testing.T) {
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

	resp, err := ExtractRecordedResponse([]byte(b.String()), "gpt-5.2")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	if resp.Text() != "Hello" {
		t.Fatalf("Text() = %q, want %q", resp.Text(), "Hello")
	}
}

// TestExtractRecordedResponse_EmptyBody covers the empty body error path.
func TestExtractRecordedResponse_EmptyBody(t *testing.T) {
	if _, err := ExtractRecordedResponse([]byte("  "), "gpt-5.2"); err == nil {
		t.Fatal("empty body should error")
	}
}

// TestExtractRecordedResponse_InvalidJSON covers the JSON decode error path.
func TestExtractRecordedResponse_InvalidJSON(t *testing.T) {
	if _, err := ExtractRecordedResponse([]byte("{invalid"), "gpt-5.2"); err == nil {
		t.Fatal("invalid JSON should error")
	}
}

// TestExtractRecordedResponse_SSEEmptyData covers the empty data path.
func TestExtractRecordedResponse_SSEEmptyData(t *testing.T) {
	var b strings.Builder
	b.WriteString("data: \n\n")
	b.WriteString("data: {\"model\":\"gpt-5.2\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	b.WriteString("data: [DONE]\n\n")
	resp, err := ExtractRecordedResponse([]byte(b.String()), "gpt-5.2")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	if resp.Text() != "hi" {
		t.Fatalf("Text() = %q, want hi", resp.Text())
	}
}

// TestExtractRecordedResponse_SSEInvalidChunk covers the JSON unmarshal error
// path: one malformed chunk is skipped, not fatal.
func TestExtractRecordedResponse_SSEInvalidChunk(t *testing.T) {
	var b strings.Builder
	b.WriteString("data: {not valid json}\n\n")
	b.WriteString("data: {\"model\":\"gpt-5.2\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
	b.WriteString("data: [DONE]\n\n")
	resp, err := ExtractRecordedResponse([]byte(b.String()), "gpt-5.2")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	if resp.Text() != "ok" {
		t.Fatalf("Text() = %q, want ok", resp.Text())
	}
}

// TestExtractRecordedResponse_SSENoChoices covers the len(choices)==0 path.
func TestExtractRecordedResponse_SSENoChoices(t *testing.T) {
	var b strings.Builder
	b.WriteString("data: {\"model\":\"gpt-5.2\",\"choices\":[]}\n\n")
	b.WriteString("data: {\"model\":\"gpt-5.2\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	b.WriteString("data: [DONE]\n\n")
	resp, err := ExtractRecordedResponse([]byte(b.String()), "gpt-5.2")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	if resp.Text() != "hi" {
		t.Fatalf("Text() = %q, want hi", resp.Text())
	}
}

// TestExtractRecordedResponse_SSEToolCallDelta covers the tool call delta
// handling path.
func TestExtractRecordedResponse_SSEToolCallDelta(t *testing.T) {
	var b strings.Builder
	b.WriteString("data: {\"model\":\"gpt-5.2\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"write_file\",\"arguments\":\"{\\\"path\\\":\\\"x\\\"}\"}}]}}]}\n\n")
	b.WriteString("data: [DONE]\n\n")
	resp, err := ExtractRecordedResponse([]byte(b.String()), "gpt-5.2")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 || calls[0].Name != "write_file" {
		t.Fatalf("ToolCalls() = %+v, want 1 call named write_file", calls)
	}
}

// TestExtractRecordedResponse_SSENoDone covers the missing [DONE] error.
func TestExtractRecordedResponse_SSENoDone(t *testing.T) {
	body := "data: {\"model\":\"gpt-5.2\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"
	if _, err := ExtractRecordedResponse([]byte(body), "gpt-5.2"); err == nil {
		t.Fatal("SSE without [DONE] should error")
	}
}

// TestExtractRecordedResponse_SSEModelFallback covers the Model=="" path
// where requestedModel is used.
func TestExtractRecordedResponse_SSEModelFallback(t *testing.T) {
	// A chunk with no model field — the requestedModel should be used.
	var b strings.Builder
	b.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	b.WriteString("data: [DONE]\n\n")
	resp, err := ExtractRecordedResponse([]byte(b.String()), "fallback-model")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	if resp.Model != "fallback-model" {
		t.Fatalf("Model = %q, want fallback-model", resp.Model)
	}
}
