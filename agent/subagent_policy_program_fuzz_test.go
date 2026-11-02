//go:build evenerfuzz

package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/sandbox"
)

// FuzzSubagentPolicyProgram exercises the policy decisions that surround a real
// prepare/spawn. The child runs through a scripted provider and a deny-all
// execution environment, so no provider, network, or process boundary is used.
func FuzzSubagentPolicyProgram(f *testing.F) {
	for _, seed := range []byte{0, 1, 2, 3, 4, 5, 6} {
		f.Add([]byte{seed})
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		selector := byte(0)
		if len(data) > 0 {
			selector = data[0] % 7
		}
		assertSubagentPolicyHelpers(t, selector)

		var parentEnv execenv.ExecutionEnvironment = &agenttest.DenyEnv{WorkDir: t.TempDir()}
		workspace := ""
		var sandboxProbers []sandbox.Prober
		if selector == 1 {
			workspace = t.TempDir()
			parentEnv = execenv.NewLocalExecutionEnvironment(workspace)
			sandboxProbers = []sandbox.Prober{bwrapCapableProber(workspace)}
		}
		parent := safzNewParent(t, agenttest.NewFakeClock(), 2, []int{0}, parentEnv, sandboxProbers...)
		args := delegateArgs{Task: "inspect policy", ReasoningEffort: "low"}
		wantRejected := false

		switch selector {
		case 0:
			// Default child and its inherited delegation policy.
		case 1:
			args.AgentType = "safz_tools"
		case 2:
			args.AgentType = "safz_alltools"
		case 3:
			args.AgentType = "unknown-policy-agent"
			wantRejected = true
		case 4:
			parent.mu.Lock()
			parent.delegationAllowance = 0
			parent.mu.Unlock()
			wantRejected = true
		case 5:
			args.DelegationAllowance = new(0)
			args.WatchParent = true
		case 6:
			args.ResultSchema = map[string]any{"type": "object", "required": []any{"result"}}
		}

		result := parent.createDelegate(context.Background(), args)
		if wantRejected {
			if result.Err == nil {
				t.Fatalf("selector %d: create succeeded, want a rejection", selector)
			}
			if parent.treeCounter.n.Load() != 0 {
				t.Fatalf("selector %d: rejected create leaked %d tree slots", selector, parent.treeCounter.n.Load())
			}
			return
		}
		if result.Err != nil {
			t.Fatalf("selector %d: createDelegate: %v", selector, result.Err)
		}
		if result.ChildSessionID == "" {
			t.Fatalf("selector %d: create result has no child session: %+v", selector, result)
		}
		sub := parent.getSub(result.ChildSessionID)
		if sub == nil || sub.sess == nil {
			t.Fatalf("selector %d: created child %q was not tracked", selector, result.ChildSessionID)
		}
		if sub.sess.reg.Get("ask_user") != nil {
			t.Fatalf("selector %d: child received protected ask_user tool", selector)
		}
		if selector == 1 {
			childEnv, ok := sub.sess.env.(*execenv.LocalExecutionEnvironment)
			if !ok || childEnv.Sandbox == nil || !childEnv.Sandbox.Enforced() || childEnv.Sandbox.Mode != sandbox.ModeReadOnly {
				t.Fatalf("selector %d: child environment = %#v, want enforced read-only sandbox", selector, sub.sess.env)
			}
			for _, name := range []string{"read_file", "grep", "task_list"} {
				if sub.sess.reg.Get(name) == nil {
					t.Fatalf("explicit-tool child missing %q", name)
				}
			}
			if sub.sess.reg.Get("delegate") != nil {
				t.Fatal("explicit-tool child received delegate outside its allow-list")
			}
		}
		safzWaitDone(t, sub)
	})
}

