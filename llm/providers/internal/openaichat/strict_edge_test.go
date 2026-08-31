package openaichat

import "testing"

// TestStrictifyJSONSchemaEdgeShapes exercises the schema shapes that, before
// strictifyJSONSchemaInPlace moved into this package, were covered directly
// (by identifier, in-package) from
// llm/providers/openai/responses_coverage_fuzz_test.go's
// responsesCoverageSchemaInputAndDecodeBranches: a nil map, and a schema
// mixing an array/items with malformed anyOf/oneOf/allOf entries. That
// coverage moved here with the function; every case below goes through the
// exported StrictifyJSONSchema entry point, since that is now the only way
// code outside this package can reach these branches. A few further shapes
// (non-map properties, an array with no items, nested combinators) are
// added for the same reason: they exercise branches the moved function has
// but the old fuzz helper never isolated.
func TestStrictifyJSONSchemaEdgeShapes(t *testing.T) {
	cases := []struct {
		name  string
		in    map[string]any
		check func(t *testing.T, out map[string]any)
	}{
		{
			name: "nil map",
			in:   nil,
			check: func(t *testing.T, out map[string]any) {
				if out == nil {
					t.Fatal("out is nil")
				}
				if len(out) != 0 {
					t.Fatalf("out = %#v, want empty", out)
				}
			},
		},
		{
			name: "properties is not a map",
			in:   map[string]any{"type": "object", "properties": "not-a-map"},
			check: func(t *testing.T, out map[string]any) {
				if out["additionalProperties"] != false {
					t.Fatalf("additionalProperties = %#v", out["additionalProperties"])
				}
				props, ok := out["properties"].(map[string]any)
				if !ok || len(props) != 0 {
					t.Fatalf("properties = %#v, want empty map", out["properties"])
				}
				req, ok := out["required"].([]string)
				if !ok || len(req) != 0 {
					t.Fatalf("required = %#v, want empty slice", out["required"])
				}
			},
		},
		{
			name: "malformed anyOf/oneOf/allOf entries",
			in: map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "object"},
				"anyOf": "bad",
				"oneOf": []any{map[string]any{"type": "object"}, "ignored"},
				"allOf": nil,
			},
			check: func(t *testing.T, out map[string]any) {
				items, ok := out["items"].(map[string]any)
				if !ok || items["additionalProperties"] != false {
					t.Fatalf("items = %#v, want strictified object", out["items"])
				}
				if out["anyOf"] != "bad" {
					t.Fatalf("anyOf = %#v, want untouched (not a []any)", out["anyOf"])
				}
				oneOf, ok := out["oneOf"].([]any)
				if !ok || len(oneOf) != 2 {
					t.Fatalf("oneOf = %#v", out["oneOf"])
				}
				strictified, ok := oneOf[0].(map[string]any)
				if !ok || strictified["additionalProperties"] != false {
					t.Fatalf("oneOf[0] = %#v, want strictified object", oneOf[0])
				}
				if oneOf[1] != "ignored" {
					t.Fatalf("oneOf[1] = %#v, want untouched (not a map)", oneOf[1])
				}
				if v, ok := out["allOf"]; !ok || v != nil {
					t.Fatalf("allOf = %#v (present=%v), want present and nil", v, ok)
				}
			},
		},
		{
			name: "array without items",
			in:   map[string]any{"type": "array"},
			check: func(t *testing.T, out map[string]any) {
				if _, has := out["items"]; has {
					t.Fatalf("out = %#v, items should stay absent", out)
				}
				if _, has := out["additionalProperties"]; has {
					t.Fatalf("out = %#v, an array must not get additionalProperties", out)
				}
			},
		},
		{
			name: "nested combinators",
			in: map[string]any{
				"anyOf": []any{
					map[string]any{
						"oneOf": []any{
							map[string]any{"type": "object"},
						},
					},
				},
			},
			check: func(t *testing.T, out map[string]any) {
				anyOf, ok := out["anyOf"].([]any)
				if !ok || len(anyOf) != 1 {
					t.Fatalf("anyOf = %#v", out["anyOf"])
				}
				level1, ok := anyOf[0].(map[string]any)
				if !ok {
					t.Fatalf("anyOf[0] = %#v", anyOf[0])
				}
				oneOf, ok := level1["oneOf"].([]any)
				if !ok || len(oneOf) != 1 {
					t.Fatalf("oneOf = %#v", level1["oneOf"])
				}
				level2, ok := oneOf[0].(map[string]any)
				if !ok || level2["additionalProperties"] != false {
					t.Fatalf("oneOf[0] = %#v, want strictified object two levels deep", oneOf[0])
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := StrictifyJSONSchema(tc.in)
			tc.check(t, out)
		})
	}
}
