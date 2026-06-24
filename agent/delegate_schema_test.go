package agent

import (
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

// TestDelegateAllowanceParamElidedWhenNoOp pins that delegation_allowance is
// hidden from the delegate schema when this session can only grant 0 (own
// allowance 1) — a single legal value is a no-op knob. With allowance >= 2 the
// param is offered.
func TestDelegateAllowanceParamElidedWhenNoOp(t *testing.T) {
	t.Parallel()
	delegateProps := func(t *testing.T, depth int) map[string]any {
		t.Helper()
		c := llm.NewClient()
		sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"),
			execenv.NewLocalExecutionEnvironment(t.TempDir()),
			SessionConfig{MaxSubagentDepth: depth})
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		t.Cleanup(sess.Close)
		for _, td := range sess.cachedToolDefs {
			props, ok := td.Parameters["properties"].(map[string]any)
			if !ok {
				continue
			}
			// Identify delegate by its signature params, independent of wire rename.
			if _, hasTask := props["task"]; hasTask {
				if _, hasAgentType := props["agent_type"]; hasAgentType {
					return props
				}
			}
		}
		t.Fatal("delegate tool def not found in session")
		return nil
	}

	// Allowance 1 (depth 1): only legal grant is 0 → param elided.
	if _, ok := delegateProps(t, 1)["delegation_allowance"]; ok {
		t.Error("depth 1 (allowance 1): delegation_allowance should be elided (no-op)")
	}
	// Allowance 2 (depth 2): grants 0 or 1 are meaningful → param present.
	if _, ok := delegateProps(t, 2)["delegation_allowance"]; !ok {
		t.Error("depth 2 (allowance 2): delegation_allowance should be present")
	}
}
