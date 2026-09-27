package repair

import (
	"fmt"
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

// A nested property that exists only inside a combinator arm is still reported
// with its full instance path, not just the leaf name (issue #622 review).
func TestExplainSchemaError_UnresolvableContainerKeepsFullPath(t *testing.T) {
	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{"top": map[string]any{"type": "string"}},
		"oneOf": []any{map[string]any{"properties": map[string]any{
			"obj": map[string]any{
				"type":       "object",
				"required":   []any{"x"},
				"properties": map[string]any{"x": map[string]any{"type": "string"}},
			},
		}}},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"top": "t", "obj": map[string]any{"x": 5}}, "obj/x", "/oneOf/0/properties/obj/properties/x/type")
	if !strings.Contains(msg, `argument "obj.x"`) {
		t.Fatalf("nested unresolved container lost its display path: %q", msg)
	}
}

// unevaluatedProperties evaluates properties declared by sibling subschemas, so
// a required name an allOf arm declares is not forbidden and the guidance must
// still be rendered (issue #622 review).
func TestExplainSchemaError_UnevaluatedPropertiesDoesNotSuppressDeclaredRequired(t *testing.T) {
	params := map[string]any{
		"type":                  "object",
		"unevaluatedProperties": false,
		"properties":            map[string]any{"task": map[string]any{"type": "string"}},
		"allOf": []any{map[string]any{"properties": map[string]any{
			"extra": map[string]any{"type": "string"},
		}}},
		"required": []any{"extra"},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{}, "extra", "")
	if !strings.Contains(msg, "missing required argument") {
		t.Fatalf("declared-but-unevaluated required property lost its guidance: %q", msg)
	}
	if !strings.Contains(msg, "extra") {
		t.Fatalf("guidance must name the missing property: %q", msg)
	}
}

// $ref siblings are applied by 2019-09/2020-12 but ignored by draft-07, so
// reference resolution must be dialect-aware (issue #622 review).
func TestDerefSchemaNode_RefSiblingsFollowDialect(t *testing.T) {
	modern := map[string]any{"definitions": map[string]any{"rule": map[string]any{"type": "string"}}}
	if got := derefSchemaNode(modern, map[string]any{"$ref": "#/definitions/rule", "maxLength": 1}); got["maxLength"] == nil {
		t.Fatalf("2020-12 $ref sibling was dropped: %#v", got)
	}
	draft07 := map[string]any{
		"$schema":     "http://json-schema.org/draft-07/schema#",
		"definitions": map[string]any{"rule": map[string]any{"type": "string"}},
	}
	if got := derefSchemaNode(draft07, map[string]any{"$ref": "#/definitions/rule", "maxLength": 1}); got["maxLength"] != nil {
		t.Fatalf("draft-07 $ref sibling was merged: %#v", got)
	}
}

// mergeSchema must intersect property-count bounds, so a relaxing arm cannot
// loosen a base bound (issue #622 review).
func TestMergeSchema_IntersectsPropertyCountBounds(t *testing.T) {
	merged := mergeSchema(
		map[string]any{"minProperties": 3, "maxProperties": 5},
		map[string]any{"minProperties": 1, "maxProperties": 9},
	)
	if got, _ := schemaInt(merged["minProperties"]); got != 3 {
		t.Fatalf("minProperties = %v, want the tighter 3", merged["minProperties"])
	}
	if got, _ := schemaInt(merged["maxProperties"]); got != 5 {
		t.Fatalf("maxProperties = %v, want the tighter 5", merged["maxProperties"])
	}
}

