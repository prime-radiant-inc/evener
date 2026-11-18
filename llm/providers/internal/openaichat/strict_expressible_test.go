package openaichat

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

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
		// Annotation keywords describe a value without naming the set it
		// belongs to, so a parameter declared with only those accepts exactly
		// what the empty schema accepts. Strict mode rejects a node that states
		// no type and no value constraint, so certifying one loses the whole
		// request rather than one tool's strictness.
		{
			name: "closed object with a required annotation-only argument",
			in: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"payload": map[string]any{"description": "Free-form payload"},
				},
				"required": []string{"payload"},
			},
			want: false,
		},
		{
			name: "annotation-only array items",
			in: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"tags": map[string]any{
						"type":  "array",
						"items": map[string]any{"description": "A tag"},
					},
				},
				"required": []string{"tags"},
			},
			want: false,
		},
		// A keyword is a constraint only if it can reject some value. Every
		// scalar bound is type-conditional and has an identity form that
		// rejects nothing at all, so its presence on an untyped node proves
		// nothing: {"minLength": 0} admits exactly what {} admits.
		{
			name: "closed object with a required identity-bound argument",
			in: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"payload": map[string]any{"minLength": 0},
				},
				"required": []string{"payload"},
			},
			want: false,
		},
		{
			name: "closed object with a required identity minItems argument",
			in: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"payload": map[string]any{"minItems": 0},
				},
				"required": []string{"payload"},
			},
			want: false,
		},
		{
			name: "closed object with a required identity uniqueItems argument",
			in: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"payload": map[string]any{"uniqueItems": false},
				},
				"required": []string{"payload"},
			},
			want: false,
		},
		{
			name: "closed object with a required empty-pattern argument",
			in: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"payload": map[string]any{"pattern": ""},
				},
				"required": []string{"payload"},
			},
			want: false,
		},
		// A closed value set does constrain an untyped node, so these are the
		// keywords that still qualify one.
		// A type list naming both object and array constrains the node two
		// ways, and strict mode needs both to be verifiable. An array side with
		// no items admits any element, so the node is not certifiable even when
		// the object side is fully specified.
		{
			name: "object and array type list without items",
			in: map[string]any{
				"type":                 []any{"object", "array"},
				"additionalProperties": false,
				"properties":           map[string]any{"a": map[string]any{"type": "string"}},
				"required":             []string{"a"},
			},
			want: false,
		},
		{
			name: "closed object with a required enumerated argument",
			in: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"payload": map[string]any{"enum": []any{"a", "b"}},
				},
				"required": []string{"payload"},
			},
			want: true,
		},
		{
			name: "closed object with a required const argument",
			in: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"payload": map[string]any{"const": "fixed"},
				},
				"required": []string{"payload"},
			},
			want: true,
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

// TestReadableKeywordsRejectSomeValue checks the classification against the
// validator rather than against itself. The structural test above reads the
// same table the gate reads, so it would agree with a wrong classification;
// this one asks the schema validator two questions the classification answers.
//
// A keyword classified readableByWalk is claimed to name a value set the walk
// can read. The evidence is that a normal form of it rejects some JSON value:
// an enum of two strings rejects a number, and const rejects everything but its
// own value. Every other keyword can certify nothing, and the identity forms
// below are the evidence — each admits every value, which is exactly why a
// bound like minLength: 0 cannot stand in for a constraint.
func TestReadableKeywordsRejectSomeValue(t *testing.T) {
	t.Parallel()
	probes := []any{0.0, "", true, nil, map[string]any{}, []any{}}
	rejectsSomething := func(argument map[string]any) bool {
		compiled := compileSchema(t, argument)
		for _, probe := range probes {
			if compiled.Validate(probe) != nil {
				return true
			}
		}
		return false
	}

	// A normal form of every certifying keyword must reject a value.
	certifying := map[string]any{
		"enum":  []any{"a", "b"},
		"const": "fixed",
	}
	for keyword, value := range certifying {
		if modeledKeywords[keyword] != readableByWalk {
			t.Fatalf("keyword %q is no longer classified readableByWalk, so nothing should be certified on it alone", keyword)
		}
		if !rejectsSomething(map[string]any{keyword: value}) {
			t.Fatalf("keyword %q is classified readableByWalk but %v accepts every probe: a keyword that rejects nothing cannot certify a node", keyword, value)
		}
	}

	// The identity form of every non-certifying keyword must accept every
	// probe, which is the reason it cannot certify a typeless node.
	identityForm := map[string]any{
		"description": "d", "title": "t", "default": nil, "examples": []any{},
		"format": "unknown-format", "pattern": "", "uniqueItems": false,
		"minLength": 0, "minItems": 0,
	}
	for keyword, value := range identityForm {
		if modeledKeywords[keyword] == readableByWalk {
			t.Fatalf("keyword %q is classified readableByWalk but only an identity form is listed here", keyword)
		}
		if rejectsSomething(map[string]any{keyword: value}) {
			t.Fatalf("keyword %q with value %v rejects a probe, so the identity form is not what this table assumes", keyword, value)
		}
	}

	// A bound applies to one type and leaves every other type admissible, so a
	// probed value of the wrong type proves nothing. Each bound below must be
	// shown to reject a value of its own type, which is what makes it a
	// constraint, and to leave a value of another type admissible, which is why
	// it still cannot certify a typeless node.
	typeConditional := []struct {
		name    string
		schema  map[string]any
		rejects any
		allows  any
	}{
		{"minLength", map[string]any{"minLength": 1}, "", 0.0},
		{"maxLength", map[string]any{"maxLength": 0}, "a", 0.0},
		{"maxItems", map[string]any{"maxItems": 0}, []any{"x"}, ""},
		{"maximum", map[string]any{"maximum": 0.0}, 1.0, ""},
		{"minimum", map[string]any{"minimum": 0.0}, -1.0, ""},
	}
	for _, tc := range typeConditional {
		compiled := compileSchema(t, tc.schema)
		if compiled.Validate(tc.rejects) == nil {
			t.Fatalf("keyword %q accepts %v, but a bound that rejects no value of its own type is an annotation wearing a constraint's clothes", tc.name, tc.rejects)
		}
		if err := compiled.Validate(tc.allows); err != nil {
			t.Fatalf("keyword %q rejects %v, a value of another type: %v", tc.name, tc.allows, err)
		}
	}
}

