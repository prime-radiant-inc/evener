package repair

import (
	"reflect"
	"strings"
	"testing"
)

// delegateOneOfParamsSwapped mirrors delegateOneOfParams with the two oneOf
// arms in the opposite order. jsonschema descends Causes[0], so this ordering
// makes the deepest cause the branch-level enum
// (/oneOf/0/properties/sandbox/enum), which is the shape constraintMessage
// must render from the branch (issue #622).
func delegateOneOfParamsSwapped() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"task":        map[string]any{"type": "string"},
			"sandbox":     map[string]any{"type": "string", "enum": []string{"off", "read-only", "workspace-write", "restricted"}},
			"sandbox_net": map[string]any{"type": "boolean"},
		},
		"required": []any{"task"},
		"oneOf": []any{
			map[string]any{
				"required": []string{"sandbox", "sandbox_net"},
				"properties": map[string]any{
					"sandbox": map[string]any{"enum": []string{"read-only", "workspace-write", "restricted"}},
				},
			},
			map[string]any{"not": map[string]any{"required": []string{"sandbox_net"}}},
		},
	}
}

// Issue #622/#621: when the failing cause is a branch-level enum, a sibling arm
// that does not mention the property still accepts any value there — arm 1
// (`not: {required: ["sandbox_net"]}`) accepts sandbox "off" as long as
// sandbox_net is omitted — so the failing arm's narrowed enum is not the
// globally accepted set. An allowed-values list would tell the caller "off" is
// invalid and push it to change a valid sandbox choice; the message must render
// the branch-level pairing rule, naming sandbox_net. The top-level list, which
// contains the rejected value, must never appear.
func TestExplainSchemaError_BranchEnumUsesBranchAllowedValues(t *testing.T) {
	msg := ExplainSchemaError("delegate", delegateOneOfParamsSwapped(), delegateOneOfArgs(), "sandbox", "/oneOf/0/properties/sandbox/enum")
	if strings.Contains(msg, "is not one of the allowed values") {
		t.Fatalf("ambiguous oneOf arm enum rendered as a global allowed-values list: %q", msg)
	}
	if strings.Contains(msg, `"off", "read-only"`) {
		t.Fatalf("message reproduced the top-level sandbox enum: %q", msg)
	}
	if !strings.Contains(msg, "oneOf constraint") {
		t.Fatalf("message must name the oneOf constraint: %q", msg)
	}
	if !strings.Contains(msg, `"sandbox" must be one of "read-only", "workspace-write", "restricted"`) {
		t.Fatalf("message must render the branch's narrowed sandbox enum: %q", msg)
	}
	if !strings.Contains(msg, "sandbox_net") {
		t.Fatalf("message must name the constrained pairing field sandbox_net: %q", msg)
	}
}

// A bare keyword (no location path) keeps the flat behavior: the field schema
// is read from the container's own properties map. This is the pre-#622 path
// and guards against the path walker breaking callers that pass only a name.
func TestExplainSchemaError_BareKeywordStillReadsContainerEnum(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"color": map[string]any{"type": "string", "enum": []string{"red", "green"}},
		},
	}
	msg := ExplainSchemaError("my_tool", params, map[string]any{"color": "blue"}, "color", "enum")
	want := `my_tool: argument "color" is not one of the allowed values: "red", "green". Value is "blue".`
	if msg != want {
		t.Fatalf("bare-keyword enum message = %q, want %q", msg, want)
	}
}

// Issue #622 follow-up: a branch-level `required` failure names the branch's
// required properties, so its Example must be rendered from the same effective
// field schema — the branch overlaid on the base property. The branch here
// carries only the additive "required" key (a common JSON-Schema idiom): a
// branch treated as the complete schema would lose the base object's type and
// its "message" property, rendering an Example the retry then fails. The
// message names "output.extra" and the Example must include both keys.
func TestExplainSchemaError_BranchRequiredExampleUsesResolvedSchema(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task":   map[string]any{"type": "string"},
			"output": map[string]any{"type": "object", "required": []string{"message"}, "properties": map[string]any{"message": map[string]any{"type": "string"}}},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{
				"output": map[string]any{"required": []string{"extra"}},
			}},
		},
	}
	args := map[string]any{"task": "ping", "output": map[string]any{"message": "hi"}}
	msg := ExplainSchemaError("probe_tool", params, args, "output", "/oneOf/0/properties/output/required")
	want := "probe_tool: argument \"output\" is missing required properties: output.extra.\n" +
		`Example: {"output": {"extra": "...", "message": "..."}}`
	if msg != want {
		t.Fatalf("branch required example:\n got: %q\nwant: %q", msg, want)
	}
}

// A branch property schema may be an internal $ref (jsonschema emits the
// $ref as a real KeywordLocation segment: /oneOf/0/properties/color/$ref/enum).
// The walker resolves it, so the branch's referenced enum wins over the broader
// top-level enum instead of the message falling back to the top-level list.
func TestExplainSchemaError_RefBranchEnumIsResolved(t *testing.T) {
	params := map[string]any{
		"$defs": map[string]any{"branch_color": map[string]any{"enum": []string{"red", "green"}}},
		"type":  "object",
		"properties": map[string]any{
			"task":  map[string]any{"type": "string"},
			"color": map[string]any{"type": "string", "enum": []string{"red", "green", "blue"}},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"color": map[string]any{"$ref": "#/$defs/branch_color"}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "color": "blue"}, "color", "/oneOf/0/properties/color/$ref/enum")
	want := `probe_tool: argument "color" is not one of the allowed values: "red", "green". Value is "blue".`
	if msg != want {
		t.Fatalf("ref branch enum message = %q, want %q", msg, want)
	}
}

// KeywordLocation is a JSON Pointer, so a property name containing "~" is
// escaped as "~0". The walker decodes it before lookup, so a branch that
// narrows such a property's enum resolves rather than falling back. A name
// containing "/" (escaped as "~1") is handled the same way by both the keyword
// walk and the instance-location walk, which now decodes its segments through
// splitInstancePath.
func TestExplainSchemaError_KeywordLocationDecodesPointerEscapes(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"a~b":  map[string]any{"type": "string", "enum": []string{"x", "y"}},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"a~b": map[string]any{"enum": []string{"y"}}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "a~b": "x"}, "a~b", "/oneOf/0/properties/a~0b/enum")
	want := `probe_tool: argument "a~b" is not one of the allowed values: "y". Value is "x".`
	if msg != want {
		t.Fatalf("pointer-escaped branch enum message = %q, want %q", msg, want)
	}
}

// A branch enum that is wider than the base property enum must be intersected
// with it, not overlaid: the branch is additive, so a value the base rejects is
// not "allowed" (issue #622 review). Here the branch widens [red] to
// [red, blue]; the effective allowed set stays [red].
func TestExplainSchemaError_BranchEnumIntersectsBase(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task":  map[string]any{"type": "string"},
			"color": map[string]any{"type": "string", "enum": []string{"red"}},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"color": map[string]any{"enum": []string{"red", "blue"}}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "color": "blue"}, "color", "/oneOf/0/properties/color/enum")
	want := `probe_tool: argument "color" is not one of the allowed values: "red". Value is "blue".`
	if msg != want {
		t.Fatalf("intersected enum message = %q, want %q", msg, want)
	}
}

