package mcp

import (
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestW2Tail_Manager_NilReceivers(t *testing.T) {
	var m *Manager
	if m.ToolDefinitions() != nil {
		t.Errorf("nil ToolDefinitions != nil")
	}
	if err := m.RegisterTools(nil); err != nil {
		t.Errorf("nil RegisterTools = %v", err)
	}
	if m.Servers() != nil {
		t.Errorf("nil Servers != nil")
	}
	m.Close() // must not panic
}

func TestW2Tail_mcpSchemaToParams_Fallbacks(t *testing.T) {
	// nil -> empty object schema.
	got := mcpSchemaToParams(nil)
	if got["type"] != "object" {
		t.Errorf("nil schema = %v", got)
	}
	// A non-map value that marshals cleanly goes through the re-marshal path.
	type s struct {
		Type string `json:"type"`
	}
	got = mcpSchemaToParams(s{Type: "object"})
	if got["type"] != "object" {
		t.Errorf("struct schema = %v", got)
	}
	// A value that cannot marshal falls back to the empty schema.
	got = mcpSchemaToParams(make(chan int))
	if got["type"] != "object" {
		t.Errorf("unmarshalable schema = %v", got)
	}
}

func TestW2Tail_mcpResultToString(t *testing.T) {
	if mcpResultToString(nil) != "" {
		t.Errorf("nil result should be empty")
	}
	// Text + non-text content, with IsError set.
	res := &mcpsdk.CallToolResult{
		IsError: true,
		Content: []mcpsdk.Content{
			&mcpsdk.TextContent{Text: "boom"},
			&mcpsdk.ImageContent{MIMEType: "image/png", Data: []byte("x")},
		},
	}
	out := mcpResultToString(res)
	if !strings.HasPrefix(out, "[MCP Error] ") {
		t.Errorf("error prefix missing: %q", out)
	}
	if !strings.Contains(out, "boom") {
		t.Errorf("text content missing: %q", out)
	}
}