// A keyword may certify a typeless node only when modeledKeywords classifies it
// readableByWalk. This is the property the predicate is built on, asserted
// through the predicate for every classified keyword, so a new entry in the
// table cannot quietly acquire the power to certify: the test reads the same
// table the gate reads and fails unless the two answers agree.
//
// It is also the regression test for minLength: 0. That keyword is covered —
// the rewrite neither reads nor rewrites it — and it used to certify, which
// sent a schema admitting every JSON value out with strict: true.
func TestOnlyReadableKeywordsCertifyATypelessNode(t *testing.T) {
	t.Parallel()
	// The envelope is closed and fully required, so the only thing deciding the
	// verdict is the argument's own keyword.
	envelope := func(argument map[string]any) map[string]any {
		return map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           map[string]any{"payload": argument},
			"required":             []string{"payload"},
		}
	}
	// Every keyword needs a value the validator can compile. The value does not
	// choose the answer: the classification does.
	probeValue := func(keyword string) any {
		switch keyword {
		case "enum":
			return []any{"a", "b"}
		case "const":
			return "fixed"
		case "properties", "patternProperties", "additionalProperties", "required":
			return map[string]any{}
		case "items", "prefixItems", "allOf", "anyOf", "oneOf":
			return []any{}
		case "type":
			return "string"
		case "uniqueItems":
			return false
		default:
			return 0
		}
	}
	keywords := make([]string, 0, len(modeledKeywords))
	for keyword := range modeledKeywords {
		keywords = append(keywords, keyword)
	}
	sort.Strings(keywords)
	for _, keyword := range keywords {
		// type is the other qualifying path, not a keyword that certifies a
		// typeless node: naming a type makes the node typed. That path is
		// asserted separately below.
		if keyword == "type" {
			continue
		}
		argument := map[string]any{keyword: probeValue(keyword)}
		if _, has := argument["description"]; !has && keyword != "description" {
			argument["description"] = "keeps the node compilable"
		}
		want := modeledKeywords[keyword] == readableByWalk
		if got := StrictExpressible(envelope(argument)); got != want {
			t.Fatalf("keyword %q (coverage %v): StrictExpressible = %v, want %v: only a keyword whose accepted set the walk can read may certify a typeless node",
				keyword, modeledKeywords[keyword], got, want)
		}
	}
	// The typed path still certifies: a supported type constrains the node, so
	// the refusal above is about typeless nodes rather than a blanket refusal.
	if !StrictExpressible(envelope(map[string]any{"type": "string"})) {
		t.Fatal("a node naming a supported type must remain certifiable")
	}
}

// A Go caller can spell a JSON null in an enum as a typed nil map. Marshaling
// renders it as null, so the advertised schema accepts null, and the gate
// certifies the schema. The projection must preserve that member: copying a nil
// map into an empty one turns null into {}, and the emitted schema then rejects
// a value the advertised schema accepts.
//
// Both directions are asserted on the same schema: advertised and emitted each
// accept the null member, and each accept the ordinary string members.
func TestProjectionPreservesATypedNilMapInAnEnum(t *testing.T) {
	t.Parallel()
	// Both spellings of a typed nil that marshals as JSON null: the copy has a
	// separate arm for each, so one test per arm is what pins both.
	var nilMap map[string]any
	var nilSlice []any
	for name, nullMember := range map[string]any{"typed nil map": nilMap, "typed nil slice": nilSlice} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			schema := map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"format": map[string]any{
						"type": []any{"string", "null"},
						"enum": []any{"outline", "markdown", nullMember},
					},
				},
				"required": []string{"format"},
			}
			if !StrictExpressible(schema) {
				t.Fatal("a closed fully-required schema must be certifiable")
			}

			null := map[string]any{"format": nil}
			outline := map[string]any{"format": "outline"}
			markdown := map[string]any{"format": "markdown"}
			if !schemaAccepts(t, schema, null) {
				t.Fatal("the advertised schema must accept the null member it declares")
			}

			emitted := StrictifyJSONSchema(schema)
			if !schemaAccepts(t, emitted, null) {
				t.Fatal("the emitted schema rejects the null member the advertised one accepts: the projection changed what it certified")
			}
			for _, args := range []map[string]any{outline, markdown} {
				if !schemaAccepts(t, schema, args) {
					t.Fatalf("the advertised schema rejects %v, one of its own string members", args)
				}
				if !schemaAccepts(t, emitted, args) {
					t.Fatalf("the emitted schema rejects %v, which the advertised schema accepts", args)
				}
			}
		})
	}
}

// A provider enforcing the strict subset requires an explicit type on every
// node, and TestManagerProviderSchemasHaveExplicitTypes records that
// requirement from a live rejection. A closed value set whose members all share
// one scalar type supplies that type without changing the set, so the gate
// certifies it and the projection states it.
//
// The invariant is the accepted set, not the presence of the keyword: for each
// shape below the advertised schema and the emitted one must accept exactly the
// same values. A projection that widened, narrowed or dropped a member would
// fail here even though the type was supplied.
func TestProjectionStatesTheTypeAClosedValueSetImplies(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		closed map[string]any
		accept map[string]any
		reject map[string]any
		want   string
	}{
		{
			name:   "const in a required position",
			closed: map[string]any{"const": "fixed"},
			accept: map[string]any{"payload": "fixed"},
			reject: map[string]any{"payload": "other"},
			want:   "string",
		},
		{
			name:   "enum in a required position",
			closed: map[string]any{"enum": []any{"a", "b"}},
			accept: map[string]any{"payload": "b"},
			reject: map[string]any{"payload": "c"},
			want:   "string",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			schema := map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{"payload": tc.closed},
				"required":             []string{"payload"},
			}
			if !StrictExpressible(schema) {
				t.Fatal("a closed value set implying one scalar type must be certifiable")
			}
			if !schemaAccepts(t, schema, tc.accept) {
				t.Fatalf("the advertised schema rejects %v, one of its own values", tc.accept)
			}
			if schemaAccepts(t, schema, tc.reject) {
				t.Fatalf("the advertised schema accepts %v, which its value set excludes", tc.reject)
			}

			emitted := StrictifyJSONSchema(schema)
			payload := emitted["properties"].(map[string]any)["payload"].(map[string]any)
			if payload["type"] != tc.want {
				t.Fatalf("the emitted argument has type %#v, want %q: a strict-enforcing provider rejects a node with no explicit type, which fails the whole request", payload["type"], tc.want)
			}
			// Supplying the type must not change the accepted set.
			if !schemaAccepts(t, emitted, tc.accept) {
				t.Fatalf("the emitted schema rejects %v, which the advertised schema accepts", tc.accept)
			}
			if schemaAccepts(t, emitted, tc.reject) {
				t.Fatalf("the emitted schema accepts %v, which the advertised schema rejects", tc.reject)
			}
		})
	}
}