// A branch maxLength looser than the base must keep the tighter base limit, so
// the message reports the limit that actually rejected the value.
func TestExplainSchemaError_BranchMaxLengthKeepsTighterBase(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"name": map[string]any{"type": "string", "maxLength": 5},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"name": map[string]any{"maxLength": 10}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "name": "abcdefg"}, "name", "/oneOf/0/properties/name/maxLength")
	want := `probe_tool: argument "name" exceeds maxLength (5). Value "abcdefg" is 7 characters.`
	if msg != want {
		t.Fatalf("intersected maxLength message = %q, want %q", msg, want)
	}
}

// Sibling allOf arms all apply conjunctively, so a branch-only `required` inside
// one arm must be merged together with the sibling arm's too — and with the
// base property's own required list. A single-arm merge would omit output.other.
func TestExplainSchemaError_SiblingAllOfArmsAreMerged(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task":   map[string]any{"type": "string"},
			"output": map[string]any{"type": "object", "required": []string{"message"}, "properties": map[string]any{"message": map[string]any{"type": "string"}}},
		},
		"required": []string{"task"},
		"allOf": []any{
			map[string]any{"properties": map[string]any{"output": map[string]any{"required": []string{"extra"}}}},
			map[string]any{"properties": map[string]any{"output": map[string]any{"required": []string{"other"}}}},
		},
	}
	args := map[string]any{"task": "ping", "output": map[string]any{"message": "hi"}}
	msg := ExplainSchemaError("probe_tool", params, args, "output", "/allOf/0/properties/output/required")
	want := "probe_tool: argument \"output\" is missing required properties: output.extra, output.other.\n" +
		`Example: {"output": {"extra": "...", "message": "...", "other": "..."}}`
	if msg != want {
		t.Fatalf("sibling allOf example:\n got: %q\nwant: %q", msg, want)
	}
}

// When the branch property is an internal $ref but the base property is not, the
// base walk must keep the base node instead of discarding it at the unhandled
// $ref step. Otherwise a branch $ref that only adds `required` loses the base
// type and properties and renders an invalid Example.
func TestExplainSchemaError_RequiredInsideRefBranchKeepsBase(t *testing.T) {
	params := map[string]any{
		"$defs": map[string]any{"out": map[string]any{"required": []string{"extra"}}},
		"type":  "object",
		"properties": map[string]any{
			"task":   map[string]any{"type": "string"},
			"output": map[string]any{"type": "object", "required": []string{"message"}, "properties": map[string]any{"message": map[string]any{"type": "string"}}},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"output": map[string]any{"$ref": "#/$defs/out"}}},
		},
	}
	args := map[string]any{"task": "ping", "output": map[string]any{"message": "hi"}}
	msg := ExplainSchemaError("probe_tool", params, args, "output", "/oneOf/0/properties/output/$ref/required")
	want := "probe_tool: argument \"output\" is missing required properties: output.extra.\n" +
		`Example: {"output": {"extra": "...", "message": "..."}}`
	if msg != want {
		t.Fatalf("ref-branch required example:\n got: %q\nwant: %q", msg, want)
	}
}

// A `not` subschema negates rather than adds, so its inner constraints must not
// be overlaid and reported as allowed. The resolver falls back to the base
// property schema (the container's own enum), not the negated inner list.
func TestExplainSchemaError_NotBranchDoesNotOverlay(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task":  map[string]any{"type": "string"},
			"color": map[string]any{"type": "string", "enum": []string{"red", "green", "blue"}},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"not": map[string]any{"properties": map[string]any{"color": map[string]any{"enum": []string{"red"}}}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "color": "red"}, "color", "/oneOf/0/not/properties/color/enum")
	want := `probe_tool: argument "color" is not one of the allowed values: "red", "green", "blue". Value is "red".`
	if msg != want {
		t.Fatalf("not-branch message = %q, want %q", msg, want)
	}
}

// A base property that is itself an internal $ref must be dereferenced before
// merging, or its referenced constraints are lost. Here the referenced base
// enum is narrower than the branch enum; without dereferencing, the merge would
// see no base enum and advertise the branch's wider list.
func TestExplainSchemaError_RefBaseSchemaIsDereferenced(t *testing.T) {
	params := map[string]any{
		"$defs": map[string]any{"color": map[string]any{"type": "string", "enum": []string{"red"}}},
		"type":  "object",
		"properties": map[string]any{
			"task":  map[string]any{"type": "string"},
			"color": map[string]any{"$ref": "#/$defs/color"},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"color": map[string]any{"enum": []string{"red", "blue"}}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "color": "blue"}, "color", "/oneOf/0/properties/color/enum")
	want := `probe_tool: argument "color" is not one of the allowed values: "red". Value is "blue".`
	if msg != want {
		t.Fatalf("ref-base enum message = %q, want %q", msg, want)
	}
}

// A sibling allOf arm whose property is an internal $ref must be dereferenced
// too, so every conjunctive arm contributes its required keys.
func TestExplainSchemaError_SiblingAllOfRefArmIsDereferenced(t *testing.T) {
	params := map[string]any{
		"$defs": map[string]any{"out": map[string]any{"required": []string{"extra"}}},
		"type":  "object",
		"properties": map[string]any{
			"task":   map[string]any{"type": "string"},
			"output": map[string]any{"type": "object", "required": []string{"message"}, "properties": map[string]any{"message": map[string]any{"type": "string"}}},
		},
		"required": []string{"task"},
		"allOf": []any{
			map[string]any{"properties": map[string]any{"output": map[string]any{"$ref": "#/$defs/out"}}},
			map[string]any{"properties": map[string]any{"output": map[string]any{"required": []string{"other"}}}},
		},
	}
	args := map[string]any{"task": "ping", "output": map[string]any{"message": "hi"}}
	msg := ExplainSchemaError("probe_tool", params, args, "output", "/allOf/0/properties/output/required")
	want := "probe_tool: argument \"output\" is missing required properties: output.extra, output.other.\n" +
		`Example: {"output": {"extra": "...", "message": "...", "other": "..."}}`
	if msg != want {
		t.Fatalf("sibling allOf $ref example:\n got: %q\nwant: %q", msg, want)
	}
}

// Hand-built schemas may store combinator arms as []map[string]any rather than
// []any; the walker must recognize both or it loses branch resolution entirely
// and falls back to the unrelated container schema.
func TestExplainSchemaError_TypedCombinatorArmsResolve(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task":  map[string]any{"type": "string"},
			"color": map[string]any{"type": "string", "enum": []string{"red", "green", "blue"}},
		},
		"required": []string{"task"},
		"oneOf": []map[string]any{
			{"properties": map[string]any{"color": map[string]any{"enum": []string{"red"}}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "color": "blue"}, "color", "/oneOf/0/properties/color/enum")
	want := `probe_tool: argument "color" is not one of the allowed values: "red". Value is "blue".`
	if msg != want {
		t.Fatalf("typed-arms enum message = %q, want %q", msg, want)
	}
}