func assertSubagentPolicyHelpers(t *testing.T, selector byte) {
	t.Helper()
	if !isRootOnlyJobPresenceTool("delegate") || isRootOnlyJobPresenceTool("read_file") {
		t.Fatal("root-only job presence classification changed")
	}
	// job_watch is available to subagents; only delegation is root-only job
	// control under the current policy.
	if got := removeRootOnlySubagentTools([]string{"read_file", "delegate", "job_watch"}); len(got) != 2 || got[0] != "read_file" || got[1] != "job_watch" {
		t.Fatalf("root-only removal = %v, want [read_file job_watch]", got)
	}
	if got := removeStrings([]string{"a", "b"}, nil); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("nil removal changed input: %v", got)
	}

	policyCases := []struct {
		agent       *plugin.Agent
		canDelegate bool
		wantAll     bool
		wantAllow   []string
		wantDeny    bool
	}{
		{agent: &plugin.Agent{AllTools: true}, wantAll: true},
		// task_list, compact_context, use_skill, and the job-supervision tools
		// ride along on every explicit allow-list: none is an opt-in capability
		// (commits cec0b9bb5, "fix(subagents): compact_context in the default
		// surface"; #1427, "delegates keep use_skill so skill-directed briefs
		// work"; and #2645, "subagents keep their own-job supervision tools").
		{agent: &plugin.Agent{Tools: []string{"read_file"}}, wantAllow: []string{"read_file", "task_list", "compact_context", "use_skill", "job_list", "job_status", "job_stop", "job_watch"}},
		{canDelegate: true},
		{wantDeny: true},
	}
	pc := policyCases[int(selector)%len(policyCases)]
	all, allowed, denied := baseSubagentToolPolicy(pc.agent, pc.canDelegate)
	if all != pc.wantAll || strings.Join(allowed, ",") != strings.Join(pc.wantAllow, ",") || (len(denied) > 0) != pc.wantDeny {
		t.Fatalf("base policy = all:%v allow:%v deny:%v", all, allowed, denied)
	}
	frozen := frozenSubagentToolNames(all, allowed, denied)
	if all && (len(frozen) != 1 || frozen[0] != "*") {
		t.Fatalf("all-tools frozen policy = %v", frozen)
	}

	for _, row := range []struct {
		name string
		want execenv.EnvVarPolicy
		ok   bool
	}{
		{"all", execenv.EnvPolicyAll, true},
		{"none", execenv.EnvPolicyNone, true},
		{"core_only", execenv.EnvPolicyCoreOnly, true},
		{"default", execenv.EnvPolicyDefault, true},
		{"unknown", execenv.EnvPolicyDefault, false},
	} {
		got, ok := localEnvPolicyFromName(row.name)
		if got != row.want || ok != row.ok {
			t.Fatalf("localEnvPolicyFromName(%q) = (%v,%v), want (%v,%v)", row.name, got, ok, row.want, row.ok)
		}
	}
	local := execenv.NewLocalExecutionEnvironment(t.TempDir())
	for _, row := range []struct {
		policy execenv.EnvVarPolicy
		name   string
	}{
		{execenv.EnvPolicyAll, "all"},
		{execenv.EnvPolicyNone, "none"},
		{execenv.EnvPolicyCoreOnly, "core_only"},
		{execenv.EnvPolicyDefault, "default"},
	} {
		local.EnvPolicy = row.policy
		if got := localEnvPolicyName(local); got != row.name {
			t.Fatalf("localEnvPolicyName(%v) = %q, want %q", row.policy, got, row.name)
		}
	}

	unmarshalable := make(chan int)
	cloned := cloneMap(map[string]any{"channel": unmarshalable})
	if cloned["channel"] != unmarshalable {
		t.Fatal("cloneMap marshal fallback did not preserve shallow value")
	}
	if _, err := restoreFrozenSkillBodies(nil, []string{"orphan"}); err == nil {
		t.Fatal("orphan frozen skill body was accepted")
	}
	if _, err := restoreFrozenSkillBodies([]string{"skill"}, nil); err == nil {
		t.Fatal("missing frozen skill body was accepted")
	}
	if _, err := restoreFrozenSkillBodies([]string{"a", "b"}, []string{"body"}); err == nil {
		t.Fatal("misaligned frozen skill descriptor was accepted")
	}
	if _, err := restoreFrozenSkillBodies([]string{"skill"}, []string{"  "}); err == nil {
		t.Fatal("blank frozen skill body was accepted")
	}
}
