package chatcompletions

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func assistantTurn(thinking, sig, text string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: thinking, Signature: sig}},
		{Kind: llm.ContentText, Text: text},
	}}
}

func TestToChatMessages_RoleAndContentCaps(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "sys"}}},
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: ""}, {Kind: llm.ContentText, Text: "hi"}}},
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "f", Arguments: json.RawMessage(`{"a":1}`)}}}},
		{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c1", Name: "f", Content: "ok"}}}},
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "next"}}},
	}
	caps := registry.Caps{Fields: registry.Baseline(registry.ProtocolOpenAIChat)}
	caps.Fields[registry.FieldDeveloperRole] = true
	caps.ToolResultName = new(true)
	caps.StripEmptyContent = new(true)
	caps.AssistantAfterToolResult = new(true)
	out, err := toChatMessages(msgs, caps, false)
	if err != nil {
		t.Fatal(err)
	}
	if out[0]["role"] != "developer" {
		t.Fatalf("developer role: %v", out[0])
	}
	if out[1]["content"] != "hi" {
		t.Fatalf("empty text must be stripped: %v", out[1])
	}
	if out[3]["name"] != "f" {
		t.Fatalf("tool result name: %v", out[3])
	}
	if out[4]["role"] != "assistant" || out[4]["content"] != "" || out[5]["role"] != "user" {
		t.Fatalf("assistant turn must be inserted after the tool result: %v", out[4:])
	}
}

func TestToChatMessages_ReasoningReplay(t *testing.T) {
	base := registry.Caps{Fields: registry.Baseline(registry.ProtocolOpenAIChat)}
	turn := []llm.Message{assistantTurn("thought", "reasoning", "answer")}

	out, _ := toChatMessages(turn, base, false)
	if out[0]["reasoning"] != "thought" {
		t.Fatalf("Signature names the field the text arrived on: %v", out[0])
	}

	field := base
	field.ReasoningField = new("reasoning_content")
	out, _ = toChatMessages(turn, field, false)
	if out[0]["reasoning_content"] != "thought" || out[0]["reasoning"] != nil {
		t.Fatalf("ReasoningField wins over the signature: %v", out[0])
	}

	details := base
	details.ReasoningField = new("reasoning_details")
	out, _ = toChatMessages(turn, details, true)
	items, ok := out[0]["reasoning_details"].([]map[string]any)
	if !ok || len(items) != 1 || items[0]["text"] != "thought" || items[0]["type"] != "reasoning.text" {
		t.Fatalf("reasoning_details replay: %v", out[0])
	}

	signed := []llm.Message{{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "thought", Signature: "reasoning_details", EncryptedContent: `[{"type":"reasoning.text","text":"","signature":"sig-1","format":"anthropic-claude-v1","index":0}]`}},
		{Kind: llm.ContentText, Text: "answer"},
	}}}
	out, _ = toChatMessages(signed, base, false)
	items = out[0]["reasoning_details"].([]map[string]any)
	if len(items) != 1 || items[0]["text"] != "thought" || items[0]["signature"] != "sig-1" {
		t.Fatalf("signed item must absorb the text: %v", items)
	}

	asText := base
	asText.ThinkingAsText = new(true)
	out, _ = toChatMessages(turn, asText, false)
	if out[0]["content"] != "thought\n\nanswer" || out[0]["reasoning"] != nil {
		t.Fatalf("thinking as text: %v", out[0])
	}

	off := base
	off.Reasoning = new(false)
	out, _ = toChatMessages(turn, off, false)
	if out[0]["reasoning"] != nil || out[0]["reasoning_content"] != nil || out[0]["reasoning_details"] != nil {
		t.Fatalf("Reasoning=false drops replayed thinking: %v", out[0])
	}

	empty := base
	empty.EmptyReasoningContent = new(true)
	out, _ = toChatMessages([]llm.Message{{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "plain"}}}}, empty, false)
	if v, has := out[0]["reasoning_content"]; !has || v != "" {
		t.Fatalf("empty reasoning_content on assistant turns: %v", out[0])
	}
}
