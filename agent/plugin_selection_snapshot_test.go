package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/internal/plugins"
	"primeradiant.com/evener/llm"
)

func TestPluginSelectionHistoricalSnapshotPreservesDirs(t *testing.T) {
	t.Parallel()
	const fixture = `{"max_tool_rounds_per_input":17,"plugin_dirs":["/historical/alpha","/historical/beta"]}`
	var snapshot schema.ConfigSnapshot
	if err := json.Unmarshal([]byte(fixture), &snapshot); err != nil {
		t.Fatalf("decode historical snapshot: %v", err)
	}
	got := configFromSnapshot(snapshot).PluginDirs
	want := []string{"/historical/alpha", "/historical/beta"}
	if !slices.Equal(got, want) {
		t.Fatalf("historical PluginDirs = %v, want %v", got, want)
	}
}

func writePluginSelectionFixture(t *testing.T, dir, name, hookMarker, mcpMarker string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"` + name + `","hooks":{"SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":"touch ` + hookMarker + `"}]}]},"mcpServers":{"` + name + `-mcp":{"command":"` + mcpMarker + `"}}}`
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "commands", "hello"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "commands", "hello.md"), []byte("hello command"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agents", "reviewer.md"), []byte("---\nname: reviewer\ndescription: reviewer\n---\nReview"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "skills", "one"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", "one", "SKILL.md"), []byte("---\nname: one\ndescription: one\n---\nSkill"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPluginSelectionExcludedContributionsDoNotInitialize(t *testing.T) {
	workDir := t.TempDir()
	selectedDir := filepath.Join(workDir, "selected")
	excludedDir := filepath.Join(workDir, "excluded")
	selectedHook := filepath.Join(workDir, "selected-hook-marker")
	excludedHook := filepath.Join(workDir, "excluded-hook-marker")
	selectedMCP := filepath.Join(workDir, "selected-mcp-marker")
	excludedMCP := filepath.Join(workDir, "excluded-mcp-marker")
	writePluginSelectionFixture(t, selectedDir, "selected", selectedHook, selectedMCP)
	writePluginSelectionFixture(t, excludedDir, "excluded", excludedHook, excludedMCP)

	resolution, err := plugins.NewManager(t.TempDir()).ResolveForLaunch([]string{selectedDir}, nil)
	if err != nil {
		t.Fatalf("ResolveForLaunch: %v", err)
	}
	if !slices.Equal(resolution.SelectedDirs, []string{selectedDir}) {
		t.Fatalf("selected dirs = %v, want [%q]", resolution.SelectedDirs, selectedDir)
	}

	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("done") },
	}}
	client := llm.NewClient()
	client.Register(adapter)
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), SessionConfig{
		PluginDirs:       resolution.SelectedDirs,
		NoProjectPrompts: true,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx := context.Background()
	if _, err := sess.ProcessInput(ctx, "finish", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	ds := sess.DetailedStatus()
	if len(ds.Plugins) != 1 || ds.Plugins[0].Name != "selected" || ds.Plugins[0].SkillCount != 1 || ds.Plugins[0].AgentCount != 1 || ds.Plugins[0].HookCount != 1 || ds.Plugins[0].MCPCount != 1 {
		t.Fatalf("loaded plugin status = %+v", ds.Plugins)
	}
	selectedSkill := false
	for _, skill := range ds.Skills {
		if skill.Name == "one" {
			selectedSkill = true
		}
		if skill.Name == "excluded:one" {
			t.Fatalf("excluded skill initialized: %+v", ds.Skills)
		}
	}
	if !selectedSkill {
		t.Fatalf("selected skill missing from status: %+v", ds.Skills)
	}
	if !slices.Contains(ds.Agents, "selected:reviewer") {
		t.Fatalf("selected agent missing from status: %+v", ds.Agents)
	}
	if slices.Contains(ds.Agents, "excluded:reviewer") {
		t.Fatalf("excluded agent initialized: %+v", ds.Agents)
	}
	if _, ok := sess.pluginCommands["selected:hello"]; !ok {
		t.Fatalf("selected command missing: %v", sess.pluginCommands)
	}
	if _, ok := sess.pluginCommands["excluded:hello"]; ok {
		t.Fatalf("excluded command initialized: %v", sess.pluginCommands)
	}
	if !slices.Contains(sess.cfg.PluginDirs, selectedDir) || slices.Contains(sess.cfg.PluginDirs, excludedDir) {
		t.Fatalf("session PluginDirs = %v", sess.cfg.PluginDirs)
	}
	for _, cfg := range sess.pluginMCPConfigs {
		if strings.Contains(cfg.Command, excludedMCP) || slices.Contains(cfg.Args, excludedMCP) {
			t.Fatalf("excluded MCP initialized: %+v", cfg)
		}
	}
	roots := SessionInfraRoots(sess.cfg, sess.currentEnv())
	if slices.Contains(roots, excludedDir) || slices.Contains(roots, filepath.Clean(excludedDir)) {
		t.Fatalf("excluded sandbox infrastructure path initialized: %v", roots)
	}
	if _, err := os.Stat(excludedHook); !os.IsNotExist(err) {
		t.Fatalf("excluded startup hook marker exists: %v", err)
	}
	if _, err := os.Stat(excludedMCP); !os.IsNotExist(err) {
		t.Fatalf("excluded MCP marker exists: %v", err)
	}
	sess.Close()
	var loaded []string
	for ev := range sess.Events() {
		if data, ok := ev.Data.(events.PluginLoadedData); ok {
			loaded = append(loaded, data.Name)
		}
	}
	if !slices.Equal(loaded, []string{"selected"}) {
		t.Fatalf("plugin diagnostics = %v, want [selected]", loaded)
	}
}
