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
