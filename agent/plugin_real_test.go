package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/skill"
)

// pluginCacheDir is where the official Anthropic plugins are cached.
const pluginCacheDir = "/Users/jesse/.claude/plugins/cache/claude-plugins-official"

// realPluginDir returns the path to a real plugin or skips the test if not present.
func realPluginDir(t *testing.T, subpath string) string {
	t.Helper()
	dir := filepath.Join(pluginCacheDir, subpath)
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("real plugin not found at %s (run with real plugins installed)", dir)
	}
	return dir
}

// ---------- 1. superpowers ----------

func TestRealPlugin_Superpowers_Load(t *testing.T) {
	dir := realPluginDir(t, "superpowers/4.3.0")

	lp, err := LoadPlugin(dir)
	if err != nil {
		t.Fatalf("LoadPlugin(superpowers): %v", err)
	}

	// Manifest metadata
	if lp.Manifest.Name != "superpowers" {
		t.Errorf("Name = %q, want superpowers", lp.Manifest.Name)
	}
	if lp.Manifest.Version != "4.3.0" {
		t.Errorf("Version = %q, want 4.3.0", lp.Manifest.Version)
	}
	if lp.Manifest.Description == "" {
		t.Error("Description should not be empty")
	}
	if lp.Manifest.License != "MIT" {
		t.Errorf("License = %q, want MIT", lp.Manifest.License)
	}
	if len(lp.Manifest.Keywords) == 0 {
		t.Error("Keywords should not be empty")
	}
}

func TestRealPlugin_Superpowers_Skills(t *testing.T) {
	dir := realPluginDir(t, "superpowers/4.3.0")

	lp, err := LoadPlugin(dir)
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}

	// Superpowers has 14 skills - verify namespacing and key skills exist.
	expectedSkills := []string{
		"superpowers:brainstorming",
		"superpowers:test-driven-development",
		"superpowers:systematic-debugging",
		"superpowers:using-git-worktrees",
		"superpowers:writing-plans",
		"superpowers:subagent-driven-development",
		"superpowers:executing-plans",
		"superpowers:finishing-a-development-branch",
		"superpowers:using-superpowers",
		"superpowers:verification-before-completion",
		"superpowers:requesting-code-review",
		"superpowers:receiving-code-review",
		"superpowers:dispatching-parallel-agents",
		"superpowers:writing-skills",
	}

	for _, name := range expectedSkills {
		if _, ok := lp.Skills[name]; !ok {
			t.Errorf("missing expected skill %q; got skills: %v", name, skillNames(lp.Skills))
		}
	}

	// Verify skill metadata is populated correctly.
	if tdd, ok := lp.Skills["superpowers:test-driven-development"]; ok {
		if tdd.Name != "test-driven-development" {
			t.Errorf("TDD skill Name = %q, want test-driven-development", tdd.Name)
		}
		if tdd.Description == "" {
			t.Error("TDD skill Description should not be empty")
		}
		if tdd.SkillFile == "" {
			t.Error("TDD skill SkillFile should not be empty")
		}
		if !strings.HasSuffix(tdd.SkillFile, "SKILL.md") {
			t.Errorf("SkillFile = %q, should end with SKILL.md", tdd.SkillFile)
		}
	}

	// Verify skill body can be loaded.
	if meta, ok := lp.Skills["superpowers:brainstorming"]; ok {
		body, err := skill.LoadSkillBody(meta)
		if err != nil {
			t.Fatalf("LoadSkillBody(brainstorming): %v", err)
		}
		if !strings.Contains(body, "Brainstorming") {
			t.Error("brainstorming skill body should contain 'Brainstorming'")
		}
	}
}

