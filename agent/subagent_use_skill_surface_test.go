package agent

import (
	"context"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/plugin"
)

// TestTypedSubagentPolicyKeepsUseSkill pins the policy half of the rule that a
// delegate must be able to follow a skill-directed brief. A typed agent's
// tools: frontmatter is an allowlist handed to RestrictKeepingResultTool, so
// any tool it omits is DELETED from the child's registry — which strips
// use_skill from every shipped typed role (subagent, explorer, the
// coordinator-workflow roles) unless the policy re-adds it. Without the tool
// the child cannot activate a skill at all; its only recourse is the untracked
// read_file fallback, which the brief-stated skill step does not describe.
func TestTypedSubagentPolicyKeepsUseSkill(t *testing.T) {
	t.Parallel()
	_, allowed, _ := baseSubagentToolPolicy(&plugin.Agent{Tools: []string{"read_file"}}, false)
	if !slices.Contains(allowed, "use_skill") {
		t.Fatalf("typed agent allow-list = %v, want use_skill in it", allowed)
	}
}

// TestSubagentRegistryHasUseSkill is the behavioral half: a prepared child
// session's registry actually serves use_skill, for both the untyped default
// surface and a typed agent whose frontmatter never mentions it (subagent and
// explorer are the shipped built-ins the issue names).
func TestSubagentRegistryHasUseSkill(t *testing.T) {
	t.Parallel()
	for _, agentType := range []string{"", "subagent", "explorer"} {
		s := newSession(t, withConfig(SessionConfig{
			MaxSubagentDepth: 3,
			NoProjectPrompts: true,
			testOnly: testConfig{
				skipGitSnapshot:     true,
				minimalSystemPrompt: true,
				noSyncJobStore:      true,
				sandboxProber:       bwrapCapableProber(t.TempDir()),
			},
		}))
		s.delegationAllowance = 1
		prepared, err := s.prepareSubagentRun(context.Background(), "task", "", "", 0, agentType, "", nil, nil)
		if err != nil {
			t.Fatalf("prepareSubagentRun(agent_type=%q): %v", agentType, err)
		}
		if prepared.sub.sess.reg.Get("use_skill") == nil {
			t.Errorf("agent_type=%q: child registry has no use_skill", agentType)
		}
		releasePreparedTreeSlot(prepared)
		prepared.sub.sess.Close()
	}
}
