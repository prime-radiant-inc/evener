package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/llm"
)

// Issue #618 regression: a delegate call that trips the oneOf
// sandbox/sandbox_net pairing constraint (parent sandbox mode off, sandbox
// backend available) must be rejected with an error naming that constraint —
// not "Required arguments: task (string)", which sends the model resending a
// task it already sent. This exercises the real prevalidation seam
// (prepareToolCall → Schema.Validate → repair → ExplainSchemaError) with the
// exact argument shape observed in session 034FsgXdyimiBvbubPlB4w.
func TestPrepareToolCall_DelegateOneOfConstraintExplainedHonestly(t *testing.T) {
	def := tool.DefDelegateWithSandbox([]string{"subagent"}, tool.DelegateSandboxSchema{
		Available:                   true,
		Modes:                       []string{"off", "read-only", "workspace-write", "restricted"},
		RequireNonOffModeForNetwork: true,
	})
	reg := tool.NewRegistry()
	if err := reg.Register(regTool(def)); err != nil {
		t.Fatalf("register: %v", err)
	}
	rt := reg.Get("delegate")

	call := llm.ToolCallData{
		ID:   "issue618",
		Name: "delegate",
		Arguments: json.RawMessage(`{"task":"ping","agent_type":"subagent","isolation":"worktree",` +
			`"sandbox":"off","sandbox_net":true,"reasoning_effort":"low","delegation_allowance":0}`),
	}
	res := prepareToolCall(call, rt, []string{"delegate"}, "delegate", "")
	if res.PrevalErr == "" {
		t.Fatalf("expected prevalidation failure for oneOf-violating delegate args, got none (changes: %v)", res.Changes)
	}
	if strings.Contains(res.PrevalErr, "Required arguments: task") {
		t.Fatalf("oneOf failure misattributed to missing 'task' (issue #618): %q", res.PrevalErr)
	}
	if !strings.Contains(res.PrevalErr, "sandbox") {
		t.Fatalf("error does not name the constrained fields: %q", res.PrevalErr)
	}
}

// The same call through execTool must surface the honest error to the model.
// Host facts are injected (backend-capable, parent mode off) so the delegate
// schema carries the oneOf constraint on every host — without this the test
// passes vacuously on backend-less hosts where the schema drops the sandbox
// fields and the runtime's "controls unavailable" error satisfies the
// assertions without exercising the oneOf path.
func TestExecTool_DelegateOneOfConstraintErrorNamed(t *testing.T) {
	s := sbxDelegateSession(t, sandbox.HostFacts{OS: "linux", Home: t.TempDir(), BwrapPath: "/usr/bin/bwrap", BwrapCapable: true})
	if err := registerStableDelegateTool(s.reg, s); err != nil {
		t.Fatalf("register delegate tool: %v", err)
	}
	res := s.execTool(context.Background(), llm.ToolCallData{
		ID:        "issue618-exec",
		Name:      "delegate",
		Arguments: json.RawMessage(`{"task":"ping","sandbox":"off","sandbox_net":true}`),
	}, "")
	if !res.IsError {
		t.Fatalf("expected error for oneOf-violating delegate args, got: %s", res.FullOutput)
	}
	if strings.Contains(res.FullOutput, "Required arguments: task") {
		t.Fatalf("model-visible error misattributes failure to missing 'task' (issue #618): %s", res.FullOutput)
	}
	// A vacuous pass on a backend-less host produces the controls-unavailable
	// refusal instead of a oneOf explanation; fail loudly on that shape.
	if strings.Contains(res.FullOutput, "sandbox controls unavailable") {
		t.Fatalf("test passed vacuously: delegate schema lacks the oneOf constraint on this host: %s", res.FullOutput)
	}
	if !strings.Contains(res.FullOutput, "sandbox") {
		t.Fatalf("error does not name the constrained fields: %s", res.FullOutput)
	}
}
