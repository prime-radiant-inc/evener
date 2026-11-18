package chatcompletions

import (
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/openaichat"
	"primeradiant.com/evener/llm/registry"
)

// toolSchema is the parameter schema one tool is sent with, whether that tool
// rides the request under strict enforcement, and whether the builder omits the
// parameters key entirely.
//
// This is the per-tool decision the builder makes, written once so the local
// estimate can reach the same answer. The rules, in the order the builder
// applies them:
//
//   - A definition that carries no parameters emits no parameters key.
//     ToChatTools has always omitted it, and the local estimate's count over the
//     advertised definitions already describes that omission, so there are no
//     bytes to price and nothing to report as changed.
//   - On a strict-capable route every tool carries the strict flag, and a tool
//     that does carry a schema has it projected.
func toolSchema(t llm.ToolDefinition, strictCaps bool) (params map[string]any, toolStrict, omit bool) {
	if t.Parameters == nil {
		return nil, strictCaps, true
	}
	if !strictCaps {
		return t.Parameters, false, false
	}
	return openaichat.StrictifyJSONSchema(t.Parameters), true, false
}

// toChatTools is the wire tool list for a request, with each tool's schema and
// strict flag decided by toolSchema.
func toChatTools(tools []llm.ToolDefinition, strictCaps bool) []map[string]any {
	out := openaichat.ToChatTools(tools)
	for i, t := range tools {
		params, _, omit := toolSchema(t, strictCaps)
		fn, ok := out[i]["function"].(map[string]any)
		if !ok {
			continue
		}
		if !omit {
			fn["parameters"] = params
		}
		if strictCaps {
			fn["strict"] = true
		}
	}
	return out
}

// EmitToolSchemas implements llm.ToolSchemaDeriver.
//
// The builder reaches its per-tool decision through toolSchema, and this maps
// the same list through the same function, so the local estimate prices the
// request the builder emits rather than the one it received. On this route the
// bytes the builder adds are the strict projection's: a closed object and a
// stated required list that the advertised schema does not carry.
//
// A tool the builder sends with no parameters key is reported unchanged: the
// count over the definitions as written already describes it, and the omission
// is the builder's own choice rather than a byte the estimate missed.
//
// The returned list is measured by the caller, so an unchanged list returns nil
// and false and the count it already holds is reused rather than re-marshaling
// the catalog.
func (*Protocol) EmitToolSchemas(baseline []llm.ToolDefinition, res registry.Resolved) ([]llm.ToolDefinition, bool) {
	if len(baseline) == 0 {
		return nil, false
	}
	strictCaps := registry.BoolValue(res.Caps.StrictTools)
	sent := make([]llm.ToolDefinition, len(baseline))
	copy(sent, baseline)
	changed := false
	for i, t := range baseline {
		params, _, omit := toolSchema(t, strictCaps)
		if omit {
			continue
		}
		// A byte count needs a byte oracle. Strict projection sorts each object's
		// required list, which reorders a schema without changing what it
		// serializes to, so an already strictly shaped catalog differs
		// structurally from its projection while emitting exactly the bytes the
		// cached count already describes. SameSerializedLength answers that
		// question without marshaling the catalog, which turns the reorder into
		// a reuse rather than a second marshal on every dispatch.
		if openaichat.SameSerializedLength(t.Parameters, params) {
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
