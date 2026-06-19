package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/llm"
)

func renderAvailableAgentsSectionForTest(t *testing.T, agents map[string]plugin.Agent) string {
	t.Helper()
	return renderAvailableAgentsSectionWithAllowance(t, agents, -1)
}

// renderAvailableAgentsSectionWithAllowance renders the available-agents section with
// the given delegationAllowance overridden (pass -1 to use the session default).
func renderAvailableAgentsSectionWithAllowance(t *testing.T, agents map[string]plugin.Agent, allowance int) string {
	return renderAvailableAgentsSectionWithAllowanceAndTools(t, agents, allowance, nil)
}

func renderAvailableAgentsSectionWithAllowanceAndTools(t *testing.T, agents map[string]plugin.Agent, allowance int, allowedTools []string) string {
	t.Helper()

	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})

	cfg := SessionConfig{}
	cfg.spawn.allowedToolNames = append([]string(nil), allowedTools...)
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if allowance >= 0 {
		sess.mu.Lock()
		sess.delegationAllowance = allowance
		sess.mu.Unlock()
	}

	sess.pluginAgents = make(map[string]plugin.Agent, len(agents))
	for name, agent := range agents {
		sess.pluginAgents[name] = agent
	}

	resolver := &sectionResolver{
		provider: sess.profile.ID(),
		agent:    defaultAgentName,
		agentFS:  embeddedAgents,
		sources: []sectionSource{
			embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"},
		},
	}
	return resolver.Section("available-agents", sess.buildPromptData())
}

// renderSubagentPromptWithAllowance builds a depth-1 child session with the
// given delegation_allowance and returns its rendered system prompt (the
// subagent template, selected because depth > 0).
func renderSubagentPromptWithAllowance(t *testing.T, allowance int) string {
	t.Helper()
	return renderSubagentPromptWithAllowanceAndTools(t, allowance, nil)
}

func renderSubagentPromptWithAllowanceAndTools(t *testing.T, allowance int, allowedTools []string) string {
	t.Helper()

	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})

	cfg := SessionConfig{
		StateDir:         t.TempDir(),
		NoProjectPrompts: true,
	}
	cfg.spawn.depth = 1
	cfg.spawn.parentSessionID = "parent-session"
	cfg.spawn.delegationAllowance = allowance
	cfg.spawn.allowedToolNames = append([]string(nil), allowedTools...)

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if sess.depth == 0 {
		t.Fatalf("expected depth > 0 subagent session, got depth 0")
	}
	return sess.renderSystemPrompt()
}

// TestSubagentPromptStatesAllowance pins spec §5: the subagent template has
// conditional sections keyed on CanDelegate. A child with allowance > 0 sees the
// delegation + background-jobs sections and is told its allowance; a leaf
// (allowance 0) child sees the leaf limits block and no delegation text.
func TestSubagentPromptStatesAllowance(t *testing.T) {
	// Allowance > 0: delegation surface present, allowance stated, no "only you".
	granting := renderSubagentPromptWithAllowance(t, 2)
	if !strings.Contains(granting, "## Delegation") {
		t.Errorf("allowance>0 subagent prompt should contain the Delegation section, got:\n%s", granting)
	}
	if !strings.Contains(granting, "## Background jobs") {
		t.Errorf("allowance>0 subagent prompt should contain the Background jobs section, got:\n%s", granting)
	}
	if !strings.Contains(granting, "2") {
		t.Errorf("allowance>0 subagent prompt should state its allowance (2), got:\n%s", granting)
	}
	if strings.Contains(granting, "Only you can call") {
		t.Errorf("allowance>0 subagent prompt must not say \"Only you can call\", got:\n%s", granting)
	}

	// Allowance 0 (leaf): leaf limits block present, no delegation text.
	leaf := renderSubagentPromptWithAllowance(t, 0)
	if !strings.Contains(leaf, "## Delegated task limits") {
		t.Errorf("allowance=0 subagent prompt should contain the leaf limits block, got:\n%s", leaf)
	}
	if strings.Contains(leaf, "## Delegation") {
		t.Errorf("allowance=0 subagent prompt must not contain the Delegation section, got:\n%s", leaf)
	}
}

func TestSubagentPromptUsesDelegateSendForFollowup(t *testing.T) {
	prompt := renderSubagentPromptWithAllowance(t, 2)
	if !strings.Contains(prompt, "delegate_send") {
		t.Fatalf("delegating subagent prompt should mention delegate_send:\n%s", prompt)
	}
	if !strings.Contains(prompt, "delegate_id") {
		t.Fatalf("delegating subagent prompt should describe delegate_id follow-up:\n%s", prompt)
	}
	if strings.Contains(prompt, "job_send_message") {
		t.Fatalf("delegating subagent prompt must not advertise removed job_send_message:\n%s", prompt)
	}
}

func TestSubagentPromptSuppressesDelegationWhenToolsUnavailable(t *testing.T) {
	prompt := renderSubagentPromptWithAllowanceAndTools(t, 1, []string{"communicate", "delegate", "job_watch"})
	if strings.Contains(prompt, "## Delegation") {
		t.Fatalf("subagent prompt must not contain delegation guidance when delegate tools are unavailable:\n%s", prompt)
	}
	if strings.Contains(prompt, "## Background jobs") {
		t.Fatalf("subagent prompt must not contain background-job guidance when job_watch is unavailable:\n%s", prompt)
	}
	if !strings.Contains(prompt, "## Delegated task limits") {
		t.Fatalf("subagent prompt should fall back to delegated-task limits when delegation tools are unavailable:\n%s", prompt)
	}
}

