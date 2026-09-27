package agent

import (
	"context"
	"maps"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/plugin"
)

// jobSupervisionTools are the job tools every subagent keeps regardless of its
// tools: frontmatter. Defined here so the policy and registry tests below stay
// in step.
var jobSupervisionTools = []string{"job_status", "job_stop", "job_watch"}

// TestTypedSubagentPolicyKeepsJobSupervisionTools pins the policy half of
// #2645: a typed role's tools: frontmatter is an allowlist handed to
// RestrictKeepingResultTool, which DELETES every registered tool the list omits.
// A role that never named the job tools (the bundled explorer, the
// coordinator-workflow implementer) used to lose them, so a long foreground
// command promoted to a background job became work the delegate could await
// with nothing, inspect with nothing, and stop with nothing. Watching is not
// delegation: any session that can run jobs can watch, inspect, and stop its
// own jobs at any depth, so the policy must add the trio to every explicit
// allow-list, exactly as it does compact_context and use_skill.
func TestTypedSubagentPolicyKeepsJobSupervisionTools(t *testing.T) {
	t.Parallel()
	for _, canDelegate := range []bool{false, true} {
		_, allowed, _ := baseSubagentToolPolicy(&plugin.Agent{Tools: []string{"read_file"}}, canDelegate)
		for _, want := range jobSupervisionTools {
			if !slices.Contains(allowed, want) {
				t.Fatalf("typed agent allow-list (canDelegate=%v) = %v, want %q in it", canDelegate, allowed, want)
			}
		}
	}
}

// TestSubagentRegistryHasJobSupervisionTools is the behavioral half: a prepared
// child session's registry actually serves the job tools, for the untyped
// default surface and for the shaped typed roles from #2645 — the bundled
// explorer and the coordinator-workflow implementer — whose frontmatter never
// mentions them.
func TestSubagentRegistryHasJobSupervisionTools(t *testing.T) {
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
			for _, want := range jobSupervisionTools {
				if prepared.sub.sess.reg.Get(want) == nil {
					t.Errorf("agent_type=%q: child registry has no %q — a session that can run jobs must be able to supervise its own jobs", agentType, want)
				}
			}
			releasePreparedTreeSlot(prepared)
			prepared.sub.sess.Close()
		})
	}
}
