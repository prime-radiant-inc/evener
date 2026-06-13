package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/toolname"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/llm"
)

// --- builtinAgents() ---

func TestBuiltinAgents_LoadsCoreRoles(t *testing.T) {
	agents, err := builtinAgents()
	if err != nil {
		t.Fatalf("builtinAgents: %v", err)
	}
	want := []string{"default", "explorer", "subagent"}
	for _, name := range want {
		agent, ok := agents[name]
		if !ok {
			t.Errorf("missing built-in agent %q", name)
			continue
		}
		if name != defaultAgentName && agent.SystemPrompt == "" {
			t.Errorf("agent %q has empty system prompt", name)
		}
		if agent.Description == "" {
			t.Errorf("agent %q has empty description", name)
		}
	}
}

func TestWorkflowPlugin_LoadWorkflowRoles(t *testing.T) {
	agents := coordinatorWorkflowPublicAgentsForTest(t)
	want := []string{"coordinator", "planner", "implementer", "reviewer", "verifier", "worker", "test-engineer"}
	for _, name := range want {
		agent, ok := agents[name]
		if !ok {
			t.Errorf("missing coordinator workflow agent %q", name)
			continue
		}
		if agent.PluginName != coordinatorWorkflowPluginName {
			t.Errorf("agent %q PluginName = %q, want %q", name, agent.PluginName, coordinatorWorkflowPluginName)
		}
		if agent.SystemPrompt == "" {
			t.Errorf("agent %q has empty system prompt", name)
		}
	}
}

func TestBuiltinAgents_DefaultUsesAllToolsShorthand(t *testing.T) {
	agents, err := builtinAgents()
	if err != nil {
		t.Fatalf("builtinAgents: %v", err)
	}
	def, ok := agents["default"]
	if !ok {
		t.Fatal("expected built-in 'default' agent")
	}
	if !def.AllTools {
		t.Fatal("default agent should opt into explicit all-tools mode")
	}
	if len(def.Tools) != 0 {
		t.Fatalf("default agent should not need an explicit tool list, got %v", def.Tools)
	}
}

func TestBuiltinAgents_DefaultHasNoTaskWorkflow(t *testing.T) {
	agents, err := builtinAgents()
	if err != nil {
		t.Fatalf("builtinAgents: %v", err)
	}
	def, ok := agents["default"]
	if !ok {
		t.Fatal("expected built-in 'default' agent")
	}
	if len(def.Tasks) != 0 {
		t.Fatalf("default agent should not define task templates, got %#v", def.Tasks)
	}
}

func TestSession_DefaultFallbackUsesDefaultAgentPrompt(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	foundDefaultRoleSource := false
	for _, src := range sess.promptSourceLog {
		if src.Label == "agent:"+defaultAgentName {
			foundDefaultRoleSource = true
			break
		}
	}
	if !foundDefaultRoleSource {
		t.Fatalf("default session prompt should resolve the default agent role, sources=%v", sess.promptSourceLog)
	}
	if strings.Contains(sess.cachedSystemPrompt, "You are a coordinator. You delegate, verify, and iterate. You do not implement.") {
		t.Fatalf("default session prompt should not use coordinator persona, got:\n%s", sess.cachedSystemPrompt)
	}
}

func TestBuiltinAgents_LoadsExplorer(t *testing.T) {
	agents, err := builtinAgents()
	if err != nil {
		t.Fatalf("builtinAgents: %v", err)
	}
	explorer, ok := agents["explorer"]
	if !ok {
		t.Fatal("expected built-in 'explorer' agent")
	}
	if explorer.Name != "explorer" {
		t.Errorf("Name = %q, want %q", explorer.Name, "explorer")
	}
	if explorer.Model != "openai/gpt-5.4-mini" {
		t.Errorf("Model = %q, want %q", explorer.Model, "openai/gpt-5.4-mini")
	}
	if explorer.PluginName != "builtin" {
		t.Errorf("PluginName = %q, want %q", explorer.PluginName, "builtin")
	}
	if explorer.Description == "" {
		t.Error("expected non-empty description")
	}
	if explorer.SystemPrompt == "" {
		t.Error("expected non-empty system prompt body")
	}
}

