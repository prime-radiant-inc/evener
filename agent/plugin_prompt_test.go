package agent

import (
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/llm"
)

func renderAvailableAgentsSectionForTest(t *testing.T, agents map[string]PluginAgent) string {
	t.Helper()

	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	sess.pluginAgents = make(map[string]PluginAgent, len(agents))
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
	result = renderAvailableAgentsSectionForTest(t, map[string]PluginAgent{})
	if result != "" {
		t.Errorf("expected empty string for empty map, got %q", result)
	}
}

func TestAvailableAgentsSection_WithAgents(t *testing.T) {
	agents := map[string]PluginAgent{
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
	if !strings.Contains(result, "spawn_agent") {
		t.Error("should mention spawn_agent usage")
	}
	if !strings.Contains(result, "Default tools: `communicate`, `grep_files`, `read_file`, `task_list`") {
		t.Errorf("should include default tool summary, got: %s", result)
	}
	if !strings.Contains(result, "Delegated tasks from `task_list` replace this step when provided.") {
		t.Errorf("should explain parent task slot behavior, got: %s", result)
	}
}

func TestAvailableAgentsSection_Sorted(t *testing.T) {
	agents := map[string]PluginAgent{
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
	agents := map[string]PluginAgent{
		"coordinator": {Name: "coordinator", Description: "Delegates to agents", Tools: []string{"read_file", "spawn_agent"}},
		"reviewer":    {Name: "reviewer", Description: "Reviews work", Tools: []string{"read_file"}},
	}
	result := renderAvailableAgentsSectionForTest(t, agents)
	if strings.Contains(result, "coordinator") {
		t.Fatalf("top-level-only agent should not be included in subagent prompt, got: %s", result)
	}
	if !strings.Contains(result, "reviewer") {
		t.Fatalf("spawnable agent should remain in prompt, got: %s", result)
	}
}
