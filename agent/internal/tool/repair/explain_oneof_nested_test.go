package repair

import (
	"strings"
	"testing"
)

// nestedOneOfParams is the issue #624 shape: a oneOf nested inside a property's
// schema (properties.opts.oneOf) rather than at the schema root.
func nestedOneOfParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{
					map[string]any{"required": []any{"x"}},
					map[string]any{"required": []any{"y"}},
				},
			},
		},
	}
}

func nestedOneOfArgs() map[string]any {
	return map[string]any{"opts": map[string]any{"z": 1}}
}

// Issue #624: a oneOf nested inside a property schema makes the deepest cause's
// instance location that property ("opts"), so the present-field path reported
// only the generic "wrong type or value" on the container. The failing
// keyword's location ("/properties/opts/oneOf/0/required") must be walked to
// the enclosing combinator and its branches rendered against the property
// path.
func TestExplainSchemaError_NestedOneOfNamesBranches(t *testing.T) {
	const loc = "properties/opts/oneOf/0/required"
	msg := ExplainSchemaError("my_tool", nestedOneOfParams(), nestedOneOfArgs(), "opts", loc)
	notMsg(t, msg, "wrong type or value", "nested oneOf explained as a wrong value (issue #624)")
	wantMsg(t, msg, "oneOf constraint", "message must name the oneOf constraint")
	for _, want := range []string{`"opts"`, `send all of "x"`, `send all of "y"`, `{"opts": {"x": "..."}}`} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message missing %q: %q", want, msg)
		}
	}
}

// A leading slash on the keyword location (the form offendingKeywordLocation
// produces before trimming) must resolve the same as its trimmed form.
func TestExplainSchemaError_NestedOneOfAcceptsLeadingSlash(t *testing.T) {
	msg := ExplainSchemaError("my_tool", nestedOneOfParams(), nestedOneOfArgs(), "opts", "/properties/opts/oneOf/0/required")
	wantMsg(t, msg, "oneOf constraint", "leading-slash keyword location was not resolved")
}

// A present field whose direct constraint fails must keep the generic
// wrong-value message even when its schema also carries an unrelated oneOf:
// the keyword location names no combinator, so nothing is attributed to it.
func TestExplainSchemaError_DirectConstraintWinsOverUnrelatedOneOf(t *testing.T) {
	params := nestedOneOfParams()
	optsSchema := params["properties"].(map[string]any)["opts"].(map[string]any)
	optsSchema["minProperties"] = 2
	msg := ExplainSchemaError("my_tool", params, map[string]any{"opts": map[string]any{}}, "opts", "properties/opts/minProperties")
	wantMsg(t, msg, "wrong type or value", "direct constraint should stay generic")
	notMsg(t, msg, "oneOf", "unrelated oneOf leaked into a direct-constraint failure")
}