func TestBuiltinAgents_ExplorerTools(t *testing.T) {
	agents, err := builtinAgents()
	if err != nil {
		t.Fatalf("builtinAgents: %v", err)
	}
	explorer := agents["explorer"]

	// Explorer tools should be serf canonical names only.
	// The frontmatter uses serf names which pass through toolname.ClaudeToSerf unchanged.
	wantTools := map[string]bool{
		"glob":      true,
		"grep":      true,
		"read_file": true,
		"shell":     true,
	}
	if len(explorer.Tools) != len(wantTools) {
		t.Fatalf("Tools = %v, want exactly %v", explorer.Tools, wantTools)
	}
	for _, tool := range explorer.Tools {
		if !wantTools[tool] {
			t.Errorf("unexpected tool %q in explorer", tool)
		}
	}
	// Explorer must NOT have write_file (read-only agent).
	for _, tool := range explorer.Tools {
		if tool == "write_file" {
			t.Error("explorer should not have write_file (read-only)")
		}
	}
}

func TestBuiltinAgents_ExplorerIsReadOnly(t *testing.T) {
	agents, err := builtinAgents()
	if err != nil {
		t.Fatalf("builtinAgents: %v", err)
	}
	explorer := agents["explorer"]
	prompt := strings.ToLower(explorer.SystemPrompt)
	if !strings.Contains(prompt, "read-only") {
		t.Error("explorer system prompt should mention read-only constraint")
	}
}

func TestBuiltinAgents_ToolNamesAreCanonical(t *testing.T) {
	agents, err := builtinAgents()
	if err != nil {
		t.Fatalf("builtinAgents: %v", err)
	}
	for name, agent := range agents {
		for _, tool := range agent.Tools {
			// If a tool name maps to something different, it was a Claude Code name.
			mapped := toolname.ClaudeToSerf(tool)
			if mapped != tool {
				t.Errorf("agent %q tool %q is a Claude Code name (maps to %q), should use serf canonical name", name, tool, mapped)
			}
		}
	}
}

// --- builtinAgents in session ---

func TestSession_HasBuiltinExplorerAgent(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	explorer, ok := sess.pluginAgents["explorer"]
	if !ok {
		t.Fatal("session should have built-in 'explorer' agent")
	}
	if explorer.PluginName != "builtin" {
		t.Errorf("explorer PluginName = %q, want %q", explorer.PluginName, "builtin")
	}
}

func TestSession_DoesNotLoadWorkflowPluginByDefault(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if _, ok := sess.pluginAgents["reviewer"]; ok {
		t.Fatal("vanilla session should not load the coordinator workflow reviewer agent")
	}
}

func TestSession_LoadsWorkflowReviewerAgentFromConfiguredPlugin(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), coordinatorWorkflowSessionConfig(t, SessionConfig{}))
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	reviewer, ok := sess.pluginAgents["reviewer"]
	if !ok {
		t.Fatal("session should expose the configured coordinator workflow reviewer agent")
	}
	if reviewer.PluginName != coordinatorWorkflowPluginName {
		t.Errorf("reviewer PluginName = %q, want %q", reviewer.PluginName, coordinatorWorkflowPluginName)
	}
}

func TestSession_PluginAgentOverridesBuiltin(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Simulate a plugin agent overriding the built-in explorer.
	sess.pluginAgents["explorer"] = plugin.Agent{
		Name:         "explorer",
		Description:  "Custom explorer from plugin",
		Model:        "inherit",
		PluginName:   "my-plugin",
		SystemPrompt: "Custom prompt.",
	}

	agent := sess.pluginAgents["explorer"]
	if agent.PluginName != "my-plugin" {
		t.Errorf("plugin agent should override built-in, got PluginName=%q", agent.PluginName)
	}
}

// --- available-agents section tag ---

