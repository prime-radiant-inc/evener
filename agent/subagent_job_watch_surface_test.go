package agent

import (
	"context"
	"maps"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/plugin"
)

// TestTypedSubagentPolicyKeepsJobWatch pins the policy half of #2645: a typed
// role's tools: frontmatter is an allowlist handed to RestrictKeepingResultTool,
// which DELETES every registered tool the list omits. A role that never named
// job_watch (the bundled explorer, the coordinator-workflow implementer) used to
// lose it on every spawn path, so a long foreground command promoted to a
// background job became work it had no way to await. Watching is not delegation:
// any session that can run jobs watches its own jobs at any depth, so the policy
// must add job_watch to every explicit allow-list, exactly as it does
// compact_context and use_skill.
func TestTypedSubagentPolicyKeepsJobWatch(t *testing.T) {
	t.Parallel()
	for _, canDelegate := range []bool{false, true} {
		_, allowed, _ := baseSubagentToolPolicy(&plugin.Agent{Tools: []string{"read_file"}}, canDelegate)
		if !slices.Contains(allowed, "job_watch") {
			t.Fatalf("typed agent allow-list (canDelegate=%v) = %v, want job_watch in it", canDelegate, allowed)
		}
	}
}

// TestSubagentRegistryHasJobWatch is the behavioral half: a prepared child
// session's registry actually serves job_watch, for the untyped default surface
// and for the shaped typed roles from #2645 — the bundled explorer and the
// coordinator-workflow implementer — whose frontmatter never mentions it.
func TestSubagentRegistryHasJobWatch(t *testing.T) {
	t.Parallel()
	for _, agentType := range []string{"", "subagent", "explorer", "implementer"} {
		t.Run(agentType+"/leaf", func(t *testing.T) {
			t.Parallel()
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
			// The coordinator-workflow implementer is a plugin agent; make its
			// type resolvable the way the bundled plugin would at load time.
			maps.Copy(s.pluginAgents, coordinatorWorkflowPublicAgentsForTest(t))
			s.delegationAllowance = 1 // child grant 0: a leaf
			prepared, err := s.prepareSubagentRun(context.Background(), "task", "", "", 0, agentType, "", nil, nil)
			if err != nil {
				t.Fatalf("prepareSubagentRun(agent_type=%q): %v", agentType, err)
			}
			if prepared.sub.sess.reg.Get("job_watch") == nil {
				t.Errorf("agent_type=%q: child registry has no job_watch — a session that can run jobs must be able to watch its own jobs", agentType)
			}
			releasePreparedTreeSlot(prepared)
			prepared.sub.sess.Close()
		})
	}
}
