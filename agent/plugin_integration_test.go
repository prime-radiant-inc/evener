package agent

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/hooks"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/skill"
	"primeradiant.com/serf/llm"
)

// --- initPlugins tests ---

func TestInitPlugins_MergesSkills(t *testing.T) {
	dir := makePluginDir(t, "skill-plugin")
	skillDir := filepath.Join(dir, "skills", "my-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: my-skill\ndescription: test\n---\nbody"), 0644); err != nil {
		t.Fatal(err)
	}

	// Verify plugin.Load populates Skills with namespaced key
	plugin, err := plugin.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := plugin.Skills["skill-plugin:my-skill"]; !ok {
		t.Errorf("skill not found, got keys: %v", keys(plugin.Skills))
	}
}

func TestInitPlugins_BuildsHookRunner(t *testing.T) {
	dir := makePluginDir(t, "hook-plugin")
	hooksDir := filepath.Join(dir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	hooksJSON := `{
		"hooks": {
			"PreToolUse": [
				{
					"matcher": "*",
					"hooks": [{"type": "command", "command": "echo ok"}]
				}
			]
		}
	}`
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), []byte(hooksJSON), 0644); err != nil {
		t.Fatal(err)
	}

	plugins, err := plugin.LoadAll([]string{dir})
	if err != nil {
		t.Fatal(err)
	}

	// Simulate what initPlugins does: build runner from plugin hooks
	runner := hooks.NewRunner(nil, "test-model")
	for _, p := range plugins {
		for event, eventHooks := range p.Hooks {
			runner.Add(event, eventHooks...)
		}
	}

	matched := runner.MatchHooks(plugin.HookPreToolUse, "Write")
	if len(matched) != 1 {
		t.Errorf("expected 1 matched hook, got %d", len(matched))
	}
}

func TestRestoreSessionFromMeta_DoesNotMatchStartupSessionStartHooks(t *testing.T) {
	dir := makePluginDir(t, "session-start-plugin")
	hooksDir := filepath.Join(dir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	hooksJSON := `{
		"hooks": {
			"SessionStart": [
				{
					"matcher": "startup|clear|compact",
					"hooks": [{"type": "command", "command": "echo startup-bootstrap"}]
				}
			]
		}
	}`
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), []byte(hooksJSON), 0644); err != nil {
		t.Fatal(err)
	}

	client := llm.NewClient()
	workDir := t.TempDir()
	stateDir := t.TempDir()
	cfg := SessionConfig{
		PluginDirs: []string{dir},
		StateDir:   stateDir,
	}

	fresh, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer fresh.Close()
	if got := fresh.SteeringQueueSnapshot(); len(got) != 1 || got[0].Text != "startup-bootstrap" {
		t.Fatalf("fresh session bootstrap steering = %+v, want startup-bootstrap", got)
	}

	meta := schema.SessionMeta{
		ID:        "resume-session",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    cfg.toSnapshot(),
	}
	restored, err := RestoreSessionFromMeta(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), meta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()
	if got := restored.SteeringQueueSnapshot(); len(got) != 0 {
		t.Fatalf("restored session matched startup-only SessionStart hook: %+v", got)
	}
}

func TestInitPlugins_MergesAgents(t *testing.T) {
	dir := makePluginDir(t, "agent-plugin")
	agentsDir := filepath.Join(dir, "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "helper.md"),
		[]byte("---\nname: helper\ndescription: A helper agent\nmodel: gpt-4\ncolor: blue\n---\nYou are a helper."), 0644); err != nil {
		t.Fatal(err)
	}

	plugin, err := plugin.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := plugin.Agents["agent-plugin:helper"]; !ok {
		t.Errorf("agent not found, got keys: %v", keys(plugin.Agents))
	}
}

func TestInitPlugins_NoPlugins(t *testing.T) {
	// Simulates initPlugins with empty PluginDirs — should be a no-op
	plugins, err := plugin.LoadAll(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plugins) != 0 {
		t.Errorf("expected 0 plugins, got %d", len(plugins))
	}
}

func TestInitPlugins_CombinesMultiplePlugins(t *testing.T) {
	// Plugin A: provides a skill
	dirA := makePluginDir(t, "plugin-a")
	skillDir := filepath.Join(dirA, "skills", "alpha")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: alpha\ndescription: Alpha skill\n---\nbody"), 0644)

	// Plugin B: provides a hook
	dirB := makePluginDir(t, "plugin-b")
	hooksDir := filepath.Join(dirB, "hooks")
	os.MkdirAll(hooksDir, 0755)
	os.WriteFile(filepath.Join(hooksDir, "hooks.json"),
		[]byte(`{"hooks":{"PostToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"echo post"}]}]}}`), 0644)

	plugins, err := plugin.LoadAll([]string{dirA, dirB})
	if err != nil {
		t.Fatal(err)
	}

	// Simulate initPlugins: merge skills from all plugins
	allSkills := map[string]skill.SkillMeta{}
	runner := hooks.NewRunner(nil, "test-model")
	allAgents := map[string]plugin.Agent{}

	for _, p := range plugins {
		for name, meta := range p.Skills {
			allSkills[name] = meta
		}
		for name, agent := range p.Agents {
			allAgents[name] = agent
		}
		for event, eventHooks := range p.Hooks {
			runner.Add(event, eventHooks...)
		}
	}

	// Plugin A's skill should be merged
	if _, ok := allSkills["plugin-a:alpha"]; !ok {
		t.Error("expected plugin-a:alpha skill")
	}

	// Plugin B's hook should be in the runner
	matched := runner.MatchHooks(plugin.HookPostToolUse, "Write")
	if len(matched) != 1 {
		t.Errorf("expected 1 matched PostToolUse hook, got %d", len(matched))
	}
}

// --- Event types ---

func TestEventPluginLoaded_Exists(t *testing.T) {
	// Verify the constant is defined and has the expected value
	if events.EventPluginLoaded != "PLUGIN_LOADED" {
		t.Errorf("EventPluginLoaded = %q, want %q", events.EventPluginLoaded, "PLUGIN_LOADED")
	}
}

func TestPluginLoadedData_Fields(t *testing.T) {
	data := events.PluginLoadedData{
		Name:       "test-plugin",
		Dir:        "/some/path",
		SkillCount: 3,
		AgentCount: 1,
	}
	if data.Name != "test-plugin" {
		t.Errorf("Name = %q", data.Name)
	}
	if data.Dir != "/some/path" {
		t.Errorf("Dir = %q", data.Dir)
	}
	if data.SkillCount != 3 {
		t.Errorf("SkillCount = %d", data.SkillCount)
	}
	if data.AgentCount != 1 {
		t.Errorf("AgentCount = %d", data.AgentCount)
	}
}