func TestAvailableAgentsSection_UsesAvailableAgentsTag(t *testing.T) {
	agents := map[string]plugin.Agent{
		"explorer": {Name: "explorer", Description: "Explores code"},
	}
	result := renderAvailableAgentsSectionForTest(t, agents)
	if !strings.Contains(result, "<available_agents>") {
		t.Error("should contain <available_agents> opening tag")
	}
	if !strings.Contains(result, "</available_agents>") {
		t.Error("should contain </available_agents> closing tag")
	}
	if strings.Contains(result, "plugin_agents") {
		t.Error("should NOT contain old 'plugin_agents' tag")
	}
}

type releaseAdapter struct {
	name    string
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (a *releaseAdapter) Name() string { return a.name }

func (a *releaseAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.once.Do(func() { close(a.started) })
	select {
	case <-a.release:
		resp := finalResponse("child done")
		resp.Provider = a.name
		if resp.Model == "" {
			resp.Model = req.Model
		}
		return resp, nil
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
}

func (a *releaseAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, llm.ErrStreamUnsupported
}

func TestSpawnAgent_NonBlockingSubagentSurvivesParentContextCancellation(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	adapter := &releaseAdapter{
		name:    "openai",
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	c.Register(adapter)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	ctx, cancel := context.WithCancel(context.Background())
	result, err := sess.spawnAgent(ctx, "test task", "", "", 0, "", "", nil, nil)
	if err != nil {
		t.Fatalf("spawnAgent: %v", err)
	}
	var spawned struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal([]byte(result.(string)), &spawned); err != nil {
		t.Fatalf("unmarshal spawn result: %v", err)
	}
	agentID := spawned.AgentID

	select {
	case <-adapter.started:
	case <-time.After(5 * time.Second):
		t.Fatal("subagent did not start")
	}

	cancel()
	close(adapter.release)

	runResult := waitForRuntimeSubagent(t, sess, agentID)
	if !runResult.Success {
		t.Fatalf("expected success=true, got false (out=%q)", runResult.Output)
	}
	if runResult.Output != "child done" {
		t.Fatalf("expected output=%q, got %q", "child done", runResult.Output)
	}
}

func TestSpawnAgent_BlockingWithExplorerAgent(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	var subagentSystemPrompt string
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				for _, m := range req.Messages {
					if m.Role == llm.RoleSystem {
						subagentSystemPrompt = m.Text()
					}
				}
				return finalResponse("exploration complete")
			},
		},
	}
	c.Register(adapter)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	agentID := spawnRuntimeAgent(t, sess, "survey the project", "", 0, "explorer", "", nil)
	waitForRuntimeSubagent(t, sess, agentID)

	// Should have used the explorer's system prompt.
	if !strings.Contains(subagentSystemPrompt, "workspace scout") {
		t.Errorf("subagent should use explorer prompt, got:\n%.200s...", subagentSystemPrompt)
	}
}

func TestSpawnAgent_PluginAgentGetsComposedPrompt(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	var subagentSystemPrompt string
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				for _, m := range req.Messages {
					if m.Role == llm.RoleSystem {
						subagentSystemPrompt = m.Text()
					}
				}
				return finalResponse("done")
			},
		},
	}
	c.Register(adapter)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	agentID := spawnRuntimeAgent(t, sess, "survey the project", "", 0, "explorer", "", nil)
	waitForRuntimeSubagent(t, sess, agentID)

	// Subagent prompt should contain template-rendered content AND the agent-specific prompt.
	if !strings.Contains(subagentSystemPrompt, "communicate") {
		t.Error("subagent prompt should contain communicate guidance")
	}
	if !strings.Contains(subagentSystemPrompt, "workspace scout") {
		t.Error("subagent prompt should contain agent-specific prompt (explorer)")
	}
}

