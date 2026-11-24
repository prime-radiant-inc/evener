package agent

import (
	"context"
	"encoding/json"
	"slices"
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
	sess.mu.Lock()
	if td, ok := sess.deferredToolDefinitionLocked("delegate"); ok {
		delegate = &td
	}
	sess.mu.Unlock()
	if delegate == nil {
		t.Fatal("deferred delegate definition missing")
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

// TestAgentTypeRosterKeyedOnAllowance verifies that the roster is listed while
// the session may start delegates (allowance > 0), that it is filtered out at
// allowance 0, and that each role's advertised tools are the ones the session's
// default grant actually gives the child.
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
			"all":      {Name: "all", Description: "every tool", AllTools: true},
		}
		return sess
	}

	t.Run("allowance=1 advertises leaf tools", func(t *testing.T) {
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

		// defaultDelegateGrant(1) is 0, so every delegate started here is a leaf:
		// the roles this roster offers must not claim the tools a leaf loses.
		for _, role := range []string{"coordinator", "all"} {
			tools := sess.defaultToolNamesForAgent(sess.pluginAgents[role])
			for _, name := range []string{"delegate", "manage_worktree", "model_list"} {
				if slices.Contains(tools, name) {
					t.Errorf("%s advertises %s at allowance=1, but a delegate started here gets allowance 0; tools %v", role, name, tools)
				}
			}
		}
		// The all-tools role's capability set must not advertise tools every
		// subagent loses at session init (#6080).
		if sess.reg.Get("urls_add") == nil {
			t.Fatal("session registry has no urls_add; the roster fixture cannot prove the strip")
		}
		allTools := sess.defaultToolNamesForAgent(sess.pluginAgents["all"])
		for _, tool := range []string{"ask_user", "urls_add", "urls_remove"} {
			if slices.Contains(allTools, tool) {
				t.Errorf("all-tools roster advertises %q; tools %v", tool, allTools)
			}
		}
	})

	t.Run("allowance=2 advertises delegation tools", func(t *testing.T) {
		sess := makeSession(2)
		defer sess.Close()

		// defaultDelegateGrant(2) is 1, so a delegate started here delegates in
		// turn and the roster must say so for the roles that carry the tool.
		coordinatorTools := sess.defaultToolNamesForAgent(sess.pluginAgents["coordinator"])
		if !slices.Contains(coordinatorTools, "delegate") {
			t.Errorf("coordinator does not advertise delegate at allowance=2; tools %v", coordinatorTools)
		}
		allTools := sess.defaultToolNamesForAgent(sess.pluginAgents["all"])
		for _, name := range []string{"delegate", "manage_worktree", "model_list"} {
			if !slices.Contains(allTools, name) {
				t.Errorf("all does not advertise %s at allowance=2; tools %v", name, allTools)
			}
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

		// A leaf grants nothing, so its roster claims no delegation tools.
		coordinatorTools := sess.defaultToolNamesForAgent(sess.pluginAgents["coordinator"])
		if slices.Contains(coordinatorTools, "delegate") {
			t.Errorf("coordinator roster advertises delegate at allowance=0; tools %v", coordinatorTools)
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
		testOnly: testConfig{
			sandboxProber: bwrapCapableProber(dir),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	defs := sess.ToolDefinitions()
	if !hasToolDef(defs, "call_tool") {
		t.Fatal("call_tool not advertised")
	}
	var delegate *llm.ToolDefinition
	for i := range defs {
		if defs[i].Name == "delegate" {
			delegate = &defs[i]
			break
		}
	}
	if delegate == nil {
		sess.mu.Lock()
		if td, ok := sess.deferredToolDefinitionLocked("delegate"); ok {
			delegate = &td
		}
		sess.mu.Unlock()
	}
	if delegate == nil {
		t.Fatal("deferred delegate definition missing")
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
		sandbox.ModeReadOnly.String() + "+nonet",
		sandbox.ModeWorkspaceWrite.String(),
		sandbox.ModeWorkspaceWrite.String() + "+nonet",
		sandbox.ModeRestricted.String(),
		sandbox.ModeRestricted.String() + "+nonet",
	}
	if !slices.Equal(sandboxEnum, wantSandboxEnum) {
		t.Fatalf("sandbox enum = %v, want %v", sandboxEnum, wantSandboxEnum)
	}
	if _, hasNet := props["sandbox_net"]; hasNet {
		t.Fatal("sandbox_net property should not exist in the combined-enum schema")
	}

	data, _ := sess.buildPromptData(sess.currentEnv())
	entryNames := make([]string, 0, len(data.AvailableAgents))
	for _, entry := range data.AvailableAgents {
		entryNames = append(entryNames, entry.Name)
	}
	if !slices.Equal(entryNames, enum) {
		t.Fatalf("available-agents typed input = %v, want schema enum %v", entryNames, enum)
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
	t.Parallel()
	root, _, _ := newDelegateResourceBootstrapSession(t)
	wantTools := root.providerVisibleToolNames(delegateToolsForRegisteredAgent(t, root, "explorer"))

	call := root.reg.ExecuteCall(context.Background(), root.currentEnv(), llm.ToolCallData{
		ID:   "delegate-capability-response",
		Name: "delegate",
		Arguments: json.RawMessage(`{
			"prompt":"TASK_SENTINEL",
			"agent_type":"explorer"
		}`),
	})
	if call.IsError {
		t.Fatalf("delegate call: %s", call.Output)
	}
	var state delegateCreateResult
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
	return delegateToolNameCeiling(sess.reg, sess.resultToolName(), allTools, allowed, denied, false, "")
}