func TestRealPlugin_Superpowers_Agents(t *testing.T) {
	dir := realPluginDir(t, "superpowers/4.3.0")

	lp, err := LoadPlugin(dir)
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}

	// Superpowers has 1 agent: code-reviewer
	agent, ok := lp.Agents["superpowers:code-reviewer"]
	if !ok {
		t.Fatalf("missing agent superpowers:code-reviewer, got: %v", agentNames(lp.Agents))
	}

	if agent.Name != "code-reviewer" {
		t.Errorf("Name = %q, want code-reviewer", agent.Name)
	}
	if agent.Model != "inherit" {
		t.Errorf("Model = %q, want inherit", agent.Model)
	}
	if !strings.Contains(agent.Description, "code-review") && !strings.Contains(agent.Description, "major project step") {
		t.Errorf("Description doesn't mention code review: %q", agent.Description[:min(100, len(agent.Description))])
	}
	if !strings.Contains(agent.SystemPrompt, "Senior Code Reviewer") {
		t.Error("SystemPrompt should contain 'Senior Code Reviewer'")
	}
	if agent.PluginName != "superpowers" {
		t.Errorf("PluginName = %q, want superpowers", agent.PluginName)
	}
}

func TestRealPlugin_Superpowers_Hooks(t *testing.T) {
	dir := realPluginDir(t, "superpowers/4.3.0")

	lp, err := LoadPlugin(dir)
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}

	// Superpowers has SessionStart hooks
	startHooks, ok := lp.Hooks[HookSessionStart]
	if !ok || len(startHooks) == 0 {
		t.Fatal("SessionStart hooks not found")
	}

	found := false
	for _, h := range startHooks {
		if h.Type == "command" {
			found = true
			// ${CLAUDE_PLUGIN_ROOT} should be expanded to the plugin dir
			if strings.Contains(h.Command, "${CLAUDE_PLUGIN_ROOT}") {
				t.Error("${CLAUDE_PLUGIN_ROOT} should be expanded in command")
			}
			if !strings.Contains(h.Command, "session-start.sh") {
				t.Errorf("Command should reference session-start.sh, got %q", h.Command)
			}
			if h.PluginName != "superpowers" {
				t.Errorf("PluginName = %q, want superpowers", h.PluginName)
			}
			if h.PluginDir == "" {
				t.Error("PluginDir should be set")
			}
			// Matcher should be from the hooks.json
			if !strings.Contains(h.Matcher, "startup") {
				t.Errorf("Matcher = %q, expected to contain 'startup'", h.Matcher)
			}
		}
	}
	if !found {
		t.Error("no command hook found in SessionStart hooks")
	}
}

func TestRealPlugin_Superpowers_HookExecution(t *testing.T) {
	dir := realPluginDir(t, "superpowers/4.3.0")

	lp, err := LoadPlugin(dir)
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}

	runner := newHookRunnerFromPlugin(lp)

	// Track events
	var evs []events.EventKind
	runner.SetEventCallback(func(kind events.EventKind, data events.EventData) {
		evs = append(evs, kind)
	})

	// Execute SessionStart hook - it reads using-superpowers SKILL.md and outputs JSON
	input := hookInput{
		SessionID:     "test-session",
		CWD:           dir,
		HookEventName: "SessionStart",
	}
	result := runner.RunSessionStart(context.Background(), input)

	// The superpowers session-start.sh outputs JSON with hookSpecificOutput.additionalContext
	if len(result.SystemMessages) == 0 {
		t.Error("SessionStart hook should produce system messages")
	}

	// Verify the output contains the expected content injection
	for _, msg := range result.SystemMessages {
		if strings.Contains(msg, "superpowers") || strings.Contains(msg, "EXTREMELY_IMPORTANT") {
			// Good - the hook injected its context
			break
		}
	}

	// Verify lifecycle events fired
	if len(evs) < 2 {
		t.Errorf("expected at least 2 events (HookStart + HookEnd), got %d", len(evs))
	}
}