func TestSpawnAgent_DefaultSubagentGetsComposedPrompt(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	var subagentSystemPrompt string
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				for _, m := range req.Messages {
					if m.Role == llm.RoleSystem {
						subagentSystemPrompt = m.Text()
					}
				}
				return finalResponse("done")
			},
		},
	}
	c.Register(adapter)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	agentID := spawnRuntimeAgent(t, sess, "do something", "", 0, "", "", nil)
	waitForRuntimeSubagent(t, sess, agentID)

	// Default subagent should get core + subagent persona.
	if !strings.Contains(subagentSystemPrompt, "communicate") {
		t.Error("default subagent prompt should contain communicate guidance from core")
	}
	if !strings.Contains(subagentSystemPrompt, "Delegated task limits") {
		t.Error("default subagent prompt should contain shared delegated-task guidance")
	}
	if !strings.Contains(subagentSystemPrompt, "focused subagent") {
		t.Error("default subagent prompt should contain subagent persona instructions")
	}
}

func TestSpawnAgent_SystemPromptFileDoesNotOverrideSubagentPrompt(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "root-system-prompt.md")
	if err := os.WriteFile(promptFile, []byte("ROOT ONLY CUSTOM PROMPT"), 0644); err != nil {
		t.Fatal(err)
	}

	c := llm.NewClient()

	var subagentSystemPrompt string
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				for _, m := range req.Messages {
					if m.Role == llm.RoleSystem {
						subagentSystemPrompt = m.Text()
					}
				}
				return finalResponse("done")
			},
		},
	}
	c.Register(adapter)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
		SystemPromptFile: promptFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	if !strings.Contains(sess.cachedSystemPrompt, "ROOT ONLY CUSTOM PROMPT") {
		t.Fatal("root cached system prompt should include the custom top-level prompt")
	}

	agentID := spawnRuntimeAgent(t, sess, "do something", "", 0, "", "", nil)
	waitForRuntimeSubagent(t, sess, agentID)

	if strings.Contains(subagentSystemPrompt, "ROOT ONLY CUSTOM PROMPT") {
		t.Fatalf("subagent prompt should not inherit the root-only system prompt override:\n%s", subagentSystemPrompt)
	}
	if !strings.Contains(subagentSystemPrompt, "Delegated task limits") {
		t.Fatalf("subagent prompt should still use the dedicated subagent template:\n%s", subagentSystemPrompt)
	}
}

func TestSpawnAgent_ReasoningEffortApplied(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	var subagentReasoningEffort *string
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				subagentReasoningEffort = req.ReasoningEffort
				return finalResponse("done")
			},
		},
	}
	c.Register(adapter)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	agentID := spawnRuntimeAgent(t, sess, "hard problem", "", 0, "", "xhigh", nil)
	waitForRuntimeSubagent(t, sess, agentID)
	if subagentReasoningEffort == nil || *subagentReasoningEffort != "xhigh" {
		got := "<nil>"
		if subagentReasoningEffort != nil {
			got = *subagentReasoningEffort
		}
		t.Errorf("subagent reasoning_effort = %s, want xhigh", got)
	}
}

// --- stuck escalation ---

func TestStuckEscalation_BumpsReasoning(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// First stuck: should bump to high
	msg := sess.stuckEscalation(1)
	if !strings.Contains(msg, "stuck in a loop") {
		t.Errorf("first escalation should mention stuck, got: %s", msg)
	}
	if sess.cfg.ReasoningEffort != "high" {
		t.Errorf("first escalation should bump to high, got: %s", sess.cfg.ReasoningEffort)
	}

	// Second stuck: should tell agent to abandon approach
	msg = sess.stuckEscalation(2)
	if !strings.Contains(msg, "different strategy") {
		t.Errorf("second escalation should say different strategy, got: %s", msg)
	}

	// Third: should tell agent to report
	msg = sess.stuckEscalation(3)
	if !strings.Contains(msg, "report what you tried") {
		t.Errorf("third escalation should tell agent to report, got: %s", msg)
	}
}

func TestStuckEscalation_BumpsFromHighToXhigh(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	sess.stuckEscalation(1)
	if sess.cfg.ReasoningEffort != "xhigh" {
		t.Errorf("should bump high to xhigh, got: %s", sess.cfg.ReasoningEffort)
	}
}

// --- reviewer agent is read-only ---

