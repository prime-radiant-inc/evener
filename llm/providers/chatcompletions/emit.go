package chatcompletions

import (
	"slices"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/openaichat"
	"primeradiant.com/evener/llm/registry"
)

// toolSchema is the parameter schema one tool is sent with, and whether that
// tool rides the request under strict enforcement.
//
// This is the per-tool decision the builder makes, written once so the local
// estimate can reach the same answer. The rules, in the order they
// are applied:
//
//   - The per-tool opt-out is resolved first, because it is a statement about
//     this tool rather than about the route: an opted-out tool never rides
//     strict enforcement, so the closed empty object is never the schema it is
//     sent with.
//   - A tool prepared from a no-argument declaration already carries the open
//     empty object, because that is the schema every adapter sends and the
//     budget prices. That holds for an opted-out tool too: the opt-out decides
//     enforcement, not which schema the validator saw. A direct call keeps the
//     caller's nil: the key is omitted, which is the caller's own no-argument
//     declaration.
//   - Strict enforcement closes a parameterless tool's schema to the empty
//     argument set, and projects whatever schema the tool then carries.
//
// The first boolean reports whether the tool is sent with strict enforcement,
// and untouched reports that params is the caller's own schema: the decision
// left the definition exactly as it arrived, so there is nothing to compare.
func toolSchema(req llm.Request, i int, strictCaps bool) (params map[string]any, toolStrict, omit, untouched bool) {
	t := req.Tools[i]
	optedOut := strictCaps && t.Strict != nil && !*t.Strict
	params = t.Parameters
	if t.DeclaresNoParameters() {
		// Preparation put the open empty object here: the validator compiled the
		// definition against it and the budget priced it, so the wire carries it
		// whatever the enforcement decision is. Strict mode is the one case that
		// replaces it, with the closed empty object that admits exactly the empty
		// argument set.
		if strictCaps && !optedOut {
			params = map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{},
				"required":             []string{},
			}
		}
	} else if params == nil {
		// A direct call never went through preparation, so its nil is the
		// caller's own declaration. Omitting the key is what every adapter did
		// before the fallback existed; synthesizing an open object would tell the
		// provider the tool accepts arbitrary keys. A strict route is different:
		// the closed empty object admits exactly {}, the same argument set "no
		// parameters" names.
		if !strictCaps || optedOut {
			return nil, false, true, false
		}
		params = map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           map[string]any{},
			"required":             []string{},
		}
	}
	if !strictCaps || optedOut {
		// The caller's own schema is what this route sends. A route without the
		// capability makes no decision about any of them, so reporting them
		// untouched keeps the deriver from walking a map whose only remaining
		// question is whether it differs from itself — a walk a cyclic map makes
		// unbounded.
		return params, false, false, true
	}
	if !openaichat.StrictExpressible(params) {
		supplied, rewritten := openaichat.SupplyDerivedTypes(params)
		return supplied, false, false, !rewritten
	}
	return openaichat.StrictifyJSONSchema(params), true, false, false
}

// EmitToolSchemas implements llm.ToolSchemaDeriver.
//
// The builder reaches its per-tool decision through toolSchema, and this maps
// the same list through the same function, so the local estimate prices the
// request the builder emits rather than the one it received. On this route the
// bytes the builder adds are the strict projection's or the derived type's: a
// closed object and a stated required list that the advertised schema does not
// carry, or the type a refused closed value set implies.
//
// A tool the decision leaves alone — the caller's own map, or no parameters key
// at all — is reported unchanged, and a list with none of them returns nil and
// false: the count the request already holds describes those bytes. Any tool the
// decision rewrites makes the list a change, and the caller measures the emitted
// list with the encoder the wire uses.
func (*Protocol) EmitToolSchemas(baseline []llm.ToolDefinition, res registry.Resolved) ([]llm.ToolDefinition, bool) {
	if len(baseline) == 0 {
		return nil, false
	}
	strictCaps := registry.BoolValue(res.Caps.StrictTools)
	req := llm.Request{Tools: baseline}
	var sent []llm.ToolDefinition
	for i := range baseline {
		params, _, omit, untouched := toolSchema(req, i, strictCaps)
		if omit || untouched {
			// The builder omits the key or sends the caller's own map, so the
			// count the request arrived with already describes the wire.
			continue
		}
		if sent == nil {
			sent = slices.Clone(baseline)
		}
		sent[i].Parameters = params
	}
	if sent == nil {
		return nil, false
	}
	return sent, true
}
