package chatcompletions

import (
	"reflect"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/openaichat"
	"primeradiant.com/evener/llm/registry"
)

// toolSchema is the parameter schema one tool is sent with, and whether that
// tool rides the request under strict enforcement.
//
// This is the per-tool decision the builder makes, written once so the local
// estimate can reach the same answer. The rules, in the order the builder
// applies them:
//
//   - A per-tool opt-out under a strict-capable route states false and keeps
//     the schema it advertised.
//   - On a prepared request a parameterless tool gains the open empty object,
//     because the local validator compiles it against that schema and the wire
//     has to agree. A direct call keeps the caller's nil: the key is omitted,
//     which is the caller's own no-argument declaration.
//   - Strict enforcement closes a parameterless tool's schema to the empty
//     argument set, and projects whatever schema the tool then carries.
//
// The boolean reports whether the tool is sent with strict enforcement.
func toolSchema(req llm.Request, i int, strictCaps bool) (params map[string]any, toolStrict bool, omit bool) {
	t := req.Tools[i]
	params = t.Parameters
	if strictCaps {
		if t.Strict != nil && !*t.Strict {
			// The opt-out is a statement about this tool, not about the route.
			return t.Parameters, false, t.Parameters == nil
		}
	}
	if t.Parameters == nil {
		if strictCaps {
			// Strict mode needs a schema to validate against, and the closed
			// empty object is the one shape that takes an empty argument set.
			params = map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{},
				"required":             []string{},
			}
		} else if req.PreparedTools() {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		} else {
			// A direct call never went through preparation, so its nil is the
			// caller's own declaration. Omitting the key is what every adapter
			// did before the fallback existed; synthesizing an open object
			// would tell the provider the tool accepts arbitrary keys.
			return nil, false, true
		}
	}
	if !strictCaps {
		return params, false, false
	}
	if !openaichat.StrictExpressible(params) {
		return openaichat.SupplyDerivedTypes(params), false, false
	}
	return openaichat.StrictifyJSONSchema(params), true, false
}

// EmitToolSchemas implements llm.ToolSchemaDeriver.
//
// The builder reaches its per-tool decision through toolSchema, and this maps
// the same list through the same function, so the local estimate prices the
// request the builder emits rather than the one it received. Both differences
// the builder makes matter near the input cap: a parameterless tool gains a
// schema on a strict-capable route even for a direct call, and a tool whose
// schema the gate refuses gains the types it implies.
//
// A tool the builder sends with no parameters key is reported unchanged here:
// the count over the definitions as written already describes it, and the
// omission is the builder's own choice rather than a byte the estimate missed.
func (*Protocol) EmitToolSchemas(baseline []llm.ToolDefinition, res registry.Resolved, prepared bool) ([]llm.ToolDefinition, bool) {
	if len(baseline) == 0 {
		return nil, false
	}
	// The builder reads the preparation marker off the request, and this is
	// handed the list rather than the request. Only the marker decides whether
	// a parameterless tool gains the open object, so it is reconstructed here
	// rather than plumbed through as a second argument to every caller.
	req := llm.Request{Tools: baseline}
	if prepared {
		req = llm.MarkPreparedTools(req)
	}
	strictCaps := registry.BoolValue(res.Caps.StrictTools)
	sent := make([]llm.ToolDefinition, len(baseline))
	copy(sent, baseline)
	changed := false
	for i := range baseline {
		params, _, omit := toolSchema(req, i, strictCaps)
		if omit {
			continue
		}
		if reflect.DeepEqual(params, baseline[i].Parameters) {
			continue
		}
		sent[i].Parameters = params
		changed = true
	}
	if !changed {
		return nil, false
	}
	return sent, true
}
