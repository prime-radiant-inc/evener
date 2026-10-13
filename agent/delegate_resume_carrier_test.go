package agent

import (
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TestRestoreDelegateWithoutCarrierKeepsRootAllowance is #4442's own
// measurement: RestoreSessionFromMetaWithConfig derives the delegation
// allowance from the spawn carrier alone, and cfg.spawn is never persisted
// (json:"-"), so a session whose persisted meta says IsSubagent with an empty
// restore carrier takes the ROOT branch — the root delegation allowance
// (MaxSubagentDepth) and the `delegate` tool — while isSubagentSession()
// reports a subagent.
//
// That contradiction is what the CLI refuses instead of restoring: a bare
// `serve --resume <delegate-id>` (and `run --resume`) now fails at the entry
// (cmd/evener/resume_guard.go), so this library state is unreachable from the
// resume operations. This test pins the library behavior the entry guard keeps
// users out of; it is deliberately NOT an assertion that the state is desired.
func TestRestoreDelegateWithoutCarrierKeepsRootAllowance(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	meta := schema.SessionMeta{
		ID: "02wMz5Txv1C3Hut0M8GCeB", ProfileID: "openai", Model: "gpt-5.2",
		IsSubagent: true,
		Config:     (SessionConfig{MaxSubagentDepth: 1}).toSnapshot(),
	}
	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer restored.Close()

	if restored.cfg.spawn.parentSessionID != "" {
		t.Fatalf("test setup: spawn carrier %q, want the bare-resume shape", restored.cfg.spawn.parentSessionID)
	}
	if restored.delegationAllowance != 1 {
		t.Fatalf("delegationAllowance = %d, want the root MaxSubagentDepth 1", restored.delegationAllowance)
	}
	if restored.reg.Get("delegate") == nil {
		t.Fatal("delegate tool not registered: the empty carrier took the root branch")
	}
	if !restored.isSubagentSession() {
		t.Fatal("isSubagentSession() = false, want true from the persisted IsSubagent flag")
	}
}
