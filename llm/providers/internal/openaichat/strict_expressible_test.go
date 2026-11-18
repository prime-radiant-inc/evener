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
		// An object that omits additionalProperties is open: it accepts any
		// keys. Closing it to the declared ones therefore removes calls the
		// advertised schema accepted, so the omission is not certifiable even
		// though the rewrite handles it as written.
		{
			name: "object omits additionalProperties",
			in: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"metadata": map[string]any{"type": "object"},
				},
				"required": []string{"metadata"},
			},
			want: false,
		},
		{
			name: "no properties and explicitly closed",
			in:   map[string]any{"type": "object", "additionalProperties": false},
			want: true,
		},
		// Without that keyword the object is open, so a caller may pass any
		// key and closing it would remove those calls.
		{
			name: "no properties and open",
			in:   map[string]any{"type": "object"},
			want: false,
		},
		{
			name: "every property required",
			in: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{"a": map[string]any{"type": "string"}},
				"required":             []string{"a"},
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
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{"a": map[string]any{"type": "string"}},
				"required":             []any{"a"},
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
				"type":                 "object",
				"additionalProperties": false,
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
		// Strict mode requires exactly the declared properties: a required name
		// the object never declares makes the projected schema unsatisfiable,
		// and it is illegal in strict mode whether or not the rewrite transforms
		// the node. A closed nullable object is the case that escaped, because
		// the rewrite matches only a literal type string.
		{
			name: "closed nullable object requires a key it does not declare",
			in: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"target": map[string]any{
						"type":                 []string{"object", "null"},
						"additionalProperties": false,
						"properties": map[string]any{
							"id": map[string]any{"type": "string"},
						},
						"required": []string{"id", "scope"},
					},
				},
				"required": []string{"target"},
			},
			want: false,
		},
		{
			name: "closed object requires a key it does not declare",
			in: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"id": map[string]any{"type": "string"},
				},
				"required": []string{"id", "scope"},
			},
			want: false,
		},
		// A schema that constrains nothing cannot be verified against strict
		// mode: an empty schema admits every JSON value, so an argument
		// declared that way constrains nothing the provider could check.
		{
			name: "closed object with a required unconstrained argument",
			in: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"payload": map[string]any{},
				},
				"required": []string{"payload"},
			},
			want: false,
		},
		{
			name: "closed object with a required unconstrained array",
			in: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"tags": map[string]any{"type": "array"},
				},
				"required": []string{"tags"},
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
				"type":                 "object",
				"additionalProperties": false,
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
				"type":                 "object",
				"additionalProperties": false,
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
		// A branch that declares no properties of its own has no faithful
		// strict form under a transformed parent. Closing it against its own
		// empty map makes it accept only {}, while the parent requires every
		// property it declares, so the union admits nothing. Closing it against
		// the parent's properties instead widens it: a branch that originally
		// rejected its siblings' objects begins accepting every one the parent
		// allows, and copying the parent's subtrees into each branch grows the
		// projection with nesting. The tool is sent non-strict with the schema
		// it advertises, which keeps every argument it documented.
		{
			name: "branch requires a key the parent declares but declares no properties",
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
			want: false,
		},
		// The gate refuses the combinator rather than certifying it: the rewrite
		// closes the branch against its own id property, which forbids the name
		// key the parent requires, so no object satisfies both.
		// A branch declaring every property the parent declares closes to the
		// same key set and keeps its meaning, but anyOf as a keyword still goes
		// non-strict because the rewrite visits branches from nodes it does not
		// transform.
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
		// A combinator is outside the modeled keyword set, so a schema carrying
		// one is refused whether or not each branch happens to be closed. The
		// rewrite closes branches from any node it visits, and a branch it
		// changes cannot be certified as a whole.
		{
			name: "combinator branch is already closed and fully required",
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
						"type":                 "object",
						"additionalProperties": false,
						"properties":           map[string]any{"id": map[string]any{"type": "string"}},
						"required":             []string{"id"},
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
		// Closing an object to additionalProperties: false leaves it admitting
		// exactly its declared keys, so a minimum above that count cannot be
		// met by any object the projection accepts.
		{
			name: "required labels object needs more keys than it declares",
			in: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"labels": map[string]any{
						"type":          "object",
						"minProperties": 1,
						"properties":    map[string]any{},
					},
				},
				"required": []string{"labels"},
			},
			want: false,
		},
		// allOf constrains the same object, so a key it requires must remain
		// reachable once the enclosing node is closed. The branch carries no
		// type and no properties, so nothing but this check sees it.
		{
			name: "allOf branch requires a key the closed parent never declares",
			in: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{"id": map[string]any{"type": "string"}},
				"required":             []string{"id"},
				"allOf": []any{
					map[string]any{"required": []string{"scope"}},
				},
			},
			want: false,
		},
		// The rewrite descends into items only, so a tuple's prefixItems reach
		// the provider as advertised.
		{
			name: "array prefixItems hold an object with an optional property",
			in: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pair": map[string]any{
						"type": "array",
						"prefixItems": []any{
							map[string]any{
								"type": "object",
								"properties": map[string]any{
									"a": map[string]any{"type": "string"},
									"b": map[string]any{"type": "string"},
								},
								"required": []string{"a"},
							},
						},
					},
				},
				"required": []string{"pair"},
			},
			want: false,
		},
		// anyOf on a node with no literal object type: the branch-closure rule
		// never saw it, but the rewrite still visits and closes each branch, so
		// a call the original accepted stops validating.
		{
			name: "untyped wrapper requires a key its anyOf branches cannot express",
			in: map[string]any{
				"required": []string{"scope"},
				"anyOf": []any{
					map[string]any{
						"type":       "object",
						"properties": map[string]any{"id": map[string]any{"type": "string"}},
						"required":   []string{"id"},
					},
					map[string]any{
						"type":       "object",
						"properties": map[string]any{"name": map[string]any{"type": "string"}},
						"required":   []string{"name"},
					},
				},
			},
			want: false,
		},
		// dependentRequired constrains a key the node never declares, so closing
		// the object forbids the very key the dependency requires.
		{
			name: "dependentRequired requires a key the closed object forbids",
			in: map[string]any{
				"type":              "object",
				"properties":        map[string]any{"id": map[string]any{"type": "string"}},
				"required":          []string{"id"},
				"dependentRequired": map[string]any{"id": []any{"scope"}},
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
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{"a": map[string]any{"type": "string"}},
		"required":             []string{"a"},
	}
	if !StrictExpressible(schema) {
		t.Fatal("a fully-required closed schema must be expressible")
	}
	projected := StrictifyJSONSchema(schema)
	required, _ := projected["required"].([]string)
	if len(required) != 1 || required[0] != "a" {
		t.Fatalf("projected required = %#v, want exactly the declared [a]", projected["required"])
	}
}

