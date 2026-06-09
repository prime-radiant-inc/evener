package agent

import (
	"slices"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/llm"
)

func TestDelegateAdvertisesAgentTypeEnum(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	sess.pluginAgents = map[string]plugin.Agent{
		"implementer": {Name: "implementer", Description: "implement"},
		"root-manager": {Name: "root-manager", Description: "manage agents", Tools: []string{
			"spawn_agent",
		}},
		"explorer": {Name: "explorer", Description: "explore"},
	}
	sess.rebuildToolDefsCache()

	var delegate *llm.ToolDefinition
	for i := range sess.cachedToolDefs {
		if sess.cachedToolDefs[i].Name == "delegate" {
			delegate = &sess.cachedToolDefs[i]
			break
		}
	}
	if delegate == nil {
		t.Fatal("delegate not advertised")
	}

	props := delegate.Parameters["properties"].(map[string]any)
	agentType := props["agent_type"].(map[string]any)
	enum, ok := agentType["enum"].([]string)
	if !ok {
		t.Fatalf("agent_type enum = %T(%v), want []string", agentType["enum"], agentType["enum"])
	}
	want := []string{"explorer", "implementer"}
	if !slices.Equal(enum, want) {
		t.Fatalf("agent_type enum = %v, want %v", enum, want)
	}
}