// The rewrite matches only a literal "object" type, so a nullable object is a
// node it never descends into: its properties keep exactly what they advertise.
// The gate still certifies such a node, so the projection has to state the
// derived type inside it — otherwise a strict-enforcing provider rejects the
// whole request over a node two levels below the root.
func TestProjectionStatesDerivedTypesUnderANullableObject(t *testing.T) {
	t.Parallel()
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"payload": map[string]any{
				"type": []any{"object", "null"},
				"properties": map[string]any{
					"mode": map[string]any{"const": "fixed"},
				},
				"required":             []string{"mode"},
				"additionalProperties": false,
			},
		},
		"required": []string{"payload"},
	}
	if !StrictExpressible(schema) {
		t.Fatal("a closed nullable object over a closed value set must be certifiable")
	}

	// The advertised schema accepts both a null payload and a valid nested
	// object, and excludes a mode its value set does not name.
	if !schemaAccepts(t, schema, map[string]any{"payload": nil}) {
		t.Fatal("the advertised schema must accept the null branch it declares")
	}
	nested := map[string]any{"payload": map[string]any{"mode": "fixed"}}
	if !schemaAccepts(t, schema, nested) {
		t.Fatal("the advertised schema rejects the valid nested object")
	}
	if schemaAccepts(t, schema, map[string]any{"payload": map[string]any{"mode": "other"}}) {
		t.Fatal("the advertised schema accepts a mode its value set excludes")
	}

	emitted := StrictifyJSONSchema(schema)
	payload := emitted["properties"].(map[string]any)["payload"].(map[string]any)
	mode := payload["properties"].(map[string]any)["mode"].(map[string]any)
	if mode["type"] != "string" {
		t.Fatalf("the emitted nested argument has type %#v, want \"string\": the rewrite never descends into a nullable object, so the type has to be stated here or the provider rejects the request", mode["type"])
	}
	// Stating the type must leave the nullable object's accepted set alone.
	if !schemaAccepts(t, emitted, map[string]any{"payload": nil}) {
		t.Fatal("the emitted schema rejects the null branch the advertised one accepts")
	}
	if !schemaAccepts(t, emitted, nested) {
		t.Fatal("the emitted schema rejects the valid nested object the advertised one accepts")
	}
	if schemaAccepts(t, emitted, map[string]any{"payload": map[string]any{"mode": "other"}}) {
		t.Fatal("the emitted schema accepts a mode the advertised schema rejects")
	}
}

// A value set this package cannot name with one type is not certifiable: the
// projection would have to guess the type, and a guess is exactly what the gate
// withholds. Mixed scalars, container values, null and an empty set all take
// that path, and the tool goes out non-strict with the schema it advertised.
func TestMixedValueSetIsNotCertified(t *testing.T) {
	t.Parallel()
	refused := map[string]map[string]any{
		"mixed scalars": {"enum": []any{"a", 1.0}},
		"null member":   {"enum": []any{"a", nil}},
		"container":     {"const": map[string]any{"a": "b"}},
		"empty set":     {"enum": []any{}},
	}
	for name, argument := range refused {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			schema := map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{"payload": argument},
				"required":             []string{"payload"},
			}
			if StrictExpressible(schema) {
				t.Fatalf("a value set with no single scalar type must not be certified: %#v", argument)
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
// reading makes the eligibility assertion fail.
func TestOmittedAdditionalPropertiesIsNotCertified(t *testing.T) {
	t.Parallel()
	// The root is explicitly closed and only the nested object omits the
	// keyword, so the refusal has to come from inspecting that child. Leaving
	// the root open would refuse the schema at the top before descending.
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"metadata": map[string]any{
				"type":       "object",
				"properties": map[string]any{"team": map[string]any{"type": "string"}},
				"required":   []string{"team"},
			},
		},
		"required": []string{"metadata"},
	}
	// The nested object declares team and stays open, so the call carries a key
	// it never declared. That is the call closing the object would remove, and
	// it is why the omission is refused rather than rewritten.
	args := map[string]any{"metadata": map[string]any{"team": "x", "slack": "y"}}

	if !schemaAccepts(t, schema, args) {
		t.Fatal("an open object must accept a key it does not declare")
	}
	if StrictExpressible(schema) {
		t.Fatal("a schema whose object omits additionalProperties must not be certified strict")
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
}

// schemaAccepts compiles schema with the validator the tool registry uses and
// reports whether args satisfies it.
func schemaAccepts(t *testing.T, schema map[string]any, args map[string]any) bool {
	t.Helper()
	return compileSchema(t, schema).Validate(args) == nil
}

// compileSchema compiles schema with the validator the tool registry uses.
func compileSchema(t *testing.T, schema map[string]any) *jsonschema.Schema {
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
	return compiled
}

// A closed value set reaches this package in two concrete shapes for the same
// JSON: a Go builder writes []string, and a decoded provider schema yields
// []any. Both describe the same enum, so the supplied type has to be the same
// one. Recognising only the decoded shape would leave a Go-authored tool
// typeless on the wire and cost the whole request.
func TestSupplyDerivedTypesReadsEveryConcreteEnumShape(t *testing.T) {
	t.Parallel()
	schemas := map[string]map[string]any{
		"[]any":    {"enum": []any{"fast", "safe"}},
		"[]string": {"enum": []string{"fast", "safe"}},
	}
	for name, argument := range schemas {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			emitted := SupplyDerivedTypes(map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{"mode": argument},
				"required":             []string{"mode"},
			})
			mode := emitted["properties"].(map[string]any)["mode"].(map[string]any)
			if mode["type"] != "string" {
				t.Fatalf("emitted argument type = %#v, want \"string\": a provider enforcing the strict subset rejects a typeless node, and both shapes describe the same enum", mode["type"])
			}
			// Stating the type must leave the accepted set alone.
			if !schemaAccepts(t, emitted, map[string]any{"mode": "fast"}) {
				t.Fatal("the emitted schema rejects a member of the advertised set")
			}
			if schemaAccepts(t, emitted, map[string]any{"mode": "slow"}) {
				t.Fatal("the emitted schema accepts a value outside the advertised set")
			}
		})
	}
}