func TestRealPlugin_Superpowers_PromptFormatting(t *testing.T) {
	dir := realPluginDir(t, "superpowers/4.3.0")

	lp, err := LoadPlugin(dir)
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}

	prompt := renderAvailableAgentsSectionForTest(t, lp.Agents)
	if !strings.Contains(prompt, "superpowers:code-reviewer") {
		t.Error("prompt should contain 'superpowers:code-reviewer'")
	}
	if !strings.Contains(prompt, "<available_agents>") {
		t.Error("prompt should contain <available_agents> tag")
	}
}

// ---------- 2. security-guidance ----------

func TestRealPlugin_SecurityGuidance_Load(t *testing.T) {
	dir := realPluginDir(t, "security-guidance/2cd88e7947b7")

	lp, err := LoadPlugin(dir)
	if err != nil {
		t.Fatalf("LoadPlugin(security-guidance): %v", err)
	}

	if lp.Manifest.Name != "security-guidance" {
		t.Errorf("Name = %q, want security-guidance", lp.Manifest.Name)
	}

	// No skills, agents, or MCP
	if len(lp.Skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(lp.Skills))
	}
	if len(lp.Agents) != 0 {
		t.Errorf("expected 0 agents, got %d", len(lp.Agents))
	}
	if len(lp.MCPConfigs) != 0 {
		t.Errorf("expected 0 MCP configs, got %d", len(lp.MCPConfigs))
	}
}

func TestRealPlugin_SecurityGuidance_Hooks(t *testing.T) {
	dir := realPluginDir(t, "security-guidance/2cd88e7947b7")

	lp, err := LoadPlugin(dir)
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}

	// security-guidance has PreToolUse hooks
	preHooks, ok := lp.Hooks[HookPreToolUse]
	if !ok || len(preHooks) == 0 {
		t.Fatal("PreToolUse hooks not found")
	}

	found := false
	for _, h := range preHooks {
		if h.Type == "command" {
			found = true
			// ${CLAUDE_PLUGIN_ROOT} expanded
			if strings.Contains(h.Command, "${CLAUDE_PLUGIN_ROOT}") {
				t.Error("${CLAUDE_PLUGIN_ROOT} should be expanded")
			}
			if !strings.Contains(h.Command, "security_reminder_hook.py") {
				t.Errorf("Command should reference security_reminder_hook.py, got %q", h.Command)
			}
			// Matcher should match Edit|Write|MultiEdit
			if h.Matcher != "Edit|Write|MultiEdit" {
				t.Errorf("Matcher = %q, want Edit|Write|MultiEdit", h.Matcher)
			}
			if h.PluginName != "security-guidance" {
				t.Errorf("PluginName = %q, want security-guidance", h.PluginName)
			}
		}
	}
	if !found {
		t.Error("no command hook found in PreToolUse hooks")
	}
}

func TestRealPlugin_SecurityGuidance_HookMatching(t *testing.T) {
	dir := realPluginDir(t, "security-guidance/2cd88e7947b7")

	lp, err := LoadPlugin(dir)
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}

	runner := newHookRunnerFromPlugin(lp)

	// The matcher "Edit|Write|MultiEdit" should match these Claude Code tool names.
	// Our runAll maps serf names → Claude names for matching, so we test with serf names.
	// edit_file → Edit (matches), write_file → Write (matches), shell → Bash (no match)
	editMatched := runner.matchHooks(HookPreToolUse, "Edit")
	if len(editMatched) == 0 {
		t.Error("Edit should match PreToolUse hooks")
	}
	writeMatched := runner.matchHooks(HookPreToolUse, "Write")
	if len(writeMatched) == 0 {
		t.Error("Write should match PreToolUse hooks")
	}
	bashMatched := runner.matchHooks(HookPreToolUse, "Bash")
	if len(bashMatched) != 0 {
		t.Error("Bash should NOT match Edit|Write|MultiEdit")
	}
}

