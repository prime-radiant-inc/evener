package anthropic

import (
	"slices"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/openaichat"
	"primeradiant.com/evener/llm/registry"
)

// wireInputSchema returns the input_schema the builder sends for t: the open
// empty object when the tool declares no parameters, else the caller's schema
// with any top-level combinator removed.
//
// This is the per-tool decision toAnthropicTools makes, written once so the local
// estimate reaches the same answer. Anthropic rejects a tool that omits
// input_schema, and it rejects anyOf, oneOf, and allOf at the root of one, so the
// builder supplies the first and drops the second.
func wireInputSchema(t llm.ToolDefinition) map[string]any {
	params := t.Parameters
	if params == nil {
		params = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	// Copy-on-write: only allocate if we need to strip keys.
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if _, has := params[key]; !has {
			continue
		}
		params = shallowCopyMap(params)
		delete(params, "anyOf")
		delete(params, "oneOf")
		delete(params, "allOf")
		break
	}
	return params
}

// EmitToolSchemas implements llm.ToolSchemaDeriver.
//
// The builder reaches its per-tool input_schema through wireInputSchema, and
// this maps the same list through the same function, so the local estimate prices
// the request the builder emits rather than the one it received. The bytes at
// stake are two: the open empty object the builder supplies for a tool that
// declares no parameters, and the root combinator it strips, which can only make
// the emitted schema smaller than the advertised one.
//
// A schema the builder emits byte-identically is reported unchanged, and a list
// with none of them returns nil and false: the count it already holds is exact,
// and re-marshaling the catalog on every dispatch is the cost that count exists
// to avoid. SameSerializedLength answers that question without marshaling
// anything, and it answers the two shapes this decision produces — the caller's
// own map, compared with itself, and a copy that dropped keys — without walking
// into a schema that contains itself.
func (*Protocol) EmitToolSchemas(baseline []llm.ToolDefinition, res registry.Resolved) ([]llm.ToolDefinition, bool) {
	if len(baseline) == 0 {
		return nil, false
	}
	var sent []llm.ToolDefinition
	changed := false
	for i := range baseline {
		params := wireInputSchema(baseline[i])
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
