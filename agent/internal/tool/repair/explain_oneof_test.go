package repair

import (
	"strings"
	"testing"
)

// delegateOneOfParams mirrors the delegate tool schema built by
// DefDelegateWithSandbox on a host with a sandbox backend when the parent
// session runs sandbox mode off (RequireNonOffModeForNetwork): the oneOf
// constraint forbids pairing sandbox "off" with sandbox_net.
func delegateOneOfParams() map[string]any {
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
			map[string]any{"not": map[string]any{"required": []string{"sandbox_net"}}},
			map[string]any{
				"required": []string{"sandbox", "sandbox_net"},
				"properties": map[string]any{
					"sandbox": map[string]any{"enum": []string{"read-only", "workspace-write", "restricted"}},
				},
			},
		},
	}
}

func delegateOneOfArgs() map[string]any {
	return map[string]any{
		"task":        "ping",
		"sandbox":     "off",
		"sandbox_net": true,
	}
}

// delegateOneOfSwappedParams is delegateOneOfParams with the two oneOf arms
// swapped — the positive arm first, the `not` arm second. This is the arm
// order issue #621 reports: the deepest first cause is now arm-internal
// (/oneOf/0/properties/sandbox/enum), so a caller that reads its last segment
// would attribute the failure to "enum" and render the top-level sandbox enum
// — which lists "off" as allowed — instead of the branch's narrowed enum.
func delegateOneOfSwappedParams() map[string]any {
	params := delegateOneOfParams()
	params["oneOf"] = []any{
		map[string]any{
			"required": []string{"sandbox", "sandbox_net"},
			"properties": map[string]any{
				"sandbox": map[string]any{"enum": []string{"read-only", "workspace-write", "restricted"}},
			},
		},
		map[string]any{"not": map[string]any{"required": []string{"sandbox_net"}}},
	}
	return params
}

// Issue #621: with the positive arm ordered first, the deepest first cause is
// arm-internal, and its bare "enum" keyword must not be read against the
// top-level sandbox property — that announces `"off" is not one of the allowed
// values: off, read-only, workspace-write, restricted`, an allowed-values list
// that literally contains the rejected value. The message must render the
// branch-level pairing rule and its narrowed enum instead.
func TestExplainSchemaError_OneOfPositiveArmFirstDoesNotRenderTopLevelEnum(t *testing.T) {
	msg := ExplainSchemaError("delegate", delegateOneOfSwappedParams(), delegateOneOfArgs(), "sandbox", "oneOf/0/properties/sandbox/enum")
	if strings.Contains(msg, "is not one of the allowed values") {
		t.Fatalf("branch-level enum failure rendered as a top-level allowed-values violation (issue #621): %q", msg)
	}
	// The top-level sandbox enum starts "off", which the failing branch narrows
	// away; the message must not reproduce that list.
	if strings.Contains(msg, `"off", "read-only"`) {
		t.Fatalf("message lists the top-level sandbox enum instead of the branch's (issue #621): %q", msg)
	}
	if !strings.Contains(msg, "oneOf constraint") {
		t.Fatalf("message must name the oneOf constraint, not the arm-internal leaf: %q", msg)
	}
	if !strings.Contains(msg, `"sandbox" must be one of "read-only", "workspace-write", "restricted"`) {
		t.Fatalf("message must render the branch's narrowed sandbox enum: %q", msg)
	}
	if !strings.Contains(msg, "sandbox_net") {
		t.Fatalf("message must name the constrained pairing field sandbox_net: %q", msg)
	}
}

// Issue #618: a oneOf-branch failure carries no property in its instance
// location (the deepest cause is /oneOf/0/not), so the explained message must
// not attribute the failure to a missing required argument. The task field is
// present and valid. branchCombinator attributes that cause to the enclosing
// oneOf (issue #621), which is the location the real caller passes here.
func TestExplainSchemaError_OneOfConstraintDoesNotReportMissingArgument(t *testing.T) {
	msg := ExplainSchemaError("delegate", delegateOneOfParams(), delegateOneOfArgs(), "", "/oneOf/0/not")
	if strings.Contains(msg, "arguments did not match the schema") {
		t.Fatalf("oneOf failure explained as generic schema mismatch: %q", msg)
	}
	if strings.Contains(msg, "Required arguments: task") {
		t.Fatalf("oneOf failure misattributed to missing 'task' (issue #618): %q", msg)
	}
	if !strings.Contains(msg, "oneOf constraint") {
		t.Fatalf("message must name the oneOf constraint, not the leaf 'not' keyword: %q", msg)
	}
	if !strings.Contains(msg, `"read-only"`) {
		t.Fatalf("message must render the branch-1 enum token: %q", msg)
	}
	if !strings.Contains(msg, "sandbox_net") {
		t.Fatalf("message must name the constrained field sandbox_net: %q", msg)
	}
}

// multipleMatchOneOfParams is the schema from issue #623: two oneOf arms that
// both require the same property, so an argument object supplying it matches
// both. The validator's failing cause is the bare /oneOf node with no per-arm
// child causes (contrast the no-match delegate shape, whose /oneOf carries one
// child per failing arm).
func multipleMatchOneOfParams() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"a": map[string]any{"type": "string"}},
		"oneOf": []any{
			map[string]any{"required": []any{"a"}},
			map[string]any{"required": []any{"a"}},
		},
	}
}

// Issue #623: a oneOf failure caused by matching MORE THAN ONE arm (oneOf means
// exactly-one) must not enumerate branch requirements the arguments already
// satisfy — a model cannot infer what to change from satisfied requirements.
// The bare /oneOf cause has no failing arm to describe, so the message must
// name the over-match and the actual recovery instead, and must not append an
// Example (minimalExample ignores oneOf, so it would print an object matching
// zero branches).
func TestExplainSchemaError_MultipleMatchOneOfNamesOverMatch(t *testing.T) {
	args := map[string]any{"a": "x"}
	msg := ExplainSchemaError("probe_tool", multipleMatchOneOfParams(), args, "", "/oneOf")
	if strings.Contains(msg, "Branch 0 requires") || strings.Contains(msg, "Branch 1 requires") {
		t.Fatalf("multiple-match oneOf rendered branch requirements the args already satisfy (issue #623): %q", msg)
	}
	if !strings.Contains(msg, "matched more than one branch") {
		t.Fatalf("message must name the over-match, not just the constraint: %q", msg)
	}
	if !strings.Contains(msg, "satisfy exactly one") {
		t.Fatalf("message must give the recovery direction (satisfy exactly one branch): %q", msg)
	}
	if strings.Contains(msg, "Example:") {
		t.Fatalf("over-match message must not append an example that matches zero branches (roborev finding 2): %q", msg)
	}
}

// A nested oneOf whose first failing arm contains an over-matching oneOf makes
// the deepest cause the *inner* combinator ("/oneOf/0/oneOf"), not the
// top-level one. The outer oneOf matched zero branches, so claiming an
// over-match would be false (roborev finding 1); the message must keep
// enumerating the outer branches it can describe instead.
func TestExplainSchemaError_NestedOneOfNoMatchDoesNotClaimOverMatch(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"oneOf": []any{
			map[string]any{"oneOf": []any{
				map[string]any{"required": []any{"a"}},
				map[string]any{"required": []any{"a"}},
			}},
			map[string]any{"required": []any{"b"}},
		},
	}
	args := map[string]any{"a": "x"}
	msg := ExplainSchemaError("probe_tool", params, args, "", "/oneOf/0/oneOf")
	if strings.Contains(msg, "matched more than one branch") {
		t.Fatalf("nested oneOf failure claimed the outer branches matched more than one (roborev finding 1): %q", msg)
	}
	if !strings.Contains(msg, `send all of "b"`) {
		t.Fatalf("outer no-match must still describe its describable branch requirement: %q", msg)
	}
	if strings.Contains(msg, "Example:") {
		t.Fatalf("nested oneOf no-match must not append an example matching zero branches (roborev follow-up): %q", msg)
	}
}