// The merged schema the Example is built from must keep the tighter
// minProperties, or exampleObjectSatisfiable certifies an object with too few
// keys that the caller's real bound still rejects (issue #622 review).
func TestExampleObjectSatisfiable_UsesMergedPropertyCountBound(t *testing.T) {
	merged := mergeSchema(
		map[string]any{
			"type":          "object",
			"required":      []any{"x"},
			"minProperties": 3,
			"properties":    map[string]any{"x": map[string]any{"type": "string"}},
		},
		map[string]any{"required": []any{"x"}, "minProperties": 1},
	)
	if exampleObjectSatisfiable(merged) {
		t.Fatalf("merged schema kept the relaxing arm's minProperties: %#v", merged)
	}
}

// mergeSchemaProps keeps a name on only one side as-is, including a boolean-true
// property schema (issue #622 review).
func TestMergeSchemaProps_PreservesOneSidedBooleanTrue(t *testing.T) {
	merged := mergeSchemaProps(
		map[string]any{"a": map[string]any{"type": "string"}},
		map[string]any{"b": true},
	)
	if v, ok := merged["b"]; !ok || v != true {
		t.Fatalf("one-sided boolean-true property was dropped: %#v", merged)
	}
	if _, ok := merged["a"]; !ok {
		t.Fatalf("base-only property was dropped: %#v", merged)
	}
}

// A map `not: {}` rejects every instance, so a sibling's allowed-values list is
// not global: the message must be the generic mismatch, not a retry no call can
// satisfy (issue #622 review).
func TestExplainSchemaError_EmptyNotSiblingStaysGeneric(t *testing.T) {
	params := map[string]any{
		"type":       "object",
		"not":        map[string]any{},
		"properties": map[string]any{"x": map[string]any{"type": "string", "enum": []string{"red", "green"}}},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"x": "yellow"}, "x", "/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("an unsatisfiable not:{} sibling still advertised allowed values: %q", msg)
	}
	if !strings.Contains(msg, "wrong type or value") {
		t.Fatalf("unsatisfiable schema must fall back to the generic mismatch: %q", msg)
	}
}

// An object-level const names an exact object the generated Example is not
// checked against, so the Example must be omitted rather than coach a retry
// that fails the const (issue #622 review).
func TestExplainSchemaError_ObjectConstRequiredChildOmitsExample(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"o": map[string]any{
				"type":       "object",
				"const":      map[string]any{"a": 1},
				"required":   []any{"a"},
				"properties": map[string]any{"a": map[string]any{"type": "integer"}},
			},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"o": map[string]any{}}, "o", "/properties/o/required")
	if strings.Contains(msg, "Example:") {
		t.Fatalf("const-constrained object rendered an Example that fails it: %q", msg)
	}
}

// The same for an object-level enum (issue #622 review).
func TestExplainSchemaError_ObjectEnumRequiredChildOmitsExample(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"o": map[string]any{
				"type":       "object",
				"enum":       []any{map[string]any{"a": 1}},
				"required":   []any{"a"},
				"properties": map[string]any{"a": map[string]any{"type": "integer"}},
			},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"o": map[string]any{}}, "o", "/properties/o/required")
	if strings.Contains(msg, "Example:") {
		t.Fatalf("enum-constrained object rendered an Example that fails it: %q", msg)
	}
}

// A sent array element inside a combinator arm has a resolved item schema, so
// its constraint is explained by instance path rather than falling back to the
// generic mismatch (issue #622 review).
func TestExplainSchemaError_ArmNestedItemEnumNamesElement(t *testing.T) {
	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{"xs": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
		"oneOf": []any{
			map[string]any{
				"required":   []any{"xs"},
				"properties": map[string]any{"xs": map[string]any{"type": "array", "items": map[string]any{"enum": []string{"a", "b"}}}},
			},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"xs": []any{"c"}}, "xs/0", "/oneOf/0/properties/xs/items/enum")
	if !strings.Contains(msg, `"xs.0"`) || !strings.Contains(msg, "allowed values") {
		t.Fatalf("arm-nested item enum was not explained by instance path: %q", msg)
	}
}

