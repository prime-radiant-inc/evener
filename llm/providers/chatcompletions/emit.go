package chatcompletions

import (
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/openaichat"
)

// toolSchema is the parameter schema one tool is sent with, and whether that
// tool rides the request under strict enforcement.
//
// This is the per-tool decision the builder makes. The rules, in the order they
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
// The boolean reports whether the tool is sent with strict enforcement.
func toolSchema(req llm.Request, i int, strictCaps bool) (params map[string]any, toolStrict bool, omit bool) {
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
			return nil, false, true
		}
		params = map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           map[string]any{},
			"required":             []string{},
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