// Type intersection honors the integer/number subtype relationship and clears
// disjoint declarations rather than reasserting a contradicted base type.
func TestIntersectTypes_SubtypeAndDisjoint(t *testing.T) {
	for _, tc := range []struct {
		name    string
		base    any
		overlay any
		want    any
	}{
		{name: "number list narrowed by integer", base: []any{"number", "string"}, overlay: "integer", want: "integer"},
		{name: "integer base widened by number", base: "integer", overlay: "number", want: "integer"},
		{name: "identical lists", base: []any{"integer", "null"}, overlay: []any{"integer", "null"}, want: []any{"integer", "null"}},
		{name: "disjoint clears type", base: []any{"string"}, overlay: []any{"integer"}, want: []any{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := intersectTypes(tc.base, tc.overlay); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("intersectTypes(%#v, %#v) = %#v, want %#v", tc.base, tc.overlay, got, tc.want)
			}
		})
	}
}

// A disjoint base/branch enum is unsatisfiable: naming either list would
// advertise a value the other side rejects, so the renderer must fall back to
// the generic message.
func TestExplainSchemaError_DisjointEnumIntersectionFallsBack(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task":  map[string]any{"type": "string"},
			"color": map[string]any{"type": "string", "enum": []string{"red"}},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"color": map[string]any{"enum": []string{"blue"}}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "color": "blue"}, "color", "/oneOf/0/properties/color/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("disjoint enum advertised values: %q", msg)
	}
	if !strings.Contains(msg, "wrong type or value") {
		t.Fatalf("disjoint enum should fall back to the generic message: %q", msg)
	}
}

// A branch enum that conflicts with a base const is likewise unsatisfiable and
// must not advertise the branch's impossible value.
func TestExplainSchemaError_ConstEnumConflictFallsBack(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task":  map[string]any{"type": "string"},
			"color": map[string]any{"const": "arm-b"},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"color": map[string]any{"enum": []string{"arm-c"}}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "color": "arm-c"}, "color", "/oneOf/0/properties/color/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("const/enum conflict advertised values: %q", msg)
	}
	if !strings.Contains(msg, "wrong type or value") {
		t.Fatalf("const/enum conflict should fall back to the generic message: %q", msg)
	}
}

// A branch enum that contains the base const narrows to that const value.
func TestExplainSchemaError_ConstEnumConjunctionNarrows(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task":  map[string]any{"type": "string"},
			"color": map[string]any{"const": "arm-b"},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"color": map[string]any{"enum": []string{"arm-b", "arm-c"}}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "color": "arm-c"}, "color", "/oneOf/0/properties/color/enum")
	want := `probe_tool: argument "color" is not one of the allowed values: "arm-b". Value is "arm-c".`
	if msg != want {
		t.Fatalf("const/enum conjunction message = %q, want %q", msg, want)
	}
}

// An intermediate combinator arm's own constraints must survive a descent into a
// nested combinator: the outer oneOf arm narrows x to [arm-a, arm-b] and its
// nested allOf arm narrows again to [arm-b, arm-c], so the conjunction is
// [arm-b]. Dropping the outer arm would advertise arm-c.
func TestExplainSchemaError_NestedCombinatorKeepsOuterArm(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"arm-a", "arm-b", "arm-c"}},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{
				"properties": map[string]any{"x": map[string]any{"enum": []string{"arm-a", "arm-b"}}},
				"allOf": []any{
					map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"arm-b", "arm-c"}}}},
				},
			},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "arm-c"}, "x", "/oneOf/0/allOf/0/properties/x/enum")
	want := `probe_tool: argument "x" is not one of the allowed values: "arm-b". Value is "arm-c".`
	if msg != want {
		t.Fatalf("nested combinator message = %q, want %q", msg, want)
	}
}

// When a oneOf/anyOf arm's enum is not the globally valid accepted set — another
// arm constrains the same property's allowed values — the renderer must not
// advertise the failing arm's list (it could omit values a sibling arm accepts).
func TestExplainSchemaError_AmbiguousDisjunctiveEnumFallsBack(t *testing.T) {
	for _, combinator := range []string{"oneOf", "anyOf"} {
		t.Run(combinator, func(t *testing.T) {
			params := map[string]any{
				"type": "object",
				"properties": map[string]any{
					"task": map[string]any{"type": "string"},
					"x":    map[string]any{"type": "string", "enum": []string{"arm-a", "arm-b", "arm-c"}},
				},
				"required": []string{"task"},
				combinator: []any{
					map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"arm-a"}}}},
					map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"arm-b"}}}},
				},
			}
			msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "arm-c"}, "x", "/"+combinator+"/0/properties/x/enum")
			if strings.Contains(msg, "allowed values") {
				t.Fatalf("ambiguous %s enum advertised a per-arm list: %q", combinator, msg)
			}
			if !strings.Contains(msg, "wrong type or value") {
				t.Fatalf("ambiguous %s enum should fall back to generic: %q", combinator, msg)
			}
		})
	}
}

// Sibling conjunctive arms must not be dropped when the path descends through a
// nested allOf: extra comes from the outer sibling arm, other and another from
// the nested allOf's two arms — all apply.
func TestExplainSchemaError_AllOfSiblingKeptAcrossNestedCombinator(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task":   map[string]any{"type": "string"},
			"output": map[string]any{"type": "object", "required": []string{"message"}, "properties": map[string]any{"message": map[string]any{"type": "string"}}},
		},
		"required": []string{"task"},
		"allOf": []any{
			map[string]any{"properties": map[string]any{"output": map[string]any{"required": []string{"extra"}}}},
			map[string]any{"allOf": []any{
				map[string]any{"properties": map[string]any{"output": map[string]any{"required": []string{"other"}}}},
				map[string]any{"properties": map[string]any{"output": map[string]any{"required": []string{"another"}}}},
			}},
		},
	}
	args := map[string]any{"task": "ping", "output": map[string]any{"message": "hi"}}
	msg := ExplainSchemaError("probe_tool", params, args, "output", "/allOf/1/allOf/1/properties/output/required")
	want := "probe_tool: argument \"output\" is missing required properties: output.extra, output.other, output.another.\n" +
		`Example: {"output": {"another": "...", "extra": "...", "message": "...", "other": "..."}}`
	if msg != want {
		t.Fatalf("allOf sibling across nested allOf:\n got: %q\nwant: %q", msg, want)
	}
}