// A schema can sit under many keys, not only properties, items and the
// combinators. A provider requiring an explicit type rejects a typeless node
// anywhere it appears, and a tool carrying any of these keywords is refused
// strict mode, so the whole schema reaches the provider as advertised and the
// fallback walk is the only thing that can state the type. This covers the
// whole schema-bearing keyword set, so a missing position is a failing row
// rather than a review finding.
func TestSupplyDerivedTypesReachesEverySchemaPosition(t *testing.T) {
	t.Parallel()
	typeless := func(t *testing.T, m map[string]any, path ...string) map[string]any {
		t.Helper()
		for _, step := range path {
			next, ok := m[step].(map[string]any)
			if !ok {
				t.Fatalf("%s is not a schema: %#v", step, m[step])
			}
			m = next
		}
		return m
	}
	inner := func() map[string]any { return map[string]any{"enum": []any{"a", "b"}} }
	// accepts, when set, is an argument set the emitted schema must admit. It is
	// only meaningful for a keyword that constrains positively; a negating or
	// conditional keyword (not, if/then/else, propertyNames, contains) admits
	// values outside the inner set, so the type assertion is the check there.
	positions := []struct {
		name    string
		build   func(subschema map[string]any) map[string]any
		read    func(schema map[string]any) map[string]any
		accepts map[string]any
		rejects map[string]any
	}{
		{"additionalProperties", func(s map[string]any) map[string]any {
			return map[string]any{"type": "object", "additionalProperties": s}
		}, func(m map[string]any) map[string]any { return typeless(t, m, "additionalProperties") },
			map[string]any{"headers": map[string]any{"x-a": "a"}},
			map[string]any{"headers": map[string]any{"x-a": "c"}}},
		{"patternProperties", func(s map[string]any) map[string]any {
			return map[string]any{"type": "object", "patternProperties": map[string]any{"^x-": s}}
		}, func(m map[string]any) map[string]any {
			pp, _ := m["patternProperties"].(map[string]any)
			sub, _ := pp["^x-"].(map[string]any)
			return sub
		}, map[string]any{"headers": map[string]any{"x-a": "a"}},
			map[string]any{"headers": map[string]any{"x-a": "c"}}},
		{"not", func(s map[string]any) map[string]any {
			return map[string]any{"type": "object", "not": s}
		}, func(m map[string]any) map[string]any { return typeless(t, m, "not") }, nil, nil},
		{"$defs", func(s map[string]any) map[string]any {
			return map[string]any{"type": "object", "$defs": map[string]any{"D": s}}
		}, func(m map[string]any) map[string]any {
			defs, _ := m["$defs"].(map[string]any)
			sub, _ := defs["D"].(map[string]any)
			return sub
		}, nil, nil},
		{"definitions", func(s map[string]any) map[string]any {
			return map[string]any{"type": "object", "definitions": map[string]any{"D": s}}
		}, func(m map[string]any) map[string]any {
			defs, _ := m["definitions"].(map[string]any)
			sub, _ := defs["D"].(map[string]any)
			return sub
		}, nil, nil},
		{"contains", func(s map[string]any) map[string]any {
			return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "contains": s}
		}, func(m map[string]any) map[string]any { return typeless(t, m, "contains") }, nil, nil},
		{"propertyNames", func(s map[string]any) map[string]any {
			return map[string]any{"type": "object", "propertyNames": s}
		}, func(m map[string]any) map[string]any { return typeless(t, m, "propertyNames") }, nil, nil},
		{"dependentSchemas", func(s map[string]any) map[string]any {
			return map[string]any{"type": "object", "dependentSchemas": map[string]any{"D": s}}
		}, func(m map[string]any) map[string]any {
			ds, _ := m["dependentSchemas"].(map[string]any)
			sub, _ := ds["D"].(map[string]any)
			return sub
		}, nil, nil},
		{"if", func(s map[string]any) map[string]any {
			return map[string]any{"type": "object", "if": s}
		}, func(m map[string]any) map[string]any { return typeless(t, m, "if") }, nil, nil},
		{"then", func(s map[string]any) map[string]any {
			return map[string]any{"type": "object", "then": s}
		}, func(m map[string]any) map[string]any { return typeless(t, m, "then") }, nil, nil},
		{"else", func(s map[string]any) map[string]any {
			return map[string]any{"type": "object", "else": s}
		}, func(m map[string]any) map[string]any { return typeless(t, m, "else") }, nil, nil},
		{"prefixItems", func(s map[string]any) map[string]any {
			return map[string]any{"type": "array", "prefixItems": []any{s}}
		}, func(m map[string]any) map[string]any {
			arr, _ := m["prefixItems"].([]any)
			sub, _ := arr[0].(map[string]any)
			return sub
		}, nil, nil},
		{"unevaluatedProperties", func(s map[string]any) map[string]any {
			return map[string]any{"type": "object", "unevaluatedProperties": s}
		}, func(m map[string]any) map[string]any { return typeless(t, m, "unevaluatedProperties") }, nil, nil},
		{"unevaluatedItems", func(s map[string]any) map[string]any {
			return map[string]any{"type": "array", "unevaluatedItems": s}
		}, func(m map[string]any) map[string]any { return typeless(t, m, "unevaluatedItems") }, nil, nil},
	}
	for _, pos := range positions {
		t.Run(pos.name, func(t *testing.T) {
			t.Parallel()
			emitted := SupplyDerivedTypes(map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{"headers": pos.build(inner())},
				"required":             []string{"headers"},
			})
			headers := emitted["properties"].(map[string]any)["headers"].(map[string]any)
			sub := pos.read(headers)
			if sub == nil {
				t.Fatalf("the advertised subschema under %s is gone: %#v", pos.name, headers)
			}
			if sub["type"] != "string" {
				t.Fatalf("the subschema under %s has type %#v, want \"string\": an unvisited node stays typeless and the provider rejects the request", pos.name, sub["type"])
			}
			// Stating the type must not change what the keyword accepts.
			if pos.accepts != nil && !schemaAccepts(t, emitted, pos.accepts) {
				t.Fatalf("the emitted schema rejects an argument set the advertised one accepts: %#v", pos.accepts)
			}
			if pos.rejects != nil && schemaAccepts(t, emitted, pos.rejects) {
				t.Fatalf("the emitted schema accepts an argument set the advertised one rejects: %#v", pos.rejects)
			}
		})
	}
}

// A property named after a keyword must not be read as that keyword. properties
// is a map of names to schemas, so a property called "enum" says nothing about
// its parent's values. Walking the name map as if it were a schema would read
// that name as a keyword and change what the parent accepts.
func TestSupplyDerivedTypesDoesNotReadAPropertyNameAsAKeyword(t *testing.T) {
	t.Parallel()
	emitted := SupplyDerivedTypes(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"enum":   map[string]any{"type": "string"},
			"not":    map[string]any{"type": "string"},
			"items":  map[string]any{"type": "string"},
			"$defs":  map[string]any{"type": "string"},
			"then":   map[string]any{"type": "string"},
			"prefix": map[string]any{"type": "string"},
		},
		"required": []string{"enum", "not", "items", "$defs", "then", "prefix"},
	})
	// The root carries no value set, so no type is invented for it.
	if _, has := emitted["type"]; !has {
		t.Fatal("the root lost its declared type")
	}
	for _, name := range []string{"enum", "not", "items", "$defs", "then", "prefix"} {
		sub, ok := emitted["properties"].(map[string]any)[name].(map[string]any)
		if !ok {
			t.Fatalf("property %q is gone", name)
		}
		if sub["type"] != "string" {
			t.Fatalf("property %q has type %#v, want the declared string", name, sub["type"])
		}
	}
	args := map[string]any{"enum": "e", "not": "n", "items": "i", "$defs": "d", "then": "t", "prefix": "p"}
	if !schemaAccepts(t, emitted, args) {
		t.Fatal("the emitted schema rejects the argument set the advertised one accepts")
	}
}

