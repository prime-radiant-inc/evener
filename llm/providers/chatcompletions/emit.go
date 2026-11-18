package chatcompletions

import (
	"reflect"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// EmitToolSchemas implements llm.ToolSchemaDeriver.
//
// This builder changes a tool's schema in exactly one case: a prepared request
// carrying a parameterless tool. Preparation leaves Parameters nil so a
// strict-capable adapter can tell "this tool takes no arguments" from "this
// tool advertises the open object", but the local validator compiles such a
// tool against the open empty object — so on a prepared request the wire has to
// carry that same schema, and a strict-enforced one carries the closed form
// instead.
//
// A direct Complete or Stream call is not prepared. Its nil is the caller's own
// no-argument declaration, there is no validator to agree with, and the builder
// omits the key, so the definitions as written are the list that dispatches. The
// same goes for a tool that opted out of strict enforcement: it is advertised
// with the open schema rather than the closed one.
//
// Every other tool is sent with the schema it arrived with. Strict mode still
// projects a certifiable schema, but projection rewrites the keywords inside
// that schema and not its byte count in any way the estimate distinguishes, so
// this reports no change and the caller keeps the count it holds. Saying so here
// is the point: the price covers what the builder substitutes, and the builder
// substitutes only the parameterless case.
func (*Protocol) EmitToolSchemas(baseline []llm.ToolDefinition, res registry.Resolved, prepared bool) ([]llm.ToolDefinition, bool) {
	if !prepared || len(baseline) == 0 {
		return nil, false
	}
	strict := registry.BoolValue(res.Caps.StrictTools)
	sent := make([]llm.ToolDefinition, len(baseline))
	copy(sent, baseline)
	changed := false
	for i, t := range baseline {
		if t.Parameters != nil {
			continue
		}
		// A per-tool opt-out reads before the fallback is synthesized: such a
		// tool is sent with the open schema, which is the schema the count over
		// the definitions as written did not include either. Only a tool sent
		// with strict enforcement gains the closed form.
		params := map[string]any{"type": "object", "properties": map[string]any{}}
		if strict && (t.Strict == nil || *t.Strict) {
			params["additionalProperties"] = false
			params["required"] = []string{}
		}
		if reflect.DeepEqual(params, t.Parameters) {
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