// A bare keyword ("oneOf") is a root-level location: the over-match shape is
// recognized when the caller passes either form.
func TestExplainSchemaError_MultipleMatchOneOfAcceptsBareKeyword(t *testing.T) {
	args := map[string]any{"a": "x"}
	msg := ExplainSchemaError("probe_tool", multipleMatchOneOfParams(), args, "", "oneOf")
	if !strings.Contains(msg, "matched more than one branch") {
		t.Fatalf("bare root-level keyword must still detect the over-match: %q", msg)
	}
}

// delegateOneOfEnumParams mirrors delegateOneOfParams with a single
// non-string-enum property in place of the string-enum "sandbox": the
// second oneOf branch requires prop and constrains it to enum. enum takes
// any slice or array type: []any (integer enum values are float64, as they
// are when a tool schema is JSON-decoded into map[string]any) or a
// genuinely typed slice like []int or []bool, which schema compilation
// accepts and preserves through cloning just the same.
func delegateOneOfEnumParams(prop, typ string, enum any) map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			prop:   map[string]any{"type": typ, "enum": enum},
		},
		"required": []any{"task"},
		"oneOf": []any{
			map[string]any{"not": map[string]any{"required": []string{prop}}},
			map[string]any{
				"required": []string{prop},
				"properties": map[string]any{
					prop: map[string]any{"enum": enum},
				},
			},
		},
	}
}

// Issue #625 case 2: a branch requirement must name its enum's allowed
// values whatever their JSON type, and render them as that type. Bare, for
// a number: quoting one would assert a JSON string and coach a retry with
// {"n": "1"}, which fails validation again the same way.
func TestExplainSchemaError_OneOfBranchNamesIntegerEnumValues(t *testing.T) {
	params := delegateOneOfEnumParams("n", "integer", []any{float64(1), float64(2), float64(3)})
	args := map[string]any{"task": "ping", "n": float64(5)}
	msg := ExplainSchemaError("delegate", params, args, "", "/oneOf/0/not")
	if !strings.Contains(msg, `"n" must be one of 1, 2, 3`) {
		t.Fatalf("message must render branch 1's integer enum allowed values bare (not quoted): %q", msg)
	}
}

// A boolean enum renders bare too. Quoted, `"flag" must be one of "true",
// "false"` is indistinguishable from how the same code renders a string
// enum, though the schema requires a JSON boolean.
func TestExplainSchemaError_OneOfBranchNamesBooleanEnumValues(t *testing.T) {
	params := delegateOneOfEnumParams("flag", "boolean", []any{true, false})
	args := map[string]any{"task": "ping", "flag": "true"}
	msg := ExplainSchemaError("delegate", params, args, "", "/oneOf/0/not")
	if !strings.Contains(msg, `"flag" must be one of true, false`) {
		t.Fatalf("message must render branch 1's boolean enum allowed values bare (not quoted): %q", msg)
	}
	if strings.Contains(msg, `"true", "false"`) {
		t.Fatalf("message quoted the boolean enum values, visually asserting the wrong JSON type: %q", msg)
	}
}

// A genuinely typed Go slice (not []any) must name its branch's allowed
// values the same as its []any equivalent above: schema compilation accepts
// this shape and preserves it through cloning, so it reaches branchRequirement
// still typed.
func TestExplainSchemaError_OneOfBranchNamesTypedIntSliceEnumValues(t *testing.T) {
	params := delegateOneOfEnumParams("n", "integer", []int{1, 2, 3})
	args := map[string]any{"task": "ping", "n": 5}
	msg := ExplainSchemaError("delegate", params, args, "", "/oneOf/0/not")
	if !strings.Contains(msg, `"n" must be one of 1, 2, 3`) {
		t.Fatalf("message must render branch 1's typed []int enum allowed values bare: %q", msg)
	}
}

func TestExplainSchemaError_OneOfBranchNamesTypedBoolSliceEnumValues(t *testing.T) {
	params := delegateOneOfEnumParams("flag", "boolean", []bool{true, false})
	args := map[string]any{"task": "ping", "flag": "true"}
	msg := ExplainSchemaError("delegate", params, args, "", "/oneOf/0/not")
	if !strings.Contains(msg, `"flag" must be one of true, false`) {
		t.Fatalf("message must render branch 1's typed []bool enum allowed values bare: %q", msg)
	}
}

// TestBranchCombinator_Attribution pins the location-based branch attribution
// directly: only a root-level oneOf or `not` (optionally behind a $ref
// wrapper) is attributed to the combinator. Everything else — a nested
// combinator, a root anyOf/allOf, an ordinary keyword — returns "" and keeps
// its leaf keyword, so the explainer never treats it as a branch.
func TestBranchCombinator_Attribution(t *testing.T) {
	for kwLoc, want := range map[string]string{
		"oneOf/0/properties/sandbox/enum": "oneOf", // issue #621: arm-internal leaf
		"/oneOf/0/not":                    "oneOf", // issue #618 shape
		"/oneOf":                          "oneOf", // bare oneOf (multiple-match)
		"oneOf":                           "oneOf",
		"/oneOf/0/oneOf":                  "oneOf", // nested oneOf keeps outer enumeration
		"/$ref/oneOf/0/required":          "oneOf", // ref-wrapped oneOf
		"/oneOf/0/properties/opts/properties/status/enum": "oneOf", // attribution is location-based; branchCoversFailure decides rendering
		"/not":                          "not", // root-level not
		"not":                           "not",
		"/properties/x/not":             "", // nested/leaf keep their leaf keyword
		"/properties/opts/oneOf/0/type": "",
		"/anyOf/0/type":                 "",
		"/allOf/1/enum":                 "",
		"/properties/x/maxLength":       "",
		"/required":                     "",
		"":                              "",
	} {
		if got := branchCombinator(kwLoc); got != want {
			t.Errorf("branchCombinator(%q) = %q, want %q", kwLoc, got, want)
		}
	}
}

// rootNotWithOneOfParams has a top-level oneOf the call SATISFIES and a
// top-level `not` the call violates.
func rootNotWithOneOfParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"a": map[string]any{"type": "string"},
			"b": map[string]any{"type": "string"},
			"c": map[string]any{"type": "string"},
		},
		"required": []any{"a"},
		"oneOf":    []any{map[string]any{"required": []any{"a"}}, map[string]any{"required": []any{"b"}}},
		"not":      map[string]any{"required": []any{"c"}},
	}
}

// follow-up: a root-level `not` failure must not be explained
// with the sibling oneOf's branch requirements — the oneOf can pass while the
// `not` fails, so that message would be actively false. It describes its own
// forbidden-property rule instead.
func TestExplainSchemaError_RootNotDoesNotBorrowSiblingOneOf(t *testing.T) {
	args := map[string]any{"a": "x", "c": "y"}
	msg := ExplainSchemaError("probe_tool", rootNotWithOneOfParams(), args, "", "/not")
	if strings.Contains(msg, "oneOf constraint") {
		t.Fatalf("root-level not failure explained with the call's satisfied oneOf branches: %q", msg)
	}
	if strings.Contains(msg, `send all of "a"`) {
		t.Fatalf("root-level not failure borrowed the sibling oneOf's branch requirements: %q", msg)
	}
	if !strings.Contains(msg, `do not send "c"`) {
		t.Fatalf("root-level not failure must describe its own forbidden property: %q", msg)
	}
}

