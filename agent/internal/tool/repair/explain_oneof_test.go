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

// delegateOneOfEnumParams mirrors delegateOneOfParams with a single
// non-string-enum property in place of the string-enum "sandbox": the
// second oneOf branch requires prop and constrains it to enum. Integer enum
// values are float64, as they are when a tool schema is JSON-decoded into
// map[string]any.
func delegateOneOfEnumParams(prop, typ string, enum []any) map[string]any {
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
