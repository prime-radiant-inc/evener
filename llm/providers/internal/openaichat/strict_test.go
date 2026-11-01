package openaichat

import (
	"reflect"
	"testing"
)

func TestStrictifyJSONSchemaRewritesObjectsWithoutMutatingInput(t *testing.T) {
	in := map[string]any{"type": "object", "properties": map[string]any{
		"b": map[string]any{"type": "string"},
		"a": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "integer"}}}},
	}}
	out := StrictifyJSONSchema(in)
	if out["additionalProperties"] != false {
		t.Fatalf("out = %#v", out)
	}
	if req := out["required"]; !reflect.DeepEqual(req, []any{"a", "b"}) && !reflect.DeepEqual(req, []string{"a", "b"}) {
		t.Fatalf("required = %#v", req)
	}
	items := out["properties"].(map[string]any)["a"].(map[string]any)["items"].(map[string]any)
	if items["additionalProperties"] != false {
		t.Fatalf("nested object not strictified: %#v", items)
	}
	if _, touched := in["additionalProperties"]; touched {
		t.Fatal("input mutated")
	}
	if !reflect.DeepEqual(StrictifyJSONSchema(out), out) {
		t.Fatal("not idempotent")
	}
}

// A nil schema strictifies to an empty schema rather than panicking: the
// deep copy normalizes the absent map, and the rewrite is a no-op on it.
func TestStrictifyJSONSchemaNilSchemaIsSafe(t *testing.T) {
	got := StrictifyJSONSchema(nil)
	if len(got) != 0 {
		t.Fatalf("StrictifyJSONSchema(nil) = %v, want an empty schema", got)
	}
}