// An InstanceLocation escapes "/" in a property name as "~1"; the segment must be
// decoded before lookup and display, or the property is reported missing
// (issue #622 review).
func TestExplainSchemaError_EscapedInstancePathSegmentIsResolved(t *testing.T) {
	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{"a/b": map[string]any{"type": "string"}},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"a/b": 5}, "a~1b", "")
	if strings.Contains(msg, "missing required argument") {
		t.Fatalf("escaped property was reported missing: %q", msg)
	}
	if !strings.Contains(msg, `"a/b"`) {
		t.Fatalf("message must name the decoded property: %q", msg)
	}
}

// Typed schema maps must be normalized like every other helper, or a typed-map
// arm's prohibition is missed and the resolved single-constraint message is used
// instead of yielding to branch prose (issue #622 review).
func TestBranchArmHasProhibition_TypedMapArms(t *testing.T) {
	if !branchArmHasProhibition(map[string]any{"oneOf": []map[string]any{{"not": map[string]any{"required": []any{"y"}}}}}, "/oneOf/0/required") {
		t.Fatalf("typed []map[string]any arm prohibition was not recognized")
	}
	if !branchArmHasProhibition(map[string]any{"oneOf": []any{map[string]map[string]any{"not": {"required": []any{"y"}}}}}, "/oneOf/0/required") {
		t.Fatalf("typed map[string]map[string]any arm prohibition was not recognized")
	}
}

// A typed-map property schema must render the same type its satisfiability was
// checked against, not the fallback string placeholder (issue #622 review).
func TestExampleObject_TypedMapChildRendersCheckedType(t *testing.T) {
	schema := map[string]any{
		"type":     "object",
		"required": []any{"n"},
		"properties": map[string]map[string]any{
			"n": {"type": "integer"},
		},
	}
	if got := exampleObject(schema, false); !strings.Contains(got, `"n": 0`) {
		t.Fatalf("typed-map child rendered the wrong placeholder: %q", got)
	}
}

// A required property declared boolean false (or matched by a boolean-false
// patternProperties schema) can never be supplied, so the guidance must be the
// generic mismatch rather than a retry that cannot succeed (issue #622 review).
func TestExplainSchemaError_FalseRequiredPropertyStaysGeneric(t *testing.T) {
	for name, params := range map[string]map[string]any{
		"properties.false": {
			"type":       "object",
			"properties": map[string]any{"x": false, "top": map[string]any{"type": "string"}},
			"required":   []any{"x"},
		},
		"patternProperties.false": {
			"type":              "object",
			"properties":        map[string]any{"top": map[string]any{"type": "string"}},
			"patternProperties": map[string]any{"^x$": false},
			"required":          []any{"x"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			msg := ExplainSchemaError("probe_tool", params, map[string]any{}, "x", "")
			if strings.Contains(msg, "Example:") {
				t.Fatalf("unsatisfiable required-false property still coached a retry: %q", msg)
			}
			if !strings.Contains(msg, "arguments did not match the schema") {
				t.Fatalf("unsatisfiable schema must be the generic mismatch: %q", msg)
			}
		})
	}
}

// dependentRequired constrains a generated object beyond its required list, so
// the Example must not be emitted while it is unmodelled (issue #622 review).
func TestExplainSchemaError_DependentRequiredOmitsExample(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"o": map[string]any{
				"type":              "object",
				"required":          []any{"x"},
				"dependentRequired": map[string]any{"x": []any{"y"}},
				"properties": map[string]any{
					"x": map[string]any{"type": "string"},
					"y": map[string]any{"type": "string"},
				},
			},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"o": map[string]any{}}, "o", "/properties/o/required")
	if strings.Contains(msg, "Example:") {
		t.Fatalf("dependentRequired not modelled; Example emitted that violates it: %q", msg)
	}
}

