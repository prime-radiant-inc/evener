package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

func TestToolRegistry_Restrict(t *testing.T) {
	reg := NewToolRegistry()
	for _, name := range []string{"read_file", "write_file", "grep", "shell", "communicate"} {
		_ = reg.Register(RegisteredTool{
			Tool: llm.Tool{Definition: llm.ToolDefinition{Name: name, Description: name}},
			Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
				return "ok", nil
			},
		})
	}

	reg.Restrict(map[string]bool{"read_file": true, "grep": true})

	names := reg.Names()
	want := map[string]bool{"read_file": true, "grep": true, "communicate": true}
	if len(names) != len(want) {
		t.Fatalf("expected %d tools, got %d: %v", len(want), len(names), names)
	}
	for _, n := range names {
		if !want[n] {
			t.Errorf("unexpected tool %q after Restrict", n)
		}
	}
}

func TestToolRegistry_Restrict_KeepsSubmitResult(t *testing.T) {
	reg := NewToolRegistry()
	_ = reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "communicate", Description: "communicate"}},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			return "ok", nil
		},
	})
	_ = reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "shell", Description: "shell"}},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			return "ok", nil
		},
	})

	// Restrict to only "shell" -- submit_result should still be kept
	reg.Restrict(map[string]bool{"shell": true})

	if reg.Get("communicate") == nil {
		t.Error("submit_result should always be kept after Restrict")
	}
	if reg.Get("shell") == nil {
		t.Error("shell should be in allowed set")
	}
}

func TestSpawnAgent_UnknownPluginAgentType(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// No pluginAgents registered, so any agent_type should fail
	_, err = sess.spawnAgent(context.Background(), "do something", "", "", 10, "nonexistent:agent", "", nil)
	if err == nil {
		t.Fatal("expected error for unknown agent_type")
	}
	if !strings.Contains(err.Error(), "unknown plugin agent type") {
		t.Errorf("error = %q, want 'unknown plugin agent type'", err.Error())
	}
}

func TestSpawnAgent_PluginAgentType_SystemPrompt(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	// Track what system prompt the subagent's LLM request uses
	var subagentSystemPrompt string
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				// This is the subagent's first LLM call
				for _, m := range req.Messages {
					if m.Role == llm.RoleSystem {
						subagentSystemPrompt = m.Text()
					}
				}
				return llm.Response{
					Message: llm.Assistant("done"),
				}
			},
		},
	}
	c.Register(adapter)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// Register a plugin agent
	sess.pluginAgents = map[string]PluginAgent{
		"my-plugin:reviewer": {
			Name:         "reviewer",
			Description:  "Code reviewer",
			Model:        "inherit",
			SystemPrompt: "You are a code review specialist. Focus on correctness.",
			PluginName:   "my-plugin",
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := sess.spawnAgent(ctx, "review this code", "", "", 10, "my-plugin:reviewer", "", nil)
	if err != nil {
		t.Fatalf("spawnAgent: %v", err)
	}

	// Should return JSON with agent_id
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result.(string)), &parsed); err != nil {
		t.Fatalf("parsing result: %v", err)
	}
	if parsed["agent_id"] == nil {
		t.Error("expected agent_id in result")
	}

	// Wait for the subagent to complete
	agentID := parsed["agent_id"].(string)
	_, _ = sess.waitAgent(ctx, agentID, 5000)

	// The subagent's system prompt should contain the plugin agent's system prompt
	if !strings.Contains(subagentSystemPrompt, "code review specialist") {
		t.Errorf("subagent system prompt should contain plugin agent prompt, got:\n%s", subagentSystemPrompt)
	}
}