// refWrappedOneOfParams hides the oneOf behind a $ref, so branchList cannot
// read the branches off the top level.
func refWrappedOneOfParams() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"a": map[string]any{"type": "string"}},
		"required":   []any{"a"},
		"$ref":       "#/$defs/rule",
		"$defs": map[string]any{"rule": map[string]any{"oneOf": []any{
			map[string]any{"required": []any{"zzz"}},
			map[string]any{"required": []any{"qqq"}},
		}}},
	}
}

// follow-up: a oneOf behind a $ref wrapper is still
// attributed to its combinator, but its branch list cannot be resolved — the
// message must stay a generic mismatch and must not fall through to the
// missing-field path, which would list the supplied `a` as required.
func TestExplainSchemaError_RefWrappedOneOfDoesNotClaimRequired(t *testing.T) {
	args := map[string]any{"a": "x"}
	msg := ExplainSchemaError("probe_tool", refWrappedOneOfParams(), args, "", "/$ref/oneOf/0/required")
	if !strings.Contains(msg, "arguments did not match the schema") {
		t.Fatalf("$ref-wrapped oneOf failure must explain the generic mismatch: %q", msg)
	}
	if strings.Contains(msg, "Required arguments") {
		t.Fatalf("$ref-wrapped oneOf failure claims supplied arguments are required: %q", msg)
	}
	// minimalExample renders only top-level required fields,
	// which need not satisfy any branch of the referenced oneOf, so the
	// fallback must not suggest one.
	if strings.Contains(msg, "Example:") {
		t.Fatalf("$ref-wrapped oneOf fallback emitted an example that satisfies no branch: %q", msg)
	}
}

// refWrappedWithSiblingOneOfParams hides one oneOf behind a $ref while another
// oneOf sits at the top level sharing the keyword.
func refWrappedWithSiblingOneOfParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"a": map[string]any{"type": "string"},
			"b": map[string]any{"type": "string"},
		},
		"required": []any{"a"},
		"$ref":     "#/$defs/rule",
		"$defs": map[string]any{"rule": map[string]any{"oneOf": []any{
			map[string]any{"required": []any{"zzz"}},
		}}},
		"oneOf": []any{map[string]any{"required": []any{"a"}}, map[string]any{"required": []any{"b"}}},
	}
}

// a $ref-wrapped oneOf keeps its branches in the referenced
// schema, so the top-level oneOf is a different, unrelated constraint. The
// message must not describe the sibling's branches.
func TestExplainSchemaError_RefWrappedOneOfIgnoresSiblingOneOf(t *testing.T) {
	msg := ExplainSchemaError("probe_tool", refWrappedWithSiblingOneOfParams(), map[string]any{"a": "x"}, "", "/$ref/oneOf/0/required")
	if strings.Contains(msg, "Branch 0 requires") || strings.Contains(msg, "Branch 1 requires") {
		t.Fatalf("$ref-wrapped oneOf rendered a sibling top-level oneOf's branches: %q", msg)
	}
	if !strings.Contains(msg, "arguments did not match the schema") {
		t.Fatalf("$ref-wrapped oneOf failure must explain the generic mismatch: %q", msg)
	}
	if strings.Contains(msg, "Example:") {
		t.Fatalf("$ref-wrapped oneOf fallback emitted an example: %q", msg)
	}
}

// armInternalTypeParams is a root oneOf whose single arm requires a present
// property and constrains that property's type.
func armInternalTypeParams() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"x": map[string]any{"type": "string"}},
		"oneOf": []any{
			map[string]any{
				"required":   []any{"x"},
				"properties": map[string]any{"x": map[string]any{"type": "integer"}},
			},
		},
	}
}

// an arm-internal property constraint is a defect in a
// single argument, which the branch prose never names — branchRequirement
// renders only required/enum. Claiming the failure is "not any single
// argument's type or value" while showing a requirement the call already
// satisfied ("send all of \"x\"" when x was sent) is worse than the
// present-field message, which names the property and its defect.
func TestExplainSchemaError_ArmInternalTypeNamesTheProperty(t *testing.T) {
	msg := ExplainSchemaError("probe_tool", armInternalTypeParams(), map[string]any{"x": "abc"}, "x", "oneOf/0/properties/x/type")
	if strings.Contains(msg, "Branch 0 requires") {
		t.Fatalf("arm-internal type constraint rendered as a branch requirement the call already satisfied: %q", msg)
	}
	if !strings.Contains(msg, `argument "x" has the wrong type or value`) {
		t.Fatalf("arm-internal type constraint must name the offending property and its defect: %q", msg)
	}
}

// armNestedRequiredParams is a root oneOf whose arm requires an object property
// that itself has required keys.
func armNestedRequiredParams() map[string]any {
	opts := func(required []any) map[string]any {
		return map[string]any{
			"type":       "object",
			"required":   required,
			"properties": map[string]any{"x": map[string]any{"type": "string"}},
		}
	}
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"opts": opts([]any{"x"})},
		"oneOf": []any{
			map[string]any{
				"required":   []any{"opts"},
				"properties": map[string]any{"opts": opts([]any{"x"})},
			},
		},
	}
}

// a required nested inside an arm property is the same
// suppression — the arm prose renders the ARM's own required list ("send all of
// \"opts\""), which the call already satisfied, not opts's missing key. It must
// fall through to the present-field path, which names the property.
func TestExplainSchemaError_ArmNestedRequiredNamesTheProperty(t *testing.T) {
	msg := ExplainSchemaError("probe_tool", armNestedRequiredParams(), map[string]any{"opts": map[string]any{}}, "opts", "oneOf/0/properties/opts/required")
	if strings.Contains(msg, "Branch 0 requires") {
		t.Fatalf("arm-nested required rendered as a branch requirement the call already satisfied: %q", msg)
	}
	if !strings.Contains(msg, "opts") {
		t.Fatalf("arm-nested required must name the offending property: %q", msg)
	}
}

// armEnumCaseParams builds a root oneOf whose single arm narrows an enum on the
// named property, while the TOP-LEVEL property carries a WIDER enum that
// contains the rejected value — the shape where reading the failure against the
// top-level property announces an allowed-values list that includes the value
// it calls disallowed.
func armEnumCaseParams(prop string, armRequires bool, nested bool) map[string]any {
	armEnum := []any{"a"}
	topEnum := []any{"a", "b"}
	armProp := map[string]any{"enum": armEnum}
	if nested {
		armProp = map[string]any{"properties": map[string]any{"status": map[string]any{"enum": armEnum}}}
	}
	arm := map[string]any{"properties": map[string]any{prop: armProp}}
	if armRequires {
		arm["required"] = []any{prop}
	}
	topProp := map[string]any{"type": "string", "enum": topEnum}
	if nested {
		topProp = map[string]any{"type": "object", "properties": map[string]any{"status": map[string]any{"type": "string", "enum": topEnum}}}
	}
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{prop: topProp},
		"oneOf":      []any{arm},
	}
}

