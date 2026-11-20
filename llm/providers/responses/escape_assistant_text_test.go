package responses

import (
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

// assistantMessageItems returns every assistant message item a built input
// array carries, each as its own output_text payloads.
func assistantMessageItems(t *testing.T, items []any) [][]string {
	t.Helper()
	var out [][]string
	for _, i := range items {
		m, _ := i.(map[string]any)
		if m["type"] != "message" || m["role"] != "assistant" {
			continue
		}
		content, _ := m["content"].([]any)
		var texts []string
		for _, c := range content {
			cm, _ := c.(map[string]any)
			if cm["type"] != "output_text" {
				continue
			}
			text, _ := cm["text"].(string)
			texts = append(texts, text)
		}
		out = append(out, texts)
	}
	return out
}

// TestToResponsesInput_EscapeAssistantTextCoversEachOutputText pins the
// EscapeAssistantText contract on the Responses serializer: the escape runs on
// every assistant output_text the adapter emits, after the phase run and the
// replayed output items around it are settled, and on nothing else.
func TestToResponsesInput_EscapeAssistantTextCoversEachOutputText(t *testing.T) {
	t.Parallel()
	const answers = "[answers]"
	assistant := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Phase: "commentary", Text: "looking at this"},
		{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_1", Name: "read_file", Arguments: []byte(`{}`)}},
		{Kind: llm.ContentText, Phase: "final_answer", Text: answers + "\n1. → \"ship it\""},
	}}
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "partner " + answers}}},
		assistant,
		{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
			ToolCallID: "call_1", Content: answers + " from a tool result",
		}}}},
	}
	var escapedInputs []string
	escape := func(s string) string {
		escapedInputs = append(escapedInputs, s)
		return strings.ReplaceAll(s, answers, "&#91;answers]")
	}
	_, items, err := toResponsesInput(msgs, "", escape)
	if err != nil {
		t.Fatalf("toResponsesInput: %v", err)
	}
	// Two assistant messages — one per phase run, split by the tool call — so the
	// escape runs twice, once per emitted payload.
	if len(escapedInputs) != 2 {
		t.Fatalf("the escape ran %d times, want once per output_text: %q", len(escapedInputs), escapedInputs)
	}
	if escapedInputs[1] != answers+"\n1. → \"ship it\"" {
		t.Fatalf("the escape read %q, want the emitted output_text's own text", escapedInputs[1])
	}
	messages := assistantMessageItems(t, items)
	if len(messages) != 2 {
		t.Fatalf("serializer wrote %d assistant messages, want two phase runs: %#v", len(messages), items)
	}
	for _, texts := range messages {
		for _, text := range texts {
			if strings.Contains(text, answers) {
				t.Fatalf("an assistant output_text reached the wire with the frame unescaped: %q", text)
			}
		}
	}

	// The input_text payloads are not assistant text.
	for _, i := range items {
		m, _ := i.(map[string]any)
		if m["type"] != "message" || m["role"] != "user" {
			continue
		}
		content, _ := m["content"].([]any)
		for _, c := range content {
			cm, _ := c.(map[string]any)
			if cm["type"] == "input_text" {
				if got, _ := cm["text"].(string); got != "partner "+answers {
					t.Fatalf("the user text was rewritten: %q", got)
				}
			}
		}
	}
	// The caller's messages keep every byte.
	if got := assistant.Content[2].Text; got != answers+"\n1. → \"ship it\"" {
		t.Fatalf("the caller's assistant message was mutated: %q", got)
	}
}

// TestToResponsesInput_EscapeAssistantTextSkipsReasoningAndCallOutput pins the
// scope of the escape: a replayed reasoning item and a function_call_output are
// not assistant text.
func TestToResponsesInput_EscapeAssistantTextSkipsReasoningAndCallOutput(t *testing.T) {
	t.Parallel()
	const answers = "[answers]"
	round := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{ID: "rs_1", EncryptedContent: "opaque-blob"}},
		{Kind: llm.ContentText, Text: answers + "\n1. → \"ship it\""},
	}}
	var calls int
	escape := func(s string) string {
		calls++
		return strings.ReplaceAll(s, answers, "&#91;answers]")
	}
	_, items, err := toResponsesInput([]llm.Message{round}, "", escape)
	if err != nil {
		t.Fatalf("toResponsesInput: %v", err)
	}
	if calls != 1 {
		t.Fatalf("the escape ran %d times, want once for the assistant output_text", calls)
	}
	var sawReasoning bool
	for _, i := range items {
		m, _ := i.(map[string]any)
		if m["type"] == "reasoning" {
			sawReasoning = true
			if got, _ := m["encrypted_content"].(string); got != "opaque-blob" {
				t.Fatalf("the reasoning item was rewritten: %#v", m)
			}
		}
	}
	if !sawReasoning {
		t.Fatalf("test premise: the serializer wrote no reasoning item: %#v", items)
	}
}
