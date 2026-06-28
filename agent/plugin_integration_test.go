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
	t.Parallel()
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
	t.Parallel()
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

// TestResume_DualFlavorPlugin_DoesNotReinject is the regression guard for the
// using-superpowers re-read on resume (session 01KVYB5S...). The superpowers
// plugin ships BOTH a .claude-plugin manifest (hooks.json, SessionStart matcher
// startup|clear|compact — resume excluded) and a .codex-plugin manifest
// (hooks-codex.json, matcher startup|resume|clear — resume INCLUDED). serf must
// load the Claude flavor, so resuming an existing session does NOT re-fire the
// SessionStart hook. With the old .codex-plugin-first precedence this delivered
// the bootstrap again on every resume.
func TestResume_DualFlavorPlugin_DoesNotReinject(t *testing.T) {
	t.Parallel()
	dir := makePluginDir(t, "dual-flavor") // writes .claude-plugin/plugin.json
	// Claude default hooks: resume excluded.
	hooksDir := filepath.Join(dir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"),
		[]byte(`{"hooks":{"SessionStart":[{"matcher":"startup|clear|compact","hooks":[{"type":"command","command":"echo claude-bootstrap"}]}]}}`), 0644); err != nil {
		t.Fatal(err)
	}
	// Codex flavor: resume included (the trap). serf must not pick this.
	codexDir := filepath.Join(dir, ".codex-plugin")
	if err := os.MkdirAll(codexDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexDir, "plugin.json"),
		[]byte(`{"name":"dual-flavor-codex","hooks":"./hooks/hooks-codex.json"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks-codex.json"),
		[]byte(`{"hooks":{"SessionStart":[{"matcher":"startup|resume|clear","hooks":[{"type":"command","command":"echo codex-bootstrap"}]}]}}`), 0644); err != nil {
		t.Fatal(err)
	}

	client := llm.NewClient()
	workDir := t.TempDir()
	stateDir := t.TempDir()
	cfg := SessionConfig{PluginDirs: []string{dir}, StateDir: stateDir}

	fresh, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	// Fresh startup fires the Claude bootstrap exactly once.
	if got := fresh.SteeringQueueSnapshot(); len(got) != 1 || got[0].Text != "<SYSTEM-REMINDER>claude-bootstrap</SYSTEM-REMINDER>" {
		t.Fatalf("fresh bootstrap steering = %+v, want claude-bootstrap (Claude flavor must load)", got)
	}
	fresh.appendTurn(schema.TurnUserInput, llm.User("hello"))
	fresh.Close()

	meta := schema.SessionMeta{ID: fresh.id, ProfileID: "openai", Model: "gpt-5.2", Config: cfg.toSnapshot(), TurnCount: 1}
	restored, err := RestoreSessionFromMeta(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), meta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()
	if got := restored.SteeringQueueSnapshot(); len(got) != 0 {
		t.Fatalf("resume re-fired a SessionStart hook (re-injection): %+v", got)
	}
}

func TestRestoreSessionFromMeta_DoesNotMatchStartupSessionStartHooks(t *testing.T) {
	t.Parallel()
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
	if got := fresh.SteeringQueueSnapshot(); len(got) != 1 || got[0].Text != "<SYSTEM-REMINDER>startup-bootstrap</SYSTEM-REMINDER>" {
		t.Fatalf("fresh session bootstrap steering = %+v, want wrapped startup-bootstrap", got)
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	// Verify the constant is defined and has the expected value
	if events.EventPluginLoaded != "PLUGIN_LOADED" {
		t.Errorf("EventPluginLoaded = %q, want %q", events.EventPluginLoaded, "PLUGIN_LOADED")
	}
}

// TestPluginLoadedData_PopulatedByInitPlugins verifies that the PLUGIN_LOADED
// event emitted by initPlugins correctly reflects the actual plugin content —
// name, dir, skill count, agent count, and manifest flavor. This exercises the
// production mapping in session_init.go, not just struct field reads.
func TestPluginLoadedData_PopulatedByInitPlugins(t *testing.T) {
	t.Parallel()
	dir := makePluginDir(t, "count-plugin")

	// Add 2 skills.
	for _, name := range []string{"alpha", "beta"} {
		sd := filepath.Join(dir, "skills", name)
		if err := os.MkdirAll(sd, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sd, "SKILL.md"),
			[]byte("---\nname: "+name+"\ndescription: test\n---\nbody"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Add 1 agent.
	agentsDir := filepath.Join(dir, "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "helper.md"),
		[]byte("---\nname: helper\ndescription: A helper agent\nmodel: gpt-4\ncolor: blue\n---\nYou are a helper."), 0644); err != nil {
		t.Fatal(err)
	}

	client := llm.NewClient()
	workDir := t.TempDir()
	cfg := SessionConfig{PluginDirs: []string{dir}}
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sess.Close()

	var got *events.PluginLoadedData
	for ev := range sess.Events() {
		if d, ok := ev.Data.(events.PluginLoadedData); ok && d.Name == "count-plugin" {
			d := d
			got = &d
		}
	}
	if got == nil {
		t.Fatal("no PLUGIN_LOADED event emitted for count-plugin")
	}
	if got.Name != "count-plugin" {
		t.Errorf("Name = %q, want %q", got.Name, "count-plugin")
	}
	if got.Dir != dir {
		t.Errorf("Dir = %q, want %q", got.Dir, dir)
	}
	if got.SkillCount != 2 {
		t.Errorf("SkillCount = %d, want 2", got.SkillCount)
	}
	if got.AgentCount != 1 {
		t.Errorf("AgentCount = %d, want 1", got.AgentCount)
	}
	if got.ManifestFlavor != "claude" {
		t.Errorf("ManifestFlavor = %q, want %q", got.ManifestFlavor, "claude")
	}
}
