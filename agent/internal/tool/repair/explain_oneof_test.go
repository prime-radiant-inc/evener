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

// Issue #618: a oneOf-branch failure carries no property in its instance
// location (the deepest cause is #/oneOf/0/not), so the explained message must
// not attribute the failure to a missing required argument. The task field is
// present and valid.
func TestExplainSchemaError_OneOfConstraintDoesNotReportMissingArgument(t *testing.T) {
	msg := ExplainSchemaError("delegate", delegateOneOfParams(), delegateOneOfArgs(), "", "not")
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
	msg := ExplainSchemaError("delegate", params, args, "", "not")
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
	msg := ExplainSchemaError("delegate", params, args, "", "not")
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
	msg := ExplainSchemaError("delegate", params, args, "", "not")
	if !strings.Contains(msg, `"n" must be one of 1, 2, 3`) {
		t.Fatalf("message must render branch 1's typed []int enum allowed values bare: %q", msg)
	}
}

func TestExplainSchemaError_OneOfBranchNamesTypedBoolSliceEnumValues(t *testing.T) {
	params := delegateOneOfEnumParams("flag", "boolean", []bool{true, false})
	args := map[string]any{"task": "ping", "flag": "true"}
	msg := ExplainSchemaError("delegate", params, args, "", "not")
	if !strings.Contains(msg, `"flag" must be one of true, false`) {
		t.Fatalf("message must render branch 1's typed []bool enum allowed values bare: %q", msg)
	}
}
