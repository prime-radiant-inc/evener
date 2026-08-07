package agent

import (
	"context"
	"slices"
	"testing"

	"primeradiant.com/serf/agent/plugin"
)

// TestTypedSubagentPolicyKeepsCompactContext pins the policy half of the rule
// ruled 2026-08-06: every subagent can manage its own context-window headroom.
// A typed agent's tools: frontmatter is an allowlist handed to
// RestrictKeepingResultTool, so any tool it omits is DELETED from the child's
// registry — which used to strip compact_context from every shipped typed
// agent, leaving those children with no way to compact but the automatic one.
func TestTypedSubagentPolicyKeepsCompactContext(t *testing.T) {
	t.Parallel()
	_, allowed, _ := baseSubagentToolPolicy(&plugin.Agent{Tools: []string{"read_file"}}, false)
	if !slices.Contains(allowed, "compact_context") {
		t.Fatalf("typed agent allow-list = %v, want compact_context in it", allowed)
	}
}

// TestSubagentRegistryHasCompactContext is the behavioral half: a prepared
// child session's registry actually serves the tool, for both the untyped
// default surface and a typed agent whose frontmatter never mentions it.
func TestSubagentRegistryHasCompactContext(t *testing.T) {
	t.Parallel()
	for _, agentType := range []string{"", "subagent", "explorer"} {
		s := newSession(t, withConfig(SessionConfig{
			MaxSubagentDepth: 3,
			NoProjectPrompts: true,
			testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
		}))
		s.delegationAllowance = 1
		prepared, err := s.prepareSubagentRun(context.Background(), "task", "", "", 0, agentType, "", nil, nil)
		if err != nil {
			t.Fatalf("prepareSubagentRun(agent_type=%q): %v", agentType, err)
		}
		if prepared.sub.sess.reg.Get("compact_context") == nil {
			t.Errorf("agent_type=%q: child registry has no compact_context", agentType)
		}
		releasePreparedTreeSlot(prepared)
		prepared.sub.sess.Close()
	}
}