// branchRequirement renders an enum only for a property the
// ARM lists as required, so an enum deeper in the arm, or on a property the arm
// does not require, is not covered by the branch prose. Taking the early return
// would name a requirement the call already satisfied, but falling through to
// constraintMessage reads the enum against the TOP-LEVEL property — often a
// wider list containing the rejected value — and reproduces the #621 lie. The
// unrenderable arm enum must therefore stay a generic property-level message.
func TestExplainSchemaError_ArmEnumNotRenderedByBranchFallsThrough(t *testing.T) {
	cases := map[string]struct {
		params map[string]any
		args   map[string]any
		field  string
		loc    string
	}{
		"enum deeper in the arm": {
			params: armEnumCaseParams("opts", true, true),
			args:   map[string]any{"opts": map[string]any{"status": "b"}},
			field:  "opts/status",
			loc:    "oneOf/0/properties/opts/properties/status/enum",
		},
		"enum on a non-required arm property": {
			params: armEnumCaseParams("x", false, false),
			args:   map[string]any{"x": "b"},
			field:  "x",
			loc:    "oneOf/0/properties/x/enum",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			msg := ExplainSchemaError("probe_tool", tc.params, tc.args, tc.field, tc.loc)
			if strings.Contains(msg, "Branch 0 requires") {
				t.Fatalf("enum the branch prose does not render was explained as a branch requirement the call already satisfied: %q", msg)
			}
			// The top-level enum is wider than the arm's and contains the
			// rejected value, so rendering it would announce a list that
			// includes the value it calls disallowed.
			if strings.Contains(msg, "is not one of the allowed values") {
				t.Fatalf("unrenderable arm enum printed the top-level allowed-values list (issue #621 misdirection): %q", msg)
			}
			leaf := tc.field[strings.IndexAny(tc.field, "/")+1:]
			if !strings.Contains(msg, leaf) {
				t.Fatalf("message must name the offending property %q: %q", leaf, msg)
			}
		})
	}
}

// refWrappedNotParams hides a root-level `not` behind a $ref.
func refWrappedNotParams() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"a": map[string]any{"type": "string"}},
		"required":   []any{"a"},
		"$ref":       "#/$defs/rule",
		"$defs":      map[string]any{"rule": map[string]any{"not": map[string]any{"required": []any{"a"}}}},
	}
}

// a $ref-wrapped branch failure keeps its structure in the
// referenced schema, so the top-level example is not checked against it, and
// minimalExample can coach an invalid retry. The fallback must be the bare
// generic mismatch — for `not` as well as `oneOf`.
func TestExplainSchemaError_RefWrappedNotOmitsExample(t *testing.T) {
	msg := ExplainSchemaError("probe_tool", refWrappedNotParams(), map[string]any{"a": "x"}, "", "/$ref/not")
	if !strings.Contains(msg, "arguments did not match the schema") {
		t.Fatalf("$ref-wrapped not failure must explain the generic mismatch: %q", msg)
	}
	if strings.Contains(msg, "Example:") {
		t.Fatalf("$ref-wrapped not fallback emitted an unchecked example: %q", msg)
	}
}

// rootNotTwoNamesParams is a root-level `not` whose required list has two names.
func rootNotTwoNamesParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"a": map[string]any{"type": "string"},
			"b": map[string]any{"type": "string"},
		},
		"not": map[string]any{"required": []any{"a", "b"}},
	}
}

// not/required rejects only when EVERY listed property is
// present — {"not": {"required": ["a", "b"]}} accepts a call that sends just
// "a" — so a multi-name list must not be rendered as if each name were
// forbidden individually.
func TestExplainSchemaError_RootNotMultipleNamesStatesTheConjunction(t *testing.T) {
	msg := ExplainSchemaError("probe_tool", rootNotTwoNamesParams(), map[string]any{"a": "x", "b": "y"}, "", "/not")
	if !strings.Contains(msg, `do not send all of "a", "b" together`) {
		t.Fatalf("multi-name not/required must state the conjunction: %q", msg)
	}
	if strings.Contains(msg, `do not send "a"`) || strings.Contains(msg, `do not send "b"`) {
		t.Fatalf("multi-name not/required must not forbid each name individually: %q", msg)
	}
}

// mixedRootNotParams is a root-level `not` that also constrains a value:
// {"mode": "on"} satisfies it, so "do not send mode" would be false.
func mixedRootNotParams() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"mode": map[string]any{"type": "string"}},
		"required":   []any{"mode"},
		"not": map[string]any{
			"required":   []any{"mode"},
			"properties": map[string]any{"mode": map[string]any{"enum": []any{"off"}}},
		},
	}
}

// the presence phrasing is only accurate for a schema that
// constrains nothing but presence. A negated schema that also constrains a
// value must not be rendered as "do not send mode" — that both misstates the
// rule and contradicts the Example (which includes mode).
func TestExplainSchemaError_RootNotWithValueConstraintStaysGeneric(t *testing.T) {
	msg := ExplainSchemaError("probe_tool", mixedRootNotParams(), map[string]any{"mode": "off"}, "", "/not")
	if strings.Contains(msg, "do not send") {
		t.Fatalf("mixed root not rendered an unconditional prohibition: %q", msg)
	}
	if strings.Contains(msg, "Example:") {
		t.Fatalf("mixed root not appended an example contradicting its own advice: %q", msg)
	}
	if !strings.Contains(msg, "arguments did not match the schema") {
		t.Fatalf("mixed root not must fall back to the generic mismatch: %q", msg)
	}
}

// armNotWithEnumParams is a oneOf whose only arm carries a `not` AND narrows a
// required property's enum.
func armNotWithEnumParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"x": map[string]any{"type": "string", "enum": []any{"a", "b"}},
			"y": map[string]any{"type": "string"},
		},
		"oneOf": []any{
			map[string]any{
				"required":   []any{"x"},
				"properties": map[string]any{"x": map[string]any{"enum": []any{"a"}}},
				"not":        map[string]any{"required": []any{"y"}},
			},
		},
	}
}

// an arm carrying both a `not` and a required, enum-narrowed
// property must render BOTH. Rendering only the prohibition hid the failing
// enum (and, when the property was omitted, the missing property itself).
func TestExplainSchemaError_ArmNotRendersRequiredAndProhibition(t *testing.T) {
	t.Run("failing enum is named", func(t *testing.T) {
		msg := ExplainSchemaError("probe_tool", armNotWithEnumParams(), map[string]any{"x": "b"}, "x", "oneOf/0/properties/x/enum")
		if !strings.Contains(msg, `"x" must be one of "a"`) {
			t.Fatalf("arm `not` hid the failing enum: %q", msg)
		}
		if !strings.Contains(msg, `do not send "y"`) {
			t.Fatalf("arm `not` prohibition must still be stated: %q", msg)
		}
	})
	t.Run("omitted required property is named", func(t *testing.T) {
		msg := ExplainSchemaError("probe_tool", armNotWithEnumParams(), map[string]any{}, "", "oneOf/0/required")
		if !strings.Contains(msg, `send all of "x"`) {
			t.Fatalf("arm `not` hid the missing required property: %q", msg)
		}
		if !strings.Contains(msg, `do not send "y"`) {
			t.Fatalf("arm `not` prohibition must still be stated: %q", msg)
		}
	})
}

// stricterLimitParams puts a stricter maxLength on the arm (or referenced
// schema) than on the top-level property of the same name.
func stricterLimitParams(refWrapped bool) map[string]any {
	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{"x": map[string]any{"type": "string", "maxLength": 100}},
	}
	if refWrapped {
		params["required"] = []any{"x"}
		params["$ref"] = "#/$defs/rule"
		params["$defs"] = map[string]any{"rule": map[string]any{"properties": map[string]any{"x": map[string]any{"maxLength": 3}}}}
		return params
	}
	params["oneOf"] = []any{
		map[string]any{"required": []any{"x"}, "properties": map[string]any{"x": map[string]any{"maxLength": 3}}},
	}
	return params
}

