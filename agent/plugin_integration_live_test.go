//go:build !short

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/mcp"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/llm"
	_ "primeradiant.com/serf/llm/providers/openai"
)

// buildLiveTestPlugin creates a fully-featured plugin in a temp directory.
// Includes: manifest, skills, agents (with/without tools), command hooks,
// prompt hook, MCP server, and a command (ignored by serf).
func buildLiveTestPlugin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)

	// Find the real path to the MCP server script.
	_, thisFile, _, _ := runtime.Caller(0)
	mcpServerPath := filepath.Join(filepath.Dir(thisFile), "testdata", "mcp_server.py")
	if _, err := os.Stat(mcpServerPath); err != nil {
		t.Fatalf("mcp_server.py not found at %s", mcpServerPath)
	}

	// .claude-plugin/plugin.json
	mkdir(t, dir, ".claude-plugin")
	writeFile(t, dir, ".claude-plugin/plugin.json", `{
		"name": "live-test",
		"version": "2.0.0",
		"description": "Live integration test plugin",
		"license": "MIT",
		"keywords": ["test", "integration"]
	}`)

	// skills/math-helper/SKILL.md
	mkdir(t, dir, "skills/math-helper")
	writeFile(t, dir, "skills/math-helper/SKILL.md", `---
name: math-helper
description: Helps with math problems
---
# Math Helper
Provide step-by-step solutions to math problems.`)

	// skills/code-review/SKILL.md
	mkdir(t, dir, "skills/code-review")
	writeFile(t, dir, "skills/code-review/SKILL.md", `---
name: code-review
description: Reviews code for quality
---
# Code Review
Check code for bugs and style issues.`)

	// agents/analyzer.md (has tools restriction + explicit model)
	mkdir(t, dir, "agents")
	writeFile(t, dir, "agents/analyzer.md", `---
name: analyzer
description: Analyzes code structure
model: inherit
color: green
tools:
  - Read
  - Grep
  - Glob
---
You are a code analyzer. Read files and report findings.`)

	// agents/writer.md (no tools restriction, no color — tests defaults)
	writeFile(t, dir, "agents/writer.md", `---
name: writer
description: Writes documentation
---
You are a documentation writer.`)

	// Hook scripts — written as files to avoid nested bash quoting issues.
	mkdir(t, dir, "hooks/scripts")

	writeFile(t, dir, "hooks/scripts/session-start.sh", `#!/bin/bash
echo '{"hookSpecificOutput":{"additionalContext":"Live test plugin loaded."}}'
`)
	writeFile(t, dir, "hooks/scripts/pre-tool-use.sh", `#!/bin/bash
input=$(cat)
if echo "$input" | grep -q dangerous; then
  echo "Blocked: dangerous path" >&2
  exit 2
fi
exit 0
`)
	writeFile(t, dir, "hooks/scripts/stop.sh", `#!/bin/bash
echo '{"decision":"approve"}'
`)

	// Make scripts executable
	for _, script := range []string{"session-start.sh", "pre-tool-use.sh", "stop.sh"} {
		if err := os.Chmod(filepath.Join(dir, "hooks", "scripts", script), 0755); err != nil {
			t.Fatal(err)
		}
	}

	// hooks/hooks.json references scripts via $CLAUDE_PLUGIN_ROOT
	writeFile(t, dir, "hooks/hooks.json", `{
		"hooks": {
			"SessionStart": [{
				"matcher": "startup|resume",
				"hooks": [{
					"type": "command",
					"command": "${CLAUDE_PLUGIN_ROOT}/hooks/scripts/session-start.sh"
				}]
			}],
			"PreToolUse": [{
				"matcher": "Write|Edit",
				"hooks": [{
					"type": "command",
					"command": "${CLAUDE_PLUGIN_ROOT}/hooks/scripts/pre-tool-use.sh"
				}]
			}],
			"Stop": [{
				"matcher": "*",
				"hooks": [{
					"type": "command",
					"command": "${CLAUDE_PLUGIN_ROOT}/hooks/scripts/stop.sh"
				}]
			}]
		}
	}`)

	// .mcp.json — stdio MCP server
	mcpJSON := fmt.Sprintf(`{
		"mcpServers": {
			"test-echo": {
				"command": "python3",
				"args": [%q]
			}
		}
	}`, mcpServerPath)
	writeFile(t, dir, ".mcp.json", mcpJSON)

	// commands/greet.md (ignored by serf, but should not cause errors)
	mkdir(t, dir, "commands")
	writeFile(t, dir, "commands/greet.md", `---
description: Greets the user
---
Say hello to the user.`)

	return dir
}

