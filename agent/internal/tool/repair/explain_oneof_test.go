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

// delegateOneOfIntEnumParams mirrors delegateOneOfParams but the second
// branch requires an integer-enum property ("n") instead of a string-enum
// one. Enum values are float64, as they are when a tool schema is
// JSON-decoded into map[string]any.
func delegateOneOfIntEnumParams() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"n":    map[string]any{"type": "integer", "enum": []any{float64(1), float64(2), float64(3)}},
		},
		"required": []any{"task"},
		"oneOf": []any{
			map[string]any{"not": map[string]any{"required": []string{"n"}}},
			map[string]any{
				"required": []string{"n"},
				"properties": map[string]any{
					"n": map[string]any{"enum": []any{float64(1), float64(2), float64(3)}},
				},
			},
		},
	}
}

func delegateOneOfIntEnumArgs() map[string]any {
	return map[string]any{
		"task": "ping",
		"n":    float64(5),
	}
}

// Issue #625 case 2: branchRequirement used asStringSlice to render a
// branch's enum-constrained properties, which silently dropped non-string
// enum values. An integer-enum branch requirement rendered as `send all of
// "n"` with no allowed-values clause at all. The branch requirement must
// name the allowed values regardless of their JSON type — bare (unquoted),
// per the adversarial review's F1: quoting a number here would visually
// assert a JSON string and coach a retry with {"n": "1"}, which fails
// validation again the same way.
func TestExplainSchemaError_OneOfBranchNamesIntegerEnumValues(t *testing.T) {
	msg := ExplainSchemaError("delegate", delegateOneOfIntEnumParams(), delegateOneOfIntEnumArgs(), "", "not")
	if !strings.Contains(msg, `"n" must be one of 1, 2, 3`) {
		t.Fatalf("message must render branch 1's integer enum allowed values bare (not quoted): %q", msg)
	}
}

// delegateOneOfBoolEnumParams mirrors delegateOneOfIntEnumParams but with a
// boolean-enum property ("flag") — the exact shape the adversarial review's
// F1 finding traced through the real joinQuoted/branchRequirement bodies to
// demonstrate the quoting defect (a boolean rendered as "true", "false"
// reads as a JSON string enum, not a JSON boolean one).
func delegateOneOfBoolEnumParams() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"flag": map[string]any{"type": "boolean", "enum": []any{true, false}},
		},
		"required": []any{"task"},
		"oneOf": []any{
			map[string]any{"not": map[string]any{"required": []string{"flag"}}},
			map[string]any{
				"required": []string{"flag"},
				"properties": map[string]any{
					"flag": map[string]any{"enum": []any{true, false}},
				},
			},
		},
	}
}

func delegateOneOfBoolEnumArgs() map[string]any {
	return map[string]any{
		"task": "ping",
		"flag": "true",
	}
}

// Adversarial review of issue #625, F1: branchRequirement quoted every enum
// value regardless of JSON type, so a boolean-enum branch requirement
// rendered `"flag" must be one of "true", "false"` — indistinguishable from
// how the same code renders a string enum, even though the schema requires
// a bare JSON boolean. The branch requirement must render bare true/false.
func TestExplainSchemaError_OneOfBranchNamesBooleanEnumValues(t *testing.T) {
	msg := ExplainSchemaError("delegate", delegateOneOfBoolEnumParams(), delegateOneOfBoolEnumArgs(), "", "not")
	if !strings.Contains(msg, `"flag" must be one of true, false`) {
		t.Fatalf("message must render branch 1's boolean enum allowed values bare (not quoted): %q", msg)
	}
	if strings.Contains(msg, `"true", "false"`) {
		t.Fatalf("message quoted the boolean enum values, visually asserting the wrong JSON type: %q", msg)
	}
}
