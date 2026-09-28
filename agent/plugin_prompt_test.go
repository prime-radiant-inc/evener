package agent

import (
	"context"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/llm"
)

// availableAgentEntriesForTest returns the available-agents entries a root
// session builds for agents. allowance >= 0 overrides the session's
// delegation allowance; allowedTools, when set, restricts its tool surface.
func availableAgentEntriesForTest(t *testing.T, agents map[string]plugin.Agent, allowance int, allowedTools []string) []agentEntry {
	t.Helper()
	cfg := SessionConfig{AgentsDocPath: filepath.Join(t.TempDir(), "no-personal-AGENTS.md")}
	cfg.spawn.allowedToolNames = append([]string(nil), allowedTools...)
	sess := newSession(t, withDir(t.TempDir()), withConfig(cfg))
	if allowance >= 0 {
		sess.mu.Lock()
		sess.delegationAllowance = allowance
		sess.mu.Unlock()
	}
	sess.pluginAgents = make(map[string]plugin.Agent, len(agents))
	maps.Copy(sess.pluginAgents, agents)
	return sess.buildPromptData(sess.currentEnv()).AvailableAgents
}

// depthOneSubagentForTest builds a depth-1 child session with the given
// delegation allowance and, when allowedTools is set, that tool surface.
func depthOneSubagentForTest(t *testing.T, allowance int, allowedTools []string) *Session {
	t.Helper()
	cfg := SessionConfig{
		StateDir:         t.TempDir(),
		NoProjectPrompts: true,
		AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
	}
	cfg.spawn.depth = 1
	cfg.spawn.parentSessionID = "parent-session"
	cfg.spawn.delegationAllowance = allowance
	cfg.spawn.allowedToolNames = append([]string(nil), allowedTools...)
	sess := newSession(t, withDir(t.TempDir()), withConfig(cfg))
	if sess.depth == 0 {
		t.Fatal("expected a depth > 0 subagent session, got depth 0")
	}
	return sess
}

// TestSubagentPromptStatesAllowance pins spec §5 as typed inputs: a child with
// allowance > 0 can delegate and carries its allowance; a leaf cannot.
func TestSubagentPromptStatesAllowance(t *testing.T) {
	t.Parallel()
	granting := depthOneSubagentForTest(t, 2, nil)
	if data := granting.buildPromptData(granting.env); !data.CanDelegate || data.DelegationAllowance != 2 {
		t.Errorf("allowance-2 child: CanDelegate=%v DelegationAllowance=%d, want true and 2", data.CanDelegate, data.DelegationAllowance)
	}
	leaf := depthOneSubagentForTest(t, 0, nil)
	if data := leaf.buildPromptData(leaf.env); data.CanDelegate {
		t.Error("allowance-0 child: CanDelegate = true, want false")
	}
}

func TestSubagentPromptSuppressesDelegationWhenToolsUnavailable(t *testing.T) {
	t.Parallel()
	sess := depthOneSubagentForTest(t, 1, []string{"communicate", "delegate", "job_watch"})
	if data := sess.buildPromptData(sess.env); data.CanDelegate {
		t.Fatal("CanDelegate = true with an incomplete delegation tool surface, want false")
	}
}

func TestUntypedDelegatingSubagentUsesDelegatingRolePrompt(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	childPromptSeen := make(chan string, 1)
	client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			var childPrompt string
			for _, msg := range req.Messages {
				if msg.Role == llm.RoleSystem {
					childPrompt = msg.Text()
					break
				}
			}
			childPromptSeen <- childPrompt
			return finalResponse("done")
		},
	}})

	sess, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		MaxSubagentDepth: 3,
		NoProjectPrompts: true,
		StateDir:         t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:                "coordinate follow-up work",
		DelegationAllowance: new(1),
	})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}
	var childPrompt string
	select {
	case childPrompt = <-childPromptSeen:
	case <-time.After(30 * time.Second): // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
		t.Fatal("delegate child never requested a model turn")
	}
	if !strings.Contains(childPrompt, "You may delegate scoped subwork") {
		t.Fatalf("delegating untyped child prompt missing delegating role guidance:\n%s", childPrompt)
	}
	if strings.Contains(childPrompt, "Do NOT try to spawn further subagents") {
		t.Fatalf("delegating untyped child prompt used leaf role guidance:\n%s", childPrompt)
	}
}

func TestAvailableAgentsSection_NoAgents(t *testing.T) {
	t.Parallel()
	if got := availableAgentEntriesForTest(t, nil, -1, nil); len(got) != 0 {
		t.Errorf("available agents = %+v, want none", got)
	}
}