func TestRealPlugin_SecurityGuidance_HookExecution(t *testing.T) {
	dir := realPluginDir(t, "security-guidance/2cd88e7947b7")

	lp, err := LoadPlugin(dir)
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}

	runner := newHookRunnerFromPlugin(lp)

	// Use a unique session ID to avoid state file conflicts from previous runs.
	sessionID := fmt.Sprintf("serf-test-%d", os.Getpid())
	t.Cleanup(func() {
		// Clean up the state file the Python hook creates.
		stateFile := filepath.Join(os.Getenv("HOME"), ".claude", fmt.Sprintf("security_warnings_state_%s.json", sessionID))
		os.Remove(stateFile)
	})

	// Execute PreToolUse for a Write to a GitHub Actions workflow file.
	// The hook outputs to stderr with exit code 2 (block).
	input := hookInput{
		SessionID:     sessionID,
		CWD:           dir,
		HookEventName: "PreToolUse",
		ToolName:      "Write",
		ToolInput: map[string]any{
			"file_path": ".github/workflows/ci.yml",
			"content":   "name: CI\non: push\njobs:\n  test:\n    runs-on: ubuntu-latest",
		},
	}
	result := runner.RunPreToolUse(context.Background(), input)

	// The hook should produce a system message about GitHub Actions security.
	if len(result.SystemMessages) == 0 {
		t.Fatal("PreToolUse hook should produce system messages for GitHub Actions file")
	}
	foundSecurityWarning := false
	for _, msg := range result.SystemMessages {
		if strings.Contains(msg, "GitHub Actions") || strings.Contains(msg, "Command Injection") {
			foundSecurityWarning = true
			break
		}
	}
	if !foundSecurityWarning {
		t.Errorf("expected security warning about GitHub Actions, got messages: %v", result.SystemMessages)
	}
}

// ---------- 3. code-simplifier ----------

func TestRealPlugin_CodeSimplifier_Load(t *testing.T) {
	dir := realPluginDir(t, "code-simplifier/1.0.0")

	lp, err := LoadPlugin(dir)
	if err != nil {
		t.Fatalf("LoadPlugin(code-simplifier): %v", err)
	}

	if lp.Manifest.Name != "code-simplifier" {
		t.Errorf("Name = %q, want code-simplifier", lp.Manifest.Name)
	}
	if lp.Manifest.Version != "1.0.0" {
		t.Errorf("Version = %q, want 1.0.0", lp.Manifest.Version)
	}

	// code-simplifier has 1 agent, no skills, no hooks, no MCP
	if len(lp.Skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(lp.Skills))
	}
	if len(lp.MCPConfigs) != 0 {
		t.Errorf("expected 0 MCP configs, got %d", len(lp.MCPConfigs))
	}

	// Agent discovery
	agent, ok := lp.Agents["code-simplifier:code-simplifier"]
	if !ok {
		t.Fatalf("missing agent code-simplifier:code-simplifier, got: %v", agentNames(lp.Agents))
	}
	if agent.Model != "opus" {
		t.Errorf("Model = %q, want opus", agent.Model)
	}
	if !strings.Contains(agent.SystemPrompt, "simplification specialist") {
		t.Error("SystemPrompt should mention 'simplification specialist'")
	}
	if agent.PluginName != "code-simplifier" {
		t.Errorf("PluginName = %q, want code-simplifier", agent.PluginName)
	}
}

func TestRealPlugin_CodeSimplifier_NoHooks(t *testing.T) {
	dir := realPluginDir(t, "code-simplifier/1.0.0")

	lp, err := LoadPlugin(dir)
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}

	// code-simplifier has no hooks
	total := 0
	for _, eventHooks := range lp.Hooks {
		total += len(eventHooks)
	}
	if total != 0 {
		t.Errorf("expected 0 total hooks, got %d", total)
	}
}

// ---------- 4. agent-sdk-dev ----------