// a maxLength reached inside a root-level arm, or behind a
// $ref, can be stricter than the top-level property's. Reading the top-level
// schema would coach "exceeds maxLength (100)" for a value that only exceeds
// the arm's limit of 3.
func TestExplainSchemaError_StricterNestedLimitDoesNotReadTopLevel(t *testing.T) {
	for name, tc := range map[string]struct {
		params map[string]any
		loc    string
		named  bool
	}{
		// An arm is attributed to the branch, so the present-field path still
		// names the property. A $ref cannot be resolved here, so the message
		// is the bare generic mismatch — the referenced schema owns both the
		// property and its guidance.
		"arm":         {stricterLimitParams(false), "oneOf/0/properties/x/maxLength", true},
		"ref-wrapped": {stricterLimitParams(true), "/$ref/properties/x/maxLength", false},
	} {
		t.Run(name, func(t *testing.T) {
			msg := ExplainSchemaError("probe_tool", tc.params, map[string]any{"x": "abcd"}, "x", tc.loc)
			if strings.Contains(msg, "maxLength") {
				t.Fatalf("nested stricter limit was read against the top-level property: %q", msg)
			}
			if tc.named && !strings.Contains(msg, `argument "x"`) {
				t.Fatalf("message must still name the offending property: %q", msg)
			}
			if !tc.named && strings.Contains(msg, "Required arguments") {
				t.Fatalf("unresolved $ref failure must not report top-level required fields: %q", msg)
			}
		})
	}
}

// the root-`not` example is built from the top-level required
// list alone, so a sibling combinator (here a oneOf the example need not
// satisfy) makes it invalid coaching.
func TestExplainSchemaError_RootNotOmitsExampleWithSiblingCombinator(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"a": map[string]any{"type": "string"},
			"b": map[string]any{"type": "string"},
		},
		"required": []any{"a"},
		"oneOf":    []any{map[string]any{"required": []any{"a"}}, map[string]any{"required": []any{"b"}}},
		"not":      map[string]any{"required": []any{"a"}},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"a": "x"}, "", "/not")
	if !strings.Contains(msg, `do not send "a"`) {
		t.Fatalf("root not must still state its prohibition: %q", msg)
	}
	if strings.Contains(msg, "Example:") {
		t.Fatalf("root not emitted an example its sibling combinator can invalidate: %q", msg)
	}
}

// the root-`not` example is the top-level required shape, so
// when that shape contains every forbidden property the example violates the
// very rule being explained. The message must state the rule without one.
func TestExplainSchemaError_RootNotOmitsExampleThatViolatesIt(t *testing.T) {
	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{"a": map[string]any{"type": "string"}},
		"required":   []any{"a"},
		"not":        map[string]any{"required": []any{"a"}},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"a": "x"}, "", "/not")
	if !strings.Contains(msg, `do not send "a"`) {
		t.Fatalf("root not must still state its prohibition: %q", msg)
	}
	if strings.Contains(msg, "Example:") {
		t.Fatalf("root not emitted an example that violates the rule it states: %q", msg)
	}
}

// a root `not` beside a sibling `$ref` also omits the
// example, whose shape the referenced schema can invalidate.
func TestExplainSchemaError_RootNotOmitsExampleWithSiblingRef(t *testing.T) {
	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{"a": map[string]any{"type": "string"}},
		"required":   []any{"a"},
		"not":        map[string]any{"required": []any{"c"}},
		"$ref":       "#/$defs/rule",
		"$defs":      map[string]any{"rule": map[string]any{"properties": map[string]any{"a": map[string]any{"minLength": 2}}}},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"c": "x"}, "", "/not")
	if !strings.Contains(msg, `do not send "c"`) {
		t.Fatalf("root not must still state its prohibition: %q", msg)
	}
	if strings.Contains(msg, "Example:") {
		t.Fatalf("root not emitted an example its sibling $ref can invalidate: %q", msg)
	}
}

// a sibling constraint on one of the example's OWN required
// properties is unmodeled too — minimalExample substitutes "..." regardless, so
// an enum (or length/pattern) on that property makes the example invalid.
func TestExplainSchemaError_RootNotOmitsExampleWithConstrainedRequiredProperty(t *testing.T) {
	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{"a": map[string]any{"type": "string", "enum": []any{"on"}}},
		"required":   []any{"a"},
		"not":        map[string]any{"required": []any{"b"}},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"b": "x"}, "", "/not")
	if !strings.Contains(msg, `do not send "b"`) {
		t.Fatalf("root not must still state its prohibition: %q", msg)
	}
	if strings.Contains(msg, "Example:") {
		t.Fatalf("root not emitted an example its own required property's enum violates: %q", msg)
	}
}

// a reference-wrapped failure whose leaf is not a combinator
// (e.g. /$ref/required) must not fall through to the top-level schema — that
// reports required fields the call already supplied.
func TestExplainSchemaError_RefWrappedNonCombinatorDoesNotReportTopLevelRequired(t *testing.T) {
	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{"a": map[string]any{"type": "string"}, "b": map[string]any{"type": "string"}},
		"required":   []any{"a"},
		"$ref":       "#/$defs/rule",
		"$defs":      map[string]any{"rule": map[string]any{"required": []any{"b"}}},
	}
	// "a" is supplied; the referenced schema requires "b".
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"a": "x"}, "", "/$ref/required")
	if !strings.Contains(msg, "arguments did not match the schema") {
		t.Fatalf("$ref failure must fall back to the generic mismatch: %q", msg)
	}
	if strings.Contains(msg, "Required arguments") {
		t.Fatalf("$ref failure reported the top-level required list: %q", msg)
	}
	if strings.Contains(msg, "Example:") {
		t.Fatalf("$ref failure emitted top-level example guidance: %q", msg)
	}
}

// a `not` that is an arm of a root oneOf keeps its template
// when the top-level required shape satisfies exactly one branch — the case the
// old `keyword == "not"` path used to emit, and a usable retry template here
// ({"task": "..."} meets the `not` arm and misses the positive arm).
func TestExplainSchemaError_OneOfNestedNotKeepsValidExample(t *testing.T) {
	msg := ExplainSchemaError("delegate", delegateOneOfParams(), delegateOneOfArgs(), "", "/oneOf/0/not")
	if !strings.Contains(msg, "oneOf constraint") {
		t.Fatalf("message must still describe the oneOf constraint: %q", msg)
	}
	if !strings.Contains(msg, "Example:") {
		t.Fatalf("a not arm of a root oneOf must keep a template that matches exactly one branch: %q", msg)
	}
}

// the example is emitted only when it satisfies exactly one
// branch. An example matching zero branches, or two, is not a valid template
// and must be omitted.
func TestExplainSchemaError_OneOfExampleOnlyWhenExactlyOneBranchMatches(t *testing.T) {
	zero := map[string]any{
		"type":       "object",
		"properties": map[string]any{"a": map[string]any{"type": "string"}},
		"required":   []any{"a"},
		"oneOf":      []any{map[string]any{"required": []any{"zzz"}}, map[string]any{"required": []any{"qqq"}}},
	}
	if msg := ExplainSchemaError("probe_tool", zero, map[string]any{"a": "x"}, "", "/oneOf/0/required"); strings.Contains(msg, "Example:") {
		t.Fatalf("a no-match oneOf whose example satisfies zero branches must omit it: %q", msg)
	}
	two := map[string]any{
		"type":       "object",
		"properties": map[string]any{"a": map[string]any{"type": "string"}},
		"required":   []any{"a"},
		"oneOf":      []any{map[string]any{"required": []any{"a"}}, map[string]any{"not": map[string]any{"required": []any{"zzz"}}}},
	}
	if msg := ExplainSchemaError("probe_tool", two, map[string]any{"a": "x"}, "", "/oneOf/0/required"); strings.Contains(msg, "Example:") {
		t.Fatalf("a oneOf whose example satisfies two branches must omit it: %q", msg)
	}
}

