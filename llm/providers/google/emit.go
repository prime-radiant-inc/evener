package google

import (
	"slices"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/openaichat"
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
// A schema the builder emits byte-identically is reported unchanged, and a list
// with none of them returns nil and false: the count it already holds is exact,
// and re-marshaling the catalog on every dispatch is the cost that count exists
// to avoid. SameSerializedLength answers that question without marshaling
// anything, and it answers a schema that contains itself without walking into it,
// which is the finite answer for a value the encoder rejects.
func (*Protocol) EmitToolSchemas(baseline []llm.ToolDefinition, res registry.Resolved) ([]llm.ToolDefinition, bool) {
	if len(baseline) == 0 {
		return nil, false
	}
	var sent []llm.ToolDefinition
	changed := false
	for i := range baseline {
		params := wireFunctionParameters(baseline[i])
		if openaichat.SameSerializedLength(baseline[i].Parameters, params) {
			continue
		}
		if sent == nil {
			sent = slices.Clone(baseline)
		}
		sent[i].Parameters = params
		changed = true
	}
	if !changed {
		return nil, false
	}
	return sent, true
}