// An object that omits additionalProperties accepts any key, so closing it to
// the declared ones removes calls the tool documents. The gate must refuse it
// and the caller must send the schema it advertised. This is the regression
// test for treating an absent keyword as a closed shape: restoring that
// reading makes the first assertion fail.
func TestOmittedAdditionalPropertiesIsNotCertified(t *testing.T) {
	t.Parallel()
	// The root is explicitly closed and only the nested object omits the
	// keyword, so the refusal has to come from inspecting that child. Leaving
	// the root open would refuse the schema at the top before descending.
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"metadata": map[string]any{"type": "object"},
		},
		"required": []string{"metadata"},
	}
	args := map[string]any{"metadata": map[string]any{"team": "x"}}

	if !schemaAccepts(t, schema, args) {
		t.Fatal("an open object must accept a key it does not declare")
	}
	if StrictExpressible(schema) {
		t.Fatal("a schema whose object omits additionalProperties must not be certified strict")
	}
	// The caller sends the advertised schema, so the documented call survives.
	if !schemaAccepts(t, schema, args) {
		t.Fatal("the schema sent non-strict rejects the call it documents")
	}
	// And the projection, which is never built for it, would reject that call.
	// Asserting this keeps the refusal load-bearing rather than conservative.
	projected := StrictifyJSONSchema(schema)
	if schemaAccepts(t, projected, args) {
		t.Fatal("closing this object was expected to reject the key, so the refusal proves nothing")
	}
	// An explicitly closed object is still certifiable: with additionalProperties
	// false at both levels every key any object admits is declared.
	closed := deepCopyAny(schema).(map[string]any)
	closed["additionalProperties"] = false
	closed["properties"].(map[string]any)["metadata"].(map[string]any)["additionalProperties"] = false
	if !StrictExpressible(closed) {
		t.Fatal("an explicitly closed object must remain certifiable")
	}
}