// An enum written as a typed Go slice and the same enum written as a decoded
// []any serialize identically, so they must receive the same explicit type.
// Reading only the decoded spelling makes the emitted schema depend on the
// caller's Go type: a builder that writes []int{0, 1} would leave the node
// typeless and cost the whole request.
func TestSupplyDerivedTypesReadsEveryTypedEnumSlice(t *testing.T) {
	t.Parallel()
	schemas := map[string]map[string]any{
		"[]any":     {"enum": []any{0, 1}},
		"[]int":     {"enum": []int{0, 1}},
		"[]int64":   {"enum": []int64{0, 1}},
		"[]float64": {"enum": []float64{0, 1}},
		"strings":   {"enum": []string{"fast", "safe"}},
		"bools":     {"enum": []bool{true, false}},
	}
	for name, argument := range schemas {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			want := "number"
			if name == "strings" {
				want = "string"
			}
			if name == "bools" {
				want = "boolean"
			}
			emitted := SupplyDerivedTypes(map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{"mode": argument},
				"required":             []string{"mode"},
			})
			mode := emitted["properties"].(map[string]any)["mode"].(map[string]any)
			if mode["type"] != want {
				t.Fatalf("emitted argument type = %#v, want %q: the same JSON enum must not depend on the Go type it was written with", mode["type"], want)
			}
			// The members are untouched.
			if _, has := mode["enum"]; !has {
				t.Fatal("the enum members were dropped")
			}
		})
	}
}

// A heterogeneous typed slice is still not nameable by one type, and a typed
// slice of containers is not a scalar set. Both must keep declining.
func TestTypedEnumSlicesStillDeclineWhatTheyCannotName(t *testing.T) {
	t.Parallel()
	for name, argument := range map[string]map[string]any{
		"mixed typed": {"enum": []any{"a", 1}},
		"containers":  {"enum": []any{map[string]any{"a": "b"}}},
		"nil members": {"enum": []any{nil}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			emitted := SupplyDerivedTypes(map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{"mode": argument},
				"required":             []string{"mode"},
			})
			mode := emitted["properties"].(map[string]any)["mode"].(map[string]any)
			if _, has := mode["type"]; has {
				t.Fatalf("type = %#v, want none: this set has no single scalar type", mode["type"])
			}
		})
	}
}

// items is legal on a node of any type, and a strict-enforcing provider rejects
// a typeless node wherever it sits. A closed, fully required object that also
// carries an items subschema must not be certified unless that subschema is
// itself verifiable; an unverifiable one sends the tool non-strict with its
// advertised schema instead of certifying a node nobody checked.
func TestItemsOnAnObjectNodeIsChecked(t *testing.T) {
	t.Parallel()
	// An items that names a readable type is strict-valid on its own terms, so
	// the object stays certifiable and nothing is lost.
	verifiable := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{"a": map[string]any{"type": "string"}},
		"required":             []string{"a"},
		"items":                map[string]any{"type": "string"},
	}
	if !StrictExpressible(verifiable) {
		t.Fatal("an items naming a readable type must not refuse the schema: it is strict-valid where it sits")
	}
	// An empty items constrains nothing, so it cannot be certified.
	emptyItems := deepCopyAny(verifiable).(map[string]any)
	emptyItems["items"] = map[string]any{}
	if StrictExpressible(emptyItems) {
		t.Fatal("an object carrying an empty items subschema must not be certified: the node is typeless and the rewrite never visits it")
	}
	// Neither can one whose subschema is a typeless node.
	typelessItems := deepCopyAny(verifiable).(map[string]any)
	typelessItems["items"] = map[string]any{"description": "free-form"}
	if StrictExpressible(typelessItems) {
		t.Fatal("an object carrying an annotation-only items subschema must not be certified")
	}
}

// const arrived in draft 6. A tool that declares an older dialect and writes
// {"const": "fixed"} accepts every value there, because the keyword asserts
// nothing; stating a type would make the emitted schema reject values the
// advertised schema accepted, which is the one thing the supply must never do.
func TestConstIsOnlyReadInADialectThatAssertsIt(t *testing.T) {
	t.Parallel()
	fixed := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{"payload": map[string]any{"const": "fixed"}},
		"required":             []string{"payload"},
	}
	for _, tc := range []struct {
		name      string
		dialect   string
		wantTyped bool
	}{
		{"draft 4", "http://json-schema.org/draft-04/schema#", false},
		{"draft 7", "http://json-schema.org/draft-07/schema#", true},
		{"draft 2020-12", "https://json-schema.org/draft/2020-12/schema", true},
		{"undeclared", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			schema := deepCopyAny(fixed).(map[string]any)
			if tc.dialect != "" {
				schema["$schema"] = tc.dialect
			}
			emitted := SupplyDerivedTypes(schema)
			payload := emitted["properties"].(map[string]any)["payload"].(map[string]any)
			_, typed := payload["type"]
			if typed != tc.wantTyped {
				t.Fatalf("type present = %v, want %v under %q: const asserts nothing before draft 6, and stating a type there would reject values the advertised schema accepts", typed, tc.wantTyped, tc.dialect)
			}
			// The members are untouched either way.
			if payload["const"] != "fixed" {
				t.Fatalf("const = %#v, want the advertised value", payload["const"])
			}
			// And every value the advertised schema accepts is still accepted.
			if !schemaAccepts(t, emitted, map[string]any{"payload": "fixed"}) {
				t.Fatal("the emitted schema rejects the value the advertised schema accepts")
			}
		})
	}
}

// enum asserts in every dialect, so it is read regardless of the declaration.
// This is the control for the const test: the dialect question is about the
// keyword, not about the schema.
func TestEnumIsReadInEveryDialect(t *testing.T) {
	t.Parallel()
	for _, dialect := range []string{"", "http://json-schema.org/draft-04/schema#"} {
		schema := map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           map[string]any{"payload": map[string]any{"enum": []any{"a", "b"}}},
			"required":             []string{"payload"},
		}
		if dialect != "" {
			schema["$schema"] = dialect
		}
		emitted := SupplyDerivedTypes(schema)
		payload := emitted["properties"].(map[string]any)["payload"].(map[string]any)
		if payload["type"] != "string" {
			t.Fatalf("enum under %q has type %#v, want \"string\": enum is an assertion in every dialect", dialect, payload["type"])
		}
	}
}