func TestUntypedDelegatingSubagentUsesDelegatingRolePrompt(t *testing.T) {
	client := llm.NewClient()
	var childPrompt string
	client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			for _, msg := range req.Messages {
				if msg.Role == llm.RoleSystem {
					childPrompt = msg.Text()
					break
				}
			}
			return communicateWithDefaultOutput("done")
		},
	}})

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		MaxSubagentDepth: 3,
		NoProjectPrompts: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:                "coordinate follow-up work",
		Background:          false,
		BlockTimeoutMS:      5000,
		DelegationAllowance: 1,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}
	if !strings.Contains(childPrompt, "You may delegate scoped subwork") {
		t.Fatalf("delegating untyped child prompt missing delegating role guidance:\n%s", childPrompt)
	}
	if strings.Contains(childPrompt, "Do NOT try to spawn further subagents") {
		t.Fatalf("delegating untyped child prompt used leaf role guidance:\n%s", childPrompt)
	}
}

func TestAvailableAgentsSection_NoAgents(t *testing.T) {
	result := renderAvailableAgentsSectionForTest(t, nil)
	if result != "" {
		t.Errorf("expected empty string for nil, got %q", result)
	}
	result = renderAvailableAgentsSectionForTest(t, map[string]plugin.Agent{})
	if result != "" {
		t.Errorf("expected empty string for empty map, got %q", result)
	}
}

func TestAvailableAgentsSection_WithAgents(t *testing.T) {
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
	result := renderAvailableAgentsSectionForTest(t, agents)
	if !strings.Contains(result, "my-plugin:reviewer") {
		t.Error("should contain agent name 'my-plugin:reviewer'")
	}
	if !strings.Contains(result, "Reviews code for quality") {
		t.Error("should contain agent description")
	}
	if !strings.Contains(result, "my-plugin:tester") {
		t.Error("should contain second agent name")
	}
	if !strings.Contains(result, "Generates test cases") {
		t.Error("should contain second agent description")
	}
	if !strings.Contains(result, "available_agents") {
		t.Error("should have available_agents XML tag")
	}
	if !strings.Contains(result, "delegate") {
		t.Error("should mention delegate usage")
	}
	if !strings.Contains(result, "Default tools: `communicate`, `grep_files`, `read_file`, `task_list`") {
		t.Errorf("should include default tool summary, got: %s", result)
	}
	if !strings.Contains(result, "Include relevant parent task details in the `delegate` task prompt for this step.") {
		t.Errorf("should explain parent task slot behavior, got: %s", result)
	}
}

func TestAvailableAgentsSection_Sorted(t *testing.T) {
	agents := map[string]plugin.Agent{
		"z-plugin:agent": {Name: "agent", Description: "Z agent", PluginName: "z-plugin"},
		"a-plugin:agent": {Name: "agent", Description: "A agent", PluginName: "a-plugin"},
	}
	result := renderAvailableAgentsSectionForTest(t, agents)
	aIdx := strings.Index(result, "a-plugin:agent")
	zIdx := strings.Index(result, "z-plugin:agent")
	if aIdx < 0 || zIdx < 0 {
		t.Fatalf("expected both agents in output, got: %s", result)
	}
	if aIdx >= zIdx {
		t.Errorf("agents should be sorted alphabetically: a at %d, z at %d", aIdx, zIdx)
	}
}

func TestAvailableAgentsSection_OmitsTopLevelOnlyAgents(t *testing.T) {
	agents := map[string]plugin.Agent{
		"coordinator": {Name: "coordinator", Description: "Delegates to agents", Tools: []string{"read_file", "delegate"}},
		"reviewer":    {Name: "reviewer", Description: "Reviews work", Tools: []string{"read_file"}},
	}

	// At allowance=0 (leaf/dark) delegate-listing types must be filtered out.
	result := renderAvailableAgentsSectionWithAllowance(t, agents, 0)
	if strings.Contains(result, "coordinator") {
		t.Fatalf("delegate-listing agent should not be included at allowance=0, got: %s", result)
	}
	if !strings.Contains(result, "reviewer") {
		t.Fatalf("spawnable agent should remain in prompt at allowance=0, got: %s", result)
	}

	// At allowance=1 (grantable) delegate-listing types ARE included in the prompt.
	result = renderAvailableAgentsSectionWithAllowance(t, agents, 1)
	if !strings.Contains(result, "coordinator") {
		t.Fatalf("delegate-listing agent should be included at allowance=1, got: %s", result)
	}
	if !strings.Contains(result, "reviewer") {
		t.Fatalf("spawnable agent should remain in prompt at allowance=1, got: %s", result)
	}
}

func TestAvailableAgentsSectionSuppressedWhenDelegationSurfaceUnavailable(t *testing.T) {
	agents := map[string]plugin.Agent{
		"reviewer": {Name: "reviewer", Description: "Reviews work", Tools: []string{"read_file"}},
	}

	result := renderAvailableAgentsSectionWithAllowanceAndTools(t, agents, 1, []string{"communicate", "delegate", "job_watch"})
	if result != "" {
		t.Fatalf("available agents should be hidden when delegation prompt surface is incomplete, got: %s", result)
	}
}