// resolveSchemaRef must traverse array indices and typed $defs maps, not just
// map[string]any, or branch constraints behind such refs are lost.
func TestResolveSchemaRef_ArrayIndexAndTypedDefs(t *testing.T) {
	root := map[string]any{
		"$defs": map[string]map[string]any{
			"out": {"allOf": []any{map[string]any{"required": []string{"extra"}}}},
		},
	}
	got := resolveSchemaRef(root, "#/$defs/out/allOf/0")
	if got == nil {
		t.Fatal("resolveSchemaRef returned nil for typed $defs + array index")
	}
	if !reflect.DeepEqual(got["required"], []string{"extra"}) {
		t.Fatalf("resolved required = %#v, want [extra]", got["required"])
	}
}

// A branch property whose $ref points through an array index into a typed $defs
// map must still contribute its required key.
func TestExplainSchemaError_RefThroughArrayIndexResolves(t *testing.T) {
	params := map[string]any{
		"$defs": map[string]map[string]any{
			"out": {"allOf": []any{map[string]any{"required": []string{"extra"}}}},
		},
		"type": "object",
		"properties": map[string]any{
			"task":   map[string]any{"type": "string"},
			"output": map[string]any{"type": "object", "required": []string{"message"}, "properties": map[string]any{"message": map[string]any{"type": "string"}}},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"output": map[string]any{"$ref": "#/$defs/out/allOf/0"}}},
		},
	}
	args := map[string]any{"task": "ping", "output": map[string]any{"message": "hi"}}
	msg := ExplainSchemaError("probe_tool", params, args, "output", "/oneOf/0/properties/output/$ref/required")
	want := "probe_tool: argument \"output\" is missing required properties: output.extra.\n" +
		`Example: {"output": {"extra": "...", "message": "..."}}`
	if msg != want {
		t.Fatalf("ref-through-array message:\n got: %q\nwant: %q", msg, want)
	}
}

// Numeric JSON Pointer tokens are object keys first: a $defs entry named "0"
// must resolve via map lookup, not be mistaken for an array index.
func TestResolveSchemaRef_NumericDefKeyIsNotAnIndex(t *testing.T) {
	root := map[string]any{
		"$defs": map[string]any{"0": map[string]any{"type": "string", "enum": []string{"red"}}},
	}
	got := resolveSchemaRef(root, "#/$defs/0")
	if got == nil {
		t.Fatal("resolveSchemaRef returned nil for numeric-named $defs key")
	}
	if !reflect.DeepEqual(got["enum"], []string{"red"}) {
		t.Fatalf("resolved enum = %#v, want [red]", got["enum"])
	}
}

// A base property reached through a numeric-named $defs key must still be
// dereferenced and intersected with the branch, not dropped.
func TestExplainSchemaError_NumericDefKeyRefIsResolved(t *testing.T) {
	params := map[string]any{
		"$defs": map[string]any{"0": map[string]any{"type": "string", "enum": []string{"red"}}},
		"type":  "object",
		"properties": map[string]any{
			"task":  map[string]any{"type": "string"},
			"color": map[string]any{"$ref": "#/$defs/0"},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"color": map[string]any{"enum": []string{"red", "blue"}}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "color": "blue"}, "color", "/oneOf/0/properties/color/enum")
	want := `probe_tool: argument "color" is not one of the allowed values: "red". Value is "blue".`
	if msg != want {
		t.Fatalf("numeric-def-key ref message = %q, want %q", msg, want)
	}
}

// Unsatisfiability must survive further merges: with three conjunctive nodes
// whose first two enums are disjoint, a later node must not resurrect its value.
func TestMergeSchema_UnsatisfiableEnumDoesNotResurrect(t *testing.T) {
	got := mergeSchema(mergeSchema(
		map[string]any{"enum": []string{"red"}},
		map[string]any{"enum": []string{"blue"}}),
		map[string]any{"enum": []string{"green"}})
	if allowed := formatEnumValues(got["enum"]); len(allowed) != 0 {
		t.Fatalf("unsatisfiable enum advertised values: %v", allowed)
	}
}

// The same three-way shape through ExplainSchemaError: base enum [red] plus two
// allOf arms [blue] and [green] is unsatisfiable, so the renderer must fall back
// to the generic message rather than advertise "green".
func TestExplainSchemaError_ThreeWayDisjointEnumFallsBack(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task":  map[string]any{"type": "string"},
			"color": map[string]any{"type": "string", "enum": []string{"red"}},
		},
		"required": []string{"task"},
		"allOf": []any{
			map[string]any{"properties": map[string]any{"color": map[string]any{"enum": []string{"blue"}}}},
			map[string]any{"properties": map[string]any{"color": map[string]any{"enum": []string{"green"}}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "color": "green"}, "color", "/allOf/0/properties/color/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("three-way disjoint enum advertised values: %q", msg)
	}
	if !strings.Contains(msg, "wrong type or value") {
		t.Fatalf("three-way disjoint enum should be generic: %q", msg)
	}
}

// A same-node enum and const must be intersected: the const narrows the enum, so
// values outside the const are not advertised.
func TestExplainSchemaError_SameNodeEnumConstNarrows(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task":  map[string]any{"type": "string"},
			"color": map[string]any{"enum": []string{"red", "blue"}, "const": "red"},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"color": map[string]any{"enum": []string{"red", "blue"}}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "color": "blue"}, "color", "/oneOf/0/properties/color/enum")
	want := `probe_tool: argument "color" is not one of the allowed values: "red". Value is "blue".`
	if msg != want {
		t.Fatalf("same-node enum/const message = %q, want %q", msg, want)
	}
}

// Disjoint types stay unsatisfiable across further merges instead of letting a
// later node re-admit a contradicted type.
func TestMergeSchema_DisjointTypeDoesNotResurrect(t *testing.T) {
	got := mergeSchema(mergeSchema(
		map[string]any{"type": "string"},
		map[string]any{"type": "integer"}),
		map[string]any{"type": "integer"})
	if names := typeNames(got["type"]); len(names) != 0 {
		t.Fatalf("unsatisfiable type resurrected: %v", names)
	}
}

// A sibling arm that constrains the same path with a non-enum keyword still
// makes the failing arm's enum list non-global: "red" satisfies both the enum
// arm and the maxLength arm, so it fails the exactly-one rule. The renderer must
// fall back to the generic message.
func TestExplainSchemaError_SiblingNonEnumConstraintIsAmbiguous(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "green", "yellow"}},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"red"}}}},
			map[string]any{"properties": map[string]any{"x": map[string]any{"maxLength": 3}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/oneOf/0/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("non-enum sibling constraint not treated as ambiguous: %q", msg)
	}
	if !strings.Contains(msg, "wrong type or value") {
		t.Fatalf("non-enum sibling should fall back to generic: %q", msg)
	}
}

// A sibling enum wrapped in its own combinator still constrains the same path,
// so it must be discovered (the literal suffix walk alone would miss it).
func TestExplainSchemaError_SiblingWrappedEnumIsAmbiguous(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "green", "yellow"}},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"red"}}}},
			map[string]any{"allOf": []any{
				map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"red", "blue"}}}},
			}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/oneOf/0/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("combinator-wrapped sibling enum not treated as ambiguous: %q", msg)
	}
	if !strings.Contains(msg, "wrong type or value") {
		t.Fatalf("combinator-wrapped sibling should fall back to generic: %q", msg)
	}
}

