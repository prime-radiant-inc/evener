package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func TestToolRegistry_Restrict(t *testing.T) {
	t.Parallel()
	reg := tool.NewRegistry()
	for _, name := range []string{"read_file", "write_file", "grep", "shell", "communicate"} {
		_ = reg.Register(tool.RegisteredTool{
			Definition: llm.ToolDefinition{Name: name, Description: name},
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
		Definition: llm.ToolDefinition{Name: "communicate", Description: "communicate"},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			return "ok", nil
		},
	})
	_ = reg.Register(tool.RegisteredTool{
		Definition: llm.ToolDefinition{Name: "shell", Description: "shell"},
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
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

func TestSpawnAgent_PluginAgentType_Model(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		agentKey     string
		agentName    string
		description  string
		model        string
		systemPrompt string
		prompt       string
		wantModel    string
		wantPrefix   string
	}{
		{
			name:         "override",
			agentKey:     "my-plugin:fast-agent",
			agentName:    "fast-agent",
			description:  "Fast agent",
			model:        "gpt-4.1-nano",
			systemPrompt: "You are fast.",
			prompt:       "do it fast",
			wantModel:    "gpt-4.1-nano",
			wantPrefix:   "",
		},
		{
			name:         "inherit",
			agentKey:     "my-plugin:helper",
			agentName:    "helper",
			description:  "Helper agent",
			model:        "inherit",
			systemPrompt: "You help.",
			prompt:       "help me",
			wantModel:    "gpt-5.2",
			wantPrefix:   "parent model ",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			c := llm.NewClient()

			var subagentModel string
			// Built in place at each registration rather than copied from one
			// value: fakeAdapter carries a sync.Mutex, and only the closure
			// (which captures subagentModel) has to be shared between them.
			steps := []func(req llm.Request) llm.Response{
				func(req llm.Request) llm.Response {
					subagentModel = req.Model
					return llm.Response{Message: llm.Assistant("done")}
				},
			}
			if tc.name == "override" {
				lister := newFakeEnumerableAdapter("openai", "gpt-5.2", "gpt-4.1-nano")
				lister.steps = steps
				// gpt-4.1-nano is a catalog row of the openai provider, so the
				// registry can name an instance that serves it.
				c = registryClient(t, map[string]registry.Provider{
					"openai": {Base: "openai", APIKey: "k"},
				}, lister)
			} else {
				c.Register(&fakeAdapter{name: "openai", steps: steps})
			}

			sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
				MaxSubagentDepth: 2,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer sess.Close()

			sess.pluginAgents = map[string]plugin.Agent{
				tc.agentKey: {
					Name:         tc.agentName,
					Description:  tc.description,
					Model:        tc.model,
					SystemPrompt: tc.systemPrompt,
					PluginName:   "my-plugin",
				},
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
			defer cancel()

			result, err := sess.spawnAgent(ctx, tc.prompt, "", "", 10, tc.agentKey, "", nil, nil)
			if err != nil {
				t.Fatalf("spawnAgent: %v", err)
			}

			var parsed map[string]any
			json.Unmarshal([]byte(result.(string)), &parsed)
			agentID := parsed["agent_id"].(string)
			waitForRuntimeSubagent(t, sess, agentID)

			// The subagent's LLM request should use the resolved model.
			if subagentModel != tc.wantModel {
				t.Errorf("subagent model = %q, want %s%q", subagentModel, tc.wantPrefix, tc.wantModel)
			}
		})
	}
}

// TestSpawnAgent_PluginModelAvailability drives the plugin-agent model rule
// end to end on an instance that serves some ids and not others: a served id is
// taken as-is, and an id nothing serves warns and falls back to the explicit
// override.
func TestSpawnAgent_PluginModelAvailability(t *testing.T) {
	t.Parallel()
	const agentType = "my-plugin:reviewer"

	spawn := func(t *testing.T, pluginModel string) (*Session, *fakeEnumerableAdapter) {
		t.Helper()
		dir := t.TempDir()
		adapter := newFakeEnumerableAdapter("kimi-anthropic-api", "k3", "k3-turbo")
		c := registryClient(t, map[string]registry.Provider{
			"kimi-anthropic-api": {Base: "moonshotai", APIKey: "k", Models: modelRows("k3", "k3-turbo")},
		}, adapter)

		sess, err := NewSession(
			c,
			WithProviderID(newKimiAnthropicProfile("k3"), "kimi-anthropic-api"),
			execenv.NewLocalExecutionEnvironment(dir),
			SessionConfig{MaxSubagentDepth: 2},
		)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(sess.Close)

		sess.pluginAgents = map[string]plugin.Agent{
			agentType: {
				Name:         "reviewer",
				Model:        pluginModel,
				SystemPrompt: "Review the code.",
				PluginName:   "my-plugin",
			},
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
		defer cancel()
		result, err := sess.spawnAgent(ctx, "review this", "k3", "", 10, agentType, "", nil, nil)
		if err != nil {
			t.Fatalf("spawnAgent: %v", err)
		}
		var spawned struct {
			AgentID string `json:"agent_id"`
		}
		if err := json.Unmarshal([]byte(result.(string)), &spawned); err != nil {
			t.Fatalf("parsing result: %v", err)
		}
		waitForRuntimeSubagent(t, sess, spawned.AgentID)
		return sess, adapter
	}

	childModels := func(t *testing.T, adapter *fakeEnumerableAdapter) []string {
		t.Helper()
		requests := adapter.Requests()
		if len(requests) == 0 {
			t.Fatal("child made no provider request")
		}
		models := make([]string, 0, len(requests))
		for _, req := range requests {
			if req.Provider != "kimi-anthropic-api" {
				t.Errorf("child request provider = %q, want kimi-anthropic-api", req.Provider)
			}
			models = append(models, req.Model)
		}
		return models
	}

	drainWarnings := func(sess *Session) []events.WarningData {
		var warnings []events.WarningData
		for {
			select {
			case ev := <-sess.Events():
				if warning, ok := ev.Data.(events.WarningData); ok {
					warnings = append(warnings, warning)
				}
			default:
				return warnings
			}
		}
	}

	// Positive control: an id the session's own instance serves is taken
	// without any fallback, so a resolver that answered "unavailable" for
	// everything would fail here.
	t.Run("served plugin model is taken", func(t *testing.T) {
		t.Parallel()
		sess, adapter := spawn(t, "k3-turbo")
		for _, model := range childModels(t, adapter) {
			if model != "k3-turbo" {
				t.Errorf("child request model = %q, want the served plugin model k3-turbo", model)
			}
		}
		if warnings := drainWarnings(sess); len(warnings) != 0 {
			t.Fatalf("buffered warnings = %+v, want none for a served plugin model", warnings)
		}
	})

	t.Run("unserved plugin model falls back to the explicit override", func(t *testing.T) {
		t.Parallel()
		sess, adapter := spawn(t, "sonnet")
		for _, model := range childModels(t, adapter) {
			if model != "k3" {
				t.Errorf("child request model = %q, want the explicit override k3", model)
			}
		}
		warnings := drainWarnings(sess)
		if len(warnings) != 1 {
			t.Fatalf("buffered warnings = %d, want 1: %+v", len(warnings), warnings)
		}
		for _, text := range []string{"my-plugin", agentType, "sonnet", "unavailable", "kimi-anthropic-api"} {
			if !strings.Contains(warnings[0].Message, text) {
				t.Errorf("warning %q does not contain %q", warnings[0].Message, text)
			}
		}
	})
}

func TestSpawnAgent_AvailablePluginModelWins(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	adapter := newFakeEnumerableAdapter("anthropic", "claude-opus-4-6", "claude-sonnet-4-6")
	c := registryClient(t, map[string]registry.Provider{
		"anthropic": {Base: "anthropic", APIKey: "k", Models: map[string]registry.Model{
			"claude-opus-4-6":   {},
			"claude-sonnet-4-6": {},
		}},
	}, adapter)

	sess, err := NewSession(
		c,
		newAnthropicProfile("claude-opus-4-6"),
		execenv.NewLocalExecutionEnvironment(dir),
		SessionConfig{MaxSubagentDepth: 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	const agentType = "my-plugin:reviewer"
	sess.pluginAgents = map[string]plugin.Agent{
		agentType: {
			Name:         "reviewer",
			Model:        "claude-sonnet-4-6",
			SystemPrompt: "Review the code.",
			PluginName:   "my-plugin",
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	result, err := sess.spawnAgent(ctx, "review this", "claude-opus-4-6", "", 10, agentType, "", nil, nil)
	if err != nil {
		t.Fatalf("spawnAgent: %v", err)
	}

	var spawned struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal([]byte(result.(string)), &spawned); err != nil {
		t.Fatalf("parsing result: %v", err)
	}
	waitForRuntimeSubagent(t, sess, spawned.AgentID)

	requests := adapter.Requests()
	if len(requests) == 0 {
		t.Fatal("child made no provider request")
	}
	for _, req := range requests {
		if req.Provider != "anthropic" {
			t.Errorf("child request provider = %q, want anthropic", req.Provider)
		}
		if req.Model != "claude-sonnet-4-6" {
			t.Errorf("child request model = %q, want claude-sonnet-4-6", req.Model)
		}
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
		testOnly:         testConfig{sandboxProber: bwrapCapableProber(dir)},
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()

	result, err := sess.spawnAgent(ctx, "read the code", "", "", 10, "my-plugin:reader", "", nil, nil)
	if err != nil {
		t.Fatalf("spawnAgent: %v", err)
	}

	var parsed map[string]any
	json.Unmarshal([]byte(result.(string)), &parsed)
	agentID := parsed["agent_id"].(string)
	waitForRuntimeSubagent(t, sess, agentID)

	// The subagent should only have read_file, grep, the recovery reader required
	// by those generically limited text tools, and the tools every subagent always
	// keeps: task_list, compact_context, and communicate.
	allowed := map[string]bool{"read_file": true, "grep": true, "read_transcript": true, "task_list": true, "compact_context": true, "communicate": true}

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
			t.Errorf("unexpected tool %q in restricted subagent (allowed: read_file, grep, read_transcript, task_list, compact_context, communicate)", name)
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
			testOnly:         testConfig{sandboxProber: bwrapCapableProber(dir)},
		})
		if err != nil {
			t.Fatal(err)
		}
		defer sess.Close()

		sess.pluginAgents = map[string]plugin.Agent{"my-plugin:coordinator": coordinatorAgent}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
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
		testOnly:         testConfig{sandboxProber: bwrapCapableProber(dir)},
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
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
	hasExecCommand := slices.Contains(subagentToolNames, "exec_command")
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
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
