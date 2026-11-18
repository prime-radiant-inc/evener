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
//   - The per-tool opt-out is resolved first, because it is a statement about
//     this tool rather than about the route: an opted-out tool never rides
//     strict enforcement, so the closed empty object is never the schema it is
//     sent with.
//   - On a prepared request a parameterless tool gains the open empty object,
//     because the local validator compiled it against that schema and the wire
//     has to agree. That holds for an opted-out tool too: the opt-out decides
//     enforcement, not which schema the validator saw. A direct call keeps the
//     caller's nil: the key is omitted, which is the caller's own no-argument
//     declaration.
//   - Strict enforcement closes a parameterless tool's schema to the empty
//     argument set, and projects whatever schema the tool then carries.
//
// The boolean reports whether the tool is sent with strict enforcement.
func toolSchema(req llm.Request, i int, strictCaps bool) (params map[string]any, toolStrict bool, omit bool) {
	t := req.Tools[i]
	optedOut := strictCaps && t.Strict != nil && !*t.Strict
	params = t.Parameters
	if t.Parameters == nil {
		switch {
		case strictCaps && !optedOut:
			// Strict mode needs a schema to validate against, and the closed
			// empty object is the one shape that takes an empty argument set.
			params = map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{},
				"required":             []string{},
			}
		case req.PreparedTools():
			// The validator compiled this tool against the open empty object,
			// so the wire carries it whatever the enforcement decision is. An
			// opt-out that omitted the key here would leave admission billing a
			// schema the builder does not send, and the provider judging the
			// tool against a schema the validator never saw.
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		default:
			// A direct call never went through preparation, so its nil is the
			// caller's own declaration. Omitting the key is what every adapter
			// did before the fallback existed; synthesizing an open object
			// would tell the provider the tool accepts arbitrary keys.
			return nil, false, true
		}
	}
	if !strictCaps || optedOut {
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
		// DeepEqual is a structural oracle and the count is a byte count. Strict
		// projection sorts each object's required list, which reorders a schema
		// without changing what it serializes to, so an already strictly shaped
		// catalog differs structurally from its projection while emitting exactly
		// the bytes the cached count already describes. The projection answers
		// that question itself (SameSerializedLength), which turns the reorder
		// into a reuse rather than a second marshal of the catalog on every
		// dispatch.
		if openaichat.SameSerializedLength(baseline[i].Parameters, params) {
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