// An arm that is an unresolved external $ref may constrain the path, so it must
// be treated as constraining rather than silently dropped (issue #622 review).
func TestSiblingConstrainsPath_UnresolvedRefIsConstraining(t *testing.T) {
	arm := map[string]any{"$ref": "https://example.com/external#/$defs/rule"}
	if !siblingConstrainsPath(map[string]any{}, arm, []string{"properties", "x"}, 0) {
		t.Fatalf("an unresolved external $ref arm must be treated as constraining")
	}
}

// The branch helpers must normalize typed schemas like the resolvers do, or a
// hand-built []map[string]any oneOf yields no branches (issue #622 review).
func TestBranchHelpers_NormalizeTypedOneOfArms(t *testing.T) {
	arm := map[string]any{
		"required":   []any{"x"},
		"properties": map[string]any{"x": map[string]any{"enum": []string{"a"}}},
	}
	params := map[string]any{
		"required": []any{"x"},
		"oneOf":    []map[string]any{arm},
	}
	if branches, source := branchList(params, "oneOf"); len(branches) != 1 || source != "oneOf" {
		t.Fatalf("branchList missed typed oneOf arms: %v %q", branches, source)
	}
	if !branchRendersArmEnum(params, "/oneOf/0/properties/x/enum") {
		t.Fatalf("branchRendersArmEnum missed typed oneOf arms")
	}
	// That helper answers only when branches constrain nothing but presence.
	presenceOnly := map[string]any{
		"required": []any{"x"},
		"oneOf":    []map[string]any{{"required": []any{"x"}}},
	}
	if !oneOfExampleMatchesExactlyOneBranch(presenceOnly) {
		t.Fatalf("oneOfExampleMatchesExactlyOneBranch missed typed oneOf arms")
	}
}

// Typed oneOf arms must be counted, or a two-arm over-match reads as
// "exactly one" and an invalid Example is emitted (issue #622 review).
func TestExactlyOneRootBranch_NormalizesTypedOneOfArms(t *testing.T) {
	schema := map[string]any{
		"oneOf": []map[string]any{{"required": []any{"x"}}, {"required": []any{"x"}}},
	}
	if exactlyOneRootBranch(schema, []string{"x"}) {
		t.Fatalf("two matching typed arms must not count as exactly one")
	}
}

// A nil field schema cannot certify an example, so exampleForField declines
// rather than claiming an empty object is valid (issue #622 review).
func TestExampleForField_NilSchemaDeclines(t *testing.T) {
	got, ok := exampleForField("x", nil)
	if ok || got != "" {
		t.Fatalf("nil schema must decline an example, got (%q, %v)", got, ok)
	}
}

// Combinator arms may be boolean schemas or hand-built typed maps; dropping them
// silently removes a constraint (a dropped `false` arm lets the parent limit
// look valid), so walkNodes must preserve both forms (issue #622 review).
func TestWalkNodes_KeepsBooleanAndTypedMapArms(t *testing.T) {
	falseNodes, ok := walkNodes(map[string]any{}, []map[string]any{{"oneOf": []any{false}}}, []string{"oneOf", "0"}, false)
	if !ok {
		t.Fatalf("walk with a false arm failed")
	}
	foundFalse := false
	for _, n := range falseNodes {
		if isAlwaysFalseNode(n) {
			foundFalse = true
		}
	}
	if !foundFalse {
		t.Fatalf("a false combinator arm was dropped: %#v", falseNodes)
	}

	// A typed-map arm must survive AND be descended into: resolving a property
	// inside it only works if the arm was appended.
	typedArm := map[string]map[string]any{"properties": {"x": map[string]any{"type": "integer"}}}
	typedNodes, ok := walkNodes(
		map[string]any{},
		[]map[string]any{{"allOf": []any{typedArm}}},
		[]string{"allOf", "0", "properties", "x"},
		false,
	)
	if !ok {
		t.Fatalf("walk with a typed-map arm failed")
	}
	if len(typedNodes) != 1 || typedNodes[0]["type"] != "integer" {
		t.Fatalf("a typed-map combinator arm was dropped or not descended: %#v", typedNodes)
	}
}