func mkdir(t *testing.T, base string, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(base, path), 0755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, base, path, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(base, path), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// ---------- Test: Full plugin loading (all components) ----------

func TestLive_PluginLoad_AllComponents(t *testing.T) {
	dir := buildLiveTestPlugin(t)

	lp, err := plugin.Load(dir)
	if err != nil {
		t.Fatalf("plugin.Load: %v", err)
	}

	// Manifest
	if lp.Manifest.Name != "live-test" {
		t.Errorf("Name = %q", lp.Manifest.Name)
	}
	if lp.Manifest.Version != "2.0.0" {
		t.Errorf("Version = %q", lp.Manifest.Version)
	}

	// Skills (2, namespaced)
	if len(lp.Skills) != 2 {
		t.Errorf("Skills count = %d, want 2; got: %v", len(lp.Skills), skillNames(lp.Skills))
	}
	if _, ok := lp.Skills["live-test:math-helper"]; !ok {
		t.Error("missing skill live-test:math-helper")
	}
	if _, ok := lp.Skills["live-test:code-review"]; !ok {
		t.Error("missing skill live-test:code-review")
	}

	// Agents (2, namespaced)
	if len(lp.Agents) != 2 {
		t.Errorf("Agents count = %d, want 2", len(lp.Agents))
	}
	analyzer := lp.Agents["live-test:analyzer"]
	if analyzer.Model != "inherit" {
		t.Errorf("analyzer Model = %q", analyzer.Model)
	}
	if analyzer.Color != "green" {
		t.Errorf("analyzer Color = %q", analyzer.Color)
	}
	// Tools mapped to serf names
	wantTools := map[string]bool{"read_file": true, "grep": true, "glob": true}
	gotTools := map[string]bool{}
	for _, tool := range analyzer.Tools {
		gotTools[tool] = true
	}
	if len(gotTools) != len(wantTools) {
		t.Errorf("analyzer tools = %v, want %v", analyzer.Tools, wantTools)
	}
	for tool := range wantTools {
		if !gotTools[tool] {
			t.Errorf("missing tool %q in analyzer", tool)
		}
	}

	// Writer agent — defaults
	writer := lp.Agents["live-test:writer"]
	if writer.Model != "inherit" {
		t.Errorf("writer Model = %q, want inherit (default)", writer.Model)
	}
	if writer.Color != "blue" {
		t.Errorf("writer Color = %q, want blue (default)", writer.Color)
	}
	if len(writer.Tools) != 0 {
		t.Errorf("writer Tools = %v, want empty", writer.Tools)
	}

	// MCP configs (1, namespaced)
	if len(lp.MCPConfigs) != 1 {
		t.Fatalf("MCP configs = %d, want 1", len(lp.MCPConfigs))
	}
	if lp.MCPConfigs[0].Name != "plugin_live-test_test-echo" {
		t.Errorf("MCP name = %q", lp.MCPConfigs[0].Name)
	}
	if lp.MCPConfigs[0].Command != "python3" {
		t.Errorf("MCP command = %q", lp.MCPConfigs[0].Command)
	}

	// Hooks (3 events)
	if len(lp.Hooks) != 3 {
		t.Errorf("Hook events = %d, want 3 (SessionStart, PreToolUse, Stop)", len(lp.Hooks))
	}
	if _, ok := lp.Hooks[plugin.HookSessionStart]; !ok {
		t.Error("missing SessionStart hooks")
	}
	if _, ok := lp.Hooks[plugin.HookPreToolUse]; !ok {
		t.Error("missing PreToolUse hooks")
	}
	if _, ok := lp.Hooks[plugin.HookStop]; !ok {
		t.Error("missing Stop hooks")
	}
}

// ---------- Test: MCP server through real stdio transport ----------

func TestLive_MCP_StdioServer(t *testing.T) {
	dir := buildLiveTestPlugin(t)

	lp, err := plugin.Load(dir)
	if err != nil {
		t.Fatalf("plugin.Load: %v", err)
	}

	if len(lp.MCPConfigs) == 0 {
		t.Fatal("no MCP configs")
	}

	// Connect to the MCP server via stdio transport.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mgr, err := mcp.NewManager(ctx, lp.MCPConfigs, nil)
	if err != nil {
		t.Fatalf("mcp.NewManager: %v", err)
	}
	defer mgr.Close()

	// Tool discovery
	defs := mgr.ToolDefinitions()
	if len(defs) == 0 {
		t.Fatal("no tools discovered from MCP server")
	}

	// Should have plugin_live-test_test-echo__echo
	var echoDef *llm.ToolDefinition
	for i, d := range defs {
		if strings.Contains(d.Name, "echo") {
			echoDef = &defs[i]
			break
		}
	}
	if echoDef == nil {
		names := make([]string, len(defs))
		for i, d := range defs {
			names[i] = d.Name
		}
		t.Fatalf("echo tool not found in MCP tools: %v", names)
	}
	if echoDef.Description != "Echoes back the input message" {
		t.Errorf("echo description = %q", echoDef.Description)
	}

	// Register tools and invoke
	reg := tool.NewRegistry()
	if err := mgr.RegisterTools(reg); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}

	tool := reg.Get(echoDef.Name)
	if tool == nil {
		t.Fatalf("tool %q not in registry", echoDef.Name)
	}

	result := reg.ExecuteCall(ctx, nil, llm.ToolCallData{
		ID:        "call_1",
		Name:      echoDef.Name,
		Arguments: json.RawMessage(`{"message":"hello from serf"}`),
	})
	if result.IsError {
		t.Fatalf("tool call error: %s", result.Output)
	}
	if !strings.Contains(result.Output, "echo: hello from serf") {
		t.Errorf("unexpected output: %s", result.Output)
	}
}

