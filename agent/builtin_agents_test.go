package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

// --- builtinAgents() ---

func TestBuiltinAgents_LoadsAllRoles(t *testing.T) {
	agents, err := builtinAgents()
	if err != nil {
		t.Fatalf("builtinAgents: %v", err)
	}
	want := []string{"explorer", "planner", "test-engineer", "implementer", "reviewer"}
	for _, name := range want {
		agent, ok := agents[name]
		if !ok {
			t.Errorf("missing built-in agent %q", name)
			continue
		}
		if agent.SystemPrompt == "" {
			t.Errorf("agent %q has empty system prompt", name)
		}
		if agent.Description == "" {
			t.Errorf("agent %q has empty description", name)
		}
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
	// The frontmatter uses serf names which pass through MapClaudeToolName unchanged.
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
			mapped := MapClaudeToolName(tool)
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), NewLocalExecutionEnvironment(dir), SessionConfig{})
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

func TestSession_PluginAgentOverridesBuiltin(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Simulate a plugin agent overriding the built-in explorer.
	sess.pluginAgents["explorer"] = PluginAgent{
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

// --- FormatPluginAgentsPrompt tag ---

func TestFormatPluginAgentsPrompt_UsesAvailableAgentsTag(t *testing.T) {
	agents := map[string]PluginAgent{
		"explorer": {Name: "explorer", Description: "Explores code"},
	}
	result := FormatPluginAgentsPrompt(agents)
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

// --- spawn_agent blocking parameter ---

func TestDefSpawnAgent_HasBlockingParameter(t *testing.T) {
	def := defSpawnAgent()
	props, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties map")
	}
	blocking, ok := props["blocking"]
	if !ok {
		t.Fatal("expected 'blocking' property in spawn_agent definition")
	}
	m, ok := blocking.(map[string]any)
	if !ok {
		t.Fatal("blocking should be a map")
	}
	if m["type"] != "boolean" {
		t.Errorf("blocking type = %v, want 'boolean'", m["type"])
	}
}

func TestSpawnAgent_BlockingMode(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("subagent done")}
			},
		},
	}
	c.Register(adapter)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// Blocking spawn should return the result directly (not an agent_id).
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res := sess.reg.ExecuteCall(ctx, sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"test task","blocking":true}`),
	})
	if res.IsError {
		t.Fatalf("blocking spawn returned error: %s", res.Output)
	}

	// Should be a SubAgentResult JSON (with output, success, turns_used),
	// NOT a spawn result JSON (with agent_id, status).
	var result SubAgentResult
	if err := json.Unmarshal([]byte(res.Output), &result); err != nil {
		t.Fatalf("parsing blocking result: %v (output: %s)", err, res.Output)
	}
	if !result.Success {
		t.Error("expected success=true")
	}
	if result.TurnsUsed < 1 {
		t.Error("expected turns_used >= 1")
	}
	// Blocking result should include agent_id so the caller can use resume_agent later.
	if !strings.Contains(res.Output, "agent_id") {
		t.Errorf("blocking result should contain agent_id for resume_agent, got: %s", res.Output)
	}
}

func TestSpawnAgent_NonBlockingMode(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("done")}
			},
		},
	}
	c.Register(adapter)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Default (no blocking param) should return agent_id.
	res := sess.reg.ExecuteCall(ctx, sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"test task"}`),
	})
	if res.IsError {
		t.Fatalf("non-blocking spawn error: %s", res.Output)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(res.Output), &parsed); err != nil {
		t.Fatalf("parsing result: %v", err)
	}
	if parsed["agent_id"] == nil {
		t.Error("non-blocking spawn should return agent_id")
	}
	if parsed["status"] != "running" {
		t.Errorf("status = %v, want 'running'", parsed["status"])
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
				return llm.Response{Message: llm.Assistant("exploration complete")}
			},
		},
	}
	c.Register(adapter)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res := sess.reg.ExecuteCall(ctx, sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"survey the project","agent_type":"explorer","blocking":true}`),
	})
	if res.IsError {
		t.Fatalf("blocking explorer spawn error: %s", res.Output)
	}

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
				return llm.Response{Message: llm.Assistant("done")}
			},
		},
	}
	c.Register(adapter)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res := sess.reg.ExecuteCall(ctx, sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"survey the project","agent_type":"explorer","blocking":true}`),
	})
	if res.IsError {
		t.Fatalf("blocking explorer spawn error: %s", res.Output)
	}

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
				return llm.Response{Message: llm.Assistant("done")}
			},
		},
	}
	c.Register(adapter)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// No agent_type = default subagent.
	res := sess.reg.ExecuteCall(ctx, sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"do something","blocking":true}`),
	})
	if res.IsError {
		t.Fatalf("blocking spawn error: %s", res.Output)
	}

	// Default subagent should get core + subagent persona.
	if !strings.Contains(subagentSystemPrompt, "communicate") {
		t.Error("default subagent prompt should contain communicate guidance from core")
	}
	if !strings.Contains(subagentSystemPrompt, "focused subagent") {
		t.Error("default subagent prompt should contain subagent persona instructions")
	}
}

// --- minWaitTimeoutMS ---

func TestMinWaitTimeoutMS_IsAtLeast120Seconds(t *testing.T) {
	if minWaitTimeoutMS < 120_000 {
		t.Errorf("minWaitTimeoutMS = %d, want >= 120000", minWaitTimeoutMS)
	}
}

func TestWaitToolDescription_SuggestsFiveMinutes(t *testing.T) {
	def := defWait()
	if !strings.Contains(def.Description, "300000") {
		t.Errorf("wait description should suggest 300000ms, got: %s", def.Description)
	}
}

// --- spawn_agent reasoning_effort parameter ---

func TestSpawnAgent_ReasoningEffortParameter(t *testing.T) {
	def := defSpawnAgent()
	props, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties map")
	}
	re, ok := props["reasoning_effort"]
	if !ok {
		t.Fatal("expected 'reasoning_effort' property in spawn_agent definition")
	}
	m, ok := re.(map[string]any)
	if !ok {
		t.Fatal("reasoning_effort should be a map")
	}
	if m["type"] != "string" {
		t.Errorf("reasoning_effort type = %v, want 'string'", m["type"])
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
				return llm.Response{Message: llm.Assistant("done")}
			},
		},
	}
	c.Register(adapter)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res := sess.reg.ExecuteCall(ctx, sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"hard problem","reasoning_effort":"xhigh","blocking":true}`),
	})
	if res.IsError {
		t.Fatalf("spawn error: %s", res.Output)
	}
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
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

func TestBuiltinAgents_ReviewerIsReadOnly(t *testing.T) {
	agents, err := builtinAgents()
	if err != nil {
		t.Fatalf("builtinAgents: %v", err)
	}
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
				return llm.Response{Message: llm.Assistant("done")}
			},
		},
	}
	c.Register(adapter)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Spawn an explorer (named agent with explicit tool list that doesn't include task_list).
	res := sess.reg.ExecuteCall(ctx, sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"explore the code","agent_type":"explorer","blocking":true}`),
	})
	if res.IsError {
		t.Fatalf("spawn error: %s", res.Output)
	}

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

// --- spawn_agent blocking description ---

func TestSpawnAgent_BlockingDescription_NoFireAndForget(t *testing.T) {
	def := defSpawnAgent()
	props := def.Parameters["properties"].(map[string]any)
	blocking := props["blocking"].(map[string]any)
	desc := fmt.Sprint(blocking["description"])
	if strings.Contains(strings.ToLower(desc), "fire-and-forget") {
		t.Error("blocking description should not say 'fire-and-forget'")
	}
}
