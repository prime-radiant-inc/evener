package llm

import (
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// Pricing a relaxed route must not walk a caller's parameters it does not
// rewrite.
//
// A relaxed Chat or Responses route sends the schema it was handed, so there is
// nothing to compare and nothing to marshal. A walk that follows the map anyway
// takes the process down on a schema that contains itself, and the encoder
// reports a cycle as an error rather than unbounded recursion.
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

			// The route leaves this schema alone, so the answer is the catalog's
			// own count: the encoder rejects it, which bills nothing, and no walk
			// of the schema happened to find that out.
			if got := EffectiveToolsJSONBytes(req, tc.res, false); got != 0 {
				t.Fatalf("priced a catalog this route sends unchanged and the encoder rejects at %d bytes, want 0", got)
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