// ---------- Test: Command hooks with real execution ----------

func TestLive_Hooks_CommandExecution(t *testing.T) {
	dir := buildLiveTestPlugin(t)

	lp, err := plugin.Load(dir)
	if err != nil {
		t.Fatalf("plugin.Load: %v", err)
	}

	runner := newHookRunnerFromPlugin(lp)

	// Track events
	var evs []events.SessionEvent
	var mu sync.Mutex
	runner.SetEventCallback(func(kind events.EventKind, data events.EventData) {
		mu.Lock()
		evs = append(evs, events.SessionEvent{Kind: kind, Data: data})
		mu.Unlock()
	})

	// --- SessionStart ---
	startResult := runner.RunSessionStart(context.Background(), hookInput{
		SessionID:     "live-test",
		CWD:           dir,
		HookEventName: "SessionStart",
	})
	if len(startResult.SystemMessages) == 0 {
		t.Error("SessionStart should produce system messages")
	}
	foundContext := false
	for _, msg := range startResult.SystemMessages {
		if strings.Contains(msg, "Live test plugin loaded") {
			foundContext = true
			break
		}
	}
	if !foundContext {
		t.Errorf("SessionStart context injection missing, got: %v", startResult.SystemMessages)
	}

	// --- PreToolUse (safe path) ---
	safeResult := runner.RunPreToolUse(context.Background(), hookInput{
		SessionID:     "live-test",
		CWD:           dir,
		HookEventName: "PreToolUse",
		ToolName:      "Write",
		ToolInput:     map[string]any{"file_path": "/tmp/safe.txt", "content": "hello"},
	})
	if safeResult.Denied {
		t.Errorf("safe path should not be denied: %s", safeResult.DenyMessage)
	}

	// --- PreToolUse (dangerous path — should be blocked via exit 2) ---
	dangerousResult := runner.RunPreToolUse(context.Background(), hookInput{
		SessionID:     "live-test",
		CWD:           dir,
		HookEventName: "PreToolUse",
		ToolName:      "Write",
		ToolInput:     map[string]any{"file_path": "/tmp/dangerous-file.txt", "content": "bad"},
	})
	foundBlocked := false
	for _, msg := range dangerousResult.SystemMessages {
		if strings.Contains(msg, "dangerous") {
			foundBlocked = true
			break
		}
	}
	if !foundBlocked {
		t.Errorf("dangerous path should produce blocking message, got: %v", dangerousResult.SystemMessages)
	}

	// --- Stop hook (should approve) ---
	stopResult := runner.RunStop(context.Background(), hookInput{
		SessionID:     "live-test",
		CWD:           dir,
		HookEventName: "Stop",
	})
	if stopResult.Blocked {
		t.Errorf("stop hook should approve, got blocked: %s", stopResult.BlockReason)
	}

	// Verify lifecycle events
	mu.Lock()
	defer mu.Unlock()
	hookStarts := 0
	hookEnds := 0
	for _, ev := range evs {
		switch ev.Kind {
		case events.EventHookStart:
			hookStarts++
		case events.EventHookEnd:
			hookEnds++
		}
	}
	// 4 hook runs: SessionStart(1) + PreToolUse(safe)(1) + PreToolUse(dangerous)(1) + Stop(1)
	if hookStarts != 4 {
		t.Errorf("HookStart events = %d, want 4", hookStarts)
	}
	if hookEnds != 4 {
		t.Errorf("HookEnd events = %d, want 4", hookEnds)
	}
}

