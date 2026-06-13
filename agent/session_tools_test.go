package agent

import (
	"slices"
	"strings"
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
			"delegate",
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
	// With allowance=2 (MaxSubagentDepth=2), the delegate-listing root-manager
	// type is grantable and must appear in the enum alongside non-delegate types.
	want := []string{"explorer", "implementer", "root-manager"}
	if !slices.Equal(enum, want) {
		t.Fatalf("agent_type enum = %v, want %v", enum, want)
	}
}

// TestAgentTypeRosterKeyedOnAllowance verifies that delegate-listing agent types
// are included in the roster when the session has grantable allowance (> 0) and
// filtered out when allowance is 0 (leaf/dark behavior).
func TestAgentTypeRosterKeyedOnAllowance(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	makeSession := func(allowance int) *Session {
		sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
			MaxSubagentDepth: 1, // root default
		})
		if err != nil {
			t.Fatal(err)
		}
		// Override allowance directly (same-package test).
		sess.mu.Lock()
		sess.delegationAllowance = allowance
		sess.mu.Unlock()
		sess.pluginAgents = map[string]plugin.Agent{
			"implementer": {Name: "implementer", Description: "implement"},
			"coordinator": {Name: "coordinator", Description: "coordinates agents", Tools: []string{
				"delegate",
			}},
			"explorer": {Name: "explorer", Description: "explore"},
		}
		return sess
	}

	t.Run("allowance=1 advertises delegate-listing type", func(t *testing.T) {
		sess := makeSession(1)
		defer sess.Close()

		entries := sess.availableAgentEntries()
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name)
		}

		if !slices.Contains(names, "coordinator") {
			t.Errorf("availableAgentEntries: delegate-listing type %q missing at allowance=1; got %v", "coordinator", names)
		}

		typeNames := sess.delegateAgentTypeNames()
		if !slices.Contains(typeNames, "coordinator") {
			t.Errorf("delegateAgentTypeNames: delegate-listing type %q missing at allowance=1; got %v", "coordinator", typeNames)
		}

		// The printed tool summary for coordinator should include "delegate" at allowance=1.
		coordinator := sess.pluginAgents["coordinator"]
		summary := sess.defaultToolSummaryForAgent(coordinator)
		if !strings.Contains(summary, "delegate") {
			t.Errorf("defaultToolSummaryForAgent: summary does not include %q at allowance=1; got %q", "delegate", summary)
		}
	})

	t.Run("allowance=0 filters delegate-listing type", func(t *testing.T) {
		sess := makeSession(0)
		defer sess.Close()

		entries := sess.availableAgentEntries()
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name)
		}

		if slices.Contains(names, "coordinator") {
			t.Errorf("availableAgentEntries: delegate-listing type %q should be absent at allowance=0; got %v", "coordinator", names)
		}

		typeNames := sess.delegateAgentTypeNames()
		if slices.Contains(typeNames, "coordinator") {
			t.Errorf("delegateAgentTypeNames: delegate-listing type %q should be absent at allowance=0; got %v", "coordinator", typeNames)
		}

		// The printed tool summary for coordinator should NOT include "delegate" at allowance=0.
		coordinator := sess.pluginAgents["coordinator"]
		summary := sess.defaultToolSummaryForAgent(coordinator)
		if strings.Contains(summary, "delegate") {
			t.Errorf("defaultToolSummaryForAgent: summary should not include %q at allowance=0; got %q", "delegate", summary)
		}
	})
}
