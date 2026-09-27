package agent

import (
	"context"
	"maps"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/plugin"
)

// TestTypedSubagentPolicyKeepsIntrinsicTools pins the policy half of #2645: a
// typed role's tools: frontmatter is an allowlist handed to
// RestrictKeepingResultTool, which DELETES every registered tool the list omits.
// A role that never named the job tools (the bundled explorer, the
// coordinator-workflow implementer) used to lose them, so a long foreground
// command promoted to a background job became work the delegate could await
// with nothing, inspect with nothing, and stop with nothing. The intrinsic tools
// are not delegation: a leaf keeps them and gains no reach over its parent's
// jobs.
func TestTypedSubagentPolicyKeepsIntrinsicTools(t *testing.T) {
	t.Parallel()
	for _, canDelegate := range []bool{false, true} {
		_, allowed, _ := baseSubagentToolPolicy(&plugin.Agent{Tools: []string{"read_file"}}, canDelegate)
		for _, want := range intrinsicSubagentTools {
			if !slices.Contains(allowed, want) {
				t.Fatalf("typed agent allow-list (canDelegate=%v) = %v, want %q in it", canDelegate, allowed, want)
			}
		}
	}
}

// TestIntrinsicSubagentToolsAreNotRootOnly guards the two policy sets apart: an
// intrinsic tool that is also root-only would be stripped from a leaf, which is
// exactly the regression #2645 fixed.
func TestIntrinsicSubagentToolsAreNotRootOnly(t *testing.T) {
	t.Parallel()
	for _, name := range intrinsicSubagentTools {
		if slices.Contains(rootOnlySubagentTools(), name) {
			t.Errorf("intrinsic subagent tool %q is also root-only; the sets must be disjoint", name)
		}
	}
}

// TestSubagentRegistryHasIntrinsicTools is the behavioral half: a prepared
// child session's registry actually serves every intrinsic tool, for the
// untyped default surface and for the shaped typed roles from #2645 — the
// bundled explorer and the coordinator-workflow implementer — whose frontmatter
// never mentions the job tools.
func TestSubagentRegistryHasIntrinsicTools(t *testing.T) {
	t.Parallel()
	// Load coordinator-workflow once; only the implementer subtest needs it, but
	// loading per subtest re-parses the plugin from disk four times.
	coordinatorAgents := coordinatorWorkflowPublicAgentsForTest(t)
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
			maps.Copy(s.pluginAgents, coordinatorAgents)
			s.delegationAllowance = 1 // child grant 0: a leaf
			prepared, err := s.prepareSubagentRun(context.Background(), "task", "", "", 0, agentType, "", nil, nil)
			if err != nil {
				t.Fatalf("prepareSubagentRun(agent_type=%q): %v", agentType, err)
			}
			for _, want := range intrinsicSubagentTools {
				if prepared.sub.sess.reg.Get(want) == nil {
					t.Errorf("agent_type=%q: child registry has no %q — a session that can run jobs must be able to supervise its own jobs", agentType, want)
				}
			}
			// Literal, not read off intrinsicSubagentTools: the untyped surface
			// keeps job_list, so a typed leaf must be able to enumerate its own
			// jobs too, and this pin must fail if job_list leaves the set.
			if prepared.sub.sess.reg.Get("job_list") == nil {
				t.Errorf("agent_type=%q: child registry has no job_list to enumerate its own jobs", agentType)
			}
			releasePreparedTreeSlot(prepared)
			prepared.sub.sess.Close()
		})
	}
}