// ---------- Test: Prompt hook with real LLM ----------

func TestLive_Hooks_PromptWithRealLLM(t *testing.T) {
	skipWithoutAPIKey(t)

	// Create a plugin with a prompt hook.
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)

	mkdir(t, dir, ".claude-plugin")
	writeFile(t, dir, ".claude-plugin/plugin.json", `{"name": "prompt-test"}`)

	mkdir(t, dir, "hooks")
	writeFile(t, dir, "hooks/hooks.json", `{
		"hooks": {
			"PreToolUse": [{
				"matcher": "Write|Edit",
				"hooks": [{
					"type": "prompt",
					"prompt": "The user is about to edit a file. The tool input is: $TOOL_INPUT. Reply with ONLY the word 'approve' if this looks safe, or 'deny' if it looks dangerous. Single word only."
				}]
			}]
		}
	}`)

	lp, err := plugin.Load(dir)
	if err != nil {
		t.Fatalf("plugin.Load: %v", err)
	}

	// Create a real LLM client for the prompt hook.
	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	runner := newHookRunner(clientAdapter{client}, integrationTestModel)
	for event, hooks := range lp.Hooks {
		runner.Add(event, hooks...)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result := runner.RunPreToolUse(ctx, hookInput{
		SessionID:     "prompt-test",
		CWD:           dir,
		HookEventName: "PreToolUse",
		ToolName:      "Write",
		ToolInput:     map[string]any{"file_path": "/tmp/test.txt", "content": "hello world"},
	})

	// The LLM should respond with something (approve or deny).
	if len(result.SystemMessages) == 0 {
		t.Error("prompt hook should produce a system message from the LLM")
	}
	t.Logf("Prompt hook LLM response: %v", result.SystemMessages)
}

// ---------- Test: Full session with plugin (MCP + hooks + agents) ----------