// An arm bearing a reference defers to the referenced schema, so it cannot be
// judged from presence: its own siblings apply only under some dialects, and
// neither can be described. The example must therefore be withheld rather than
// certified as matching exactly one branch.
func TestExplainSchemaError_RefBearingArmWithholdsExample(t *testing.T) {
	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{"a": map[string]any{"type": "string"}, "b": map[string]any{"type": "boolean"}},
		"required":   []any{"a"},
		"$defs":      map[string]any{"rule": map[string]any{"required": []any{"zzz"}}},
		"oneOf": []any{
			map[string]any{"not": map[string]any{"required": []any{"b"}}},
			map[string]any{"$ref": "#/$defs/rule", "required": []any{"c"}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"a": "x"}, "", "/oneOf/0/not")
	if strings.Contains(msg, "Example:") {
		t.Fatalf("example emitted although a reference-bearing arm cannot be judged: %q", msg)
	}
}

// A property may be named "properties" as well as "$ref": the name is not a
// keyword, so the reference after it is still a reference and the top-level
// sibling limit must not be reported.
func TestExplainSchemaError_PropertyNamedPropertiesKeepsRefDetection(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"properties": map[string]any{"type": "string", "maxLength": 100, "$ref": "#/$defs/t"},
		},
		"required": []any{"properties"},
		"$defs":    map[string]any{"t": map[string]any{"maxLength": 3}},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"properties": "abcd"}, "properties", "/properties/properties/$ref/maxLength")
	if strings.Contains(msg, "maxLength") {
		t.Fatalf("reference after a property named properties was missed, reporting the top-level limit: %q", msg)
	}
	if !strings.Contains(msg, `argument "properties"`) {
		t.Fatalf("message must still name the offending property: %q", msg)
	}
}

// A property may legitimately be named like a reference keyword; only a segment
// in keyword position is a reference, so such a property keeps its own
// constraint message.
func TestExplainSchemaError_PropertyNamedRefIsNotAReference(t *testing.T) {
	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{"$ref": map[string]any{"type": "string", "enum": []any{"a"}}},
		"required":   []any{"$ref"},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"$ref": "b"}, "$ref", "/properties/$ref/enum")
	if !strings.Contains(msg, "is not one of the allowed values") {
		t.Fatalf("a property named $ref must keep its constraint message: %q", msg)
	}
}

// minimalExample always renders an object, so a root that does not accept one
// must not be handed that example.
func TestExplainSchemaError_NonObjectRootOmitsObjectExample(t *testing.T) {
	params := map[string]any{
		"type":  "null",
		"oneOf": []any{map[string]any{"required": []any{}}, map[string]any{"required": []any{"zzz"}}},
	}
	msg := ExplainSchemaError("probe_tool", params, nil, "", "/oneOf/1/required")
	if strings.Contains(msg, "Example:") {
		t.Fatalf("a non-object root must not be handed an object example: %q", msg)
	}
}

// an arm whose `not` constrains a value is not described by
// branchRequirement, so its enums are not rendered either. The coverage check
// must agree, or an enum failure inside such an arm falls to the bare generic
// mismatch — dropping the property name and constraint this change preserves.
func TestExplainSchemaError_EnumInValueConstrainedNotArmNamesTheProperty(t *testing.T) {
	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{"x": map[string]any{"type": "string", "enum": []any{"a", "b"}}, "y": map[string]any{"type": "string"}},
		"oneOf": []any{
			map[string]any{
				"required":   []any{"x"},
				"properties": map[string]any{"x": map[string]any{"enum": []any{"a"}}},
				"not":        map[string]any{"required": []any{"y"}, "properties": map[string]any{"y": map[string]any{"enum": []any{"z"}}}},
			},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"x": "b"}, "x", "oneOf/0/properties/x/enum")
	if msg == "probe_tool: arguments did not match the schema." {
		t.Fatalf("enum failure in a value-constrained-`not` arm degraded to the bare generic mismatch: %q", msg)
	}
	if !strings.Contains(msg, `argument "x"`) {
		t.Fatalf("message must name the failing property: %q", msg)
	}
}

// a root $dynamicRef is a reference too, so — like a sibling
// $ref — it withholds the example, whose shape the referenced schema can
// invalidate.
func TestExplainSchemaError_OneOfExampleOmittedWithSiblingDynamicRef(t *testing.T) {
	params := map[string]any{
		"type":        "object",
		"properties":  map[string]any{"task": map[string]any{"type": "string"}},
		"required":    []any{"task"},
		"$dynamicRef": "#task",
		"$defs":       map[string]any{"t": map[string]any{"$dynamicAnchor": "task", "type": "string", "minLength": 4}},
		"oneOf":       []any{map[string]any{"required": []any{"task"}}, map[string]any{"required": []any{"other"}}},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "abcd"}, "", "/oneOf/1/required")
	if strings.Contains(msg, "Example:") {
		t.Fatalf("example emitted despite a sibling $dynamicRef the example may violate: %q", msg)
	}
}

// a property-level `$ref` reached deeper in the path must not
// be read against the top-level property, whose sibling constraint can state a
// limit the referenced schema does not have.
func TestExplainSchemaError_PropertyRefDoesNotReadTopLevelConstraint(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"x": map[string]any{"type": "string", "maxLength": 100, "$ref": "#/$defs/t"},
		},
		"required": []any{"x"},
		"$defs":    map[string]any{"t": map[string]any{"maxLength": 3}},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"x": "abcd"}, "x", "/properties/x/$ref/maxLength")
	if strings.Contains(msg, "maxLength") {
		t.Fatalf("property-level $ref failure read the top-level sibling limit: %q", msg)
	}
	if strings.Contains(msg, "Example:") {
		t.Fatalf("property-level $ref failure emitted an unverified example: %q", msg)
	}
	if !strings.Contains(msg, `argument "x"`) {
		t.Fatalf("message must still name the offending property: %q", msg)
	}
}

// $dynamicRef and $recursiveRef resolve like $ref, so a
// constraint reached through one is equally unreadable here — it must not be
// reported as a top-level defect nor carry an unverified example.
func TestExplainSchemaError_DynamicRefTreatedAsReference(t *testing.T) {
	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{"task": map[string]any{"$dynamicRef": "#task"}},
		"required":   []any{"task"},
		"$defs":      map[string]any{"task": map[string]any{"type": "string", "minLength": 4}},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "ping"}, "task", "/properties/task/$dynamicRef/minLength")
	if strings.Contains(msg, "minLength") {
		t.Fatalf("$dynamicRef failure stated a constraint from the top-level schema: %q", msg)
	}
	if strings.Contains(msg, "Example:") {
		t.Fatalf("$dynamicRef failure emitted an unverified example: %q", msg)
	}
	if !strings.Contains(msg, `argument "task"`) {
		t.Fatalf("message must still name the offending property: %q", msg)
	}
}

// draft-07 `dependencies` is unmodeled by the top-level
// example, so a schema relying on it must withhold the example.
func TestExplainSchemaError_OneOfExampleOmittedForDependencies(t *testing.T) {
	params := map[string]any{
		"type":         "object",
		"properties":   map[string]any{"a": map[string]any{"type": "string"}, "other": map[string]any{"type": "string"}},
		"required":     []any{"a"},
		"dependencies": map[string]any{"a": []any{"b"}},
		"oneOf":        []any{map[string]any{"required": []any{"a"}}, map[string]any{"required": []any{"other"}}},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"a": "x"}, "", "/oneOf/1/required")
	if strings.Contains(msg, "Example:") {
		t.Fatalf("example emitted despite draft-07 dependencies the example violates: %q", msg)
	}
}