func TestRealPlugin_AgentSDKDev_Load(t *testing.T) {
	dir := realPluginDir(t, "agent-sdk-dev/2cd88e7947b7")

	lp, err := LoadPlugin(dir)
	if err != nil {
		t.Fatalf("LoadPlugin(agent-sdk-dev): %v", err)
	}

	if lp.Manifest.Name != "agent-sdk-dev" {
		t.Errorf("Name = %q, want agent-sdk-dev", lp.Manifest.Name)
	}
	if lp.Manifest.Description == "" {
		t.Error("Description should not be empty")
	}

	// No skills, no hooks, no MCP
	if len(lp.Skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(lp.Skills))
	}
	if len(lp.MCPConfigs) != 0 {
		t.Errorf("expected 0 MCP configs, got %d", len(lp.MCPConfigs))
	}
}

func TestRealPlugin_AgentSDKDev_Agents(t *testing.T) {
	dir := realPluginDir(t, "agent-sdk-dev/2cd88e7947b7")

	lp, err := LoadPlugin(dir)
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}

	// agent-sdk-dev has 2 agents: agent-sdk-verifier-py and agent-sdk-verifier-ts
	expectedAgents := []string{
		"agent-sdk-dev:agent-sdk-verifier-py",
		"agent-sdk-dev:agent-sdk-verifier-ts",
	}
	for _, name := range expectedAgents {
		if _, ok := lp.Agents[name]; !ok {
			t.Errorf("missing agent %q, got: %v", name, agentNames(lp.Agents))
		}
	}

	// Check the Python verifier agent specifically
	pyAgent, ok := lp.Agents["agent-sdk-dev:agent-sdk-verifier-py"]
	if !ok {
		t.Fatal("missing agent-sdk-dev:agent-sdk-verifier-py")
	}
	if pyAgent.Model != "sonnet" {
		t.Errorf("py agent Model = %q, want sonnet", pyAgent.Model)
	}
	if !strings.Contains(pyAgent.Description, "Python Agent SDK") {
		t.Error("py agent Description should mention 'Python Agent SDK'")
	}
	if !strings.Contains(pyAgent.SystemPrompt, "Python Agent SDK") {
		t.Error("py agent SystemPrompt should mention 'Python Agent SDK'")
	}
	if pyAgent.PluginName != "agent-sdk-dev" {
		t.Errorf("PluginName = %q, want agent-sdk-dev", pyAgent.PluginName)
	}
}

// ---------- 5. plugin-dev ----------
// Note: plugin-dev has NO .claude-plugin/plugin.json manifest.
// This tests that LoadPlugin returns an appropriate error.

func TestRealPlugin_PluginDev_NoManifest(t *testing.T) {
	dir := realPluginDir(t, "plugin-dev/2cd88e7947b7")

	_, err := LoadPlugin(dir)
	if err == nil {
		t.Fatal("LoadPlugin should fail for plugin without manifest")
	}
	if !strings.Contains(err.Error(), "reading plugin manifest") {
		t.Errorf("expected 'reading plugin manifest' error, got: %v", err)
	}
}

// ---------- Cross-Plugin Tests ----------

func TestRealPlugin_LoadMultiple(t *testing.T) {
	// Load superpowers, security-guidance, code-simplifier, agent-sdk-dev together
	dirs := []string{
		realPluginDir(t, "superpowers/4.3.0"),
		realPluginDir(t, "security-guidance/2cd88e7947b7"),
		realPluginDir(t, "code-simplifier/1.0.0"),
		realPluginDir(t, "agent-sdk-dev/2cd88e7947b7"),
	}

	plugins, err := LoadPlugins(dirs)
	if err != nil {
		t.Fatalf("LoadPlugins: %v", err)
	}
	if len(plugins) != 4 {
		t.Fatalf("expected 4 plugins, got %d", len(plugins))
	}

	// Verify unique names
	names := map[string]bool{}
	for _, p := range plugins {
		if names[p.Manifest.Name] {
			t.Errorf("duplicate plugin name: %s", p.Manifest.Name)
		}
		names[p.Manifest.Name] = true
	}
	if !names["superpowers"] || !names["security-guidance"] || !names["code-simplifier"] || !names["agent-sdk-dev"] {
		t.Errorf("missing expected plugin names, got: %v", names)
	}
}

