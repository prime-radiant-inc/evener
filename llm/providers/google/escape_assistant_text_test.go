package google

import (
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

// TestToGeminiContents_EscapeAssistantTextCoversEachTextPart pins the
// EscapeAssistantText contract on the Google serializer: the escape runs on
// every assistant text part the adapter emits, after the whitespace-only parts
// it drops, and on nothing else.
func TestToGeminiContents_EscapeAssistantTextCoversEachTextPart(t *testing.T) {
	t.Parallel()
	const answers = "[answers]"
	assistant := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: "first part"},
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
	_, contents, err := toGeminiContents("gemini-3-pro", msgs, false, escape)
	if err != nil {
		t.Fatalf("toGeminiContents: %v", err)
	}
	if len(escapedInputs) != 2 {
		t.Fatalf("the escape ran %d times, want once per emitted text part: %q", len(escapedInputs), escapedInputs)
	}
	if escapedInputs[1] != answers+"\n1. → \"ship it\"" {
		t.Fatalf("the escape read %q, want the emitted part's own text", escapedInputs[1])
	}

	modelParts, ok := contents[1]["parts"].([]map[string]any)
	if !ok || len(modelParts) != 2 {
		t.Fatalf("model parts = %#v, want two", contents[1]["parts"])
	}
	for _, part := range modelParts {
		if got, _ := part["text"].(string); strings.Contains(got, answers) {
			t.Fatalf("an assistant part reached the wire with the frame unescaped: %q", got)
		}
	}
	userParts, ok := contents[0]["parts"].([]map[string]any)
	if !ok || len(userParts) != 1 {
		t.Fatalf("user parts = %#v, want one", contents[0]["parts"])
	}
	if got, _ := userParts[0]["text"].(string); got != "partner "+answers {
		t.Fatalf("the user text was rewritten: %q", got)
	}
	if got := assistant.Content[2].Text; got != answers+"\n1. → \"ship it\"" {
		t.Fatalf("the caller's assistant message was mutated: %q", got)
	}
}

// TestToGeminiContents_EscapeAssistantTextSkipsFunctionCalls pins the scope of
// the escape: a function call part and a function response part are not
// assistant text.
func TestToGeminiContents_EscapeAssistantTextSkipsFunctionCalls(t *testing.T) {
	t.Parallel()
	const answers = "[answers]"
	call := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: "answer " + answers},
		{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_1", Name: "read_file", Arguments: []byte(`{"path":"a"}`)}},
	}}
	result := llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_1", Name: "read_file", Content: answers + " from a result"}},
	}}
	var calls int
	escape := func(s string) string {
		calls++
		return strings.ReplaceAll(s, answers, "&#91;answers]")
	}
	_, contents, err := toGeminiContents("gemini-3-pro", []llm.Message{call, result}, false, escape)
	if err != nil {
		t.Fatalf("toGeminiContents: %v", err)
	}
	if calls != 1 {
		t.Fatalf("the escape ran %d times, want once for the assistant text part", calls)
	}
	modelParts, _ := contents[0]["parts"].([]map[string]any)
	if len(modelParts) != 2 {
		t.Fatalf("model parts = %#v, want text and functionCall", contents[0]["parts"])
	}
	if _, ok := modelParts[1]["functionCall"]; !ok {
		t.Fatalf("part 1 = %#v, want the functionCall part", modelParts[1])
	}
	if got, _ := modelParts[0]["text"].(string); strings.Contains(got, answers) {
		t.Fatalf("the assistant text kept its frame: %q", got)
	}
}
