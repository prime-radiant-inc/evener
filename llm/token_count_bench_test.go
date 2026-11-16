package llm

import (
	"strconv"
	"testing"
)

// benchmarkToolDefs builds a tool list shaped like a session's advertised set:
// named tools with a long description and a nested JSON Schema, which is what
// makes the serialized list large enough to matter (#6406).
func benchmarkToolDefs(count int) []ToolDefinition {
	defs := make([]ToolDefinition, count)
	for i := range defs {
		defs[i] = ToolDefinition{
			Name:        "tool_" + strconv.Itoa(i),
			Description: "Run the " + strconv.Itoa(i) + " operation and report what it did. Long descriptions are the normal shape of an advertised tool, and the estimator bills them by their serialized length.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path to operate on, resolved against the working directory.",
					},
					"recursive": map[string]any{
						"type":        "boolean",
						"description": "Whether to descend into subdirectories.",
					},
					"options": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"limit":   map[string]any{"type": "integer", "minimum": 0},
							"pattern": map[string]any{"type": "string"},
						},
						"additionalProperties": false,
					},
				},
				"required": []string{"path"},
			},
		}
	}
	return defs
}

// BenchmarkEstimateInputTokensTools isolates the tool-list share of
// EstimateInputTokens: the same message list every iteration, with the full
// advertised tool set. The message list is one short user turn so the tools
// dominate the work the estimator does (#6406).
//
// The two sub-cases are the same request priced two ways: unstamped, which
// re-serializes the list every call, and stamped with the length a caller
// measured once, which is what a session now sends. Their per-op allocation
// difference is the marshal-and-discard the fix removes.
func BenchmarkEstimateInputTokensTools(b *testing.B) {
	base := Request{
		Provider: "anthropic",
		Model:    "claude-test",
		Messages: []Message{User("hello")},
		Tools:    benchmarkToolDefs(32),
	}
	stamped := base
	stamped.ToolsJSONBytes = ToolsJSONBytes(base.Tools)
	for _, tc := range []struct {
		name string
		req  Request
	}{
		{"unstamped", base},
		{"stamped", stamped},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = EstimateInputTokens(tc.req)
			}
		})
	}
}
