package mcp

import (
	"encoding/json"
	"testing"
)

// FuzzMCPSchemaToParams drives mcpSchemaToParams — the package's real MCP
// InputSchema decode/normalize seam (the re-marshal/json.Unmarshal fallback that
// coerces a non-map schema into the map[string]any form llm.ToolDefinition
// requires). Input is arbitrary JSON decoded into the `any` shape the MCP SDK
// hands over. Beyond no-panic it asserts the post-condition the converter
// guarantees: the result is never nil, so a tool definition always has a usable
// parameters object.
func FuzzMCPSchemaToParams(f *testing.F) {
	seeds := []string{
		`{"type":"object","properties":{"path":{"type":"string"}}}`,
		`{"type":"object"}`,
		`[1,2,3]`,
		`"a string"`,
		`42`,
		`true`,
		`null`,
		`{}`,
		``,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		var schema any
		if err := json.Unmarshal(raw, &schema); err != nil {
			return // not valid JSON: no-panic floor proven, stop
		}

		params := mcpSchemaToParams(schema)
		if params == nil {
			t.Fatalf("mcpSchemaToParams returned nil for input %q", raw)
		}
	})
}