func TestLive_Session_WithPlugin(t *testing.T) {
	skipWithoutAPIKey(t)

	pluginDir := buildLiveTestPlugin(t)
	workDir := t.TempDir()
	workDir, _ = filepath.EvalSymlinks(workDir)

	// Create settings file
	mkdir(t, workDir, ".claude")
	writeFile(t, workDir, ".claude/live-test.local.md", "---\nstrict: true\n---\nTest config.")

	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	sess, err := NewSession(client, NewOpenAIProfile(integrationTestModel), execenv.NewLocalExecutionEnvironment(workDir), SessionConfig{
		PluginDirs:            []string{pluginDir},
		MaxToolRoundsPerInput: 10,
		MaxTurns:              3,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// Drain events
	var evs []events.SessionEvent
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		for ev := range sess.Events() {
			mu.Lock()
			evs = append(evs, ev)
			mu.Unlock()
		}
		close(done)
	}()

	// Verify plugin loaded: skills should be in session
	if _, ok := sess.skills["live-test:math-helper"]; !ok {
		t.Error("skill live-test:math-helper not in session")
	}
	if _, ok := sess.skills["live-test:code-review"]; !ok {
		t.Error("skill live-test:code-review not in session")
	}

	// Verify plugin agents available
	if _, ok := sess.pluginAgents["live-test:analyzer"]; !ok {
		t.Error("agent live-test:analyzer not in session")
	}
	if _, ok := sess.pluginAgents["live-test:writer"]; !ok {
		t.Error("agent live-test:writer not in session")
	}

	// Verify MCP tools registered
	mcpToolFound := false
	for _, td := range sess.mcpTools {
		if strings.Contains(td.Name, "echo") {
			mcpToolFound = true
			break
		}
	}
	if !mcpToolFound {
		names := make([]string, len(sess.mcpTools))
		for i, td := range sess.mcpTools {
			names[i] = td.Name
		}
		t.Errorf("MCP echo tool not found in session, tools: %v", names)
	}

	// Verify hook runner is set up
	if sess.hookRunner == nil {
		t.Fatal("hookRunner should not be nil")
	}

	// Verify settings loadable from this workDir
	settings, err := plugin.LoadSettings(workDir, "live-test")
	if err != nil {
		t.Fatalf("plugin.LoadSettings: %v", err)
	}
	if settings == nil || settings.Frontmatter["strict"] != true {
		t.Error("settings not loaded correctly")
	}

	// Process a simple input through the session.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result, err := sess.ProcessInput(ctx, "What is 2+2? Reply with just the number.", nil)
	sess.Close()
	<-done

	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result from ProcessInput")
	}
	t.Logf("ProcessInput result: %s", result)

	// Check events
	mu.Lock()
	defer mu.Unlock()

	foundPluginLoaded := false
	foundHookStart := false
	for _, ev := range evs {
		switch ev.Kind {
		case events.EventPluginLoaded:
			foundPluginLoaded = true
		case events.EventHookStart:
			foundHookStart = true
		}
	}
	if !foundPluginLoaded {
		t.Error("missing PLUGIN_LOADED event")
	}
	if !foundHookStart {
		t.Error("missing HOOK_START event (SessionStart hooks should have fired)")
	}
}

// ---------- Test: Session with MCP tool call through real LLM ----------

func TestLive_Session_MCPToolCall(t *testing.T) {
	skipWithoutAPIKey(t)

	pluginDir := buildLiveTestPlugin(t)
	workDir := t.TempDir()

	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	sess, err := NewSession(client, NewOpenAIProfile(integrationTestModel), execenv.NewLocalExecutionEnvironment(workDir), SessionConfig{
		PluginDirs:            []string{pluginDir},
		MaxToolRoundsPerInput: 20,
		MaxTurns:              5,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// Drain events
	done := make(chan struct{})
	go func() {
		for range sess.Events() {
		}
		close(done)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Ask the LLM to use the MCP echo tool.
	// Find the actual tool name first.
	var echoToolName string
	for _, td := range sess.mcpTools {
		if strings.Contains(td.Name, "echo") {
			echoToolName = td.Name
			break
		}
	}
	if echoToolName == "" {
		sess.Close()
		t.Fatal("echo MCP tool not found")
	}

	result, err := sess.ProcessInput(ctx,
		fmt.Sprintf("Use the %s tool to echo the message 'integration test passed'. "+
			"Then report what the tool returned.", echoToolName), nil)
	sess.Close()
	<-done

	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !strings.Contains(strings.ToLower(result), "integration test passed") {
		t.Errorf("expected result to contain 'integration test passed', got: %s", result)
	}
}

// ---------- Test: Plugin agent prompt in system message ----------

func TestLive_Session_PluginAgentsInSystemPrompt(t *testing.T) {
	skipWithoutAPIKey(t)

	pluginDir := buildLiveTestPlugin(t)
	workDir := t.TempDir()

	// Use fakeAdapter to inspect the system prompt.
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				// Check system prompt for plugin agents section. Return PASS/FAIL
				// via a synthetic communicate tool_call so the session intercept
				// fires and ProcessInput returns the message (the agent loop
				// rejects bare text responses).
				sysPrompt := ""
				for _, msg := range req.Messages {
					if msg.Role == llm.RoleSystem {
						sysPrompt = msg.Text()
						break
					}
				}
				if !strings.Contains(sysPrompt, "<available_agents>") {
					return finalResponse("FAIL: no <available_agents> in system prompt")
				}
				if !strings.Contains(sysPrompt, "live-test:analyzer") {
					return finalResponse("FAIL: missing live-test:analyzer")
				}
				if !strings.Contains(sysPrompt, "live-test:writer") {
					return finalResponse("FAIL: missing live-test:writer")
				}
				return finalResponse("PASS: plugin agents in system prompt")
			},
		},
	}

	c := llm.NewClient()
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), SessionConfig{
		PluginDirs: []string{pluginDir},
		MaxTurns:   2,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// Drain events
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := sess.ProcessInput(ctx, "hello", nil)
	sess.Close()

	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !strings.Contains(result, "PASS") {
		t.Errorf("system prompt check failed: %s", result)
	}
}

