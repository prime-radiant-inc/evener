package repair

import (
	"maps"
	"strings"
	"testing"
)

// Issue #622 review follow-ups: ambiguity detection must fail closed for
// property-map, array-item, accept-all, and typed-map sibling arms, and for
// constraints nested past the recursion bound.

// A sibling arm whose additionalProperties is boolean false forbids the failing
// property, so the failing arm's enum list is not globally accepted.
func TestExplainSchemaError_BooleanAdditionalPropertiesSiblingIsAmbiguous(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "green", "blue"}},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"red"}}}},
			map[string]any{"additionalProperties": false},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "green"}, "x", "/oneOf/0/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("boolean additionalProperties sibling not treated as ambiguous: %q", msg)
	}
}

// A sibling arm whose items is boolean false rejects every array element, so it
// constrains the item path and the failing arm's item enum is not global.
func TestExplainSchemaError_BooleanItemsSiblingIsAmbiguous(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"xs":   map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"a", "b"}}},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"xs": map[string]any{"items": map[string]any{"enum": []string{"a"}}}}},
			map[string]any{"properties": map[string]any{"xs": map[string]any{"items": false}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "xs": []any{"b"}}, "xs/0", "/oneOf/0/properties/xs/items/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("boolean items sibling not treated as ambiguous: %q", msg)
	}
}

// A boolean `true` sibling arm accepts every instance, so it overlaps the failing
// arm and the failing arm's enum list is not the globally accepted set.
func TestExplainSchemaError_AcceptAllSiblingArmIsAmbiguous(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "green", "blue"}},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"red"}}}},
			true,
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "green"}, "x", "/oneOf/0/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("accept-all sibling arm not treated as ambiguous: %q", msg)
	}
}

// Typed-map sibling arms (map[string]map[string]any) must be normalized like
// plain maps so an overlapping sibling is not silently dropped.
func TestExplainSchemaError_TypedMapSiblingArmsAreAmbiguous(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]map[string]any{
			"task": {"type": "string"},
			"x":    {"type": "string", "enum": []any{"red", "green", "blue"}},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"red"}}}},
			map[string]map[string]any{"properties": {"x": map[string]any{"enum": []string{"red", "blue"}}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "green"}, "x", "/oneOf/0/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("typed-map sibling arms not treated as ambiguous: %q", msg)
	}
}

// nestedAllOfEnum builds a schema with the enum buried under n nested allOf
// wrappers, so resolving it requires descending past the recursion bound.
func nestedAllOfEnum(n int) map[string]any {
	node := map[string]any{"type": "string", "enum": []string{"red"}}
	for range n {
		node = map[string]any{"allOf": []any{node}}
	}
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"x": node},
	}
}

// A constraint nested past the resolver's recursion bound must fail closed: the
// bounded walk reports it unmodeled rather than dropping it and advertising the
// top-level list.
func TestExplainSchemaError_DeepNestingFailsClosed(t *testing.T) {
	params := nestedAllOfEnum(12)
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"x": "green"}, "x", "/properties/x/allOf/0/allOf/0/allOf/0/allOf/0/allOf/0/allOf/0/allOf/0/allOf/0/allOf/0/allOf/0/allOf/0/allOf/0/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("constraint nested past the recursion bound advertised a value: %q", msg)
	}
}

// A sibling arm whose `not` is the empty schema (`not: {}`) rejects every
// instance, so it constrains the failing path and the failing arm's enum list is
// not globally accepted.
func TestExplainSchemaError_NotEmptySiblingIsAmbiguous(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "green", "blue"}},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"red"}}}},
			map[string]any{"not": map[string]any{}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "green"}, "x", "/oneOf/0/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("not:{} sibling not treated as ambiguous: %q", msg)
	}
}