// A failing const inside oneOf/anyOf is not rendered as an allowed-value list at
// all (constraintMessage has no const renderer), so the guard is symmetric with
// enum and the message stays generic.
func TestExplainSchemaError_DisjunctiveConstFailureIsGeneric(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string"},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"x": map[string]any{"const": "red"}}},
			map[string]any{"properties": map[string]any{"x": map[string]any{"const": "blue"}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/oneOf/0/properties/x/const")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("const failure rendered an allowed-value list: %q", msg)
	}
	if !strings.Contains(msg, "wrong type or value") {
		t.Fatalf("const failure should be generic: %q", msg)
	}
}

// When the field schema exists only inside a combinator arm (base walk resolves
// nothing), a single node must still be reconciled: its own enum and const
// intersect, so "blue" is not advertised.
func TestExplainSchemaError_SingleNodeEnumConstIsReconciled(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"red", "blue"}, "const": "red"}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "blue"}, "x", "/oneOf/0/properties/x/enum")
	want := `probe_tool: argument "x" is not one of the allowed values: "red". Value is "blue".`
	if msg != want {
		t.Fatalf("single-node enum/const message = %q, want %q", msg, want)
	}
}

// A sibling whose overlapping constraint is nested under an intermediate
// property (properties.obj.allOf[...]) still constrains the same instance path,
// so the failing arm's enum must not be advertised.
func TestExplainSchemaError_DeeplyNestedSiblingIsAmbiguous(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"obj": map[string]any{
				"type":       "object",
				"properties": map[string]any{"x": map[string]any{"enum": []string{"red"}}},
			}}},
			map[string]any{"properties": map[string]any{"obj": map[string]any{
				"type": "object",
				"allOf": []any{
					map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"red", "blue"}}}},
				},
			}}},
		},
	}
	// obj/x exists only inside the branches, so the schema walk cannot resolve
	// the container; it must still be treated as a present-but-invalid value.
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "obj": map[string]any{"x": "yellow"}}, "obj/x", "/oneOf/0/properties/obj/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("deeply nested sibling not treated as ambiguous: %q", msg)
	}
	if strings.Contains(msg, "missing required argument") {
		t.Fatalf("present branch-only value misreported as missing: %q", msg)
	}
}

// A sibling constraint wrapped in `not` still constrains the same path (it
// forbids a value), so the failing arm's enum must not be advertised.
func TestExplainSchemaError_NotWrappedSiblingIsAmbiguous(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "green", "yellow"}},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"red"}}}},
			map[string]any{"not": map[string]any{"properties": map[string]any{"x": map[string]any{"const": "green"}}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/oneOf/0/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("not-wrapped sibling not treated as ambiguous: %q", msg)
	}
}

// Branch-level `required` guidance is not global either: when two oneOf arms
// both require the same property, adding it satisfies both and oneOf still
// fails, so the renderer must fall back to the generic message.
func TestExplainSchemaError_RequiredInsideOverlappingOneOfIsAmbiguous(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task":   map[string]any{"type": "string"},
			"output": map[string]any{"type": "object", "required": []string{"message"}, "properties": map[string]any{"message": map[string]any{"type": "string"}}},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"output": map[string]any{"required": []string{"extra"}}}},
			map[string]any{"properties": map[string]any{"output": map[string]any{"required": []string{"extra"}}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "output": map[string]any{"message": "hi"}}, "output", "/oneOf/0/properties/output/required")
	if strings.Contains(msg, "missing required properties") {
		t.Fatalf("overlapping oneOf required guidance not treated as ambiguous: %q", msg)
	}
	if !strings.Contains(msg, "wrong type or value") {
		t.Fatalf("overlapping oneOf required should be generic: %q", msg)
	}
}

// A property literally named "oneOf" must not be mistaken for a combinator and
// abort the scan before a later real combinator is inspected.
func TestExplainSchemaError_PropertyNamedOneOfDoesNotHideCombinator(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"oneOf": map[string]any{
				"type":       "object",
				"properties": map[string]any{"x": map[string]any{"type": "string", "enum": []string{"red", "green", "yellow"}}},
				"oneOf": []any{
					map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"red"}}}},
					map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"blue"}}}},
				},
			},
		},
		"required": []string{"task"},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "oneOf": map[string]any{"x": "yellow"}}, "oneOf/x", "/properties/oneOf/oneOf/0/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("real combinator after a 'oneOf'-named property was missed: %q", msg)
	}
}

// An enum whose effective type conjunction is unsatisfiable must not be
// advertised as allowed.
func TestExplainSchemaError_UnsatisfiableTypeSuppressesEnum(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "green"}},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"x": map[string]any{"type": "integer", "enum": []string{"red"}}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "red"}, "x", "/oneOf/0/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("unsatisfiable type still advertised enum values: %q", msg)
	}
}

// A pattern constraint this renderer does not model makes the enum list
// incomplete, so the message must fall back rather than name values the pattern
// might reject.
func TestExplainSchemaError_PatternSuppressesEnum(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"a1", "b2"}, "pattern": "^a"},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"a1", "b2"}, "pattern": "^a"}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "b2"}, "x", "/oneOf/0/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("pattern-modelled enum still advertised values: %q", msg)
	}
}

// A numeric path segment is a literal map key when the container is an object;
// only an array treats it as an index.
func TestInstancePathPresent_NumericMapKey(t *testing.T) {
	if !instancePathPresent(map[string]any{"0": "invalid"}, "0") {
		t.Fatal("numeric map key reported absent")
	}
	if !instancePathPresent(map[string]any{"obj": map[string]any{"0": true}}, "obj/0") {
		t.Fatal("nested numeric map key reported absent")
	}
	if !instancePathPresent(map[string]any{"a": []any{"v"}}, "a/0") {
		t.Fatal("array index reported absent")
	}
	if instancePathPresent(map[string]any{"a": []any{"v"}}, "a/1") {
		t.Fatal("out-of-range index reported present")
	}
}

// A sibling constraint wrapped in a double negation still constrains the same
// path, so the failing arm's enum must not be advertised.
func TestExplainSchemaError_DoublyNegatedSiblingIsAmbiguous(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "green", "yellow"}},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"red"}}}},
			map[string]any{"not": map[string]any{"not": map[string]any{"properties": map[string]any{"x": map[string]any{"const": "green"}}}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/oneOf/0/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("doubly-negated sibling not treated as ambiguous: %q", msg)
	}
}

// An enum value whose JSON type the effective schema forbids is not a usable
// suggestion, so the enum guidance must fall back rather than name it.
func TestExplainSchemaError_EnumIncompatibleWithTypeIsSuppressed(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string"},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []any{1}}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": true}, "x", "/oneOf/0/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("type-incompatible enum advertised: %q", msg)
	}
	if !strings.Contains(msg, "wrong type or value") {
		t.Fatalf("type-incompatible enum should be generic: %q", msg)
	}
}

