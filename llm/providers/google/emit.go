package google

import (
	"slices"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// wireFunctionParameters returns the parameters Gemini accepts for t: the open
// empty object when the tool declares no parameters, else the sanitized schema
// its Schema proto can carry.
//
// This is the per-tool decision toGeminiFunctionDecls makes, written once so the
// local estimate reaches the same answer. Gemini's Schema is a restricted subset
// of JSON Schema: it has no field for additionalProperties or the $-prefixed
// document metadata, so a schema carrying either is emitted smaller than it
// arrived, and a request priced by the advertised bytes is overcharged.
func wireFunctionParameters(t llm.ToolDefinition) map[string]any {
	params := t.Parameters
	if params == nil {
		params = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	sanitized, _ := sanitizeGeminiSchema(params).(map[string]any)
	return sanitized
}

// EmitToolSchemas implements llm.ToolSchemaDeriver.
//
// The builder reaches its per-tool parameters through wireFunctionParameters,
// and this maps the same list through the same function, so the local estimate
// prices the request the builder emits rather than the one it received. Both
// differences point the same way: the open empty object the builder supplies for
// a tool that declares no parameters is larger than the nil it replaces, and the
// fields sanitizeGeminiSchema drops — additionalProperties, the $-prefixed
// document metadata, a nullable type list — make the emitted schema smaller.
//
// The sanitizer rebuilds every schema, so this route always reports a change and
// the caller measures the emitted list with the encoder the wire uses. A
// schema nothing was dropped from still measures the bytes the catalog
// measures, so the price is the same either way; skipping that marshal is a
// separate cost to take up on its own.
func (*Protocol) EmitToolSchemas(baseline []llm.ToolDefinition, res registry.Resolved) ([]llm.ToolDefinition, bool) {
	if len(baseline) == 0 {
		return nil, false
	}
	sent := slices.Clone(baseline)
	for i := range sent {
		sent[i].Parameters = wireFunctionParameters(baseline[i])
	}
	return sent, true
}
