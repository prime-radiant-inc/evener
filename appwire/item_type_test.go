package appwire

import (
	"encoding/json"
	"testing"
)

func TestThreadItemMarshalUsesCodexItemTypes(t *testing.T) {
	for _, typ := range []string{"userMessage", "agentMessage", "commandExecution"} {
		data, err := json.Marshal(ThreadItem{Type: typ, ID: "item_1"})
		if err != nil {
			t.Fatalf("marshal %q: %v", typ, err)
		}
		var got struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("decode %s: %v", data, err)
		}
		if got.Type != typ {
			t.Fatalf("type=%q, want %q in %s", got.Type, typ, data)
		}
	}
}

func TestThreadItemUnmarshalUsesCodexItemTypes(t *testing.T) {
	for _, typ := range []string{"userMessage", "agentMessage", "commandExecution", "mcpToolCall", "dynamicToolCall"} {
		var item ThreadItem
		if err := json.Unmarshal([]byte(`{"type":"`+typ+`","id":"item_1"}`), &item); err != nil {
			t.Fatalf("unmarshal %q: %v", typ, err)
		}
		if item.Type != typ {
			t.Fatalf("type=%q, want %q", item.Type, typ)
		}
	}
}