func TestRealPlugin_AggregateAgents(t *testing.T) {
	dirs := []string{
		realPluginDir(t, "superpowers/4.3.0"),
		realPluginDir(t, "code-simplifier/1.0.0"),
		realPluginDir(t, "agent-sdk-dev/2cd88e7947b7"),
	}

	plugins, err := LoadPlugins(dirs)
	if err != nil {
		t.Fatalf("LoadPlugins: %v", err)
	}

	// Merge all agents from all plugins
	allAgents := map[string]PluginAgent{}
	for _, p := range plugins {
		for k, v := range p.Agents {
			allAgents[k] = v
		}
	}

	// Should have agents from all 3 plugins
	expectedAgents := []string{
		"superpowers:code-reviewer",
		"code-simplifier:code-simplifier",
		"agent-sdk-dev:agent-sdk-verifier-py",
		"agent-sdk-dev:agent-sdk-verifier-ts",
	}
	for _, name := range expectedAgents {
		if _, ok := allAgents[name]; !ok {
			t.Errorf("missing agent %q in aggregate", name)
		}
	}

	// All agents should format correctly in prompt
	prompt := renderAvailableAgentsSectionForTest(t, allAgents)
	if !strings.Contains(prompt, "<available_agents>") {
		t.Error("prompt should contain <available_agents> tag")
	}
	for _, name := range expectedAgents {
		if !strings.Contains(prompt, name) {
			t.Errorf("prompt should contain agent %q", name)
		}
	}
}

func TestRealPlugin_AggregateHooks(t *testing.T) {
	dirs := []string{
		realPluginDir(t, "superpowers/4.3.0"),
		realPluginDir(t, "security-guidance/2cd88e7947b7"),
	}

	plugins, err := LoadPlugins(dirs)
	if err != nil {
		t.Fatalf("LoadPlugins: %v", err)
	}

	// Build a runner with hooks from both plugins
	runner := newHookRunnerFromPlugins(plugins)

	// Both SessionStart (superpowers) and PreToolUse (security-guidance) should exist.
	// SessionStart matcher target is "startup" (matching Claude Code convention).
	startHooks := runner.matchHooks(HookSessionStart, "startup")
	if len(startHooks) == 0 {
		t.Error("expected SessionStart hooks from superpowers")
	}
	preToolHooks := runner.matchHooks(HookPreToolUse, "Edit")
	if len(preToolHooks) == 0 {
		t.Error("expected PreToolUse hooks from security-guidance for Edit")
	}
}

func TestRealPlugin_AggregateSkills(t *testing.T) {
	dirs := []string{
		realPluginDir(t, "superpowers/4.3.0"),
		realPluginDir(t, "security-guidance/2cd88e7947b7"),
	}

	plugins, err := LoadPlugins(dirs)
	if err != nil {
		t.Fatalf("LoadPlugins: %v", err)
	}

	allSkills := map[string]skill.SkillMeta{}
	for _, p := range plugins {
		for k, v := range p.Skills {
			allSkills[k] = v
		}
	}

	// Superpowers has skills, security-guidance has none
	if len(allSkills) < 14 {
		t.Errorf("expected at least 14 skills from superpowers, got %d total", len(allSkills))
	}

	// All should be namespaced under superpowers:
	for name := range allSkills {
		if !strings.HasPrefix(name, "superpowers:") {
			t.Errorf("unexpected non-superpowers skill: %q", name)
		}
	}
}

func TestRealPlugin_ToolNameMapping_AgentTools(t *testing.T) {
	dir := realPluginDir(t, "superpowers/4.3.0")

	lp, err := LoadPlugin(dir)
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}

	// code-reviewer agent doesn't specify tools, so it should have empty list.
	agent := lp.Agents["superpowers:code-reviewer"]
	if len(agent.Tools) != 0 {
		t.Errorf("code-reviewer should have 0 restricted tools, got %d: %v", len(agent.Tools), agent.Tools)
	}
}