// A constraint referenced beneath `not` still constrains the same path, so it
// must be dereferenced when the negation is expanded; otherwise the failing
// arm's enum is advertised.
func TestExplainSchemaError_NegatedRefSiblingIsAmbiguous(t *testing.T) {
	params := map[string]any{
		"$defs": map[string]any{"yellow": map[string]any{"properties": map[string]any{"x": map[string]any{"const": "yellow"}}}},
		"type":  "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "green", "yellow"}},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"red"}}}},
			map[string]any{"not": map[string]any{"$ref": "#/$defs/yellow"}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/oneOf/0/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("negated $ref sibling not treated as ambiguous: %q", msg)
	}
}

// A numeric property name must resolve to its value through resolveInstanceValue
// too, so the rendered value is not reported as empty.
func TestExplainSchemaError_NumericMapKeyValueRendered(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"0":    map[string]any{"type": "string", "maxLength": 2},
		},
		"required": []string{"task"},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "0": "long"}, "0", "/properties/0/maxLength")
	want := `probe_tool: argument "0" exceeds maxLength (2). Value "long" is 4 characters.`
	if msg != want {
		t.Fatalf("numeric-key value message = %q, want %q", msg, want)
	}
}

// A sibling allOf at the same schema level as the named oneOf still applies, so
// the effective allowed set is its intersection with the arm's — "blue" must not
// be advertised.
func TestExplainSchemaError_SiblingAllOfAtSameLevelIsApplied(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "blue", "green", "yellow"}},
		},
		"required": []string{"task"},
		"allOf": []any{
			map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"red"}}}},
		},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"red", "blue"}}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/oneOf/0/properties/x/enum")
	want := `probe_tool: argument "x" is not one of the allowed values: "red". Value is "yellow".`
	if msg != want {
		t.Fatalf("sibling allOf message = %q, want %q", msg, want)
	}
}

// A sibling anyOf at the same schema level as the named oneOf is a disjunction
// whose accepted set cannot be computed here, so the renderer must fall back to
// generic guidance rather than advertise a per-arm list.
func TestExplainSchemaError_SiblingAnyOfAtSameLevelIsAmbiguous(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "green", "yellow"}},
		},
		"required": []string{"task"},
		"anyOf": []any{
			map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"red", "blue"}}}},
		},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"red", "green"}}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/oneOf/0/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("sibling anyOf advertised a per-arm list: %q", msg)
	}
	if !strings.Contains(msg, "wrong type or value") {
		t.Fatalf("sibling anyOf should fall back to generic: %q", msg)
	}
}

// An enum accompanied by a constraint this renderer does not model (maxLength)
// must not list values that constraint can reject — fall back to generic.
func TestExplainSchemaError_UnmodeledLengthSuppressesEnum(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "maxLength": 2, "enum": []string{"a", "bb", "ccc"}},
		},
		"required": []string{"task"},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "dddd"}, "x", "/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("unmodeled maxLength still advertised enum values: %q", msg)
	}
}

// A numeric bound is likewise unmodeled for enum rendering.
func TestExplainSchemaError_UnmodeledBoundSuppressesEnum(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"n":    map[string]any{"type": "integer", "minimum": 5, "enum": []any{1, 2, 6}},
		},
		"required": []string{"task"},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "n": 1}, "n", "/properties/n/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("unmodeled minimum still advertised enum values: %q", msg)
	}
}

// A sibling oneOf (not the combinator the path names) cannot be conjoined:
// intersecting its arms can yield a value that matches several arms and so fails
// exactly-one. The renderer must fall back to generic guidance instead.
func TestExplainSchemaError_SiblingOneOfIsNotConjoined(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "blue", "green"}},
		},
		"required": []string{"task"},
		"allOf": []any{
			map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"red", "blue", "green"}}}},
		},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"red", "blue"}}}},
			map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"red", "green"}}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/allOf/0/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("sibling oneOf conjoined instead of treated as ambiguous: %q", msg)
	}
}

// An unmodeled if/then/else along the path can depend on the failing field, so
// its presence must make the enum guidance conservative too.
func TestExplainSchemaError_ConditionalAlongPathIsAmbiguous(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "blue", "green"}},
		},
		"required": []string{"task"},
		"allOf": []any{
			map[string]any{
				"if":   map[string]any{"properties": map[string]any{"x": map[string]any{"const": "red"}}},
				"then": map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"red"}}}},
			},
		},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"red", "blue"}}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/oneOf/0/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("conditional along path not treated as ambiguous: %q", msg)
	}
}

// `items` can reject an array enum candidate, so it is an unmodeled constraint
// for enum rendering and must suppress the list.
func TestExplainSchemaError_UnmodeledItemsSuppressesEnum(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "enum": []any{[]any{"bad"}}},
		},
		"required": []string{"task"},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": []any{"bad"}}, "x", "/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("unmodeled items still advertised enum values: %q", msg)
	}
}

// A bare keyword location falls back to the property schema; that fallback must
// still reconcile a same-node enum/const pair.
func TestExplainSchemaError_BareKeywordReconcilesEnumConst(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"enum": []string{"red", "blue"}, "const": "red"},
		},
		"required": []string{"task"},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "blue"}, "x", "enum")
	want := `probe_tool: argument "x" is not one of the allowed values: "red". Value is "blue".`
	if msg != want {
		t.Fatalf("bare-keyword enum/const message = %q, want %q", msg, want)
	}
}

// A root $ref target may carry a sibling oneOf; it must be dereferenced before
// the ambiguity guard inspects the root, or the arm's constraint is dropped when
// the walker descends into properties and the guidance advertises a rejected
// value.
func TestExplainSchemaError_RootRefSiblingOneOfIsAmbiguous(t *testing.T) {
	params := map[string]any{
		"$ref": "#/$defs/root",
		"$defs": map[string]any{
			"root": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"task": map[string]any{"type": "string"},
					"x":    map[string]any{"type": "string", "enum": []string{"red", "blue"}},
				},
				"required": []string{"task"},
				"oneOf": []any{
					map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"red"}}}},
				},
			},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("root-$ref sibling oneOf not treated as ambiguous: %q", msg)
	}
}

// A top-level combinator that does not touch the failing field must not suppress
// its guidance: the enum list is still definitive for that property.
func TestExplainSchemaError_UnrelatedSiblingCombinatorKeepsGuidance(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task":  map[string]any{"type": "string"},
			"color": map[string]any{"type": "string", "enum": []string{"red", "green"}},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"required": []string{"y"}},
			map[string]any{"required": []string{"z"}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "color": "blue"}, "color", "/properties/color/enum")
	want := `probe_tool: argument "color" is not one of the allowed values: "red", "green". Value is "blue".`
	if msg != want {
		t.Fatalf("unrelated sibling combinator suppressed guidance: %q, want %q", msg, want)
	}
}