// A value the encoder renders as JSON need not be a Go slice of the members it
// renders. json.RawMessage is a []byte, so reading its elements as numbers
// would state type "number" beside a JSON array of strings and leave a schema
// accepting nothing the advertised one accepted. Inference has to read what the
// encoder sends, not the Go spelling.
func TestSupplyDerivedTypesReadsTheJSONAnEnumEncodes(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		enum any
		want string
		args string
	}{
		"json.RawMessage array of strings": {
			enum: json.RawMessage(`["fast","safe"]`),
			want: "string",
			args: `{"mode":"fast"}`,
		},
		"json.RawMessage array of numbers": {
			enum: json.RawMessage(`[1,2]`),
			want: "number",
			args: `{"mode":1}`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			emitted := SupplyDerivedTypes(map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{"mode": map[string]any{"enum": tc.enum}},
				"required":             []string{"mode"},
			})
			mode := emitted["properties"].(map[string]any)["mode"].(map[string]any)
			if mode["type"] != tc.want {
				t.Fatalf("emitted argument type = %#v, want %q: the type must describe the JSON the encoder sends, not the Go element type", mode["type"], tc.want)
			}
			var args map[string]any
			if err := json.Unmarshal([]byte(tc.args), &args); err != nil {
				t.Fatalf("build args: %v", err)
			}
			if !schemaAccepts(t, emitted, args) {
				t.Fatalf("the emitted schema rejects %s, a member the advertised enum lists", tc.args)
			}
		})
	}
}

// properties reaches this package either as the decoded map[string]any or as a
// Go builder's map[string]map[string]any. The walk must reach the children of
// both, or the same typeless enum is supplied a type on one path and left
// typeless on the other — and a provider enforcing the strict subset rejects
// the whole request over the one it misses.
func TestSupplyDerivedTypesWalksTypedPropertyMaps(t *testing.T) {
	t.Parallel()
	typed := map[string]map[string]any{
		"mode": {"enum": []string{"fast", "safe"}},
	}
	emitted := SupplyDerivedTypes(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           typed,
		"required":             []string{"mode"},
	})
	props, ok := emitted["properties"].(map[string]any)
	if !ok {
		t.Fatalf("emitted properties = %T, want a map the walk can carry the supplied type in", emitted["properties"])
	}
	mode, ok := props["mode"].(map[string]any)
	if !ok {
		t.Fatalf("emitted argument = %T, want a copied schema", props["mode"])
	}
	if got := mode["type"]; got != "string" {
		t.Fatalf("emitted argument type = %#v, want \"string\": a typed properties map must be walked like a decoded one", got)
	}
	if _, mutated := typed["mode"]["type"]; mutated {
		t.Fatal("the supply wrote through the caller's map instead of copying the child first")
	}
	if !schemaAccepts(t, emitted, map[string]any{"mode": "fast"}) {
		t.Fatal("the emitted schema rejects a member of the advertised set")
	}
	if schemaAccepts(t, emitted, map[string]any{"mode": "slow"}) {
		t.Fatal("the emitted schema accepts a value outside the advertised set")
	}
}

// $schema may be declared again at an embedded schema resource, and it governs
// the keywords below that node. A const under a draft-4 resource asserts
// nothing: it accepts every value there, so the outer dialect must not state a
// type for it.
func TestSupplyDerivedTypesReadsAnEmbeddedDialect(t *testing.T) {
	t.Parallel()
	const draft4 = "http://json-schema.org/draft-04/schema#"
	emitted := SupplyDerivedTypes(map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"payload": map[string]any{
				"$schema": draft4,
				"const":   "fixed",
			},
		},
		"required": []string{"payload"},
	})
	payload := emitted["properties"].(map[string]any)["payload"].(map[string]any)
	if _, has := payload["type"]; has {
		t.Fatalf("emitted argument type = %#v, want none: const asserts nothing under draft 4, so stating a type would reject values the advertised schema accepts", payload["type"])
	}
	if payload["const"] != "fixed" {
		t.Fatalf("emitted const = %#v, want it preserved", payload["const"])
	}
}

// stringlyInt is a named integer type whose encoder emits a JSON string, the
// shape a builder reaches for when it wants a readable name on the wire. Its Go
// kind says number and its encoder says string, so a classification by kind
// states "number" beside a set of string members and no advertised member
// survives the projection.
type stringlyInt int

const (
	stringlyFast stringlyInt = iota
	stringlySafe
)

func (s stringlyInt) MarshalJSON() ([]byte, error) {
	if s == stringlyFast {
		return []byte(`"fast"`), nil
	}
	return []byte(`"safe"`), nil
}

// A named integer type that encodes as a string is still a string on the wire,
// and the type stated beside an enum has to describe the members the encoder
// sends. Classifying by Go kind states "number" for an enum of strings, so the
// emitted schema rejects every member the advertised one lists.
func TestSupplyDerivedTypesReadsANamedTypeThroughItsEncoder(t *testing.T) {
	t.Parallel()
	t.Run("enum of a string-encoding named int", func(t *testing.T) {
		t.Parallel()
		emitted := SupplyDerivedTypes(map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"mode": map[string]any{"enum": []stringlyInt{stringlyFast, stringlySafe}},
			},
			"required": []string{"mode"},
		})
		mode := emitted["properties"].(map[string]any)["mode"].(map[string]any)
		if mode["type"] != "string" {
			t.Fatalf("emitted type = %#v, want \"string\": the type must describe the JSON the encoder sends, not the Go kind", mode["type"])
		}
		if !schemaAccepts(t, emitted, map[string]any{"mode": "fast"}) {
			t.Fatal("the emitted schema rejects a member the advertised enum lists")
		}
		if !schemaAccepts(t, emitted, map[string]any{"mode": "safe"}) {
			t.Fatal("the emitted schema rejects a member the advertised enum lists")
		}
	})
	t.Run("const of a string-encoding named int", func(t *testing.T) {
		t.Parallel()
		emitted := SupplyDerivedTypes(map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"payload": map[string]any{"const": stringlyFast},
			},
			"required": []string{"payload"},
		})
		payload := emitted["properties"].(map[string]any)["payload"].(map[string]any)
		if payload["type"] != "string" {
			t.Fatalf("emitted type = %#v, want \"string\": the same rule decides const and enum", payload["type"])
		}
		if !schemaAccepts(t, emitted, map[string]any{"payload": "fast"}) {
			t.Fatal("the emitted schema rejects the value the advertised const names")
		}
	})
}