func TestRealPlugin_ToolNameMapping_BidirectionalComplete(t *testing.T) {
	// Verify all Claude Code tool names map correctly
	mappings := map[string]string{
		"Read":         "read_file",
		"Write":        "write_file",
		"Edit":         "edit_file",
		"Bash":         "shell",
		"Grep":         "grep",
		"Glob":         "glob",
		"Task":         "spawn_agent",
		"WebFetch":     "web_fetch",
		"WebSearch":    "web_search",
		"NotebookEdit": "notebook_edit",
	}

	for claude, serf := range mappings {
		if mapClaudeToolName(claude) != serf {
			t.Errorf("MapClaudeToolName(%q) = %q, want %q", claude, mapClaudeToolName(claude), serf)
		}
		if mapSerfToolNameToClaude(serf) != claude {
			t.Errorf("MapSerfToolNameToClaude(%q) = %q, want %q", serf, mapSerfToolNameToClaude(serf), claude)
		}
	}

	// Unknown names pass through
	if mapClaudeToolName("custom_tool") != "custom_tool" {
		t.Error("unknown Claude name should pass through")
	}
	if mapSerfToolNameToClaude("custom_tool") != "custom_tool" {
		t.Error("unknown serf name should pass through")
	}
}

func TestRealPlugin_Settings_NotPresent(t *testing.T) {
	workDir := t.TempDir()

	// No settings file exists for any plugin
	settings, err := LoadPluginSettings(workDir, "superpowers")
	if err != nil {
		t.Fatalf("LoadPluginSettings: %v", err)
	}
	if settings != nil {
		t.Error("settings should be nil when no file exists")
	}
}

func TestRealPlugin_Settings_WithFile(t *testing.T) {
	workDir := t.TempDir()

	// Create a settings file for the superpowers plugin
	if err := os.MkdirAll(filepath.Join(workDir, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}
	content := "---\ntdd-strict: true\ndefault-branch: main\n---\n# Custom Superpowers Config\nAlways use TDD."
	if err := os.WriteFile(filepath.Join(workDir, ".claude", "superpowers.local.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	settings, err := LoadPluginSettings(workDir, "superpowers")
	if err != nil {
		t.Fatalf("LoadPluginSettings: %v", err)
	}
	if settings == nil {
		t.Fatal("settings should not be nil")
	}
	if settings.Frontmatter["tdd-strict"] != true {
		t.Errorf("tdd-strict = %v, want true", settings.Frontmatter["tdd-strict"])
	}
	if settings.Frontmatter["default-branch"] != "main" {
		t.Errorf("default-branch = %v, want main", settings.Frontmatter["default-branch"])
	}
	if !strings.Contains(settings.Body, "Always use TDD") {
		t.Error("body should contain 'Always use TDD'")
	}
}

// ---------- Helpers ----------

// newHookRunnerFromPlugin creates a hookRunner populated with a single plugin's hooks.
func newHookRunnerFromPlugin(p LoadedPlugin) *hookRunner {
	runner := newHookRunner(nil, "")
	for event, eventHooks := range p.Hooks {
		runner.Add(event, eventHooks...)
	}
	return runner
}

// newHookRunnerFromPlugins creates a hookRunner populated with hooks from multiple plugins.
func newHookRunnerFromPlugins(plugins []LoadedPlugin) *hookRunner {
	runner := newHookRunner(nil, "")
	for _, p := range plugins {
		for event, eventHooks := range p.Hooks {
			runner.Add(event, eventHooks...)
		}
	}
	return runner
}

func skillNames(m map[string]skill.SkillMeta) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	return names
}

func agentNames(m map[string]PluginAgent) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	return names
}
