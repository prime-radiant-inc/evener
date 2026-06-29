package openai

import (
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

// toolCallSeq returns the comma-joined names of the tool calls in a decoded
// assistant message, in the order they appear in the message content.
func toolCallSeq(r *llm.Response) string {
	if r == nil {
		return ""
	}
	var names []string
	for _, part := range r.Message.Content {
		if part.Kind == llm.ContentToolCall && part.ToolCall != nil {
			names = append(names, part.ToolCall.Name)
		}
	}
	return strings.Join(names, ",")
}

// TestChatCompletionsParallelToolCallOrderStable pins that two parallel tool
// calls in one Chat Completions stream accumulate in a stable wire-order (by
// delta index) across repeated parses of the identical bytes. The [DONE] handler
// used to range a map[int]*toolCallState directly, so Go's randomized map
// iteration shuffled the assembled message's tool-call order run-to-run.
func TestChatCompletionsParallelToolCallOrderStable(t *testing.T) {
	sse := []byte(strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[` +
			`{"index":0,"id":"call_a","type":"function","function":{"name":"alpha","arguments":"{}"}},` +
			`{"index":1,"id":"call_b","type":"function","function":{"name":"beta","arguments":"{}"}}]}}]}`,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		"data: [DONE]",
		"",
	}, "\n\n"))

	a := &Adapter{BaseURL: "http://fuzz.local"}
	var first string
	for i := 0; i < 50; i++ {
		resp, _ := accumulateChatCompletionsSSE(a, sse, false)
		got := toolCallSeq(resp)
		if got == "" {
			t.Fatalf("parse %d: expected two tool calls, got none", i)
		}
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("parallel tool-call order not deterministic: parse %d = %q, first = %q", i, got, first)
		}
	}
	if first != "alpha,beta" {
		t.Fatalf("tool calls not assembled in wire (index) order: got %q, want %q", first, "alpha,beta")
	}
}