// A Go builder writes a subschema list as []map[string]any, and the walk only
// accepted []any, so every branch of such a list was skipped: a typeless const
// inside it reached the provider unchanged and cost the whole request.
func TestSupplyDerivedTypesWalksTypedSubschemaLists(t *testing.T) {
	t.Parallel()
	typed := []map[string]any{
		{"const": "fast"},
		{"const": "safe"},
	}
	emitted := SupplyDerivedTypes(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{"mode": map[string]any{"anyOf": typed}},
		"required":             []string{"mode"},
	})
	mode := emitted["properties"].(map[string]any)["mode"].(map[string]any)
	branches, ok := mode["anyOf"].([]any)
	if !ok {
		t.Fatalf("emitted anyOf = %T, want the branches preserved as a walkable list", mode["anyOf"])
	}
	if len(branches) != 2 {
		t.Fatalf("emitted anyOf has %d branches, want 2: the walk must not drop a branch", len(branches))
	}
	for i, want := range []string{"fast", "safe"} {
		branch := branches[i].(map[string]any)
		if branch["type"] != "string" {
			t.Fatalf("branch %d type = %#v, want \"string\": a typed subschema list must be walked like a decoded one", i, branch["type"])
		}
		if branch["const"] != want {
			t.Fatalf("branch %d const = %#v, want %q: the accepted values must be preserved", i, branch["const"], want)
		}
	}
	if _, mutated := typed[0]["type"]; mutated {
		t.Fatal("the supply wrote through the caller's slice instead of copying the branch first")
	}
	if !schemaAccepts(t, emitted, map[string]any{"mode": "fast"}) {
		t.Fatal("the emitted schema rejects a branch value the advertised schema accepts")
	}
}

// A typed nil pointer to a value-receiver encoder is what the request carries
// as JSON null. (*json.RawMessage)(nil) is the shape: the pointer's method set
// includes RawMessage's value-receiver MarshalJSON, so calling the method
// dereferences a nil receiver and panics while building the request. The
// encoder emits null for that same value without calling the method at all.
//
// Both halves are asserted, because either one alone is satisfiable by the
// wrong change: the type must be inferred from the encoded null rather than
// from the pointer, and a set whose only member is null still names no scalar,
// so the supply has to decline it instead of panicking or stating a type.
func TestTypedNilPointerEncoderDoesNotPanicInInference(t *testing.T) {
	t.Parallel()
	var nilRaw *json.RawMessage
	var nilTime *time.Time

	// The panic itself: every entry point that reads a value reaches
	// scalarJSONType, so both are driven here.
	for name, member := range map[string]any{
		"*json.RawMessage": nilRaw,
		"*time.Time":       nilTime,
	} {
		t.Run(name+" declines without panicking", func(t *testing.T) {
			t.Parallel()
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("inference panicked on a typed nil pointer: %v", r)
				}
			}()
			emitted := SupplyDerivedTypes(map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{"mode": map[string]any{"enum": []any{member}}},
				"required":             []string{"mode"},
			})
			// A set whose every member is null has no scalar type to state,
			// so nothing may be added: stating one would reject the null the
			// schema accepts.
			mode := emitted["properties"].(map[string]any)["mode"].(map[string]any)
			if _, has := mode["type"]; has {
				t.Fatalf("emitted type = %#v for an all-null set: a null member names no scalar", mode["type"])
			}
			if !schemaAccepts(t, emitted, map[string]any{"mode": nil}) {
				t.Fatal("the emitted schema rejects the null member the advertised one accepts")
			}
		})
	}

	t.Run("a null beside a string leaves the set unnameable", func(t *testing.T) {
		t.Parallel()
		emitted := SupplyDerivedTypes(map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"mode": map[string]any{"enum": []any{"fast", nilRaw}},
			},
			"required": []string{"mode"},
		})
		mode := emitted["properties"].(map[string]any)["mode"].(map[string]any)
		if _, has := mode["type"]; has {
			t.Fatalf("emitted type = %#v for a set containing null: one type cannot name null and a string", mode["type"])
		}
	})

	// const reaches scalarJSONType without passing through the enum reader,
	// which is the shape the panic was reported on: a typeless const whose
	// value is a typed nil pointer to a value-receiver encoder.
	//
	// Do not rewrite this as an enum test. enumMembers decodes the whole enum
	// through the request encoder before scalarJSONType sees any member, so a
	// nil pointer arrives there already normalised to null and the test would
	// pass against a scalarJSONType that panics. This subtest is the only one
	// that reaches the bug, and it is separate for that reason.
	for name, member := range map[string]any{
		"*json.RawMessage": nilRaw,
		"*time.Time":       nilTime,
	} {
		t.Run("const of "+name+" declines without panicking", func(t *testing.T) {
			t.Parallel()
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("inference panicked on a typed nil const: %v", r)
				}
			}()
			emitted := SupplyDerivedTypes(map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{"mode": map[string]any{"const": member}},
				"required":             []string{"mode"},
			})
			mode := emitted["properties"].(map[string]any)["mode"].(map[string]any)
			if _, has := mode["type"]; has {
				t.Fatalf("emitted type = %#v for a null const: null names no scalar", mode["type"])
			}
		})
	}
}

// A named integer type with a pointer-receiver encoder is encoded on the wire
// through that method when it appears as a slice element: a slice element is
// addressable, so the encoder reaches *T's method set even though the slice
// holds T values. Reading each element out into an interface and classifying it
// drops the method, states "number" beside a set of strings, and leaves a
// schema that accepts none of its own members.
//
// The control is the same set spelled []pointerlyInt: if the type is read from
// the encoder, the slice of values and the slice of pointers agree.
type pointerlyInt int

const (
	pointerlyFast pointerlyInt = iota
	pointerlySafe
)

func (p *pointerlyInt) MarshalJSON() ([]byte, error) {
	if *p == pointerlyFast {
		return []byte(`"fast"`), nil
	}
	return []byte(`"safe"`), nil
}

