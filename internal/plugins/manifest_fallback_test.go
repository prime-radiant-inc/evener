package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentplugin "primeradiant.com/serf/agent/plugin"
)

func TestHasPluginManifest(t *testing.T) {
	withClaudeManifest := filepath.Join(t.TempDir(), "has")
	writePlugin(t, withClaudeManifest, "widget", nil)
	if !hasPluginManifest(withClaudeManifest) {
		t.Error("hasPluginManifest = false, want true for a dir with .claude-plugin/plugin.json")
	}

	withCodexManifest := filepath.Join(t.TempDir(), "codex")
	os.MkdirAll(filepath.Join(withCodexManifest, ".codex-plugin"), 0o755)
	os.WriteFile(filepath.Join(withCodexManifest, ".codex-plugin", "plugin.json"), []byte(`{"name":"widget"}`), 0o644)
	if !hasPluginManifest(withCodexManifest) {
		t.Error("hasPluginManifest = false, want true for a .codex-plugin manifest")
	}

	bare := t.TempDir()
	if hasPluginManifest(bare) {
		t.Error("hasPluginManifest = true, want false for a bare directory")
	}
}

func TestEnsureManifestFallback_ExistingManifestIsNoop(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "widget")
	writePlugin(t, dir, "widget", nil)
	// The entry ALSO declares an MCP server — it must be ignored, since dir
	// already has its own plugin.json (Part 2 is fallback-only, never merge).
	cp := CatalogPlugin{Name: "widget", MCPServers: json.RawMessage(`{"x":{"command":"echo"}}`)}
	if err := ensureManifestFallback(dir, true, cp); err != nil {
		t.Fatalf("ensureManifestFallback on a plugin with its own manifest: %v", err)
	}
	inst, err := agentplugin.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(inst.MCPConfigs) != 0 {
		t.Errorf("MCPConfigs = %+v, want none — entry must be ignored when plugin.json exists", inst.MCPConfigs)
	}
}

func TestEnsureManifestFallback_NoManifestNoFields_ClearError(t *testing.T) {
	dir := t.TempDir() // bare: no plugin.json, no components anywhere
	cp := CatalogPlugin{Name: "bare-plugin"}
	err := ensureManifestFallback(dir, true, cp)
	if err == nil {
		t.Fatal("expected an error for a manifest-less plugin with no usable entry fields")
	}
	if strings.Contains(err.Error(), ".codex-plugin") {
		t.Errorf("error must not name the misleading .codex-plugin path, got: %v", err)
	}
	if !strings.Contains(err.Error(), "bare-plugin") {
		t.Errorf("error should name the plugin, got: %v", err)
	}
}

func TestEnsureManifestFallback_NotStaged_ClearError(t *testing.T) {
	dir := t.TempDir() // stands in for a directory-source plugin referenced in place
	cp := CatalogPlugin{Name: "dev-plugin", MCPServers: json.RawMessage(`{"x":{"command":"echo"}}`)}
	err := ensureManifestFallback(dir, false, cp)
	if err == nil {
		t.Fatal("expected an error for a not-staged (directory-source) manifest-less plugin")
	}
	if strings.Contains(err.Error(), ".codex-plugin") {
		t.Errorf("error must not name the misleading .codex-plugin path, got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".claude-plugin", "plugin.json")); statErr == nil {
		t.Fatal("must not write a manifest into a directory-source plugin serf does not own")
	}
}

func TestEnsureManifestFallback_SynthesizesManifest_MCPServerRegisters(t *testing.T) {
	dir := t.TempDir() // a cache-dir stand-in: bare, no manifest
	cp := CatalogPlugin{
		Name:        "private-journal-mcp",
		Description: "Journal MCP server",
		MCPServers:  json.RawMessage(`{"private-journal":{"command":"npx","args":["-y","private-journal-mcp"]}}`),
	}
	if err := ensureManifestFallback(dir, true, cp); err != nil {
		t.Fatalf("ensureManifestFallback: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude-plugin", "plugin.json")); err != nil {
		t.Fatalf("synthesized plugin.json missing: %v", err)
	}
	inst, err := agentplugin.Load(dir)
	if err != nil {
		t.Fatalf("Load after synthesis: %v", err)
	}
	if len(inst.MCPConfigs) != 1 || inst.MCPConfigs[0].Name != "plugin_private-journal-mcp_private-journal" {
		t.Fatalf("MCPConfigs = %+v, want one entry named plugin_private-journal-mcp_private-journal", inst.MCPConfigs)
	}
}

// TestEnsureManifestFallback_SkillsFieldNotHonored pins the accepted v1 gap
// documented on CatalogPlugin.Skills: a custom skills path declared in the
// entry is NOT picked up, only the plugin's own default skills/ directory.
func TestEnsureManifestFallback_SkillsFieldNotHonored(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "skills", "default-skill"), 0o755)
	os.WriteFile(filepath.Join(dir, "skills", "default-skill", "SKILL.md"),
		[]byte("---\nname: default-skill\ndescription: d\n---\nbody"), 0o644)
	os.MkdirAll(filepath.Join(dir, "extra-skills", "extra-skill"), 0o755)
	os.WriteFile(filepath.Join(dir, "extra-skills", "extra-skill", "SKILL.md"),
		[]byte("---\nname: extra-skill\ndescription: d\n---\nbody"), 0o644)

	cp := CatalogPlugin{
		Name:       "skills-plugin",
		MCPServers: json.RawMessage(`{"x":{"command":"echo"}}`), // needs >=1 usable field to trigger synthesis
		Skills:     json.RawMessage(`["./extra-skills/"]`),
	}
	if err := ensureManifestFallback(dir, true, cp); err != nil {
		t.Fatalf("ensureManifestFallback: %v", err)
	}
	inst, err := agentplugin.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := inst.Skills["skills-plugin:default-skill"]; !ok {
		t.Errorf("default skills/ dir should still load: %+v", inst.Skills)
	}
	if _, ok := inst.Skills["skills-plugin:extra-skill"]; ok {
		t.Errorf("entry's custom skills path must NOT be honored (documented v1 gap), got: %+v", inst.Skills)
	}
}
