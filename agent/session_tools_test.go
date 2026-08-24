package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/llm"
)

func TestDelegateAdvertisesAgentTypeEnum(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

		if len(names) != 0 {
			t.Fatalf("availableAgentEntries: allowance=0 should advertise no agents; got %v", names)
		}
		if slices.Contains(names, "coordinator") {
			t.Errorf("availableAgentEntries: delegate-listing type %q should be absent at allowance=0; got %v", "coordinator", names)
		}

		typeNames := sess.delegateAgentTypeNames()
		if len(typeNames) != 0 {
			t.Fatalf("delegateAgentTypeNames: allowance=0 should advertise no agent types; got %v", typeNames)
		}
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

func TestDelegateSurfaceDescribesEffectiveAgentCapabilities(t *testing.T) {
	t.Parallel()
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

	defs := sess.ToolDefinitions()
	var delegate *llm.ToolDefinition
	for i := range defs {
		if defs[i].Name == "delegate" {
			delegate = &defs[i]
			break
		}
	}
	if delegate == nil {
		t.Fatal("delegate not advertised")
	}
	props := delegate.Parameters["properties"].(map[string]any)
	agentType := props["agent_type"].(map[string]any)
	description := agentType["description"].(string)
	if !strings.Contains(delegate.Description, "Role capabilities are listed in the agent_type schema") {
		t.Fatalf("delegate description omits generated capability roster: %q", delegate.Description)
	}
	if !strings.Contains(description, "explorer") || !strings.Contains(description, "exec_command") {
		t.Fatalf("delegate agent_type description omits explorer's effective shell capability: %q", description)
	}
	if !strings.Contains(description, "write_file") {
		t.Fatalf("delegate agent_type description omits broader role capability: %q", description)
	}
	entries := sess.availableAgentEntries()
	var explorerTools, defaultTools string
	for _, entry := range entries {
		if strings.Contains(entry.DefaultTools, "ask_user") {
			t.Fatalf("agent %q advertises ask_user although subagents cannot receive it: %q", entry.Name, entry.DefaultTools)
		}
		switch entry.Name {
		case "explorer":
			explorerTools = entry.DefaultTools
			if description := strings.ToLower(entry.Description); !strings.Contains(description, "network") || !strings.Contains(description, "sandbox") {
				t.Fatalf("assembled explorer description omits environment-bounded network reach: %q", entry.Description)
			}
		case "default":
			defaultTools = entry.DefaultTools
		}
	}
	if strings.Contains(explorerTools, "write_file") || !strings.Contains(explorerTools, "exec_command") {
		t.Fatalf("assembled explorer capabilities = %q, want shell reach without write_file", explorerTools)
	}
	if !strings.Contains(defaultTools, "write_file") {
		t.Fatalf("assembled default capabilities = %q, want broader write capability", defaultTools)
	}

	prompt := sess.cachedSystemPrompt
	if !strings.Contains(prompt, "Capabilities:") || !strings.Contains(prompt, "explorer") {
		t.Fatalf("available-agents prompt omits generated capability summaries: %q", prompt)
	}
}

func TestDelegateCallResponseReportsSelectedAgentCapabilities(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	root.pluginAgents = map[string]plugin.Agent{
		"explorer": {
			Name:        "explorer",
			Description: "read-only scout",
			Tools:       []string{"glob", "grep", "read_file", "shell"},
			PluginName:  "test-plugin",
		},
		"implementer": {
			Name:        "implementer",
			Description: "broader implementation agent",
			Tools:       []string{"glob", "grep", "read_file", "write_file", "shell"},
			PluginName:  "test-plugin",
		},
	}
	root.rebuildToolDefsCache()

	call := root.reg.ExecuteCall(context.Background(), root.currentEnv(), llm.ToolCallData{
		ID:   "delegate-capability-response",
		Name: "delegate",
		Arguments: json.RawMessage(`{
			"task":"report your capability surface",
			"agent_type":"explorer"
		}`),
	})
	if call.IsError {
		t.Fatalf("delegate call: %s", call.Output)
	}
	state := map[string]any{}
	if err := json.Unmarshal(toolResultJSON(call), &state); err != nil {
		t.Fatalf("decode delegate result: %v", err)
	}
	if got := state["agent_type"]; got != "explorer" {
		t.Fatalf("delegate result agent_type = %#v, want explorer", got)
	}
	tools, ok := state["tools"].([]any)
	if !ok {
		t.Fatalf("delegate result tools = %#v, want array", state["tools"])
	}
	if !slices.Contains(toStringSlice(tools), "shell") {
		t.Fatalf("explorer result tools = %#v, want shell", tools)
	}
	if slices.Contains(toStringSlice(tools), "write_file") {
		t.Fatalf("explorer result tools = %#v, must not claim broader write capability", tools)
	}
}