func TestEnumReadsAPointerReceiverEncoderPerElement(t *testing.T) {
	t.Parallel()
	for name, enum := range map[string]any{
		"slice of values":   []pointerlyInt{pointerlyFast, pointerlySafe},
		"slice of pointers": []*pointerlyInt{ptr(pointerlyFast), ptr(pointerlySafe)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			emitted := SupplyDerivedTypes(map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{"mode": map[string]any{"enum": enum}},
				"required":             []string{"mode"},
			})
			mode := emitted["properties"].(map[string]any)["mode"].(map[string]any)
			if mode["type"] != "string" {
				t.Fatalf("emitted type = %#v, want \"string\": the encoder sends strings, and it is the encoder that decides", mode["type"])
			}
			// The members are the ones the encoder wrote, so both survive.
			for _, want := range []string{"fast", "safe"} {
				if !schemaAccepts(t, emitted, map[string]any{"mode": want}) {
					t.Fatalf("the emitted schema rejects %q, a member the advertised enum lists", want)
				}
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

// properties constrains object instances only, and the rewrite closes only a
// node whose type is the literal "object". On an array node the keyword reaches
// the provider as advertised with its children unwalked, so a child that states
// no type would be sent into a strict-enforcing provider — the same explicit-
// type rejection this gate exists to prevent, reached through the shape it did
// not check.
func TestPropertiesOnANonObjectNodeDeclinesStrict(t *testing.T) {
	t.Parallel()
	arrays := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"tags": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
				// Legal on an array node: it constrains the object instances
				// among the array's elements, not the elements themselves.
				"properties": map[string]any{"label": map[string]any{"description": "untyped"}},
			},
		},
		"required": []string{"tags"},
	}
	if StrictExpressible(arrays) {
		t.Fatal("an array node carrying an unchecked, typeless properties child must not be certified strict")
	}
	// The control: the same array without the stray keyword still certifies,
	// so the refusal is about the unchecked child rather than about arrays.
	clean := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"tags": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"required": []string{"tags"},
	}
	if !StrictExpressible(clean) {
		t.Fatal("an array whose items are typed must still be certifiable")
	}
}

// A recursive schema graph is something a Go caller can build by linking a child
// back to an ancestor through a typed properties map. Before the typed copy arms
// existed that map reached encoding/json, which returned a cycle error. Copying
// it recursively instead would recurse until the stack is gone, which takes the
// process rather than the request.
//
// The cut is asserted three ways, because a guard that is too broad is as wrong
// as none: the call must return, the looping edge must become the empty schema,
// and the sibling subtree that shares a node with a different ancestor must keep
// both of its copies. Only a node containing itself may be cut.
func TestRecursiveSchemaIsCopiedWithoutUnboundedRecursion(t *testing.T) {
	t.Parallel()
	t.Run("self-referential properties", func(t *testing.T) {
		t.Parallel()
		child := map[string]any{"type": "string"}
		props := map[string]any{}
		schema := map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           props,
			"required":             []string{"self"},
		}
		props["self"] = schema
		child["description"] = "unrelated"
		done := make(chan map[string]any, 1)
		go func() { done <- SupplyDerivedTypes(schema) }()
		select {
		case out := <-done:
			self, ok := out["properties"].(map[string]any)["self"].(map[string]any)
			if !ok {
				t.Fatalf("properties.self = %#v, want a schema", out["properties"].(map[string]any)["self"])
			}
			if len(self) != 0 {
				t.Fatalf("the looping edge = %#v, want the empty schema: the cut must not keep a partially copied cycle", self)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("copying a self-referential schema did not return: the recursion is unbounded")
		}
	})

	t.Run("a shared acyclic subtree keeps both copies", func(t *testing.T) {
		t.Parallel()
		shared := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}, "required": []string{}}
		schema := map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"left":  shared,
				"right": shared,
			},
			"required": []string{"left", "right"},
		}
		out := SupplyDerivedTypes(schema)
		props := out["properties"].(map[string]any)
		for _, name := range []string{"left", "right"} {
			node, ok := props[name].(map[string]any)
			if !ok || node["type"] != "object" {
				t.Fatalf("properties.%s = %#v, want the shared schema: a node with two parents is not a cycle", name, props[name])
			}
		}
	})
}

// A caller may write a property schema as raw JSON. The walk branches on
// map[string]any, so a raw child used to be skipped entirely while the decoded
// spelling of the same schema got the explicit type a provider requires. Which
// Go type the caller chose then decided whether the tool's request survived,
// which is the same "depends on how it was spelled" defect the encoder rule
// removed one layer up.
func TestRawJSONSubschemaIsReadLikeADecodedOne(t *testing.T) {
	t.Parallel()
	decoded := map[string]any{"enum": []any{"fast", "safe"}}
	for name, child := range map[string]any{
		"decoded":     decoded,
		"raw message": json.RawMessage(`{"enum":["fast","safe"]}`),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			emitted := SupplyDerivedTypes(map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{"mode": child},
				"required":             []string{"mode"},
			})
			mode, ok := emitted["properties"].(map[string]any)["mode"].(map[string]any)
			if !ok {
				t.Fatalf("emitted argument = %#v, want a schema: a raw subschema must be read, not passed through", emitted["properties"].(map[string]any)["mode"])
			}
			if mode["type"] != "string" {
				t.Fatalf("emitted type = %#v, want \"string\": the raw and decoded spellings describe the same schema", mode["type"])
			}
			if !schemaAccepts(t, emitted, map[string]any{"mode": "fast"}) {
				t.Fatal("the emitted schema rejects a member the advertised enum lists")
			}
		})
	}
}

// additionalProperties may hold a schema rather than a boolean, and it is legal
// on a node of any type. The rewrite reads the keyword only to write false on an
// object it closes, so a schema value reaches the provider as advertised: a
// nested object with an optional property behind it is validated by strict
// enforcement and can reject the whole request, while the gate never looked.
//
// This is the same shape as properties on a non-object node one level deeper, so
// it is refused on the same rule: the walk does not skip a schema-bearing
// keyword because the node's declared type suggests it will not be there.
func TestSchemaValuedAdditionalPropertiesIsWalkedOnEveryNode(t *testing.T) {
	t.Parallel()
	// A scalar argument whose additionalProperties holds an object that leaves
	// its own property optional. Nothing about the root is unusual, so only the
	// nested keyword can decide the answer.
	unchecked := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"payload": map[string]any{
				"type": "string",
				"additionalProperties": map[string]any{
					"type":       "object",
					"properties": map[string]any{"label": map[string]any{"type": "string"}},
				},
			},
		},
		"required": []string{"payload"},
	}
	if StrictExpressible(unchecked) {
		t.Fatal("a schema-valued additionalProperties holding an object with an optional property must not be certified strict")
	}

	// The control: the same node with a boolean additionalProperties certifies,
	// so the refusal is about the unchecked subschema and not about the keyword.
	checked := deepCopyAny(unchecked).(map[string]any)
	checked["properties"].(map[string]any)["payload"].(map[string]any)["additionalProperties"] = false
	if !StrictExpressible(checked) {
		t.Fatal("a boolean additionalProperties must still be certifiable")
	}

	// And a schema value that is fully specified certifies too, because the walk
	// read it rather than refusing the keyword outright.
	closed := deepCopyAny(unchecked).(map[string]any)
	closed["properties"].(map[string]any)["payload"].(map[string]any)["additionalProperties"] = map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{"label": map[string]any{"type": "string"}},
		"required":             []string{"label"},
	}
	if !StrictExpressible(closed) {
		t.Fatal("a schema-valued additionalProperties that is fully specified must be certifiable")
	}
}