// a defect nested beneath an arm's items is not branch
// structure — the arm prose would tell the caller to supply a field it already
// sent — and the walk cannot resolve the item, so the message must be the
// generic mismatch rather than naming a field that does not exist.
func TestExplainSchemaError_ArmNestedItemDefectStaysGeneric(t *testing.T) {
	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{"xs": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
		"oneOf": []any{
			map[string]any{"required": []any{"xs"}, "properties": map[string]any{"xs": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"xs": []any{"abc"}}, "xs/0", "oneOf/0/properties/xs/items/type")
	if msg != "probe_tool: arguments did not match the schema." {
		t.Fatalf("arm-nested item defect must be the generic mismatch, got: %q", msg)
	}
}

// the `contains` family on a required array property is
// unmodeled — minimalExample renders "[]", which satisfies no contains — so the
// example must be withheld.
func TestExplainSchemaError_OneOfExampleOmittedForContains(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"xs":    map[string]any{"type": "array", "contains": map[string]any{"type": "integer"}},
			"other": map[string]any{"type": "string"},
		},
		"required": []any{"xs"},
		"oneOf":    []any{map[string]any{"required": []any{"xs"}}, map[string]any{"required": []any{"other"}}},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"xs": []any{1}}, "", "/oneOf/1/required")
	if strings.Contains(msg, "Example:") {
		t.Fatalf("example emitted for a required array whose contains it cannot satisfy: %q", msg)
	}
}

// a property-level $ref means the referenced schema owns the
// real constraint, so the present-field fallback must name the property without
// suggesting an example it cannot verify.
func TestExplainSchemaError_PropertyRefOmitsExample(t *testing.T) {
	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{"mode": map[string]any{"$ref": "#/$defs/mode"}},
		"required":   []any{"mode"},
		"$defs":      map[string]any{"mode": map[string]any{"enum": []any{"on"}}},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"mode": "off"}, "mode", "/properties/mode/$ref/enum")
	if strings.Contains(msg, "Example:") {
		t.Fatalf("property-level $ref failure emitted an unverified example: %q", msg)
	}
	if !strings.Contains(msg, `argument "mode"`) {
		t.Fatalf("message must still name the offending property: %q", msg)
	}
}

// a required property typed "null" must render a valid
// placeholder — the generic "..." string asserts the wrong type.
func TestExplainSchemaError_NullTypePlaceholderIsValid(t *testing.T) {
	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{"mode": map[string]any{"type": "null"}},
		"required":   []any{"mode"},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"mode": "x"}, "mode", "/properties/mode/type")
	if !strings.Contains(msg, `Example: {"mode": null}`) {
		t.Fatalf("null-typed property must render a null placeholder: %q", msg)
	}

	// The oneOf path must not withhold a valid null example either.
	params["oneOf"] = []any{map[string]any{"required": []any{"mode"}}, map[string]any{"required": []any{"other"}}}
	msg = ExplainSchemaError("probe_tool", params, map[string]any{"mode": nil}, "", "/oneOf/1/required")
	if !strings.Contains(msg, `Example: {"mode": null}`) {
		t.Fatalf("null-typed property must keep its valid example on the oneOf path: %q", msg)
	}
}

// root `patternProperties` constrains explicitly declared
// properties, so the top-level example can violate it and must be withheld.
func TestExplainSchemaError_OneOfExampleOmittedForRootPatternProperties(t *testing.T) {
	params := map[string]any{
		"type":              "object",
		"properties":        map[string]any{"task": map[string]any{"type": "string"}, "other": map[string]any{"type": "string"}},
		"required":          []any{"task"},
		"patternProperties": map[string]any{"^task$": map[string]any{"minLength": 4}},
		"oneOf":             []any{map[string]any{"required": []any{"task"}}, map[string]any{"required": []any{"other"}}},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"task": "ping"}, "", "/oneOf/1/required")
	if strings.Contains(msg, "Example:") {
		t.Fatalf("example emitted despite root patternProperties the placeholder violates: %q", msg)
	}
}

// a branch-attributed failure the branch prose cannot
// describe falls back to the present-field path, whose example is built from the
// top-level required shape and is not checked against the enclosing combinator —
// for this schema it satisfies no branch. Name the field without one.
func TestExplainSchemaError_BranchFallbackOmitsUncheckedExample(t *testing.T) {
	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{"x": map[string]any{}},
		"oneOf": []any{
			map[string]any{"required": []any{"x"}, "properties": map[string]any{"x": map[string]any{"type": "string"}}},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"x": 5}, "x", "oneOf/0/properties/x/type")
	if strings.Contains(msg, "Example:") {
		t.Fatalf("branch-attributed fallback emitted an example it cannot check: %q", msg)
	}
	if !strings.Contains(msg, `argument "x" has the wrong type or value`) {
		t.Fatalf("fallback must still name the offending property: %q", msg)
	}
}

// root-level assertions the top-level required shape cannot
// be shown to satisfy — object cardinality and property-name rules here — must
// withhold the example rather than suggest an object the schema rejects. Pinned
// with an explicit branch location, since the validator surfaces some of these
// shapes as present-field failures.
func TestExplainSchemaError_OneOfExampleOmittedForUnmodeledRootAssertion(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{
			"type": "object",
			"oneOf": []any{
				map[string]any{"required": []any{"a"}, "oneOf": []any{map[string]any{}, map[string]any{}}},
				map[string]any{"not": map[string]any{"required": []any{"b"}}},
			},
		}
	}
	minProps := base()
	minProps["minProperties"] = 1

	propNames := base()
	propNames["propertyNames"] = map[string]any{"pattern": "^[a-z]+$"}
	propNames["properties"] = map[string]any{"A": map[string]any{"type": "string"}}
	propNames["required"] = []any{"A"}
	propNames["oneOf"] = []any{map[string]any{"required": []any{"A"}}, map[string]any{"required": []any{"other"}}}

	depReq := base()
	depReq["dependentRequired"] = map[string]any{"a": []any{"b"}}

	for name, params := range map[string]map[string]any{
		"minProperties":     minProps,
		"propertyNames":     propNames,
		"dependentRequired": depReq,
	} {
		t.Run(name, func(t *testing.T) {
			msg := ExplainSchemaError("probe_tool", params, map[string]any{"a": 1, "b": 1}, "", "/oneOf/0/required")
			if strings.Contains(msg, "Example:") {
				t.Fatalf("example emitted for a root assertion the example cannot satisfy: %q", msg)
			}
		})
	}
}

// the example gate must see constraints it cannot model on
// the example's required properties — a $ref (unresolvable here) and a nested
// object's own required property — or it appends an example the schema rejects.
func TestExplainSchemaError_OneOfExampleOmitsUnmodeledPropertyConstraint(t *testing.T) {
	cases := map[string]struct {
		params map[string]any
		args   map[string]any
	}{
		"$ref-typed required property": {
			params: map[string]any{
				"type":       "object",
				"properties": map[string]any{"mode": map[string]any{"$ref": "#/$defs/mode"}},
				"required":   []any{"mode"},
				"$defs":      map[string]any{"mode": map[string]any{"enum": []any{"on"}}},
				"oneOf":      []any{map[string]any{"required": []any{"mode"}}, map[string]any{"required": []any{"other"}}},
			},
			args: map[string]any{"mode": "off"},
		},
		"nested object property constraint": {
			params: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"opts": map[string]any{
						"type":       "object",
						"required":   []any{"status"},
						"properties": map[string]any{"status": map[string]any{"enum": []any{"a"}}},
					},
				},
				"required": []any{"opts"},
				"oneOf":    []any{map[string]any{"required": []any{"opts"}}, map[string]any{"required": []any{"other"}}},
			},
			args: map[string]any{"opts": map[string]any{"status": "a"}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			msg := ExplainSchemaError("probe_tool", tc.params, tc.args, "", "/oneOf/1/required")
			if strings.Contains(msg, "Example:") {
				t.Fatalf("example emitted despite an unmodeled constraint on its own property: %q", msg)
			}
		})
	}
}