// A scalar limit beside an unresolved `$ref` must not be reported: the reference
// is an unevaluated applicator that may be the failing arm with its own limit, so
// the merged top-level limit is not provably the one that failed. Here the value
// (12 characters) does not even violate the top-level maxLength (20), so naming
// it is a false claim (issue #622 review).
func TestExplainSchemaError_UnresolvedRefScalarConstraintIsNotLeaked(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"x": map[string]any{
				"type":      "string",
				"maxLength": 20,
				"oneOf": []any{
					map[string]any{"$ref": "https://example.com/schemas#/$defs/limited"},
				},
			},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"x": "abcdefghijkl"}, "x", "/properties/x/maxLength")
	if strings.Contains(msg, "maxLength") {
		t.Fatalf("unresolved $ref leaked the top-level maxLength: %q", msg)
	}
}

// The suppression is scoped to unresolved references: a fully modeled applicator
// (here an allOf arm this resolver can evaluate) does not hide a different limit,
// so the directly named scalar constraint is still reported (issue #622 review).
func TestExplainSchemaError_ModeledApplicatorKeepsScalarConstraint(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"x": map[string]any{
				"type":      "string",
				"maxLength": 20,
				"allOf":     []any{map[string]any{"type": "string"}},
			},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"x": "abcdefghijklmnopqrstuv"}, "x", "/properties/x/maxLength")
	if !strings.Contains(msg, "maxLength (20)") {
		t.Fatalf("modeled applicator suppressed the direct scalar constraint: %q", msg)
	}
}

// not/if/then/else hold a single schema (or a boolean), never a list, so the
// unresolved-reference scan must read them directly: a top-level scalar limit
// beside a `$ref` under one of them may not be the constraint that failed
// (issue #622 review).
func TestExplainSchemaError_SingleSchemaApplicatorUnresolvedRefSuppressesScalarConstraint(t *testing.T) {
	for name, applicator := range map[string]map[string]any{
		"not-ref":  {"not": map[string]any{"$ref": "https://example.com/schemas#/$defs/forbidden"}},
		"then-ref": {"if": map[string]any{"type": "string"}, "then": map[string]any{"$ref": "https://example.com/schemas#/$defs/limited"}},
		"else-ref": {"if": map[string]any{"type": "string"}, "else": map[string]any{"$ref": "https://example.com/schemas#/$defs/limited"}},
		// `not: true` rejects every instance, so the scalar limit is not the
		// constraint that failed.
		"not-true": {"not": true},
	} {
		t.Run(name, func(t *testing.T) {
			field := map[string]any{"type": "string", "maxLength": 20}
			maps.Copy(field, applicator)
			params := map[string]any{"type": "object", "properties": map[string]any{"x": field}}
			msg := ExplainSchemaError("probe_tool", params, map[string]any{"x": "abcdefghijkl"}, "x", "/properties/x/maxLength")
			if strings.Contains(msg, "maxLength") {
				t.Fatalf("single-schema applicator %q leaked the top-level maxLength: %q", name, msg)
			}
		})
	}
}

// `not: false` imposes nothing (false matches no instance), so the directly
// named scalar limit is still the failing constraint and must be reported.
func TestExplainSchemaError_HarmlessBooleanApplicatorKeepsScalarConstraint(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"x": map[string]any{"type": "string", "maxLength": 20, "not": false},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"x": "abcdefghijklmnopqrstuv"}, "x", "/properties/x/maxLength")
	if !strings.Contains(msg, "maxLength (20)") {
		t.Fatalf("harmless boolean applicator suppressed the direct scalar constraint: %q", msg)
	}
}

// A boolean-false dependent schema rejects whenever its trigger property is
// present, which the resolver cannot rule out, so the merged scalar limit is
// not provably the failing constraint (issue #622 review).
func TestExplainSchemaError_FalseDependentSchemaSuppressesScalarConstraint(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"x": map[string]any{
				"type":             "string",
				"maxLength":        20,
				"dependentSchemas": map[string]any{"x": false},
			},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"x": "abcdefghijkl"}, "x", "/properties/x/maxLength")
	if strings.Contains(msg, "maxLength") {
		t.Fatalf("false dependent schema leaked the top-level maxLength: %q", msg)
	}
}

