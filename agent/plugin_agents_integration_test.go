package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/skill"
	"primeradiant.com/serf/llm"
)

func TestToolRegistry_Restrict(t *testing.T) {
	t.Parallel()
	reg := tool.NewRegistry()
	for _, name := range []string{"read_file", "write_file", "grep", "shell", "communicate"} {
		_ = reg.Register(tool.RegisteredTool{
			Tool: llm.Tool{Definition: llm.ToolDefinition{Name: name, Description: name}},
			Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
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

func TestToolRegistry_Restrict_KeepsCommunicate(t *testing.T) {
	t.Parallel()
	reg := tool.NewRegistry()
	_ = reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "communicate", Description: "communicate"}},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			return "ok", nil
		},
	})
	_ = reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "shell", Description: "shell"}},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			return "ok", nil
		},
	})

	// Restrict to only "shell" -- communicate should still be kept
	reg.Restrict(map[string]bool{"shell": true})

	if reg.Get("communicate") == nil {
		t.Error("communicate should always be kept after Restrict")
	}
	if reg.Get("shell") == nil {
		t.Error("shell should be in allowed set")
	}
}

func TestSpawnAgent_UnknownPluginAgentType(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// No pluginAgents registered, so any agent_type should fail
	_, err = sess.spawnAgent(context.Background(), "do something", "", "", 10, "nonexistent:agent", "", nil, nil)
	if err == nil {
		t.Fatal("expected error for unknown agent_type")
	}
	if !strings.Contains(err.Error(), "unknown plugin agent type") {
		t.Errorf("error = %q, want 'unknown plugin agent type'", err.Error())
	}
}

