package anthropic

import (
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// wireInputSchema returns the input_schema the builder sends for t: the open
// empty object when the tool declares no parameters, else the caller's schema
// with any top-level combinator removed. untouched reports that the returned
// map is the caller's own: the builder then sends the schema it was handed, and
// the bytes a caller measured over the catalog still describe it.
//
// This is the per-tool decision toAnthropicTools makes, written once so the local
// estimate reaches the same answer. Anthropic rejects a tool that omits
// input_schema, and it rejects anyOf, oneOf, and allOf at the root of one, so the
// builder supplies the first and drops the second.
func wireInputSchema(t llm.ToolDefinition) (params map[string]any, untouched bool) {
	params = t.Parameters
	if params == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}, false
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
		return params, false
	}
	return params, true
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
// A tool the builder leaves untouched keeps the caller's own map, so the count
// the request already holds describes it and the list is reported unchanged. A
// rewritten tool makes the list a change, and the caller measures the emitted
// list with the encoder the wire uses.
func (*Protocol) EmitToolSchemas(baseline []llm.ToolDefinition, res registry.Resolved) ([]llm.ToolDefinition, bool) {
	return llm.ProjectToolSchemas(baseline, wireInputSchema)
}