// ---------- Test: Tool restriction for plugin agents ----------

func TestLive_ToolRestriction_PluginAgent(t *testing.T) {
	dir := buildLiveTestPlugin(t)

	lp, err := plugin.Load(dir)
	if err != nil {
		t.Fatalf("plugin.Load: %v", err)
	}

	// Build a full tool registry
	reg := tool.NewRegistry()
	// Register some dummy tools (including communicate, which subagents always need)
	for _, name := range []string{"read_file", "grep", "glob", "shell", "write_file", "edit_file", "communicate"} {
		n := name
		if err := reg.Register(tool.RegisteredTool{
			Tool: llm.Tool{Definition: llm.ToolDefinition{
				Name:        n,
				Description: "test tool " + n,
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			}},
			Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
				return n + " called", nil
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Verify full registry has all tools
	allNames := reg.RegisteredNames()
	if len(allNames) != 7 {
		t.Fatalf("expected 7 tools, got %d: %v", len(allNames), allNames)
	}

	// Apply the analyzer agent's tool restriction
	analyzer := lp.Agents["live-test:analyzer"]
	allowed := make(map[string]bool, len(analyzer.Tools))
	for _, tool := range analyzer.Tools {
		allowed[tool] = true
	}
	reg.Restrict(allowed)

	// After restriction: should have read_file, grep, glob + communicate (auto-kept)
	restricted := reg.RegisteredNames()
	if !restricted["read_file"] {
		t.Error("read_file should survive restriction")
	}
	if !restricted["grep"] {
		t.Error("grep should survive restriction")
	}
	if !restricted["glob"] {
		t.Error("glob should survive restriction")
	}
	if !restricted["communicate"] {
		t.Error("communicate should always be kept")
	}
	if restricted["shell"] {
		t.Error("shell should be removed by restriction")
	}
	if restricted["write_file"] {
		t.Error("write_file should be removed by restriction")
	}
	if restricted["edit_file"] {
		t.Error("edit_file should be removed by restriction")
	}
}

// ---------- Test: Real superpowers plugin through Session ----------

func TestLive_Session_RealSuperpowersPlugin(t *testing.T) {
	superpowersDir := filepath.Join(pluginCacheDir, "superpowers", "4.3.0")
	if _, err := os.Stat(superpowersDir); err != nil {
		t.Skip("superpowers plugin not installed")
	}

	// Use fakeAdapter to inspect what the session does with the plugin.
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				sysPrompt := ""
				for _, msg := range req.Messages {
					if msg.Role == llm.RoleSystem {
						sysPrompt = msg.Text()
						break
					}
				}
				if !strings.Contains(sysPrompt, "superpowers:code-reviewer") {
					return llm.Response{Message: llm.Assistant("FAIL: superpowers agent not in prompt")}
				}
				return llm.Response{Message: llm.Assistant("PASS: superpowers loaded")}
			},
		},
	}

	c := llm.NewClient()
	c.Register(f)

	workDir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), SessionConfig{
		PluginDirs: []string{superpowersDir},
		MaxTurns:   2,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// Drain events
	go func() {
		for range sess.Events() {
		}
	}()

	// Verify skills were loaded
	tddKey := "superpowers:test-driven-development"
	if _, ok := sess.skills[tddKey]; !ok {
		sess.Close()
		t.Fatalf("TDD skill not loaded into session")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := sess.ProcessInput(ctx, "hi", nil)
	sess.Close()

	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !strings.Contains(result, "PASS") {
		t.Errorf("superpowers session check failed: %s", result)
	}
}