// A dropped typed-map arm removes a tight constraint, so the renderer would
// advertise the parent's looser limit as if it were the arm's (issue #622
// review).
func TestExplainSchemaError_TypedMapArmLimitIsResolved(t *testing.T) {
	typedArm := map[string]map[string]any{"properties": {"x": map[string]any{"maxLength": 3}}}
	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{"x": map[string]any{"type": "string", "maxLength": 100}},
		"allOf":      []any{typedArm},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"x": "abcd"}, "x", "/allOf/0/properties/x/maxLength")
	if !strings.Contains(msg, "maxLength (3)") {
		t.Fatalf("typed-map arm's own limit was not resolved: %q", msg)
	}
	if strings.Contains(msg, "maxLength (100)") {
		t.Fatalf("typed-map arm dropped; parent limit advertised: %q", msg)
	}
}

// Every matching patternProperties schema must be inspected: a boolean-false
// match forbids the required property whatever the map iteration order
// (issue #622 review).
func TestPropertyMapForbidsRequired_AllMatchingPatterns(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"patternProperties": map[string]any{
			"^x$":  map[string]any{"type": "string"},
			"^.*$": false,
		},
		"required": []any{"x"},
	}
	for range 100 {
		if !propertyMapForbidsRequired(params) {
			t.Fatalf("a matching boolean-false pattern was not treated as forbidden")
		}
	}
}

// A required key absent from properties is governed by a schema-valued
// additionalProperties, so the generated Example must satisfy it rather than
// using the unconstrained string placeholder (issue #622 review).
func TestExampleObject_SchemaAdditionalPropertiesPlaceholder(t *testing.T) {
	schema := map[string]any{
		"type":                 "object",
		"required":             []any{"extra"},
		"additionalProperties": map[string]any{"type": "integer"},
	}
	if got := exampleObject(schema, false); !strings.Contains(got, `"extra": 0`) {
		t.Fatalf("undeclared required key did not use the additionalProperties placeholder: %q", got)
	}
}

// patternProperties and a schema-valued additionalProperties cannot change which
// keys are required, so the missing-key guidance must survive them; the Example
// is governed by the example guards instead (issue #622 review).
func TestExplainSchemaError_ValueShapeApplicatorsKeepRequiredGuidance(t *testing.T) {
	for name, extra := range map[string]map[string]any{
		"patternProperties":    {"patternProperties": map[string]any{"^y": map[string]any{"type": "string"}}},
		"additionalProperties": {"additionalProperties": map[string]any{"type": "string"}},
	} {
		t.Run(name, func(t *testing.T) {
			o := map[string]any{
				"type":       "object",
				"required":   []any{"x"},
				"properties": map[string]any{"x": map[string]any{"type": "string"}},
			}
			maps.Copy(o, extra)
			params := map[string]any{"type": "object", "properties": map[string]any{"o": o}}
			msg := ExplainSchemaError("probe_tool", params, map[string]any{"o": map[string]any{}}, "o", "/properties/o/required")
			if !strings.Contains(msg, "missing required properties") || !strings.Contains(msg, "o.x") {
				t.Fatalf("required guidance was dropped: %q", msg)
			}
		})
	}
}

