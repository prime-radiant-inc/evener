package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/sandbox"
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

func TestDelegateSurfaceUsesAgentRegistryCapabilities(t *testing.T) {
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
	enum, ok := agentType["enum"].([]string)
	if !ok {
		t.Fatalf("agent_type enum = %T(%v), want []string", agentType["enum"], agentType["enum"])
	}
	if want := sess.delegateAgentTypeNames(); !slices.Equal(enum, want) {
		t.Fatalf("agent_type enum = %v, want registry names %v", enum, want)
	}

	sandboxParam := props["sandbox"].(map[string]any)
	sandboxEnum, ok := sandboxParam["enum"].([]string)
	if !ok {
		t.Fatalf("sandbox enum = %T(%v), want []string", sandboxParam["enum"], sandboxParam["enum"])
	}
	wantSandboxEnum := []string{
		sandbox.ModeOff.String(),
		sandbox.ModeReadOnly.String(),
		sandbox.ModeWorkspaceWrite.String(),
		sandbox.ModeRestricted.String(),
	}
	if !slices.Equal(sandboxEnum, wantSandboxEnum) {
		t.Fatalf("sandbox enum = %v, want %v", sandboxEnum, wantSandboxEnum)
	}
	if got := props["sandbox_net"].(map[string]any)["type"]; got != "boolean" {
		t.Fatalf("sandbox_net type = %v, want boolean", got)
	}

	data := sess.buildPromptData(sess.currentEnv())
	entryNames := make([]string, 0, len(data.AvailableAgents))
	for _, entry := range data.AvailableAgents {
		entryNames = append(entryNames, entry.Name)
	}
	if !slices.Equal(entryNames, enum) {
		t.Fatalf("available-agents typed input = %v, want schema enum %v", entryNames, enum)
	}
	const availableAgentsSection = "embedded:prompts/sections/available-agents.md.tmpl"
	if !slices.ContainsFunc(sess.promptSourceLog, func(source promptSource) bool {
		return source.Label == availableAgentsSection
	}) {
		t.Fatalf("prompt sources = %#v, want %q section", sess.promptSourceLog, availableAgentsSection)
	}

	explorerTools := delegateToolsForRegisteredAgent(t, sess, "explorer")
	defaultTools := delegateToolsForRegisteredAgent(t, sess, "default")
	if !slices.Contains(explorerTools, "shell") || slices.Contains(explorerTools, "write_file") {
		t.Fatalf("explorer tools = %v, want shell without write_file", explorerTools)
	}
	if !slices.Contains(defaultTools, "write_file") {
		t.Fatalf("default tools = %v, want write_file", defaultTools)
	}
	for name, tools := range map[string][]string{"explorer": explorerTools, "default": defaultTools} {
		if slices.Contains(tools, "ask_user") {
			t.Fatalf("%s tools = %v, ask_user is root-only", name, tools)
		}
	}
}

func TestDelegateCallResponseReportsSelectedAgentCapabilities(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	wantTools := delegateToolsForRegisteredAgent(t, root, "explorer")

	call := root.reg.ExecuteCall(context.Background(), root.currentEnv(), llm.ToolCallData{
		ID:   "delegate-capability-response",
		Name: "delegate",
		Arguments: json.RawMessage(`{
			"task":"TASK_SENTINEL",
			"agent_type":"explorer"
		}`),
	})
	if call.IsError {
		t.Fatalf("delegate call: %s", call.Output)
	}
	var state stableDelegateCreateResult
	if err := json.Unmarshal(toolResultJSON(call), &state); err != nil {
		t.Fatalf("decode delegate result: %v", err)
	}
	if state.AgentType != "explorer" {
		t.Fatalf("delegate result agent_type = %q, want explorer", state.AgentType)
	}
	if !slices.Equal(state.Tools, wantTools) {
		t.Fatalf("delegate result tools = %v, want registry-derived %v", state.Tools, wantTools)
	}
	if state.Sandbox == nil || state.Sandbox.Mode != "read-only" || !state.Sandbox.Network {
		t.Fatalf("delegate result sandbox = %#v, want effective read-only sandbox with network", state.Sandbox)
	}
}

func delegateToolsForRegisteredAgent(t *testing.T, sess *Session, name string) []string {
	t.Helper()
	agent, ok := sess.pluginAgents[name]
	if !ok {
		t.Fatalf("agent registry missing %q", name)
	}
	allTools, allowed, denied := baseSubagentToolPolicy(&agent, false)
	return stableDelegateToolNameCeiling(sess.reg, sess.resultToolName(), allTools, allowed, denied, false, false, "")
}
