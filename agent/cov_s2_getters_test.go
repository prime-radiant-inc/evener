package agent

import (
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

// s2cov_ tests for trivial session accessors and the closed-session guards on
// the runtime mutators.

func TestS2Cov_SessionAccessors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sess := newSession(t, withDir(dir), withConfig(SessionConfig{MaxSubagentDepth: 1, StateDir: dir}))

	if sess.StateDir() != dir {
		t.Fatalf("StateDir = %q, want %q", sess.StateDir(), dir)
	}
	if sess.Client() == nil {
		t.Fatal("Client returned nil")
	}
	if got := sess.CommunicateOutput(); got != "" {
		t.Fatalf("CommunicateOutput = %q, want empty before any communicate", got)
	}
	if sess.ContextPressure() < 0 {
		t.Fatalf("ContextPressure = %v, want >= 0", sess.ContextPressure())
	}
}

func TestS2Cov_MutatorsAfterClose(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{MaxSubagentDepth: 1})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sess.Close()

	// Post-close mutators must take the closed-guard early return, not panic.
	sess.SetModel("gpt-5.1")
	sess.SetReasoningEffort("high")

	if sess.State() != SessionClosed {
		t.Fatalf("State = %v, want SessionClosed", sess.State())
	}
}