// mergeSchema must keep the conjunction of both sides' allowed values for every
// enum/const combination, and must not depend on map iteration order (issue #622
// review, refuted here).
func TestMergeSchema_EnumConstConjunctionInvariant(t *testing.T) {
	vals := []any{"a", "b", "c"}
	sides := []map[string]any{{}}
	for _, v := range vals {
		sides = append(sides, map[string]any{"const": v})
	}
	for i := range vals {
		for j := i + 1; j < len(vals); j++ {
			sides = append(sides, map[string]any{"enum": []any{vals[i], vals[j]}})
		}
	}
	sides = append(sides, map[string]any{"enum": vals})
	for _, v := range vals {
		sides = append(sides, map[string]any{"enum": vals, "const": v})
	}
	for _, base := range sides {
		for _, overlay := range sides {
			first := fmt.Sprint(mergeSchema(base, overlay))
			for range 19 {
				got := fmt.Sprint(mergeSchema(base, overlay))
				if got != first {
					t.Fatalf("merge is order-dependent: base=%v overlay=%v: %q vs %q", base, overlay, first, got)
				}
			}
			merged := mergeSchema(base, overlay)
			allowed, set := allowedValues(merged)
			if !set {
				continue
			}
			bb, bset := allowedValues(base)
			ob, oset := allowedValues(overlay)
			for _, v := range allowed {
				if bset && !enumContainsValue(bb, v) {
					t.Fatalf("merged allows %v rejected by base %v (overlay %v)", v, base, overlay)
				}
				if oset && !enumContainsValue(ob, v) {
					t.Fatalf("merged allows %v rejected by overlay %v (base %v)", v, overlay, base)
				}
			}
		}
	}
}

func enumContainsValue(list []any, v any) bool {
	for _, x := range list {
		if formatEnumValue(x) == formatEnumValue(v) {
			return true
		}
	}
	return false
}

// An undeclared required key is governed by its matching patternProperties
// schema, so the root Example must render a placeholder that satisfies it
// (issue #622 review).
func TestExplainSchemaError_RootPatternPropertiesPlaceholderIsValid(t *testing.T) {
	params := map[string]any{
		"type":              "object",
		"required":          []any{"x"},
		"patternProperties": map[string]any{"^x$": map[string]any{"type": "integer"}},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{}, "x", "")
	if strings.Contains(msg, `"x": "..."`) {
		t.Fatalf("placeholder violates the matching patternProperties schema: %q", msg)
	}
	if !strings.Contains(msg, `"x": 0`) {
		t.Fatalf("expected the integer placeholder from patternProperties: %q", msg)
	}
}

// A constraint on the governing additionalProperties schema that the placeholder
// cannot satisfy must omit the Example rather than coach a failing retry
// (issue #622 review).
func TestExplainSchemaError_RootAdditionalPropertiesBoundsOmitExample(t *testing.T) {
	params := map[string]any{
		"type":                 "object",
		"required":             []any{"x"},
		"additionalProperties": map[string]any{"type": "integer", "minimum": 5},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{}, "x", "")
	if strings.Contains(msg, "Example:") {
		t.Fatalf("example violates the additionalProperties minimum: %q", msg)
	}
}

// A declared required property whose placeholder cannot satisfy its own bounds
// (minItems/minLength/minimum/minProperties) must omit the Example rather than
// coach a retry that fails validation (issue #622 review).
func TestExplainSchemaError_BoundedRequiredPropertyOmitsExample(t *testing.T) {
	for name, prop := range map[string]map[string]any{
		"minItems":      {"type": "array", "minItems": 1},
		"minLength":     {"type": "string", "minLength": 10},
		"minimum":       {"type": "integer", "minimum": 5},
		"minProperties": {"type": "object", "minProperties": 2},
	} {
		t.Run(name, func(t *testing.T) {
			params := map[string]any{
				"type":       "object",
				"properties": map[string]any{"p": prop},
				"required":   []any{"p"},
			}
			msg := ExplainSchemaError("probe_tool", params, map[string]any{}, "p", "")
			if strings.Contains(msg, "Example:") {
				t.Fatalf("bounded required property still got an invalid Example: %q", msg)
			}
		})
	}
}

// The same holds for a bound on a key nested inside a required object property
// (issue #622 review).
func TestExplainSchemaError_NestedRequiredBoundOmitsExample(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"p": map[string]any{
				"type":       "object",
				"required":   []any{"q"},
				"properties": map[string]any{"q": map[string]any{"type": "array", "minItems": 1}},
			},
		},
		"required": []any{"p"},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{}, "p", "")
	if strings.Contains(msg, "Example:") {
		t.Fatalf("nested bounded required key still got an invalid Example: %q", msg)
	}
}

