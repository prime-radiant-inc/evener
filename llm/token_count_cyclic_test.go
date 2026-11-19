package llm

import (
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// Pricing a relaxed route must not walk a caller's parameters it does not
// rewrite.
//
// A relaxed Chat or Responses route sends the schema it was handed, so the only
// question the length comparison could answer is whether that schema differs
// from itself. Walking it anyway takes the process down on a map that contains
// itself: the root schema check passes and the recursive comparison follows the
// cycle until the stack runs out. The count this comparison replaced was a
// json.Marshal, which reports a cycle as an encoding error and leaves the
// request alone.
//
// A stack overflow is unrecoverable, so this test cannot catch it: the run dies.
// That is the failure it is written to show, and the fix is what lets the call
// return.
func TestPricingARelaxedRouteDoesNotWalkACyclicCatalog(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		res  registry.Resolved
	}{
		{"chat without strict tools", chatRowOn(false)},
		{"chat with a tool that opts out", chatRowOn(true)},
		{"responses without strict tools", responsesRowOn(false)},
		{"responses with a tool that opts out", responsesRowOn(true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tool := ToolDefinition{Name: "cyclic", Strict: new(false)}
			tool.Parameters = selfReferentialSchema()
			req := Request{Tools: []ToolDefinition{tool}}

			got := priceEmittedToolSchemas(req, tc.res)
			// Every route here sends the caller's own schema, so nothing was
			// rewritten and there is no emission to record.
			if got.emittedToolTarget != (emittedToolTargetKey{}) {
				t.Fatalf("priced a catalog the route sends unchanged: recorded target %+v", got.emittedToolTarget)
			}
		})
	}
}

// selfReferentialSchema is a schema whose properties map contains the schema
// itself: legal Go, rejected by the encoder, and unbounded to a walk that does
// not track the containers it has entered.
func selfReferentialSchema() map[string]any {
	schema := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"self"}}
	props := map[string]any{}
	schema["properties"] = props
	props["self"] = schema
	return schema
}
