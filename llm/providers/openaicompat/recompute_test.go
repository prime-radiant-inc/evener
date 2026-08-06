package openaicompat

import (
	"testing"

	"primeradiant.com/serf/llm"
)

// TestExtractRecordedResponse_JSON pins that recompute reuses
// fromChatCompletionResponse -- the live non-streamed parser for the
// openai_compatible_chat_completions family -- rather than a second
// hand-rolled decoder: reasoning_content (a field a narrower struct could
// easily drop) must round-trip.
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

func TestExtractRecordedResponse_RejectsSSE(t *testing.T) {
	body := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n")

	if _, err := ExtractRecordedResponse(body, "gpt-5.2"); err == nil {
		t.Fatal("ExtractRecordedResponse accepted an SSE body; want an error (SSE recomputation not supported for this family)")
	}
}
