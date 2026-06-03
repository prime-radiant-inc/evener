package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/toolname"
)

// setupFullTestPlugin creates a temp directory with a complete plugin containing
// all component types: skills, agents, hooks, MCP configs. The hooks.json uses
// the plugin directory itself as the target for the "touch" marker file.
func setupFullTestPlugin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	// .claude-plugin/plugin.json
	metaDir := filepath.Join(dir, ".claude-plugin")
	if err := os.MkdirAll(metaDir, 0755); err != nil {
		t.Fatalf("mkdir .claude-plugin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metaDir, "plugin.json"),
		[]byte(`{"name": "e2e-plugin", "version": "1.0.0"}`), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	// skills/test-skill/SKILL.md
	skillDir := filepath.Join(dir, "skills", "test-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("mkdir skills: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: test-skill\ndescription: A test skill\n---\nTest skill body"), 0644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	// agents/helper.md
	agentsDir := filepath.Join(dir, "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatalf("mkdir agents: %v", err)
	}
	agentMD := "---\nname: helper\ndescription: Helps with tasks\nmodel: inherit\ncolor: blue\ntools:\n  - Read\n  - Grep\n---\nYou are a helpful assistant."
	if err := os.WriteFile(filepath.Join(agentsDir, "helper.md"), []byte(agentMD), 0644); err != nil {
		t.Fatalf("write agent: %v", err)
	}

	// hooks/hooks.json — the command creates a marker file in the plugin dir
	hooksDir := filepath.Join(dir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	hooksJSON := `{"hooks": {"SessionStart": [{"matcher": "*", "hooks": [{"type": "command", "command": "touch ` + dir + `/started"}]}]}}`
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), []byte(hooksJSON), 0644); err != nil {
		t.Fatalf("write hooks: %v", err)
	}

	// .mcp.json
	mcpJSON := `{"mcpServers": {"test-srv": {"command": "echo", "args": ["hello"]}}}`
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(mcpJSON), 0644); err != nil {
		t.Fatalf("write mcp: %v", err)
	}

	return dir
}

