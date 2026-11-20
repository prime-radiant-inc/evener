package chatcompletions

import (
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// TestBuildBody_EscapeAssistantTextCoversTheAssembledBody pins the
// EscapeAssistantText contract on the Chat Completions serializer: the escape
// runs on the assistant content the adapter assembles — the joined text parts
// with any promoted reasoning already in front of them — and on nothing else.
func TestBuildBody_EscapeAssistantTextCoversTheAssembledBody(t *testing.T) {
	t.Parallel()
	const (
		reasoning  = "considering the fold"
		answers    = "[answers]"
		userText   = "the partner's words"
		systemText = "system words"
	)
	caps := registry.Caps{ThinkingAsText: new(true), Fields: registry.Baseline(registry.ProtocolOpenAIChat)}
	res := registry.Resolved{
		Instance:  "openai",
		Protocol:  registry.ProtocolOpenAIChat,
		ModelID:   "gpt-5.4",
		WireID:    "gpt-5.4",
		Transport: registry.Transport{Endpoint: "/chat/completions"},
		Caps:      caps,
	}
	assistant := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: reasoning}},
		{Kind: llm.ContentText, Text: "first part"},
		{Kind: llm.ContentText, Text: answers + "\n1. → \"ship it\""},
	}}
	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: systemText}}},
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: userText}}},
		assistant,
		{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
			ToolCallID: "call_1", Content: answers + " from a tool result",
		}}}},
	}
	// The escape marks the payload it received, so the assertion is about where
	// it ran rather than about the frame spelling alone.
	var escapedInputs []string
	escape := func(s string) string {
		escapedInputs = append(escapedInputs, s)
		return strings.ReplaceAll(s, answers, "&#91;answers]")
	}
	req := llm.Request{Model: "gpt-5.4", Messages: msgs, EscapeAssistantText: escape}
	body, err := (&Protocol{}).BuildBody(llm.ShapeRequest(req, res), res)
	if err != nil {
		t.Fatalf("BuildBody: %v", err)
	}
	out, ok := body["messages"].([]map[string]any)
	if !ok || len(out) != 4 {
		t.Fatalf("serializer wrote %d messages, want 4", len(out))
	}

	if len(escapedInputs) != 1 {
		t.Fatalf("the escape ran %d times, want once for the one assistant body: %q", len(escapedInputs), escapedInputs)
	}
	// The input is the assembled body: promoted reasoning in front of the joined
	// text parts, which is what the provider reads as one message.
	want := reasoning + "\n\nfirst part\n" + answers + "\n1. → \"ship it\""
	if escapedInputs[0] != want {
		t.Fatalf("the escape read %q, want the assembled assistant body %q", escapedInputs[0], want)
	}
	content, _ := out[2]["content"].(string)
	if strings.Contains(content, answers) {
		t.Fatalf("the assistant body reached the wire with the frame unescaped: %q", content)
	}
	// The reasoning is part of the same body, so it is escaped with it.
	if !strings.Contains(content, reasoning+"\n\n") {
		t.Fatalf("the promoted reasoning left the assembled body: %q", content)
	}
	for i, wantText := range map[int]string{0: systemText, 1: userText} {
		if got, _ := out[i]["content"].(string); got != wantText {
			t.Fatalf("message %d = %q, want the caller's own bytes %q", i, got, wantText)
		}
	}
	if got, _ := out[3]["content"].(string); got != answers+" from a tool result" {
		t.Fatalf("the tool result was rewritten: %q", got)
	}
	// The caller's messages keep every byte.
	if got := assistant.Content[2].Text; got != answers+"\n1. → \"ship it\"" {
		t.Fatalf("the caller's assistant message was mutated: %q", got)
	}
}

// TestBuildBody_EscapeAssistantTextSkipsToolCallArguments pins the scope of the
// escape: a tool call's arguments are not assistant text and never pass
// through it.
func TestBuildBody_EscapeAssistantTextSkipsToolCallArguments(t *testing.T) {
	t.Parallel()
	const answers = "[answers]"
	args := `{"path":"` + answers + `"}`
	msg := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: "reading " + answers},
		{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_1", Name: "read_file", Arguments: []byte(args)}},
	}}
	var calls int
	escape := func(s string) string {
		calls++
		return strings.ReplaceAll(s, answers, "&#91;answers]")
	}
	out, err := toChatMessages([]llm.Message{msg}, registry.Caps{}, false, escape)
	if err != nil {
		t.Fatalf("toChatMessages: %v", err)
	}
	if calls != 1 {
		t.Fatalf("the escape ran %d times, want once for the assistant text alone", calls)
	}
	callsOut, ok := out[0]["tool_calls"].([]map[string]any)
	if !ok || len(callsOut) != 1 {
		t.Fatalf("tool_calls = %#v, want one call", out[0]["tool_calls"])
	}
	fn, _ := callsOut[0]["function"].(map[string]any)
	if got, _ := fn["arguments"].(string); got != args {
		t.Fatalf("the tool call's arguments were rewritten: %q, want %q", got, args)
	}
}