// Conjunctive applicators must combine rather than let the overlay replace the
// base: a boolean false on either side wins, and patternProperties unions
// (issue #622 review).
func TestMergeSchema_ConjunctiveApplicators(t *testing.T) {
	if got := mergeSchema(map[string]any{"additionalProperties": false}, map[string]any{"additionalProperties": map[string]any{"type": "string"}})["additionalProperties"]; got != false {
		t.Fatalf("additionalProperties:false was replaced by %v", got)
	}
	if got := mergeSchema(map[string]any{"items": false}, map[string]any{"items": map[string]any{"type": "string"}})["items"]; got != false {
		t.Fatalf("items:false was replaced by %v", got)
	}
	pat := schemaChildMap(mergeSchema(
		map[string]any{"patternProperties": map[string]any{"^a$": false}},
		map[string]any{"patternProperties": map[string]any{"^b$": map[string]any{"type": "string"}}},
	)["patternProperties"])
	if _, ok := pat["^a$"]; !ok {
		t.Fatalf("base patternProperties was dropped: %v", pat)
	}
	if _, ok := pat["^b$"]; !ok {
		t.Fatalf("overlay patternProperties was dropped: %v", pat)
	}
	merged := schemaChildMap(mergeSchema(
		map[string]any{"additionalProperties": map[string]any{"type": "string"}},
		map[string]any{"additionalProperties": map[string]any{"maxLength": 2}},
	)["additionalProperties"])
	if merged["type"] != "string" || merged["maxLength"] != 2 {
		t.Fatalf("schema-valued additionalProperties did not merge: %v", merged)
	}
}

// Branch prose must not coach a retry the enclosing schema forbids: an arm
// requiring an undeclared property under additionalProperties:false falls back to
// the generic mismatch (issue #622 review).
func TestExplainSchemaError_BranchProseForbiddenPropertyStaysGeneric(t *testing.T) {
	params := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{"a": map[string]any{"type": "string"}},
		"required":             []any{"a"},
		"oneOf":                []any{map[string]any{"required": []any{"b"}}},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"a": "x"}, "", "/oneOf/0/required")
	if strings.Contains(msg, `send all of "b"`) {
		t.Fatalf("branch prose coached a forbidden property: %q", msg)
	}
	if !strings.Contains(msg, "arguments did not match the schema") {
		t.Fatalf("forbidden branch prose must fall back to the generic mismatch: %q", msg)
	}
}

// A conjunctive keyword this merger cannot combine must fail closed rather than
// let the overlay replace the base constraint (issue #622 review).
func TestMergeSchema_UncombinedConjunctiveKeywordFailsClosed(t *testing.T) {
	for name, pair := range map[string][2]map[string]any{
		"$ref":        {{"$ref": "#/$defs/a"}, {"$ref": "#/$defs/b"}},
		"multipleOf":  {{"multipleOf": 2}, {"multipleOf": 3}},
		"pattern":     {{"pattern": "^a"}, {"pattern": "b$"}},
		"uniqueItems": {{"uniqueItems": true}, {"uniqueItems": false}},
		"format":      {{"format": "uri"}, {"format": "email"}},
	} {
		if merged := mergeSchema(pair[0], pair[1]); merged[unmodeledKey] != true {
			t.Fatalf("%s: unconjoined conjunctive keyword must fail closed, got %v", name, merged)
		}
	}
	// Annotations do not constrain, so the overlay may replace them.
	if merged := mergeSchema(map[string]any{"description": "a"}, map[string]any{"description": "b"}); merged[unmodeledKey] == true {
		t.Fatalf("annotation replacement must not mark the merge unmodeled")
	}
}
