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
			// opt-out that omitted the key here would have the provider judge
			// the tool against a schema the validator never saw.
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