func TestPlugin_EndToEnd(t *testing.T) {
	pluginDir := setupFullTestPlugin(t)
	workDir := t.TempDir()
	workDir, _ = filepath.EvalSymlinks(workDir)

	// Create settings file in workDir
	if err := os.MkdirAll(filepath.Join(workDir, ".claude"), 0755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, ".claude", "e2e-plugin.local.md"),
		[]byte("---\nstrict: true\n---\nProject-specific config."), 0644); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	// Load the plugin
	lp, err := LoadPlugin(pluginDir)
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}

	// 1. Plugin metadata
	if lp.Manifest.Name != "e2e-plugin" {
		t.Errorf("Name = %q, want %q", lp.Manifest.Name, "e2e-plugin")
	}
	if lp.Manifest.Version != "1.0.0" {
		t.Errorf("Version = %q, want %q", lp.Manifest.Version, "1.0.0")
	}
	if lp.Dir != pluginDir {
		t.Errorf("Dir = %q, want %q", lp.Dir, pluginDir)
	}

	// 2. Skills namespaced correctly
	if _, ok := lp.Skills["e2e-plugin:test-skill"]; !ok {
		t.Errorf("skill not found, got: %v", keys(lp.Skills))
	}
	if meta := lp.Skills["e2e-plugin:test-skill"]; meta.Description != "A test skill" {
		t.Errorf("skill description = %q, want %q", meta.Description, "A test skill")
	}

	// 3. Agents discovered and namespaced
	if _, ok := lp.Agents["e2e-plugin:helper"]; !ok {
		t.Errorf("agent not found, got keys: %v", keys(lp.Agents))
	}
	agent := lp.Agents["e2e-plugin:helper"]
	if agent.Description != "Helps with tasks" {
		t.Errorf("agent desc = %q, want %q", agent.Description, "Helps with tasks")
	}
	if agent.Model != "inherit" {
		t.Errorf("agent model = %q, want %q", agent.Model, "inherit")
	}
	// Tools should be mapped to serf canonical names
	if len(agent.Tools) != 2 {
		t.Fatalf("agent tools count = %d, want 2", len(agent.Tools))
	}
	toolSet := map[string]bool{}
	for _, tool := range agent.Tools {
		toolSet[tool] = true
	}
	if !toolSet["read_file"] {
		t.Errorf("Read should map to read_file, got tools: %v", agent.Tools)
	}
	if !toolSet["grep"] {
		t.Errorf("Grep should map to grep, got tools: %v", agent.Tools)
	}

	// 4. MCP configs discovered and namespaced
	if len(lp.MCPConfigs) != 1 {
		t.Fatalf("MCP configs = %d, want 1", len(lp.MCPConfigs))
	}
	if lp.MCPConfigs[0].Name != "plugin_e2e-plugin_test-srv" {
		t.Errorf("MCP name = %q, want %q", lp.MCPConfigs[0].Name, "plugin_e2e-plugin_test-srv")
	}
	if lp.MCPConfigs[0].Command != "echo" {
		t.Errorf("MCP command = %q, want %q", lp.MCPConfigs[0].Command, "echo")
	}

	// 5. Hooks discovered
	if sessionStartHooks, ok := lp.Hooks[HookSessionStart]; !ok || len(sessionStartHooks) == 0 {
		t.Error("SessionStart hook not found")
	}

	// 6. Settings loadable
	settings, err := LoadPluginSettings(workDir, "e2e-plugin")
	if err != nil {
		t.Fatalf("LoadPluginSettings: %v", err)
	}
	if settings == nil {
		t.Fatal("settings should not be nil")
	}
	if settings.Frontmatter["strict"] != true {
		t.Errorf("strict = %v, want true", settings.Frontmatter["strict"])
	}
	if !strings.Contains(settings.Body, "Project-specific") {
		t.Error("settings body should contain 'Project-specific'")
	}

	// 7. Tool name mapping bidirectional
	if toolname.ClaudeToSerf("Read") != "read_file" {
		t.Errorf("ClaudeToSerf(Read) = %q, want read_file", toolname.ClaudeToSerf("Read"))
	}
	if toolname.SerfToClaude("read_file") != "Read" {
		t.Errorf("SerfToClaude(read_file) = %q, want Read", toolname.SerfToClaude("read_file"))
	}

	// 8. Plugin agent prompt formatting
	agentPrompt := renderAvailableAgentsSectionForTest(t, lp.Agents)
	if !strings.Contains(agentPrompt, "e2e-plugin:helper") {
		t.Error("prompt should contain agent name 'e2e-plugin:helper'")
	}
	if !strings.Contains(agentPrompt, "Helps with tasks") {
		t.Error("prompt should contain agent description 'Helps with tasks'")
	}

	// 9. Multiple plugins with unique names work
	dir2 := makePluginDir(t, "second-plugin")
	plugins, err := LoadPlugins([]string{pluginDir, dir2})
	if err != nil {
		t.Fatalf("LoadPlugins: %v", err)
	}
	if len(plugins) != 2 {
		t.Errorf("got %d plugins, want 2", len(plugins))
	}
}

func TestPlugin_EndToEnd_HookExecution(t *testing.T) {
	pluginDir := setupFullTestPlugin(t)

	lp, err := LoadPlugin(pluginDir)
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}

	runner := newHookRunner(nil, "") // no prompt client needed for command hooks
	for event, eventHooks := range lp.Hooks {
		runner.Add(event, eventHooks...)
	}

	// Track events
	var evs []events.EventKind
	runner.SetEventCallback(func(kind events.EventKind, data events.EventData) {
		evs = append(evs, kind)
	})

	// Fire SessionStart
	input := hookInput{
		SessionID:     "test-session",
		CWD:           pluginDir,
		HookEventName: "SessionStart",
	}
	result := runner.RunSessionStart(context.Background(), input)

	// RunSessionStart returns hookRunResult; any system messages are informational.
	// The absence of error-level messages means the hook succeeded.
	_ = result

	// Verify the marker file was created by the hook command
	if _, err := os.Stat(filepath.Join(pluginDir, "started")); err != nil {
		t.Errorf("SessionStart hook did not create marker file: %v", err)
	}

	// Verify lifecycle events were emitted (HookStart + HookEnd per hook)
	if len(evs) != 2 {
		t.Fatalf("expected 2 events (HookStart + HookEnd), got %d: %v", len(evs), evs)
	}
	if evs[0] != events.EventHookStart {
		t.Errorf("events[0] = %q, want %q", evs[0], events.EventHookStart)
	}
	if evs[1] != events.EventHookEnd {
		t.Errorf("events[1] = %q, want %q", evs[1], events.EventHookEnd)
	}
}
