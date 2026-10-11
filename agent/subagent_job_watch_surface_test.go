package agent

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/llm"
)

// unsandboxedProber reports a host without a usable bwrap, so a child that runs
// a shell in these tests does not depend on the runner having a sandbox wrapper
// (the CI runners have none). home must be absolute: the credential denylist
// anchor needs a resolvable home. BwrapPath points nowhere so an accidental
// wrapper exec fails here too, not only in CI.
func unsandboxedProber(home string) sandbox.Prober {
	return sandbox.FakeProber{Facts: sandbox.HostFacts{OS: "linux", Home: home, BwrapPath: "/nonexistent/bwrap", BwrapCapable: false}}
}

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

// startBackgroundJob runs a background shell in s and returns its job id,
// stopping it at test cleanup.
func startBackgroundJob(t *testing.T, s *Session) string {
	t.Helper()
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID: "background-shell", Name: "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","mode":"background"}`),
	})
	if res.IsError {
		t.Fatalf("background shell: %s", res.Output)
	}
	var out struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil || out.JobID == "" {
		t.Fatalf("background shell output has no job_id: %v (%s)", err, res.Output)
	}
	t.Cleanup(func() { _, _ = s.jobManager.stop(out.JobID) })
	return out.JobID
}

// assertCannotReachParentJobs asserts that child is refused every job tool
// against a job owned by another session: a source="parent" watch without the
// grant, and status/stop on the parent's job id, with job_list not exposing it.
// Holding the job tools for one's own jobs must not widen access to a parent's.
func assertCannotReachParentJobs(t *testing.T, child *Session, parentJobID string) {
	t.Helper()
	ctx := context.Background()
	if res := child.reg.ExecuteCall(ctx, child.env, llm.ToolCallData{
		ID: "watch-parent", Name: "job_watch",
		Arguments: json.RawMessage(`{"operation":"create","source":"parent","events":["communicate"]}`),
	}); !res.IsError {
		t.Errorf("child watched a parent without the grant: %s", res.Output)
	}
	if res := child.reg.ExecuteCall(ctx, child.env, llm.ToolCallData{
		ID: "status-parent", Name: "job_status",
		Arguments: json.RawMessage(`{"target":"` + parentJobID + `"}`),
	}); !res.IsError {
		t.Errorf("child read a parent-owned job: %s", res.Output)
	}
	if res := child.reg.ExecuteCall(ctx, child.env, llm.ToolCallData{
		ID: "stop-parent", Name: "job_stop",
		Arguments: json.RawMessage(`{"target":"` + parentJobID + `","max_wait_ms":0}`),
	}); !res.IsError {
		t.Errorf("child stopped a parent-owned job: %s", res.Output)
	}
	list := child.reg.ExecuteCall(ctx, child.env, llm.ToolCallData{
		ID: "list-own", Name: "job_list", Arguments: json.RawMessage(`{}`),
	})
	if list.IsError {
		t.Errorf("child job_list failed: %s", list.Output)
	} else if strings.Contains(list.Output, parentJobID) {
		t.Errorf("child job_list exposed a parent-owned job:\n%s", list.Output)
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
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
			sandboxProber:       bwrapCapableProber(t.TempDir()),
		},
	}))
	ctx := context.Background()
	parentJobID := startBackgroundJob(t, s)

	s.delegationAllowance = 1 // child grant 0: a leaf
	prepared, err := s.prepareSubagentRun(ctx, "task", "", "", 0, "explorer", "", nil, nil)
	if err != nil {
		t.Fatalf("prepareSubagentRun: %v", err)
	}
	defer releasePreparedTreeSlot(prepared)
	defer prepared.sub.sess.Close()

	assertCannotReachParentJobs(t, prepared.sub.sess, parentJobID)
}

// TestTypedLeafSupervisesItsOwnJobs is the positive half of #2645: a typed leaf
// that starts a background job must be able to enumerate, inspect, await, and
// stop it. Without this, an ownership check that also denied a leaf's own jobs
// would keep the denial tests green while leaving #2645 unfixed.
func TestTypedLeafSupervisesItsOwnJobs(t *testing.T) {
	t.Parallel()
	s := newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth: 3,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
			sandboxProber:       unsandboxedProber(t.TempDir()),
		},
	}))
	ctx := context.Background()
	s.delegationAllowance = 1 // child grant 0: a leaf
	prepared, err := s.prepareSubagentRun(ctx, "task", "", "", 0, "explorer", "", nil, nil)
	if err != nil {
		t.Fatalf("prepareSubagentRun: %v", err)
	}
	defer releasePreparedTreeSlot(prepared)
	defer prepared.sub.sess.Close()
	child := prepared.sub.sess

	jobID := startBackgroundJob(t, child)
	list := child.reg.ExecuteCall(ctx, child.env, llm.ToolCallData{
		ID: "list-own", Name: "job_list", Arguments: json.RawMessage(`{}`),
	})
	if list.IsError {
		t.Fatalf("job_list on the leaf's own job: %s", list.Output)
	}
	if !strings.Contains(list.Output, jobID) {
		t.Fatalf("job_list does not expose the leaf's own job %s:\n%s", jobID, list.Output)
	}
	if st := child.reg.ExecuteCall(ctx, child.env, llm.ToolCallData{
		ID: "status-own", Name: "job_status",
		Arguments: json.RawMessage(`{"target":"` + jobID + `"}`),
	}); st.IsError {
		t.Fatalf("job_status on the leaf's own job: %s", st.Output)
	}
	if w := child.reg.ExecuteCall(ctx, child.env, llm.ToolCallData{
		ID: "watch-own", Name: "job_watch",
		Arguments: json.RawMessage(`{"operation":"create","source":"` + jobID + `","progress_interval_ms":120000}`),
	}); w.IsError {
		t.Fatalf("job_watch on the leaf's own job: %s", w.Output)
	}
	if stop := child.reg.ExecuteCall(ctx, child.env, llm.ToolCallData{
		ID: "stop-own", Name: "job_stop",
		Arguments: json.RawMessage(`{"target":"` + jobID + `","max_wait_ms":5000}`),
	}); stop.IsError {
		t.Fatalf("job_stop on the leaf's own job: %s", stop.Output)
	}
}