func TestSpawnAgent_PluginAgentType_ModelOverride(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	var subagentModel string
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				subagentModel = req.Model
				return llm.Response{Message: llm.Assistant("done")}
			},
		},
	}
	c.Register(adapter)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	sess.pluginAgents = map[string]PluginAgent{
		"my-plugin:fast-agent": {
			Name:         "fast-agent",
			Description:  "Fast agent",
			Model:        "gpt-4.1-nano",
			SystemPrompt: "You are fast.",
			PluginName:   "my-plugin",
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := sess.spawnAgent(ctx, "do it fast", "", "", 10, "my-plugin:fast-agent", "", nil)
	if err != nil {
		t.Fatalf("spawnAgent: %v", err)
	}

	var parsed map[string]any
	json.Unmarshal([]byte(result.(string)), &parsed)
	agentID := parsed["agent_id"].(string)
	_, _ = sess.waitAgent(ctx, agentID, 5000)

	// The subagent's LLM request should use the plugin agent's model
	if subagentModel != "gpt-4.1-nano" {
		t.Errorf("subagent model = %q, want %q", subagentModel, "gpt-4.1-nano")
	}
}

func TestSpawnAgent_PluginAgentType_InheritModel(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	var subagentModel string
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				subagentModel = req.Model
				return llm.Response{Message: llm.Assistant("done")}
			},
		},
	}
	c.Register(adapter)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	sess.pluginAgents = map[string]PluginAgent{
		"my-plugin:helper": {
			Name:         "helper",
			Description:  "Helper agent",
			Model:        "inherit",
			SystemPrompt: "You help.",
			PluginName:   "my-plugin",
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := sess.spawnAgent(ctx, "help me", "", "", 10, "my-plugin:helper", "", nil)
	if err != nil {
		t.Fatalf("spawnAgent: %v", err)
	}

	var parsed map[string]any
	json.Unmarshal([]byte(result.(string)), &parsed)
	agentID := parsed["agent_id"].(string)
	_, _ = sess.waitAgent(ctx, agentID, 5000)

	// With "inherit", the subagent should use the parent's model
	if subagentModel != "gpt-5.2" {
		t.Errorf("subagent model = %q, want parent model %q", subagentModel, "gpt-5.2")
	}
}

func TestSpawnAgent_PluginAgentType_RestrictsTools(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	var subagentToolNames []string
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				for _, td := range req.Tools {
					subagentToolNames = append(subagentToolNames, td.Name)
				}
				return llm.Response{Message: llm.Assistant("done")}
			},
		},
	}
	c.Register(adapter)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	sess.pluginAgents = map[string]PluginAgent{
		"my-plugin:reader": {
			Name:         "reader",
			Description:  "Read-only agent",
			Model:        "inherit",
			Tools:        []string{"read_file", "grep"},
			SystemPrompt: "You only read files.",
			PluginName:   "my-plugin",
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := sess.spawnAgent(ctx, "read the code", "", "", 10, "my-plugin:reader", "", nil)
	if err != nil {
		t.Fatalf("spawnAgent: %v", err)
	}

	var parsed map[string]any
	json.Unmarshal([]byte(result.(string)), &parsed)
	agentID := parsed["agent_id"].(string)
	_, _ = sess.waitAgent(ctx, agentID, 5000)

	// The subagent should only have read_file, grep, task_list (always kept), and communicate (always kept)
	allowed := map[string]bool{"read_file": true, "grep": true, "task_list": true, "communicate": true}

	// Check tool names against the allowed set.
	// Note: OpenAI profile maps tool names (shell->exec_command, etc.), so we
	// need to check using the names that actually appear in the request tools.
	// The profile ToolDefinitions() applies the name map. But the registry uses
	// canonical names. The allToolDefinitions() method on session returns
	// profile.ToolDefinitions() which maps names. So the LLM request will have
	// provider-mapped names. We check the registry canonical names instead.
	sub := sess.getSub(agentID)
	if sub == nil {
		t.Fatal("subagent not found")
	}
	regNames := sub.sess.reg.Names()
	for _, name := range regNames {
		if !allowed[name] {
			t.Errorf("unexpected tool %q in restricted subagent (allowed: read_file, grep, submit_result)", name)
		}
	}
	// Ensure the allowed tools are present
	for name := range allowed {
		if sub.sess.reg.Get(name) == nil {
			t.Errorf("expected tool %q to be present in restricted subagent", name)
		}
	}
}

func TestSpawnAgent_PluginAgentType_InjectsSkillContent(t *testing.T) {
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
		MaxSubagentDepth: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// Create a temp skill directory with a SKILL.md
	skillDir := filepath.Join(dir, "skills", "test-skill")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: test-skill\ndescription: A test skill\n---\nThis is the test skill body content for injection.\n"), 0644)

	// Register the skill in the session's skills map
	sess.skills["test-skill"] = SkillMeta{
		Name:        "test-skill",
		Description: "A test skill",
		Dir:         skillDir,
		SkillFile:   filepath.Join(skillDir, "SKILL.md"),
	}

	// Register a plugin agent that references this skill
	sess.pluginAgents = map[string]PluginAgent{
		"my-plugin:test-eng": {
			Name:         "test-eng",
			Description:  "Test engineer",
			Model:        "inherit",
			Skills:       []string{"test-skill"},
			SystemPrompt: "You are a test engineer.",
			PluginName:   "my-plugin",
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := sess.spawnAgent(ctx, "write tests", "", "", 10, "my-plugin:test-eng", "", nil)
	if err != nil {
		t.Fatalf("spawnAgent: %v", err)
	}

	var parsed map[string]any
	json.Unmarshal([]byte(result.(string)), &parsed)
	agentID := parsed["agent_id"].(string)
	_, _ = sess.waitAgent(ctx, agentID, 5000)

	// The subagent's system prompt should contain the injected skill body
	if !strings.Contains(subagentSystemPrompt, "test skill body content for injection") {
		t.Errorf("subagent system prompt should contain skill body, got:\n%s", subagentSystemPrompt)
	}
	// Also should still contain the agent's own system prompt
	if !strings.Contains(subagentSystemPrompt, "test engineer") {
		t.Errorf("subagent system prompt should contain agent system prompt, got:\n%s", subagentSystemPrompt)
	}
}

func TestDefSpawnAgent_HasAgentTypeParameter(t *testing.T) {
	def := defSpawnAgent()
	props, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties map")
	}
	agentType, ok := props["agent_type"]
	if !ok {
		t.Fatal("expected agent_type property in spawn_agent definition")
	}
	m, ok := agentType.(map[string]any)
	if !ok {
		t.Fatal("agent_type should be a map")
	}
	if m["type"] != "string" {
		t.Errorf("agent_type type = %v, want 'string'", m["type"])
	}
}
