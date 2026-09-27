package mcp

import (
	"context"
	"reflect"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/evener/agent/mcpconfig"
)

// MCP servers built on the TS SDK stamp every tool schema with
// "$schema": draft-07 and "additionalProperties": false (zodToJsonSchema).
// Gemini rejects a request outright over either key, and no provider performs
// the validation they describe, so the single seam every provider request path
// shares — mcpSchemaToParams — must strip both keys, whatever their value.
func TestMCPSchemaToParams_StripsSchemaMetaKeys(t *testing.T) {
	tests := []struct {
		name   string
		schema map[string]any
	}{
		{
			name: "zodToJsonSchema shape",
			schema: map[string]any{
				"$schema":              "http://json-schema.org/draft-07/schema#",
				"type":                 "object",
				"properties":           map[string]any{},
				"additionalProperties": false,
			},
		},
		{
			name: "boolean true is stripped too, whatever the value",
			schema: map[string]any{
				"$schema":              "https://json-schema.org/draft/2020-12/schema",
				"type":                 "object",
				"additionalProperties": true,
			},
		},
		{
			name: "schema-object form is stripped too",
			schema: map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "string"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := mcpSchemaToParams(tt.schema)
			if _, ok := params["$schema"]; ok {
				t.Errorf(`params still carry "$schema": %v`, params)
			}
			if _, ok := params["additionalProperties"]; ok {
				t.Errorf(`params still carry "additionalProperties": %v`, params)
			}
			if params["type"] != "object" {
				t.Errorf(`params["type"] = %v, want "object" kept`, params["type"])
			}
		})
	}
}

// A schema without the meta keys passes through untouched: the scrub must
// remove only the two keys, never reshape a clean schema.
func TestMCPSchemaToParams_CleanSchemaUntouched(t *testing.T) {
	in := map[string]any{
		"type":       "object",
		"properties": map[string]any{"path": map[string]any{"type": "string"}},
		"required":   []string{"path"},
	}
	if got := mcpSchemaToParams(in); !reflect.DeepEqual(got, in) {
		t.Fatalf("clean schema altered: got %v, want %v", got, in)
	}
}

// The re-marshal fallback (a schema that is not already map[string]any) must
// scrub too: both decode paths converge on the same params map.
func TestMCPSchemaToParams_FallbackPathStripsSchemaMetaKeys(t *testing.T) {
	params := mcpSchemaToParams(map[string]string{
		"$schema": "http://json-schema.org/draft-07/schema#",
		"type":    "object",
	})
	if _, ok := params["$schema"]; ok {
		t.Errorf(`params still carry "$schema": %v`, params)
	}
	if params["type"] != "object" {
		t.Fatalf(`params["type"] = %v, want "object" kept`, params["type"])
	}
}

// The daemon-level pin: a tool whose advertised schema carries the meta keys
// reaches ToolDefinitions — what provider request paths actually consume —
// without them.
func TestMCPManager_ToolDefinitions_ParametersScrubbed(t *testing.T) {
	ctx := context.Background()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "s", Version: "v1"}, nil)
	server.AddTool(&mcpsdk.Tool{
		Name:        "list_items",
		Description: "Lists items",
		InputSchema: map[string]any{
			"$schema":              "http://json-schema.org/draft-07/schema#",
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{}, nil
	})
	st, ct := mcpsdk.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	mgr, outcomes := NewManager(ctx, []mcpconfig.ServerConfig{{Name: "srv", Type: "stdio"}},
		[]func(context.Context) (mcpsdk.Transport, error){staticDial(ct)})
	if len(outcomes) != 0 {
		t.Fatalf("NewManager: %+v", outcomes)
	}
	defer mgr.Close()

	defs := mgr.ToolDefinitions()
	if len(defs) != 1 {
		t.Fatalf("expected 1 tool definition, got %d", len(defs))
	}
	params := defs[0].Parameters
	if _, ok := params["$schema"]; ok {
		t.Error(`ToolDefinition.Parameters still carries "$schema"`)
	}
	if _, ok := params["additionalProperties"]; ok {
		t.Error(`ToolDefinition.Parameters still carries "additionalProperties"`)
	}
	if params["type"] != "object" {
		t.Errorf(`Parameters["type"] = %v, want "object" kept`, params["type"])
	}
}
