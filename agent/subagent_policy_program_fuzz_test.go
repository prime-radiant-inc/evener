//go:build evenerfuzz

package agent

import (
	"context"
	"encoding/json"
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
	for _, seed := range []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9} {
		f.Add([]byte{seed})
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		selector := byte(0)
		if len(data) > 0 {
			selector = data[0] % 10
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
		ctx := context.Background()
		agentType := ""
		grantTools := []string(nil)
		wantError := ""

		switch selector {
		case 0:
			// Default child and its inherited delegation policy.
		case 1:
			agentType = "safz_tools"
		case 2:
			agentType = "safz_alltools"
		case 3:
			agentType = "unknown-policy-agent"
			wantError = "unknown plugin agent type"
		case 4:
			grantTools = []string{"delegate"}
			wantError = "delegation tools are enabled"
		case 5:
			grantTools = []string{"ask_user"}
			wantError = "root-only"
		case 6:
			grantTools = []string{"nonsense_tool"}
			wantError = "cannot grant tool(s)"
		case 7:
			parent.mu.Lock()
			parent.delegationAllowance = 0
			parent.mu.Unlock()
			wantError = "delegation not permitted"
		case 8:
			ctx = context.WithValue(ctx, ctxDelegationAllowance, 0)
			ctx = context.WithValue(ctx, ctxWatchParent, true)
		case 9:
			ctx = context.WithValue(ctx, ctxCommunicateOutputSchema,
				map[string]any{"type": "object", "required": []any{"result"}})
			grantTools = []string{"read_file"}
		}

		result, err := parent.spawnAgent(ctx, "inspect policy", "", "", 3, agentType, "low", nil, grantTools)
		if wantError != "" {
			if err == nil || !strings.Contains(err.Error(), wantError) {
				t.Fatalf("selector %d: spawn error = %v, want substring %q", selector, err, wantError)
			}
			if parent.treeCounter.n.Load() != 0 {
				t.Fatalf("selector %d: rejected spawn leaked %d tree slots", selector, parent.treeCounter.n.Load())
			}
			return
		}
		if err != nil {
			t.Fatalf("selector %d: spawnAgent: %v", selector, err)
		}
		if parent.treeCounter.n.Load() != 0 {
			t.Fatalf("selector %d: in-process spawn retained %d tree slots", selector, parent.treeCounter.n.Load())
		}

		var response struct {
			AgentID string         `json:"agent_id"`
			Status  SubagentStatus `json:"status"`
		}
		text, ok := result.(string)
		if !ok || json.Unmarshal([]byte(text), &response) != nil || response.AgentID == "" {
			t.Fatalf("selector %d: malformed spawn response %#v", selector, result)
		}
		if response.Status != SubagentRunning {
			t.Fatalf("selector %d: response status = %q, want %q", selector, response.Status, SubagentRunning)
		}
		sub := parent.getSub(response.AgentID)
		if sub == nil || sub.sess == nil {
			t.Fatalf("selector %d: spawned child %q was not tracked", selector, response.AgentID)
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
		// task_list and compact_context ride along on every explicit allow-list:
		// compact_context is context hygiene, not an opt-in capability (commit
		// cec0b9bb5, "fix(subagents): compact_context in the default surface").
		{agent: &plugin.Agent{Tools: []string{"read_file"}}, wantAllow: []string{"read_file", "task_list", "compact_context"}},
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
