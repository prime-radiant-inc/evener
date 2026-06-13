package agent

import (
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
	t.Helper()

	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
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