// A required child declared with a union the default string placeholder does
// not satisfy must render a placeholder the declaration admits: {"type":
// ["array","null"]} must not be shown as the string "..." that the schema
// rejects, or the guidance would coach a retry that fails validation (issue
// #622 review).
func TestExplainSchemaError_UnionTypedRequiredChildRendersAdmittedType(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"output": map[string]any{
				"type":     "object",
				"required": []string{"items"},
				"properties": map[string]any{
					"items": map[string]any{"type": []any{"array", "null"}},
				},
			},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"output": map[string]any{}}, "output", "/properties/output/required")
	if strings.Contains(msg, `"items": "..."`) {
		t.Fatalf("union-typed required child rendered an invalid Example: %q", msg)
	}
	if !strings.Contains(msg, `"items": []`) {
		t.Fatalf("union-typed required child did not render its admitted array type: %q", msg)
	}
	if !strings.Contains(msg, "output.items") {
		t.Fatalf("missing-key guidance was lost: %q", msg)
	}
}

// The field's own example has the same constraint: a declared union must render
// a placeholder it admits rather than the string "..." it rejects (issue #622
// review).
func TestExplainSchemaError_UnionTypedRequiredFieldRendersAdmittedType(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"o": map[string]any{
				"type":       []any{"object", "null"},
				"required":   []string{"x"},
				"properties": map[string]any{"x": map[string]any{"type": "string"}},
			},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"o": map[string]any{}}, "o", "/properties/o/required")
	if !strings.Contains(msg, `Example: {"o": {"x": "..."}}`) {
		t.Fatalf("union-typed required field did not render its admitted object type: %q", msg)
	}
}

// A required child whose schema is boolean false admits no value, so the
// missing-property guidance must not render an Example that violates it.
func TestExplainSchemaError_RequiredChildFalseOmitsExample(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"o": map[string]any{
				"type":       "object",
				"required":   []string{"x"},
				"properties": map[string]any{"x": false},
			},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"o": map[string]any{}}, "o", "/properties/o/required")
	if strings.Contains(msg, "Example:") {
		t.Fatalf("unsatisfiable required child still emitted an Example: %q", msg)
	}
}

// A required child carrying a constraint the placeholder does not satisfy
// (minLength) makes the generated nested shape invalid; the Example is omitted.
func TestExplainSchemaError_RequiredChildConstraintOmitsExample(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"o": map[string]any{
				"type":     "object",
				"required": []string{"x"},
				"properties": map[string]any{
					"x": map[string]any{"type": "string", "minLength": 10},
				},
			},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"o": map[string]any{}}, "o", "/properties/o/required")
	if strings.Contains(msg, "Example:") {
		t.Fatalf("constrained required child still emitted an Example: %q", msg)
	}
}

// An object-level minProperties larger than the required list cannot be met by
// an Example that carries only the required keys.
func TestExplainSchemaError_RequiredMinPropertiesOmitsExample(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"o": map[string]any{
				"type":          "object",
				"required":      []string{"x"},
				"minProperties": 3,
				"properties":    map[string]any{"x": map[string]any{"type": "string"}},
			},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"o": map[string]any{}}, "o", "/properties/o/required")
	if strings.Contains(msg, "Example:") {
		t.Fatalf("minProperties-infeasible Example was still emitted: %q", msg)
	}
}

// A required child whose nested object has its own required list is rendered as
// "{}", which violates it; the Example is omitted rather than coach it.
func TestExplainSchemaError_NestedRequiredChildOmitsExample(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"o": map[string]any{
				"type":     "object",
				"required": []string{"x"},
				"properties": map[string]any{
					"x": map[string]any{
						"type":       "object",
						"required":   []string{"y"},
						"properties": map[string]any{"y": map[string]any{"type": "string"}},
					},
				},
			},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"o": map[string]any{}}, "o", "/properties/o/required")
	if strings.Contains(msg, "Example:") {
		t.Fatalf("nested required child still emitted an invalid Example: %q", msg)
	}
}

