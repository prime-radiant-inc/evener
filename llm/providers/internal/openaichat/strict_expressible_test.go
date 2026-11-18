package openaichat

import "testing"

// StrictExpressible decides whether a schema may be projected into OpenAI
// strict mode without making a documented-optional argument mandatory. Strict
// mode requires every property of every object to be present and listed in
// required, so only a schema that already does so is expressible (#6200).
func TestStrictExpressible(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   map[string]any
		want bool
	}{
		{
			name: "no properties",
			in:   map[string]any{"type": "object"},
			want: true,
		},
		{
			name: "every property required",
			in: map[string]any{
				"type":       "object",
				"properties": map[string]any{"a": map[string]any{"type": "string"}},
				"required":   []string{"a"},
			},
			want: true,
		},
		{
			name: "one property left optional",
			in: map[string]any{
				"type":       "object",
				"properties": map[string]any{"a": map[string]any{"type": "string"}, "b": map[string]any{"type": "string"}},
				"required":   []string{"a"},
			},
			want: false,
		},
		{
			name: "no required list at all",
			in: map[string]any{
				"type":       "object",
				"properties": map[string]any{"a": map[string]any{"type": "string"}},
			},
			want: false,
		},
		{
			name: "required as a decoded []any",
			in: map[string]any{
				"type":       "object",
				"properties": map[string]any{"a": map[string]any{"type": "string"}},
				"required":   []any{"a"},
			},
			want: true,
		},
		{
			name: "nested object leaves a property optional",
			in: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"inner": map[string]any{
						"type":       "object",
						"properties": map[string]any{"x": map[string]any{"type": "string"}, "y": map[string]any{"type": "string"}},
						"required":   []string{"x"},
					},
				},
				"required": []string{"inner"},
			},
			want: false,
		},
		{
			name: "array items leave a property optional",
			in: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"rows": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type":       "object",
							"properties": map[string]any{"id": map[string]any{"type": "integer"}, "note": map[string]any{"type": "string"}},
							"required":   []string{"id"},
						},
					},
				},
				"required": []string{"rows"},
			},
			want: false,
		},
		{
			name: "combinator branch leaves a property optional",
			in: map[string]any{
				"oneOf": []any{
					map[string]any{
						"type":       "object",
						"properties": map[string]any{"a": map[string]any{"type": "string"}, "b": map[string]any{"type": "string"}},
						"required":   []string{"a"},
					},
				},
			},
			want: false,
		},
		{
			name: "nil schema",
			in:   nil,
			want: true,
		},
		// A nullable object spelled as a type list is the shape the
		// rewrite cannot match, so an optional property behind it would
		// reach OpenAI intact while the tool was certified strict (#6200).
		{
			name: "nullable object type list leaves a property optional",
			in: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"filter": map[string]any{
						"type":                 []string{"object", "null"},
						"additionalProperties": false,
						"properties": map[string]any{
							"tool_name": map[string]any{"type": "string"},
							"status":    map[string]any{"type": "string"},
						},
						"required": []string{"tool_name"},
					},
				},
				"required": []string{"filter"},
			},
			want: false,
		},
		{
			name: "nullable object type list that is already strict-valid",
			in: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"filter": map[string]any{
						"type":                 []any{"object", "null"},
						"additionalProperties": false,
						"properties": map[string]any{
							"tool_name": map[string]any{"type": "string"},
						},
						"required": []string{"tool_name"},
					},
				},
				"required": []string{"filter"},
			},
			want: true,
		},
		{
			name: "nullable object type list without additionalProperties false",
			in: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"filter": map[string]any{
						"type":       []string{"object", "null"},
						"properties": map[string]any{"a": map[string]any{"type": "string"}},
						"required":   []string{"a"},
					},
				},
				"required": []string{"filter"},
			},
			want: false,
		},
		{
			name: "nullable array type list leaves items optional",
			in: map[string]any{
				"type":  []string{"array", "null"},
				"items": map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}},
			},
			want: false,
		},
		// A $ref into local $defs is never resolved by the rewrite, so the
		// referenced object's optional property survives certification.
		{
			name: "ref into local defs leaves a property optional",
			in: map[string]any{
				"type": "object",
				"$defs": map[string]any{
					"target": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"properties": map[string]any{
							"job":   map[string]any{"type": "string"},
							"limit": map[string]any{"type": "integer"},
						},
						"required": []string{"job"},
					},
				},
				"properties": map[string]any{
					"target": map[string]any{"$ref": "#/$defs/target"},
				},
				"required": []string{"target"},
			},
			want: false,
		},
		{
			name: "a bare ref is not certified",
			in:   map[string]any{"$ref": "#/$defs/row"},
			want: false,
		},
		// An object-shaped node without an object type keeps its own
		// required list, so it is refused rather than assumed.
		{
			name: "properties without an object type",
			in: map[string]any{
				"properties": map[string]any{"a": map[string]any{"type": "string"}, "b": map[string]any{"type": "string"}},
				"required":   []string{"a"},
			},
			want: false,
		},
		{
			name: "fully required node without an object type",
			in: map[string]any{
				"properties": map[string]any{"a": map[string]any{"type": "string"}},
				"required":   []string{"a"},
			},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := StrictExpressible(tc.in); got != tc.want {
				t.Fatalf("StrictExpressible(%#v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// A schema StrictExpressible accepts must survive the projection with its
// declared required set intact, so the predicate and the rewrite agree.
func TestStrictExpressibleAgreesWithTheProjection(t *testing.T) {
	t.Parallel()
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"a": map[string]any{"type": "string"}},
		"required":   []string{"a"},
	}
	if !StrictExpressible(schema) {
		t.Fatal("a fully-required schema must be expressible")
	}
	projected := StrictifyJSONSchema(schema)
	required, _ := projected["required"].([]string)
	if len(required) != 1 || required[0] != "a" {
		t.Fatalf("projected required = %#v, want exactly the declared [a]", projected["required"])
	}
}