// A sibling `not` that constrains the failing path forbids a value, so the enum
// guidance must not advertise it.
func TestExplainSchemaError_SiblingNotIsAmbiguous(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "green"}},
		},
		"required": []string{"task"},
		"not":      map[string]any{"properties": map[string]any{"x": map[string]any{"const": "green"}}},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("sibling not constraint ignored: %q", msg)
	}
}

// Combining a $ref target's allOf with the referring node's sibling allOf must
// union the arms, not drop one.
func TestDerefSchemaNode_ConjoinsAllOfArms(t *testing.T) {
	params := map[string]any{
		"$ref":  "#/$defs/t",
		"$defs": map[string]any{"t": map[string]any{"allOf": []any{map[string]any{"required": []string{"extra"}}}}},
		"allOf": []any{map[string]any{"required": []string{"other"}}},
	}
	got := derefSchemaNode(params, params)
	if got == nil {
		t.Fatal("derefSchemaNode returned nil")
	}
	if arms := valueList(got["allOf"]); len(arms) != 2 {
		t.Fatalf("allOf arms = %d, want 2 (target + sibling)", len(arms))
	}
}

// A disjunction nested inside a sibling allOf arm still applies; the scan must
// expand the allOf before inspecting, or its constraint is dropped and "green"
// is advertised.
func TestExplainSchemaError_OneOfInsideSiblingAllOfIsAmbiguous(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "green"}},
		},
		"required": []string{"task"},
		"allOf": []any{
			map[string]any{"oneOf": []any{
				map[string]any{"properties": map[string]any{"x": map[string]any{"const": "red"}}},
			}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("oneOf inside sibling allOf not treated as ambiguous: %q", msg)
	}
}

// A sibling that constrains the path through patternProperties must not be
// ignored; the enum guidance falls back to generic.
func TestExplainSchemaError_PatternPropertiesSiblingIsAmbiguous(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "green"}},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"patternProperties": map[string]any{"^x$": map[string]any{"const": "red"}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("patternProperties sibling not treated as ambiguous: %q", msg)
	}
}

// A conditional nested inside an unselected oneOf arm can accept the suggested
// value, so it must be treated as ambiguous.
func TestExplainSchemaError_ConditionalInsideSiblingArmIsAmbiguous(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "green"}},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"required": []string{"y"}},
			map[string]any{
				"if":   map[string]any{"properties": map[string]any{"x": map[string]any{"const": "red"}}},
				"then": map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"red"}}}},
			},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("conditional inside sibling arm not treated as ambiguous: %q", msg)
	}
}

// A constraint nested under an intermediate property (here patternProperties on
// obj) must be found when resolving a sibling arm, so its rejected value is not
// advertised.
func TestExplainSchemaError_NestedSiblingPatternPropertiesIsAmbiguous(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"obj":  map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "string", "enum": []string{"red", "green"}}}},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"obj": map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"red", "green"}}}}}},
			map[string]any{"properties": map[string]any{"obj": map[string]any{"patternProperties": map[string]any{"^x$": map[string]any{"const": "red"}}}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "obj": map[string]any{"x": "yellow"}}, "obj/x", "/oneOf/0/properties/obj/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("nested sibling patternProperties not treated as ambiguous: %q", msg)
	}
}

// A root if/then about an unrelated property must not suppress specific guidance
// for a field it does not constrain (regression: over-suppression).
func TestExplainSchemaError_UnrelatedConditionalKeepsGuidance(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"name": map[string]any{"type": "string", "maxLength": 5},
		},
		"required": []string{"task"},
		"if":       map[string]any{"properties": map[string]any{"mode": map[string]any{"const": "x"}}},
		"then":     map[string]any{"properties": map[string]any{"mode": map[string]any{"enum": []string{"x"}}}},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "name": "abcdefg"}, "name", "/properties/name/maxLength")
	want := `probe_tool: argument "name" exceeds maxLength (5). Value "abcdefg" is 7 characters.`
	if msg != want {
		t.Fatalf("unrelated conditional suppressed guidance: %q, want %q", msg, want)
	}
}

// dependentSchemas can reject a value when its trigger property is present; a
// sibling arm using it must be treated as constraining.
func TestExplainSchemaError_DependentSchemasSiblingIsAmbiguous(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "green"}},
		},
		"required": []string{"task"},
		"allOf": []any{
			map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"red", "green"}}}},
		},
		"oneOf": []any{
			map[string]any{"required": []string{"other"}},
			map[string]any{"dependentSchemas": map[string]any{"other": map[string]any{"properties": map[string]any{"x": map[string]any{"const": "red"}}}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/allOf/0/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("dependentSchemas sibling not treated as ambiguous: %q", msg)
	}
}

// Merging a $ref target that carries oneOf with a sibling oneOf cannot be
// represented; the result must be marked unmodeled.
func TestDerefSchemaNode_RepeatedOneOfIsMarkedUnmodeled(t *testing.T) {
	params := map[string]any{
		"$ref":  "#/$defs/t",
		"$defs": map[string]any{"t": map[string]any{"oneOf": []any{map[string]any{"required": []string{"a"}}}}},
		"oneOf": []any{map[string]any{"required": []string{"b"}}},
	}
	got := derefSchemaNode(params, params)
	if got == nil || got[unmodeledKey] != true {
		t.Fatalf("repeated oneOf not marked unmodeled: %#v", got)
	}
}

// An allOf arm of `false` makes the schema unsatisfiable, so an enum from
// another branch must not be advertised.
func TestExplainSchemaError_BooleanFalseAllOfSuppressesEnum(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "green"}},
		},
		"required": []string{"task"},
		"allOf":    []any{false},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("unsatisfiable boolean allOf still advertised enum values: %q", msg)
	}
}

// An empty patternProperties pattern matches every property name, so it must be
// treated as constraining the path.
func TestExplainSchemaError_EmptyPatternPropertiesIsAmbiguous(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "green"}},
		},
		"required":          []string{"task"},
		"patternProperties": map[string]any{"": map[string]any{"const": "red"}},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("empty patternProperties pattern ignored: %q", msg)
	}
}

// A boolean `false` then/else branch rejects every instance, so the enum
// guidance must fall back to generic.
func TestExplainSchemaError_BooleanFalseThenSuppressesEnum(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "green"}},
		},
		"required": []string{"task"},
		"if":       map[string]any{},
		"then":     false,
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("boolean false then branch ignored: %q", msg)
	}
}

// A sibling arm that merely declares the property (even with an empty schema)
// still applies to the same path and can make two oneOf arms match.
func TestExplainSchemaError_SiblingEmptyPropertySchemaIsAmbiguous(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "green"}},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"red"}}}},
			map[string]any{"properties": map[string]any{"x": map[string]any{}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/oneOf/0/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("sibling empty property schema not treated as ambiguous: %q", msg)
	}
}