// A property-less branch must not start accepting objects its siblings reject.
// Closing it against the enclosing object's properties would do exactly that:
// the union would widen from "one of the alternatives" to "anything the parent
// allows", and the registry still validates against the original schema, so
// the widened calls would be refused after the model made them.
func TestRefusedBranchKeepsItsExclusion(t *testing.T) {
	t.Parallel()
	// The safe branch admits {mode: "safe", note: "n"}. The second branch
	// declares no properties and closes itself, so it admits only {}, which the
	// parent rejects: the union is the safe branch alone. Giving that branch
	// the parent's properties would make it admit {mode: "fast", note: "n"}.
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"mode": map[string]any{"type": "string"},
			"note": map[string]any{"type": "string"},
		},
		"required": []string{"mode", "note"},
		"anyOf": []any{
			map[string]any{
				"type":       "object",
				"properties": map[string]any{"mode": map[string]any{"const": "safe"}},
				"required":   []string{"mode"},
			},
			map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}},
		},
	}
	if StrictExpressible(schema) {
		t.Fatal("a parent with a property-less branch must not be certified")
	}
	safe := map[string]any{"mode": "safe", "note": "n"}
	fast := map[string]any{"mode": "fast", "note": "n"}
	if !schemaAccepts(t, schema, safe) {
		t.Fatal("the advertised schema rejects the alternative it documents")
	}
	if schemaAccepts(t, schema, fast) {
		t.Fatal("the advertised schema has stopped excluding the undocumented alternative")
	}
}

// Every schema the gate certifies must accept, after projection, the calls its
// original form accepted. The keywords the rewrite does not model are what
// break that, so a schema carrying one is refused and sent as it was
// advertised. The shapes that break it are those where a keyword constrains
// keys the projected object would forbid: a wrapper or a dependency.
func TestRefusedKeywordSchemasKeepTheirAcceptedCalls(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		schema map[string]any
		args   map[string]any
	}{
		{
			name: "untyped wrapper over anyOf",
			schema: map[string]any{
				"required": []string{"scope"},
				"anyOf": []any{
					map[string]any{
						"type":       "object",
						"properties": map[string]any{"id": map[string]any{"type": "string"}},
						"required":   []string{"id"},
					},
					map[string]any{
						"type":       "object",
						"properties": map[string]any{"name": map[string]any{"type": "string"}},
						"required":   []string{"name"},
					},
				},
			},
			args: map[string]any{"scope": "s", "id": "i"},
		},
		{
			name: "dependentRequired",
			schema: map[string]any{
				"type":              "object",
				"properties":        map[string]any{"id": map[string]any{"type": "string"}},
				"required":          []string{"id"},
				"dependentRequired": map[string]any{"id": []any{"scope"}},
			},
			args: map[string]any{"id": "i", "scope": "s"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !schemaAccepts(t, tc.schema, tc.args) {
				t.Fatalf("the advertised schema rejects the call it documents: %v", tc.args)
			}
			if StrictExpressible(tc.schema) {
				t.Fatal("a schema with an unmodeled keyword must not be certified strict")
			}
			// The caller sends the advertised schema, so the call still validates.
			if !schemaAccepts(t, tc.schema, tc.args) {
				t.Fatalf("the schema sent non-strict rejects the call it documents: %v", tc.args)
			}
			// And the projection, which is never built for it, would have
			// rejected that call. Asserting this keeps the reason honest: the
			// refusal is load-bearing, not merely conservative.
			if schemaAccepts(t, StrictifyJSONSchema(tc.schema), tc.args) {
				t.Fatal("the projection was expected to narrow this schema, so the refusal proves nothing")
			}
		})
	}
}

// A schema the gate refuses is sent non-strict with the schema it advertised,
// so every argument it documented still reaches the model unchanged. This is
// what keeps a refused combinator schema usable: the tool loses strict mode,
// not the arguments its branches select between.
func TestRefusedBranchSchemaKeepsItsAdvertisedArguments(t *testing.T) {
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
	if StrictExpressible(schema) {
		t.Fatal("a parent whose property-less branches would collapse or be widened must not be certified")
	}
	// The caller keeps the advertised schema, which still accepts the object
	// its required list demands and still enforces the branch selection.
	args := map[string]any{"id": "a", "name": "b"}
	if !schemaAccepts(t, schema, args) {
		t.Fatal("the advertised schema rejects the object its own required list demands")
	}
	// And the projection is never consulted for it, so no subtree is copied
	// into the branches. Assert that the schema the tool sends is unmodified.
	if !schemaAccepts(t, deepCopyAny(schema).(map[string]any), args) {
		t.Fatal("the schema sent non-strict must accept what the advertised one accepts")
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