// an arm whose `not` constrains a value must not be judged
// from presence — that either skips it (letting a two-match example through) or
// counts it wrongly. The example must be withheld when any arm cannot be judged.
func TestExplainSchemaError_ValueConstrainedNotArmIsNotJudged(t *testing.T) {
	cases := map[string]map[string]any{
		"not constrains a value and names a required property": {
			"type":       "object",
			"properties": map[string]any{"mode": map[string]any{"type": "string"}, "extra": map[string]any{"type": "boolean"}},
			"required":   []any{"mode"},
			"oneOf": []any{
				map[string]any{"not": map[string]any{"required": []any{"mode"}, "properties": map[string]any{"mode": map[string]any{"enum": []any{"off"}}}}},
				map[string]any{"not": map[string]any{"required": []any{"extra"}}},
			},
		},
		"not constrains a value with no required list": {
			"type":       "object",
			"properties": map[string]any{"mode": map[string]any{"type": "string"}},
			"required":   []any{"mode"},
			"oneOf": []any{
				map[string]any{"not": map[string]any{"properties": map[string]any{"mode": map[string]any{"enum": []any{"off"}}}}},
				map[string]any{"required": []any{"mode"}},
			},
		},
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			msg := ExplainSchemaError("probe_tool", params, map[string]any{"mode": "off", "extra": true}, "", "/oneOf/0/not")
			if strings.Contains(msg, "Example:") {
				t.Fatalf("example emitted for an arm set it does not provably satisfy: %q", msg)
			}
		})
	}
}

// the guard must match the renderer's expansion depth — a
// two-level nested required object renders as a bare "{}", which the schema
// rejects, so the example must be withheld.
func TestExplainSchemaError_OneOfExampleOmitsDeepNestedRequired(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type":       "object",
				"required":   []any{"inner"},
				"properties": map[string]any{"inner": map[string]any{"type": "object", "required": []any{"id"}, "properties": map[string]any{"id": map[string]any{"type": "string"}}}},
			},
		},
		"required": []any{"opts"},
		"oneOf":    []any{map[string]any{"required": []any{"opts"}}, map[string]any{"required": []any{"other"}}},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"opts": map[string]any{}}, "", "/oneOf/1/required")
	if strings.Contains(msg, "Example:") {
		t.Fatalf("example emitted for a two-level nested required object it cannot render: %q", msg)
	}
}

// the gate must also cover property-count and name
// constraints, and honor a type union the placeholder can resolve.
func TestExplainSchemaError_OneOfExampleGateCoversPropertyCountsAndUnions(t *testing.T) {
	counts := map[string]any{
		"type":       "object",
		"properties": map[string]any{"opts": map[string]any{"type": "object", "minProperties": 1}},
		"required":   []any{"opts"},
		"oneOf":      []any{map[string]any{"required": []any{"opts"}}, map[string]any{"required": []any{"other"}}},
	}
	if msg := ExplainSchemaError("probe_tool", counts, map[string]any{"opts": map[string]any{"a": 1}}, "", "/oneOf/1/required"); strings.Contains(msg, "Example:") {
		t.Fatalf("example emitted despite minProperties on a rendered property: %q", msg)
	}

	union := map[string]any{
		"type":       "object",
		"properties": map[string]any{"n": map[string]any{"type": []any{"integer", "null"}}},
		"required":   []any{"n"},
		"oneOf":      []any{map[string]any{"required": []any{"n"}}, map[string]any{"required": []any{"other"}}},
	}
	msg := ExplainSchemaError("probe_tool", union, map[string]any{"n": 3}, "", "/oneOf/1/required")
	if !strings.Contains(msg, `Example: {"n": 0}`) {
		t.Fatalf("a resolvable type union must render its non-null placeholder: %q", msg)
	}

	unresolvable := map[string]any{
		"type":       "object",
		"properties": map[string]any{"n": map[string]any{"type": []any{"string", "integer"}}},
		"required":   []any{"n"},
		"oneOf":      []any{map[string]any{"required": []any{"n"}}, map[string]any{"required": []any{"other"}}},
	}
	if msg := ExplainSchemaError("probe_tool", unresolvable, map[string]any{"n": 3}, "", "/oneOf/1/required"); strings.Contains(msg, "Example:") {
		t.Fatalf("example emitted for a type union the placeholder cannot render: %q", msg)
	}
}

// an arm with a required list and a value-constrained `not`
// must not summarize the required fields — the failure may be the value, and
// naming already-supplied fields is the misdirection this work removes.
func TestExplainSchemaError_ArmValueConstrainedNotDoesNotNameRequired(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"x":    map[string]any{"type": "string"},
			"mode": map[string]any{"type": "string"},
		},
		"oneOf": []any{
			map[string]any{
				"required": []any{"x"},
				"not":      map[string]any{"properties": map[string]any{"mode": map[string]any{"enum": []any{"off"}}}},
			},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"x": "a", "mode": "off"}, "", "/oneOf/0/not")
	if strings.Contains(msg, `send all of "x"`) {
		t.Fatalf("value-constrained `not` failure named an already-supplied required field: %q", msg)
	}
}

// a bare /$ref location is reference-wrapped too, so it must
// take the generic mismatch rather than top-level required/example guidance.
func TestExplainSchemaError_BareRefLocationStaysGeneric(t *testing.T) {
	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{"a": map[string]any{"type": "string"}},
		"required":   []any{"a"},
		"$ref":       "#/$defs/rule",
		"$defs":      map[string]any{"rule": false},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"a": "x"}, "", "/$ref")
	if !strings.Contains(msg, "arguments did not match the schema") {
		t.Fatalf("bare /$ref failure must fall back to the generic mismatch: %q", msg)
	}
	if strings.Contains(msg, "Required arguments") || strings.Contains(msg, "Example:") {
		t.Fatalf("bare /$ref failure emitted top-level required/example guidance: %q", msg)
	}
}

// refWrappedPresentEnumParams hides an arm that narrows a present property's
// enum behind a $ref, while the top-level property keeps the wider enum.
func refWrappedPresentEnumParams() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"x": map[string]any{"type": "string", "enum": []any{"off", "a"}}},
		"required":   []any{"x"},
		"$ref":       "#/$defs/rule",
		"$defs": map[string]any{"rule": map[string]any{"oneOf": []any{
			map[string]any{
				"required":   []any{"x"},
				"properties": map[string]any{"x": map[string]any{"enum": []any{"a"}}},
			},
		}}},
	}
}

// a present property whose arm-internal enum sits behind a
// $ref must not fall through to constraintMessage — that function reads the
// enum against the TOP-LEVEL property, whose wider list contains the rejected
// value, reproducing the exact #621 misdirection. The $ref gate must fire
// whatever the keyword or presence, so the message stays the generic mismatch.
func TestExplainSchemaError_RefWrappedPresentEnumStaysGeneric(t *testing.T) {
	msg := ExplainSchemaError("probe_tool", refWrappedPresentEnumParams(), map[string]any{"x": "off"}, "x", "/$ref/oneOf/0/properties/x/enum")
	if strings.Contains(msg, "is not one of the allowed values") {
		t.Fatalf("$ref-wrapped arm enum rendered a top-level allowed-values list containing the rejected value: %q", msg)
	}
	if !strings.Contains(msg, "arguments did not match the schema") {
		t.Fatalf("$ref-wrapped arm enum must fall back to the generic mismatch: %q", msg)
	}
}