func TestAvailableAgentsSection_WithAgents(t *testing.T) {
	t.Parallel()
	agents := map[string]plugin.Agent{
		"my-plugin:reviewer": {
			Name:        "reviewer",
			Description: "Reviews code for quality",
			PluginName:  "my-plugin",
			Tools:       []string{"read_file", "grep"},
			Tasks: []task.TaskTemplate{
				{Title: "Review deliverable", Prompt: "Read the deliverable and compare it to the spec."},
				{Title: "Report findings", Prompt: "Report the findings.", Insert: "parent_tasks"},
			},
		},
		"my-plugin:tester": {Name: "tester", Description: "Generates test cases", PluginName: "my-plugin"},
	}
	entries := availableAgentEntriesForTest(t, agents, -1, nil)
	byName := make(map[string]agentEntry, len(entries))
	for _, e := range entries {
		byName[e.Name] = e
	}
	reviewer, ok := byName["my-plugin:reviewer"]
	if !ok || reviewer.Description != "Reviews code for quality" {
		t.Fatalf("reviewer entry = %+v (present %v)", reviewer, ok)
	}
	if tester, ok := byName["my-plugin:tester"]; !ok || tester.Description != "Generates test cases" {
		t.Fatalf("tester entry = %+v (present %v)", tester, ok)
	}
	for _, want := range []string{"communicate", "grep_files", "read_file", "task_list"} {
		if !strings.Contains(reviewer.DefaultTools, "`"+want+"`") {
			t.Errorf("reviewer DefaultTools = %q, want %q", reviewer.DefaultTools, want)
		}
	}
	if len(reviewer.TaskList) != 2 || reviewer.TaskList[0].ReplacedByParentTasks || !reviewer.TaskList[1].ReplacedByParentTasks {
		t.Errorf("reviewer TaskList = %+v, want only the second task to insert parent tasks", reviewer.TaskList)
	}
}

func TestAvailableAgentsSection_Sorted(t *testing.T) {
	t.Parallel()
	agents := map[string]plugin.Agent{
		"z-plugin:agent": {Name: "agent", Description: "Z agent", PluginName: "z-plugin"},
		"a-plugin:agent": {Name: "agent", Description: "A agent", PluginName: "a-plugin"},
	}
	entries := availableAgentEntriesForTest(t, agents, -1, nil)
	aIdx := slices.IndexFunc(entries, func(e agentEntry) bool { return e.Name == "a-plugin:agent" })
	zIdx := slices.IndexFunc(entries, func(e agentEntry) bool { return e.Name == "z-plugin:agent" })
	if aIdx < 0 || zIdx < 0 {
		t.Fatalf("expected both agents, got %+v", entries)
	}
	if aIdx >= zIdx {
		t.Errorf("agents should be sorted alphabetically: a at %d, z at %d", aIdx, zIdx)
	}
}

func TestAvailableAgentsSection_OmitsTopLevelOnlyAgents(t *testing.T) {
	t.Parallel()
	agents := map[string]plugin.Agent{
		"coordinator": {Name: "coordinator", Description: "Delegates to agents", Tools: []string{"read_file", "delegate"}},
		"reviewer":    {Name: "reviewer", Description: "Reviews work", Tools: []string{"read_file"}},
	}

	// At allowance=0 (leaf/dark) no agent types are actionable through delegate.
	if got := availableAgentEntriesForTest(t, agents, 0, nil); len(got) != 0 {
		t.Fatalf("available agents should be hidden at allowance=0, got %+v", got)
	}
	// At allowance=1 (grantable) delegate-listing types are included.
	entries := availableAgentEntriesForTest(t, agents, 1, nil)
	for _, name := range []string{"coordinator", "reviewer"} {
		if !slices.ContainsFunc(entries, func(e agentEntry) bool { return e.Name == name }) {
			t.Fatalf("%s should be available at allowance=1, got %+v", name, entries)
		}
	}
}

func TestAvailableAgentsSectionSuppressedWhenDelegationSurfaceUnavailable(t *testing.T) {
	t.Parallel()
	agents := map[string]plugin.Agent{
		"reviewer": {Name: "reviewer", Description: "Reviews work", Tools: []string{"read_file"}},
	}
	if got := availableAgentEntriesForTest(t, agents, 1, []string{"communicate", "delegate", "job_watch"}); len(got) != 0 {
		t.Fatalf("available agents should be hidden when the delegation surface is incomplete, got %+v", got)
	}
}