func TestWorkflowPlugin_ReviewerIsReadOnly(t *testing.T) {
	agents := coordinatorWorkflowPublicAgentsForTest(t)
	reviewer := agents["reviewer"]
	for _, tool := range reviewer.Tools {
		if tool == "write_file" || tool == "edit_file" || tool == "apply_patch" {
			t.Errorf("reviewer should not have write tool %q", tool)
		}
	}
}

// --- task_list preserved for all subagents ---

func TestSpawnAgent_TaskListPreservedForNamedAgent(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	var subagentTools []string
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				for _, td := range req.Tools {
					subagentTools = append(subagentTools, td.Name)
				}
				return finalResponse("done")
			},
		},
	}
	c.Register(adapter)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// Spawn an explorer (named agent with explicit tool list that doesn't include task_list).
	agentID := spawnRuntimeAgent(t, sess, "explore the code", "", 0, "explorer", "", nil)
	waitForRuntimeSubagent(t, sess, agentID)

	// task_list should be in the subagent's tools even though explorer.md doesn't list it.
	hasTaskList := false
	for _, name := range subagentTools {
		if name == "task_list" {
			hasTaskList = true
			break
		}
	}
	if !hasTaskList {
		t.Errorf("named subagent should have task_list, got tools: %v", subagentTools)
	}
}

func TestSpawnAgent_AllToolsAgentStripsAgentManagementTools(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	var subagentTools []string
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				for _, td := range req.Tools {
					subagentTools = append(subagentTools, td.Name)
				}
				return finalResponse("done")
			},
		},
	}
	c.Register(adapter)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	agentID := spawnRuntimeAgent(t, sess, "help with the task", "", 0, "default", "", nil)
	waitForRuntimeSubagent(t, sess, agentID)

	for _, want := range []string{"task_list"} {
		found := false
		for _, name := range subagentTools {
			if name == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("all-tools agent should retain %q, got tools: %v", want, subagentTools)
		}
	}
	for _, forbidden := range rootOnlySubagentTools() {
		for _, name := range subagentTools {
			if name == forbidden {
				t.Errorf("all-tools subagent should not receive root-only tool %q, got tools: %v", forbidden, subagentTools)
			}
		}
	}
}

func TestSpawnAgent_GrantTools_RejectsRootOnlyTool(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("done") },
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	_, err = sess.spawnAgent(context.Background(), "help with follow-up work", "", "", 0, "", "", nil, []string{"delegate"})
	if err == nil {
		t.Fatal("expected root-only grant rejection")
	}
	if !strings.Contains(err.Error(), "delegation_allowance") {
		t.Fatalf("grant error = %q, want allowance-truthful message referencing delegation_allowance", err)
	}
}

func TestSpawnAgent_DirectNestedCallRejected(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("done") },
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	result, err := sess.spawnAgent(context.Background(), "help with a task", "", "", 10, "", "", nil, nil)
	if err != nil {
		t.Fatalf("spawnAgent: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result.(string)), &parsed); err != nil {
		t.Fatalf("unmarshal spawn result: %v", err)
	}
	agentID := fmt.Sprint(parsed["agent_id"])
	sub := sess.getSub(agentID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("subagent %q not found", agentID)
	}

	_, err = sub.sess.spawnAgent(context.Background(), "nested", "", "", 10, "", "", nil, nil)
	if err == nil {
		t.Fatal("expected delegation rejection for child with allowance 0")
	}
	if !strings.Contains(err.Error(), "delegation not permitted: your delegation_allowance is 0") {
		t.Fatalf("error = %q, want delegation_allowance=0 rejection message", err)
	}
}

// --- subagent.md includes task_list ---

func TestBuiltinAgents_SubagentHasTaskList(t *testing.T) {
	agents, err := builtinAgents()
	if err != nil {
		t.Fatalf("builtinAgents: %v", err)
	}
	sub := agents["subagent"]
	hasTaskList := false
	for _, tool := range sub.Tools {
		if tool == "task_list" {
			hasTaskList = true
			break
		}
	}
	if !hasTaskList {
		t.Errorf("subagent should have task_list in tools, got: %v", sub.Tools)
	}
}
