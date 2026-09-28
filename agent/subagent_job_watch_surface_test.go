package agent

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/llm"
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

// TestTypedLeafCannotReachParentJobs is the isolation half of #2645: granting
// the own-job supervision tools to a typed leaf must not let it watch, read,
// list, or stop a job it does not own. Each tool authorizes its own target, so
// this pins the boundary for a typed role rather than only the untyped default
// (TestLeafDelegateWatchesItsOwnJobsOnly).
func TestTypedLeafCannotReachParentJobs(t *testing.T) {
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
	ctx := context.Background()

	// A job owned by the parent session the leaf must not reach.
	parentShell := s.reg.ExecuteCall(ctx, s.env, llm.ToolCallData{
		ID: "parent-shell", Name: "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","mode":"background"}`),
	})
	if parentShell.IsError {
		t.Fatalf("parent background shell: %s", parentShell.Output)
	}
	var shellOut struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(toolResultJSON(parentShell), &shellOut); err != nil || shellOut.JobID == "" {
		t.Fatalf("parent shell output has no job_id: %v (%s)", err, parentShell.Output)
	}
	t.Cleanup(func() { _, _ = s.jobManager.stop(shellOut.JobID) })

	s.delegationAllowance = 1 // child grant 0: a leaf
	prepared, err := s.prepareSubagentRun(ctx, "task", "", "", 0, "explorer", "", nil, nil)
	if err != nil {
		t.Fatalf("prepareSubagentRun: %v", err)
	}
	defer releasePreparedTreeSlot(prepared)
	defer prepared.sub.sess.Close()
	child := prepared.sub.sess

	// A leaf never inherits the parent-watch grant, so source="parent" is refused
	// even though the leaf now holds job_watch for its own jobs.
	if res := child.reg.ExecuteCall(ctx, child.env, llm.ToolCallData{
		ID: "watch-parent", Name: "job_watch",
		Arguments: json.RawMessage(`{"operation":"create","source":"parent","events":["communicate"]}`),
	}); !res.IsError {
		t.Fatalf("typed leaf watched its parent without the grant: %s", res.Output)
	}
	if res := child.reg.ExecuteCall(ctx, child.env, llm.ToolCallData{
		ID: "status-parent", Name: "job_status",
		Arguments: json.RawMessage(`{"target":"` + shellOut.JobID + `"}`),
	}); !res.IsError {
		t.Fatalf("typed leaf read a parent-owned job: %s", res.Output)
	}
	if res := child.reg.ExecuteCall(ctx, child.env, llm.ToolCallData{
		ID: "stop-parent", Name: "job_stop",
		Arguments: json.RawMessage(`{"target":"` + shellOut.JobID + `","max_wait_ms":0}`),
	}); !res.IsError {
		t.Fatalf("typed leaf stopped a parent-owned job: %s", res.Output)
	}
	list := child.reg.ExecuteCall(ctx, child.env, llm.ToolCallData{
		ID: "list", Name: "job_list", Arguments: json.RawMessage(`{}`),
	})
	if list.IsError {
		t.Fatalf("typed leaf job_list failed: %s", list.Output)
	}
	if strings.Contains(list.Output, shellOut.JobID) {
		t.Fatalf("typed leaf job_list exposed a parent-owned job:\n%s", list.Output)
	}
}