func TestSpawnAgent_PluginAgentType_SystemPrompt(t *testing.T) {
	t.Parallel()
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// Register a plugin agent
	sess.pluginAgents = map[string]plugin.Agent{
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

	result, err := sess.spawnAgent(ctx, "review this code", "", "", 10, "my-plugin:reviewer", "", nil, nil)
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
	waitForRuntimeSubagent(t, sess, agentID)

	// The subagent's system prompt should contain the plugin agent's system prompt
	if !strings.Contains(subagentSystemPrompt, "code review specialist") {
		t.Errorf("subagent system prompt should contain plugin agent prompt, got:\n%s", subagentSystemPrompt)
	}
}

func TestSpawnAgent_PluginAgentType_ModelOverride(t *testing.T) {
	t.Parallel()
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	sess.pluginAgents = map[string]plugin.Agent{
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

	result, err := sess.spawnAgent(ctx, "do it fast", "", "", 10, "my-plugin:fast-agent", "", nil, nil)
	if err != nil {
		t.Fatalf("spawnAgent: %v", err)
	}

	var parsed map[string]any
	json.Unmarshal([]byte(result.(string)), &parsed)
	agentID := parsed["agent_id"].(string)
	waitForRuntimeSubagent(t, sess, agentID)

	// The subagent's LLM request should use the plugin agent's model
	if subagentModel != "gpt-4.1-nano" {
		t.Errorf("subagent model = %q, want %q", subagentModel, "gpt-4.1-nano")
	}
}

func TestSpawnAgent_PluginAgentType_InheritModel(t *testing.T) {
	t.Parallel()
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	sess.pluginAgents = map[string]plugin.Agent{
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

	result, err := sess.spawnAgent(ctx, "help me", "", "", 10, "my-plugin:helper", "", nil, nil)
	if err != nil {
		t.Fatalf("spawnAgent: %v", err)
	}

	var parsed map[string]any
	json.Unmarshal([]byte(result.(string)), &parsed)
	agentID := parsed["agent_id"].(string)
	waitForRuntimeSubagent(t, sess, agentID)

	// With "inherit", the subagent should use the parent's model
	if subagentModel != "gpt-5.2" {
		t.Errorf("subagent model = %q, want parent model %q", subagentModel, "gpt-5.2")
	}
}

func TestSpawnAgent_PluginAgentType_RestrictsTools(t *testing.T) {
	t.Parallel()
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	sess.pluginAgents = map[string]plugin.Agent{
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

	result, err := sess.spawnAgent(ctx, "read the code", "", "", 10, "my-plugin:reader", "", nil, nil)
	if err != nil {
		t.Fatalf("spawnAgent: %v", err)
	}

	var parsed map[string]any
	json.Unmarshal([]byte(result.(string)), &parsed)
	agentID := parsed["agent_id"].(string)
	waitForRuntimeSubagent(t, sess, agentID)

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
			t.Errorf("unexpected tool %q in restricted subagent (allowed: read_file, grep, communicate)", name)
		}
	}
	// Ensure the allowed tools are present
	for name := range allowed {
		if sub.sess.reg.Get(name) == nil {
			t.Errorf("expected tool %q to be present in restricted subagent", name)
		}
	}
}

func TestSpawnAgent_PluginAgentType_AllowanceGated(t *testing.T) {
	t.Parallel()
	coordinatorAgent := plugin.Agent{
		Name:         "coordinator",
		Description:  "Delegates to agents",
		Model:        "inherit",
		Tools:        []string{"read_file", "delegate"},
		SystemPrompt: "You coordinate by delegating.",
		PluginName:   "my-plugin",
	}

	// Case 1: allowance 0 (leaf session) — spawn must be rejected.
	t.Run("rejected at allowance 0", func(t *testing.T) {
		dir := t.TempDir()
		c := llm.NewClient()
		c.Register(&fakeAdapter{name: "openai"})

		sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
			MaxSubagentDepth: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer sess.Close()

		// Force allowance to 0 to simulate a leaf that cannot delegate.
		sess.mu.Lock()
		sess.delegationAllowance = 0
		sess.mu.Unlock()

		sess.pluginAgents = map[string]plugin.Agent{"my-plugin:coordinator": coordinatorAgent}

		_, err = sess.spawnAgent(context.Background(), "try to coordinate", "", "", 10, "my-plugin:coordinator", "", nil, nil)
		if err == nil {
			t.Fatal("expected delegation rejection at allowance 0")
		}
		if !strings.Contains(err.Error(), "delegation not permitted: your delegation_allowance is 0") {
			t.Fatalf("error = %q, want delegation_allowance=0 rejection message", err)
		}
	})

	// Case 2: allowance > 0 (root session, MaxSubagentDepth 2) — spawn must succeed.
	t.Run("spawnable at allowance > 0", func(t *testing.T) {
		dir := t.TempDir()
		c := llm.NewClient()
		c.Register(&fakeAdapter{
			name: "openai",
			steps: []func(req llm.Request) llm.Response{
				func(req llm.Request) llm.Response {
					return llm.Response{Message: llm.Assistant("done")}
				},
			},
		})

		sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
			MaxSubagentDepth: 2,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer sess.Close()

		sess.pluginAgents = map[string]plugin.Agent{"my-plugin:coordinator": coordinatorAgent}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		result, err := sess.spawnAgent(ctx, "try to coordinate", "", "", 10, "my-plugin:coordinator", "", nil, nil)
		if err != nil {
			t.Fatalf("expected spawn to succeed at allowance > 0, got error: %v", err)
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(result.(string)), &parsed); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		agentID := parsed["agent_id"].(string)
		waitForRuntimeSubagent(t, sess, agentID)
	})
}

func TestSpawnAgent_PluginAgentType_GrantTools_AddsProviderVisibleTool(t *testing.T) {
	t.Parallel()
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	sess.pluginAgents = map[string]plugin.Agent{
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

	result, err := sess.spawnAgent(ctx, "inspect and optionally shell out", "", "", 10, "my-plugin:reader", "", nil, []string{"exec_command"})
	if err != nil {
		t.Fatalf("spawnAgent: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result.(string)), &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	agentID := parsed["agent_id"].(string)
	waitForRuntimeSubagent(t, sess, agentID)

	sub := sess.getSub(agentID)
	if sub == nil {
		t.Fatal("subagent not found")
	}
	if sub.sess.reg.Get("shell") == nil {
		t.Fatalf("granted provider-visible exec_command should map to shell; tools=%v", sub.sess.reg.Names())
	}
	hasExecCommand := false
	for _, name := range subagentToolNames {
		if name == "exec_command" {
			hasExecCommand = true
			break
		}
	}
	if !hasExecCommand {
		t.Fatalf("subagent request should include provider-visible exec_command, got %v", subagentToolNames)
	}
}

func TestSpawnAgent_GrantTools_RejectsUnavailableParentTool(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 2,
		spawn:            spawnConfig{allowedToolNames: []string{"read_file"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	sess.pluginAgents = map[string]plugin.Agent{
		"my-plugin:reader": {
			Name:         "reader",
			Description:  "Read-only agent",
			Model:        "inherit",
			Tools:        []string{"read_file", "grep"},
			SystemPrompt: "You only read files.",
			PluginName:   "my-plugin",
		},
	}

	_, err = sess.spawnAgent(context.Background(), "try to escalate", "", "", 10, "my-plugin:reader", "", nil, []string{"write_file"})
	if err == nil {
		t.Fatal("expected grant rejection")
	}
	if !strings.Contains(err.Error(), "not currently callable") {
		t.Fatalf("error = %q, want not-currently-callable message", err)
	}
}

func TestSpawnAgent_PluginAgentType_InjectsSkillContent(t *testing.T) {
	t.Parallel()
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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
	sess.skills["test-skill"] = skill.SkillMeta{
		Name:        "test-skill",
		Description: "A test skill",
		Dir:         skillDir,
		SkillFile:   filepath.Join(skillDir, "SKILL.md"),
	}

	// Register a plugin agent that references this skill
	sess.pluginAgents = map[string]plugin.Agent{
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

	result, err := sess.spawnAgent(ctx, "write tests", "", "", 10, "my-plugin:test-eng", "", nil, nil)
	if err != nil {
		t.Fatalf("spawnAgent: %v", err)
	}

	var parsed map[string]any
	json.Unmarshal([]byte(result.(string)), &parsed)
	agentID := parsed["agent_id"].(string)
	waitForRuntimeSubagent(t, sess, agentID)

	// The subagent's system prompt should contain the injected skill body
	if !strings.Contains(subagentSystemPrompt, "test skill body content for injection") {
		t.Errorf("subagent system prompt should contain skill body, got:\n%s", subagentSystemPrompt)
	}
	// Also should still contain the agent's own system prompt
	if !strings.Contains(subagentSystemPrompt, "test engineer") {
		t.Errorf("subagent system prompt should contain agent system prompt, got:\n%s", subagentSystemPrompt)
	}
}
