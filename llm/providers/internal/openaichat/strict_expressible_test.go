package openaichat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// StrictExpressible decides whether a schema may be projected into OpenAI
// strict mode without making a documented-optional argument mandatory. Strict
// mode requires every property of every object to be present and listed in
// required, so only a schema that already does so is expressible.
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
		// reach OpenAI intact while the tool was certified strict.
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
		// The rewrite stops at a nullable array's type list, so nothing
		// below it is rewritten either. An item object that requires every
		// property but omits additionalProperties: false still reaches the
		// provider as advertised, and strict mode rejects it.
		{
			name: "nullable array items omit additionalProperties false",
			in: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"rows": map[string]any{
						"type": []string{"array", "null"},
						"items": map[string]any{
							"type":       "object",
							"properties": map[string]any{"id": map[string]any{"type": "integer"}},
							"required":   []string{"id"},
						},
					},
				},
				"required": []string{"rows"},
			},
			want: false,
		},
		{
			name: "nullable array items are already strict-valid",
			in: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"rows": map[string]any{
						"type": []string{"array", "null"},
						"items": map[string]any{
							"type":                 "object",
							"additionalProperties": false,
							"properties":           map[string]any{"id": map[string]any{"type": "integer"}},
							"required":             []string{"id"},
						},
					},
				},
				"required": []string{"rows"},
			},
			want: true,
		},
		// A descendant of a nullable object is equally unreachable.
		{
			name: "nullable object property descendant omits additionalProperties false",
			in: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"filter": map[string]any{
						"type":                 []string{"object", "null"},
						"additionalProperties": false,
						"properties": map[string]any{
							"nested": map[string]any{
								"type":       "object",
								"properties": map[string]any{"a": map[string]any{"type": "string"}},
								"required":   []string{"a"},
							},
						},
						"required": []string{"nested"},
					},
				},
				"required": []string{"filter"},
			},
			want: false,
		},
		{
			name: "nullable object property descendant that is already strict-valid",
			in: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"filter": map[string]any{
						"type":                 []string{"object", "null"},
						"additionalProperties": false,
						"properties": map[string]any{
							"nested": map[string]any{
								"type":                 "object",
								"additionalProperties": false,
								"properties":           map[string]any{"a": map[string]any{"type": "string"}},
								"required":             []string{"a"},
							},
						},
						"required": []string{"nested"},
					},
				},
				"required": []string{"filter"},
			},
			want: true,
		},
		// A Go caller can hand properties over as a typed map. The rewrite
		// would replace it with an empty map and drop every advertised
		// argument, so it cannot be certified.
		{
			name: "properties as a typed map is unreadable",
			in: map[string]any{
				"type":       "object",
				"properties": map[string]map[string]any{"a": {"type": "string"}},
				"required":   []string{"a"},
			},
			want: false,
		},
		{
			name: "combinator branch as a typed slice is unreadable",
			in: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"a": map[string]any{"type": "string"},
				},
				"required": []string{"a"},
				"oneOf": []map[string]any{
					{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "string"}}},
				},
			},
			want: false,
		},
		// The rewrite sets additionalProperties: false on every object it
		// transforms. An object that explicitly allows other keys — with
		// true, or with a schema for their values — would be narrowed to
		// accepting none of them.
		{
			name: "transformed object allows additional keys with true",
			in: map[string]any{
				"type":                 "object",
				"additionalProperties": true,
				"properties":           map[string]any{"a": map[string]any{"type": "string"}},
				"required":             []string{"a"},
			},
			want: false,
		},
		{
			name: "transformed object allows additional keys by value schema",
			in: map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "string"},
				"properties":           map[string]any{"a": map[string]any{"type": "string"}},
				"required":             []string{"a"},
			},
			want: false,
		},
		{
			name: "nested transformed object allows additional keys by value schema",
			in: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"headers": map[string]any{
						"type":                 "object",
						"additionalProperties": map[string]any{"type": "string"},
					},
				},
				"required": []string{"headers"},
			},
			want: false,
		},
		// A property child the walk cannot read is not verified, and the
		// rewrite skips it too, so it must not be certified.
		{
			name: "property child is not a schema map",
			in: map[string]any{
				"type":       "object",
				"properties": map[string]any{"options": []byte(`{"type":"object"}`)},
				"required":   []string{"options"},
			},
			want: false,
		},
		{
			name: "combinator entry is not a schema map",
			in: map[string]any{
				"type":       "object",
				"properties": map[string]any{"a": map[string]any{"type": "string"}},
				"required":   []string{"a"},
				"anyOf":      []any{[]byte(`{"type":"object"}`)},
			},
			want: false,
		},
		// Closing an object to additionalProperties: false forbids every key
		// it does not declare, so a branch requiring an undeclared key can
		// no longer be satisfied by the projection.
		{
			name: "branch requires a key the closed parent never declares",
			in: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{},
				"anyOf": []any{
					map[string]any{"type": "object", "required": []string{"id"}},
					map[string]any{"type": "object", "required": []string{"name"}},
				},
			},
			want: false,
		},
		// The gate only certifies a parent that requires every property it
		// declares, so a branch narrowing required to a subset of those names
		// was already redundant. Closing the branch against the parent's
		// properties keeps that so, where closing it against an empty map
		// would have made it accept only {}.
		{
			name: "branch requires a key the parent declares",
			in: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"id":   map[string]any{"type": "string"},
					"name": map[string]any{"type": "string"},
				},
				"required": []string{"id", "name"},
				"anyOf": []any{
					map[string]any{"type": "object", "required": []string{"id"}},
					map[string]any{"type": "object", "required": []string{"name"}},
				},
			},
			want: true,
		},
		// The rewrite gives such a branch the enclosing object's declared
		// properties before closing it, so the branch still admits the object
		// the enclosing node requires. A branch carrying its own properties
		// would instead have those narrowed against the rest, so it is
		// refused rather than certified.
		{
			name: "closed branch declares properties of its own",
			in: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"id":   map[string]any{"type": "string"},
					"name": map[string]any{"type": "string"},
				},
				"required": []string{"id", "name"},
				"anyOf": []any{
					map[string]any{
						"type":       "object",
						"properties": map[string]any{"id": map[string]any{"type": "string"}},
						"required":   []string{"id"},
					},
				},
			},
			want: false,
		},
		// A type list naming both object and array has two shapes to satisfy,
		// and the node is not transformed, so the array side is never
		// rewritten either. An item object with an optional property still
		// reaches the provider, which strict mode rejects.
		{
			name: "mixed object and array type list leaves an item property optional",
			in: map[string]any{
				"type":                 []string{"object", "array"},
				"additionalProperties": false,
				"properties":           map[string]any{"split": map[string]any{"type": "string"}},
				"required":             []string{"split"},
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"row":  map[string]any{"type": "string"},
						"note": map[string]any{"type": "string"},
					},
					"required": []string{"row"},
				},
			},
			want: false,
		},
		{
			name: "mixed object and array type list whose item object is strict-valid",
			in: map[string]any{
				"type":                 []string{"object", "array"},
				"additionalProperties": false,
				"properties":           map[string]any{"split": map[string]any{"type": "string"}},
				"required":             []string{"split"},
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties":           map[string]any{"row": map[string]any{"type": "string"}},
					"required":             []string{"row"},
				},
			},
			want: true,
		},
		// The rewrite replaces required with exactly the declared property
		// names, so a requirement on a key the node never declares would be
		// dropped rather than projected.
		{
			name: "required names a key the node never declares",
			in: map[string]any{
				"type":       "object",
				"properties": map[string]any{"a": map[string]any{"type": "string"}},
				"required":   []string{"a", "b"},
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

// A schema StrictExpressible accepts must still accept the arguments its
// original form accepted. Closing a combinator branch sets
// additionalProperties: false against the branch's own properties, so a
// branch that only requires keys accepts the empty object once closed and
// then admits nothing the enclosing object requires.
func TestStrictProjectionKeepsRequiredBranchesSatisfiable(t *testing.T) {
	t.Parallel()
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"id":   map[string]any{"type": "string"},
			"name": map[string]any{"type": "string"},
		},
		"required": []string{"id", "name"},
		"anyOf": []any{
			map[string]any{"type": "object", "required": []string{"id"}},
			map[string]any{"type": "object", "required": []string{"name"}},
		},
	}
	args := map[string]any{"id": "a", "name": "b"}
	if !schemaAccepts(t, schema, args) {
		t.Fatal("the declared schema does not accept both required keys")
	}
	if !StrictExpressible(schema) {
		t.Fatal("a schema whose branches only require declared keys must be expressible")
	}
	if !schemaAccepts(t, StrictifyJSONSchema(schema), args) {
		t.Fatal("the projection rejects the object its own required list demands")
	}
}

// schemaAccepts compiles schema with the validator the tool registry uses and
// reports whether args satisfies it.
func schemaAccepts(t *testing.T, schema map[string]any, args map[string]any) bool {
	t.Helper()
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("mem://strict.json", strings.NewReader(string(raw))); err != nil {
		t.Fatalf("add schema resource: %v", err)
	}
	compiled, err := compiler.Compile("mem://strict.json")
	if err != nil {
		t.Fatalf("compile schema %s: %v", raw, err)
	}
	return compiled.Validate(args) == nil
}
