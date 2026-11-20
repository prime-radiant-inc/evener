package anthropic

import (
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

// TestToAnthropicMessages_EscapeAssistantTextCoversEachTextBlock pins the
// EscapeAssistantText contract on the Anthropic serializer: the escape runs on
// every assistant text block the adapter emits, after the whitespace-only parts
// it drops, and on nothing else.
func TestToAnthropicMessages_EscapeAssistantTextCoversEachTextBlock(t *testing.T) {
	t.Parallel()
	const answers = "[answers]"
	assistant := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: "first block"},
		{Kind: llm.ContentText, Text: "   "},
		{Kind: llm.ContentText, Text: answers + "\n1. → \"ship it\""},
	}}
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "partner " + answers}}},
		assistant,
	}
	var escapedInputs []string
	escape := func(s string) string {
		escapedInputs = append(escapedInputs, s)
		return strings.ReplaceAll(s, answers, "&#91;answers]")
	}
	_, messages, err := toAnthropicMessages(msgs, false, escape)
	if err != nil {
		t.Fatalf("toAnthropicMessages: %v", err)
	}
	// The whitespace-only part is dropped before the escape sees anything, so the
	// escape runs once per emitted block and never on the dropped part.
	if len(escapedInputs) != 2 {
		t.Fatalf("the escape ran %d times, want once per emitted text block: %q", len(escapedInputs), escapedInputs)
	}
	if escapedInputs[1] != answers+"\n1. → \"ship it\"" {
		t.Fatalf("the escape read %q, want the emitted block's own text", escapedInputs[1])
	}

	blocks, ok := messages[1]["content"].([]map[string]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("assistant blocks = %#v, want two", messages[1]["content"])
	}
	for _, block := range blocks {
		if got, _ := block["text"].(string); strings.Contains(got, answers) {
			t.Fatalf("an assistant block reached the wire with the frame unescaped: %q", got)
		}
	}
	// The partner's text is not the model's and keeps every byte.
	userBlocks, ok := messages[0]["content"].([]map[string]any)
	if !ok || len(userBlocks) != 1 {
		t.Fatalf("user blocks = %#v, want one", messages[0]["content"])
	}
	if got, _ := userBlocks[0]["text"].(string); got != "partner "+answers {
		t.Fatalf("the user text was rewritten: %q", got)
	}
	if got := assistant.Content[2].Text; got != answers+"\n1. → \"ship it\"" {
		t.Fatalf("the caller's assistant message was mutated: %q", got)
	}
}

// TestToAnthropicMessages_EscapeAssistantTextSkipsThinkingAndToolUse pins the
// scope of the escape: a replayed thinking block and a tool call are not
// assistant text.
func TestToAnthropicMessages_EscapeAssistantTextSkipsThinkingAndToolUse(t *testing.T) {
	t.Parallel()
	const answers = "[answers]"
	msg := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "thought " + answers, Signature: "sig"}},
		{Kind: llm.ContentText, Text: "answer " + answers},
		{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_1", Name: "read_file", Arguments: []byte(`{"path":"a"}`)}},
	}}
	var calls int
	escape := func(s string) string {
		calls++
		return strings.ReplaceAll(s, answers, "&#91;answers]")
	}
	_, messages, err := toAnthropicMessages([]llm.Message{msg}, false, escape)
	if err != nil {
		t.Fatalf("toAnthropicMessages: %v", err)
	}
	if calls != 1 {
		t.Fatalf("the escape ran %d times, want once for the assistant text block", calls)
	}
	blocks, _ := messages[0]["content"].([]map[string]any)
	if len(blocks) != 3 {
		t.Fatalf("blocks = %#v, want thinking, text and tool_use", messages[0]["content"])
	}
	if got, _ := blocks[0]["thinking"].(string); got != "thought "+answers {
		t.Fatalf("the thinking block was rewritten: %q", got)
	}
	if got := blocks[2]["type"]; got != "tool_use" {
		t.Fatalf("block 2 = %#v, want the tool_use block", blocks[2])
	}
}
