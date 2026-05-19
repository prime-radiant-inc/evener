package appwire

import (
	"encoding/json"
	"testing"
)

func TestThreadItemMarshalUsesCodexItemTypes(t *testing.T) {
	tests := map[string]string{
		"user_message":  "userMessage",
		"agent_message": "agentMessage",
		"tool_call":     "commandExecution",
	}
	for in, want := range tests {
		data, err := json.Marshal(ThreadItem{Type: in, ID: "item_1"})
		if err != nil {
			t.Fatalf("marshal %q: %v", in, err)
		}
		var got struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("decode %s: %v", data, err)
		}
		if got.Type != want {
			t.Fatalf("type=%q, want %q in %s", got.Type, want, data)
		}
	}
}

func TestThreadItemUnmarshalAcceptsCodexAndLegacyItemTypes(t *testing.T) {
	tests := map[string]string{
		"userMessage":      "user_message",
		"user_message":     "user_message",
		"agentMessage":     "agent_message",
		"agent_message":    "agent_message",
		"commandExecution": "tool_call",
		"mcpToolCall":      "tool_call",
		"dynamicToolCall":  "tool_call",
		"tool_call":        "tool_call",
	}
	for in, want := range tests {
		var item ThreadItem
		if err := json.Unmarshal([]byte(`{"type":"`+in+`","id":"item_1"}`), &item); err != nil {
			t.Fatalf("unmarshal %q: %v", in, err)
		}
		if item.Type != want {
			t.Fatalf("type=%q, want %q", item.Type, want)
		}
	}
}