// A sibling arm requiring the path property applies to the same path.
func TestExplainSchemaError_SiblingRequiredPathPropertyIsAmbiguous(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "green"}},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"red"}}}},
			map[string]any{"required": []string{"x"}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/oneOf/0/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("sibling required-property arm not treated as ambiguous: %q", msg)
	}
}

// An unresolved allOf on the field's own schema must suppress specific guidance.
func TestExplainSchemaError_UnresolvedAllOfSuppressesEnum(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"allOf": []any{map[string]any{"enum": []string{"red"}}}},
		},
		"required": []string{"task"},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("unresolved allOf still advertised enum values: %q", msg)
	}
}

// not: true rejects every instance, so a sibling `not` of true must suppress
// enum guidance.
func TestExplainSchemaError_NotTrueIsAmbiguous(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "green"}},
		},
		"required": []string{"task"},
		"allOf": []any{
			map[string]any{"not": true},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("not:true still advertised enum values: %q", msg)
	}
}

// An inert conditional (`if: false` with no else) imposes no constraint, so
// specific enum guidance must survive.
func TestExplainSchemaError_InertConditionalKeepsGuidance(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "green"}},
		},
		"required": []string{"task"},
		"if":       false,
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/properties/x/enum")
	want := `probe_tool: argument "x" is not one of the allowed values: "red", "green". Value is "yellow".`
	if msg != want {
		t.Fatalf("inert conditional suppressed guidance: %q, want %q", msg, want)
	}
}

// A patternProperties pattern that fails to compile is treated as constraining
// (fail closed), so its rejected value is not advertised.
func TestExplainSchemaError_InvalidPatternIsAmbiguous(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "green"}},
		},
		"required":          []string{"task"},
		"patternProperties": map[string]any{"[": map[string]any{"const": "red"}},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("invalid patternProperties pattern ignored: %q", msg)
	}
}

// A boolean false property schema in the base must not be overwritten by a
// branch's schema; the false stays on that child (not the whole map).
func TestMergeSchemaProps_BooleanFalseChildStaysFalse(t *testing.T) {
	base := map[string]any{"properties": map[string]any{"x": false}}
	overlay := map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []any{"red"}}}}
	got := mergeSchema(base, overlay)
	props, _ := got["properties"].(map[string]any)
	if props == nil || props["x"] != false {
		t.Fatalf("boolean-false property not kept on child: %#v", props)
	}
	if props[unmodeledKey] == true {
		t.Fatalf("whole properties map marked unmodeled: %#v", props)
	}
}

// An ancestor-level const sibling overlaps the failing arm's value, so it must
// make the arm's enum guidance non-global.
func TestExplainSchemaError_SiblingConstIsAmbiguous(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "green"}},
		},
		"required": []string{"task"},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []string{"red"}}}},
			map[string]any{"const": map[string]any{"x": "red"}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/oneOf/0/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("sibling const not treated as ambiguous: %q", msg)
	}
}

// A scalar limit alongside an unrelated, unresolved allOf must still render the
// exact limit (the guard is scoped to enum/const/required).
func TestExplainSchemaError_ScalarLimitWithUnrelatedAllOfKeepsLimit(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"name": map[string]any{"type": "string", "maxLength": 5, "allOf": []any{map[string]any{"type": "string"}}},
		},
		"required": []string{"task"},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "name": "abcdefg"}, "name", "/properties/name/maxLength")
	want := `probe_tool: argument "name" exceeds maxLength (5). Value "abcdefg" is 7 characters.`
	if msg != want {
		t.Fatalf("scalar limit degraded by unrelated applicator: %q, want %q", msg, want)
	}
}

// A schema-valued if predicate that depends on the failing path makes a
// restrictive branch potentially active, so guidance must be conservative.
func TestExplainSchemaError_SchemaPredicateConditionalIsAmbiguous(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "green"}},
		},
		"required": []string{"task"},
		"if":       map[string]any{"properties": map[string]any{"x": map[string]any{"const": "red"}}},
		"then":     map[string]any{"not": map[string]any{}},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("schema-valued predicate conditional not treated as ambiguous: %q", msg)
	}
}

// A conditional branch stored as a typed map must still be resolved.
func TestExplainSchemaError_TypedConditionalBranchIsAmbiguous(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "green"}},
		},
		"required": []string{"task"},
		"if":       true,
		"then": map[string]map[string]any{
			"properties": {"x": map[string]any{"const": "red"}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("typed conditional branch ignored: %q", msg)
	}
}

// Forbidding one optional property must not suppress guidance for an unrelated
// property in the same object.
func TestExplainSchemaError_UnrelatedPropertyKeepsGuidanceAfterBooleanMerge(t *testing.T) {
	params := map[string]any{
		"$ref": "#/$defs/t",
		"$defs": map[string]any{"t": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task":     map[string]any{"type": "string"},
				"disabled": false,
				"x":        map[string]any{"type": "string", "enum": []string{"red", "green"}},
			},
			"required": []string{"task"},
		}},
		"properties": map[string]any{"disabled": true},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/properties/x/enum")
	want := `probe_tool: argument "x" is not one of the allowed values: "red", "green". Value is "yellow".`
	if msg != want {
		t.Fatalf("unrelated property guidance lost: %q, want %q", msg, want)
	}
}

// An inert then branch (uniqueItems: false) imposes nothing, so guidance stays.
func TestExplainSchemaError_InertUniqueItemsBranchKeepsGuidance(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "green"}},
		},
		"required": []string{"task"},
		"if":       true,
		"then":     map[string]any{"uniqueItems": false},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/properties/x/enum")
	want := `probe_tool: argument "x" is not one of the allowed values: "red", "green". Value is "yellow".`
	if msg != want {
		t.Fatalf("inert uniqueItems branch suppressed guidance: %q, want %q", msg, want)
	}
}

// An unrestricted then branch (true) imposes nothing, so guidance stays.
func TestExplainSchemaError_UnrestrictedThenKeepsGuidance(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "green"}},
		},
		"required": []string{"task"},
		"if":       map[string]any{"properties": map[string]any{"x": map[string]any{"const": "red"}}},
		"then":     true,
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "yellow"}, "x", "/properties/x/enum")
	want := `probe_tool: argument "x" is not one of the allowed values: "red", "green". Value is "yellow".`
	if msg != want {
		t.Fatalf("unrestricted then suppressed guidance: %q, want %q", msg, want)
	}
}

// An always-false then branch (not: {}) rejects every instance, so guidance must
// fall back to generic.
func TestExplainSchemaError_AlwaysFalseThenIsAmbiguous(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"x":    map[string]any{"type": "string", "enum": []string{"red", "green"}},
		},
		"required": []string{"task"},
		"if":       map[string]any{"properties": map[string]any{"x": map[string]any{"const": "red"}}},
		"then":     map[string]any{"not": map[string]any{}},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "t", "x": "blue"}, "x", "/properties/x/enum")
	if strings.Contains(msg, "allowed values") {
		t.Fatalf("always-false then branch not treated as ambiguous: %q", msg)
	}
}