// An unconstrained required child still renders the Example, so the guard is not
// over-broad.
func TestExplainSchemaError_PlainRequiredChildKeepsExample(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"o": map[string]any{
				"type":       "object",
				"required":   []string{"x"},
				"properties": map[string]any{"x": map[string]any{"type": "string"}},
			},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"o": map[string]any{}}, "o", "/properties/o/required")
	if !strings.Contains(msg, `Example: {"o": {"x": "..."}}`) {
		t.Fatalf("plain required child lost its Example: %q", msg)
	}
}

// A type error on an array element outside any combinator must not read as a
// missing argument: the element was sent, so the fallback names its instance
// path rather than inventing a missing field (issue #622 review).
func TestExplainSchemaError_PresentArrayItemTypeErrorNamesElement(t *testing.T) {
	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{"xs": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"xs": []any{"ok", 5}}, "xs/1", "/properties/xs/items/type")
	if strings.Contains(msg, "missing required argument") {
		t.Fatalf("present array element reported as a missing argument: %q", msg)
	}
	if !strings.Contains(msg, `argument "xs.1"`) {
		t.Fatalf("message must name the failing element: %q", msg)
	}
}

// A container declared only inside an always-applying allOf arm must still be
// resolved for the required guidance, so the level the message names and the
// schema it reads agree — the root's required list must not appear labelled as
// the nested container (issue #622 review).
func TestExplainSchemaError_BranchOnlyContainerRequiredGuidanceUsesNestedSchema(t *testing.T) {
	params := map[string]any{
		"type":       "object",
		"required":   []any{"top"},
		"properties": map[string]any{"top": map[string]any{"type": "string"}},
		"allOf": []any{map[string]any{"properties": map[string]any{
			"obj": map[string]any{
				"type":       "object",
				"required":   []any{"x"},
				"properties": map[string]any{"x": map[string]any{"type": "string"}},
			},
		}}},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"top": "t", "obj": map[string]any{}}, "obj/x", "/allOf/0/properties/obj/required")
	if !strings.Contains(msg, `missing required argument "x" in obj`) {
		t.Fatalf("message must name the nested missing key: %q", msg)
	}
	if !strings.Contains(msg, "Required arguments in obj: x (string)") {
		t.Fatalf("required list must come from the nested container: %q", msg)
	}
	if strings.Contains(msg, "Required arguments in obj: top") {
		t.Fatalf("root-level required guidance leaked into the nested level: %q", msg)
	}
}

// roborev's reproducer: a self-referential $defs node reachable through a root
// if/then. derefSchemaNode re-resolves the $ref to a fresh map on every pass, so
// the conditional walkers are mutually recursive over a cyclic node graph. The
// depth bound must make the walk fail closed instead of recursing until the
// stack dies.
func TestExplainSchemaError_RecursiveConditionalSiblingDoesNotOverflow(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"x": map[string]any{"type": "string", "enum": []string{"a"}},
		},
		"if":   map[string]any{"type": "object"},
		"then": map[string]any{"$ref": "#/$defs/self"},
		"$defs": map[string]any{
			"self": map[string]any{
				"if":   map[string]any{"type": "object"},
				"then": map[string]any{"$ref": "#/$defs/self"},
			},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"x": "b"}, "x", "/properties/x/enum")
	// The walk must terminate and, failing closed on the unresolved/recursive
	// conditional, must not advertise the failing arm's list as global.
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("recursive conditional sibling advertised a value: %q", msg)
	}
}

// A container that forbids undeclared properties while requiring one is
// unsatisfiable; the required-property guidance must not coach an impossible
// retry.
func TestExplainSchemaError_RequiredForbiddenByAdditionalPropertiesStaysGeneric(t *testing.T) {
	params := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{"task": map[string]any{"type": "string"}},
		"required":             []string{"task", "extra"},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t"}, "", "")
	if msg != "probe_tool: arguments did not match the schema." {
		t.Fatalf("unsatisfiable required guidance must be the generic mismatch, got: %q", msg)
	}
}
