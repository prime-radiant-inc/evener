package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/llm"
)

// The delegate sandbox schema now uses a single `sandbox` enum encoding both
// mode and network (e.g. "read-only+nonet") instead of separate `sandbox` +
// `sandbox_net` fields with a oneOf constraint. These tests verify the new
// behavior: a legacy `sandbox_net` field is dropped as unknown by repair (it
// is no longer in the schema), and the handler rejects invalid combos like
// "off+nonet" (off applies no network confinement) with a clear error naming
// the `sandbox` field.

// Test: a delegate call with a legacy `sandbox_net` field should have that
// field dropped by repair (additionalProperties: false), and the call should
// NOT be rejected at prevalidation — the oneOf constraint no longer exists.
func TestPrepareToolCall_DelegateLegacySandboxNetDropped(t *testing.T) {
	def := tool.DefDelegateWithSandbox([]string{"subagent"}, tool.DelegateSandboxSchema{
		Available:   true,
		Modes:       []string{"off", "read-only", "workspace-write", "restricted"},
		SandboxEnum: []string{"off", "read-only", "read-only+nonet", "workspace-write", "workspace-write+nonet", "restricted", "restricted+nonet", "nonet"},
	})
	reg := tool.NewRegistry()
	if err := reg.Register(regTool(def)); err != nil {
		t.Fatalf("register: %v", err)
	}
	rt := reg.Get("delegate")

	call := llm.ToolCallData{
		ID:   "legacy-net",
		Name: "delegate",
		Arguments: json.RawMessage(`{"task":"ping","agent_type":"subagent","isolation":"worktree",` +
			`"sandbox":"off","sandbox_net":true,"reasoning_effort":"low","delegation_allowance":0}`),
	}
	res := prepareToolCall(call, rt, []string{"delegate"}, "delegate", "communicate", "")
	// sandbox_net is no longer in the schema, so it must be dropped — not
	// cause a prevalidation error. The oneOf constraint is gone.
	if res.PrevalErr != "" {
		t.Fatalf("expected no prevalidation failure (oneOf removed), got: %q (changes: %v)", res.PrevalErr, res.Changes)
	}
	// Verify sandbox_net was dropped.
	foundDrop := false
	for _, ch := range res.Changes {
		if strings.Contains(ch.Detail, "sandbox_net") && strings.Contains(ch.Detail, "dropped") {
			foundDrop = true
		}
	}
	if !foundDrop {
		t.Fatalf("expected sandbox_net to be dropped as unknown, changes: %v", res.Changes)
	}
}

// Test: the schema has no oneOf constraint — sandbox_net is not a property.
func TestDelegateSandboxSchemaNoOneOfOrSandboxNet(t *testing.T) {
	def := tool.DefDelegateWithSandbox([]string{"subagent"}, tool.DelegateSandboxSchema{
		Available:   true,
		Modes:       []string{"off", "read-only", "workspace-write", "restricted"},
		SandboxEnum: []string{"off", "read-only", "read-only+nonet", "workspace-write", "workspace-write+nonet", "restricted", "restricted+nonet", "nonet"},
	})
	params := def.Parameters
	if _, hasOneOf := params["oneOf"]; hasOneOf {
		t.Fatal("schema still has a oneOf constraint")
	}
	props := params["properties"].(map[string]any)
	if _, hasNet := props["sandbox_net"]; hasNet {
		t.Fatal("schema still has sandbox_net property")
	}
	sandboxProp, ok := props["sandbox"].(map[string]any)
	if !ok {
		t.Fatal("schema missing sandbox property")
	}
	enum, ok := sandboxProp["enum"].([]string)
	if !ok {
		t.Fatal("sandbox property missing enum")
	}
	// Verify combined enum values are present.
	for _, want := range []string{"off", "read-only", "read-only+nonet", "nonet"} {
		if !slices.Contains(enum, want) {
			t.Fatalf("sandbox enum missing %q: %v", want, enum)
		}
	}
}

// Test: the handler rejects sandbox="off+nonet" (off has no network
// confinement) with a clear error naming the sandbox field.
func TestExecTool_DelegateOffPlusNonetRejectedByHandler(t *testing.T) {
	s := sbxDelegateSession(t, sandbox.HostFacts{OS: "linux", Home: t.TempDir(), BwrapPath: "/usr/bin/bwrap", BwrapCapable: true})
	if err := registerStableDelegateTool(s.reg, s); err != nil {
		t.Fatalf("register delegate tool: %v", err)
	}
	res := s.execTool(context.Background(), llm.ToolCallData{
		ID:        "off-nonet",
		Name:      "delegate",
		Arguments: json.RawMessage(`{"task":"ping","sandbox":"off+nonet"}`),
	}, "")
	if !res.IsError {
		t.Fatalf("expected error for off+nonet (off has no network confinement), got: %s", res.FullOutput)
	}
	if strings.Contains(res.FullOutput, "Required arguments: task") {
		t.Fatalf("error misattributes failure to missing 'task': %s", res.FullOutput)
	}
	if !strings.Contains(res.FullOutput, "sandbox") {
		t.Fatalf("error does not name the sandbox field: %s", res.FullOutput)
	}
}
