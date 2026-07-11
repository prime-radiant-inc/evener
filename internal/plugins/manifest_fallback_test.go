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
	if _, err := ensureManifestFallback(dir, true, cp); err != nil {
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

// TestEnsureManifestFallback_NoManifestNoFields_Installs mirrors the real
// shape of private-journal-mcp in superpowers-marketplace: a bare npm MCP
// server repo with NO plugin scaffolding at all (no .claude-plugin/, no
// .mcp.json, no commands/agents/skills/hooks dirs) and a marketplace entry
// that declares no components either. Claude Code installs this shape
// successfully (the plugin just contributes whatever conventional dirs
// exist — here, none), so serf must too: synthesize a minimal manifest from
// the entry's name/description and load with zero components.
func TestEnsureManifestFallback_NoManifestNoFields_Installs(t *testing.T) {
	dir := t.TempDir()
	// Bare npm-package layout, as in the real cached plugin.
	os.WriteFile(filepath.Join(dir, "package.json"),
		[]byte(`{"name":"private-journal-mcp","version":"2.0.1","bin":{"private-journal-mcp":"./dist/index.js"}}`), 0o644)
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# private-journal-mcp"), 0o644)

	cp := CatalogPlugin{Name: "private-journal-mcp", Description: "Private journaling MCP server"}
	if _, err := ensureManifestFallback(dir, true, cp); err != nil {
		t.Fatalf("ensureManifestFallback on a manifest-less, component-less plugin: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatalf("synthesized plugin.json missing: %v", err)
	}
	var m agentplugin.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("synthesized plugin.json is not valid JSON: %v", err)
	}
	if m.Name != "private-journal-mcp" || m.Description != "Private journaling MCP server" {
		t.Errorf("synthesized manifest = %+v, want entry's name/description", m)
	}
	inst, err := agentplugin.Load(dir)
	if err != nil {
		t.Fatalf("Load after synthesis: %v", err)
	}
	if len(inst.MCPConfigs) != 0 || len(inst.Commands) != 0 || len(inst.Agents) != 0 || len(inst.Skills) != 0 {
		t.Errorf("expected zero components, got %+v", inst)
	}
}

func TestEnsureManifestFallback_NotStaged_ClearError(t *testing.T) {
	dir := t.TempDir() // stands in for a directory-source plugin referenced in place
	cp := CatalogPlugin{Name: "dev-plugin", MCPServers: json.RawMessage(`{"x":{"command":"echo"}}`)}
	_, err := ensureManifestFallback(dir, false, cp)
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
	if _, err := ensureManifestFallback(dir, true, cp); err != nil {
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

// writeBinPlugin lays out a bare npm MCP-server repo: a package.json with the
// given bin map plus real files for each of targets (relative paths).
func writeBinPlugin(t *testing.T, dir, name, binJSON string, targets ...string) {
	t.Helper()
	pkg := `{"name":"` + name + `","version":"1.0.0","bin":` + binJSON + `}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, rel := range targets {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/usr/bin/env node\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// fallbackAndLoad runs ensureManifestFallback (staged) and loads the result.
func fallbackAndLoad(t *testing.T, dir string, cp CatalogPlugin) (agentplugin.Instance, string) {
	t.Helper()
	note, err := ensureManifestFallback(dir, true, cp)
	if err != nil {
		t.Fatalf("ensureManifestFallback: %v", err)
	}
	inst, err := agentplugin.Load(dir)
	if err != nil {
		t.Fatalf("Load after synthesis: %v", err)
	}
	return inst, note
}

// TestEnsureManifestFallback_PackageJSONBin_WiresMCP is the feature's happy
// path: a manifest-less bare npm MCP-server repo whose package.json declares
// exactly one bin, with the bin target actually built and present. serf goes
// beyond Claude Code's zero-component parity here: the synthesized manifest
// carries an mcpServers entry (node + the bin path under
// ${CLAUDE_PLUGIN_ROOT}) so installing the plugin makes its MCP available.
func TestEnsureManifestFallback_PackageJSONBin_WiresMCP(t *testing.T) {
	dir := t.TempDir()
	writeBinPlugin(t, dir, "private-journal-mcp",
		`{"private-journal-mcp":"./dist/index.js"}`, "dist/index.js")

	cp := CatalogPlugin{Name: "private-journal-mcp", Description: "Journal MCP"}
	inst, note := fallbackAndLoad(t, dir, cp)
	if note != "" {
		t.Errorf("note = %q, want none on the wired path", note)
	}
	if len(inst.MCPConfigs) != 1 {
		t.Fatalf("MCPConfigs = %+v, want exactly one auto-wired server", inst.MCPConfigs)
	}
	got := inst.MCPConfigs[0]
	if got.Name != "plugin_private-journal-mcp_private-journal-mcp" {
		t.Errorf("Name = %q", got.Name)
	}
	if got.Command != "node" {
		t.Errorf("Command = %q, want node", got.Command)
	}
	wantArg := filepath.Join(inst.Dir, "dist", "index.js")
	if len(got.Args) != 1 || got.Args[0] != wantArg {
		t.Errorf("Args = %v, want [%s]", got.Args, wantArg)
	}
}

// TestEnsureManifestFallback_BinTargetMissing_NoteNoWire mirrors the REAL
// cached private-journal-mcp/2.0.1: package.json points bin at ./dist/index.js
// but the git clone ships only src/ (dist/ is produced by npm's prepare
// script, which never ran). Wiring node at a nonexistent file would install a
// broken server, so serf must skip wiring and surface an install-time note.
func TestEnsureManifestFallback_BinTargetMissing_NoteNoWire(t *testing.T) {
	dir := t.TempDir()
	writeBinPlugin(t, dir, "private-journal-mcp",
		`{"private-journal-mcp":"./dist/index.js"}`) // no dist/ on disk

	inst, note := fallbackAndLoad(t, dir, CatalogPlugin{Name: "private-journal-mcp"})
	if len(inst.MCPConfigs) != 0 {
		t.Errorf("MCPConfigs = %+v, want none for a missing bin target", inst.MCPConfigs)
	}
	if !strings.Contains(note, "dist/index.js") || !strings.Contains(note, "not wiring") {
		t.Errorf("note = %q, want it to name the missing bin target and say the server was not wired", note)
	}
}

// TestEnsureManifestFallback_MultipleBins_PicksPluginName: with several bins,
// only the one matching the plugin name is wired — no guessing among the rest.
func TestEnsureManifestFallback_MultipleBins_PicksPluginName(t *testing.T) {
	dir := t.TempDir()
	writeBinPlugin(t, dir, "multi-tool",
		`{"helper":"./bin/helper.js","multi-tool":"./bin/main.js"}`,
		"bin/helper.js", "bin/main.js")

	inst, note := fallbackAndLoad(t, dir, CatalogPlugin{Name: "multi-tool"})
	if note != "" {
		t.Errorf("note = %q, want none", note)
	}
	if len(inst.MCPConfigs) != 1 || inst.MCPConfigs[0].Name != "plugin_multi-tool_multi-tool" {
		t.Fatalf("MCPConfigs = %+v, want just the name-matching bin", inst.MCPConfigs)
	}
	if want := filepath.Join(inst.Dir, "bin", "main.js"); len(inst.MCPConfigs[0].Args) != 1 || inst.MCPConfigs[0].Args[0] != want {
		t.Errorf("Args = %v, want [%s]", inst.MCPConfigs[0].Args, want)
	}
}

// TestEnsureManifestFallback_MultipleBins_NoMatch_NoteSkip: several bins, none
// named after the plugin — serf must not guess which is the MCP server.
func TestEnsureManifestFallback_MultipleBins_NoMatch_NoteSkip(t *testing.T) {
	dir := t.TempDir()
	writeBinPlugin(t, dir, "some-plugin",
		`{"alpha":"./a.js","beta":"./b.js"}`, "a.js", "b.js")

	inst, note := fallbackAndLoad(t, dir, CatalogPlugin{Name: "some-plugin"})
	if len(inst.MCPConfigs) != 0 {
		t.Errorf("MCPConfigs = %+v, want none when no bin matches the plugin name", inst.MCPConfigs)
	}
	if note == "" || !strings.Contains(note, "not wiring") {
		t.Errorf("note = %q, want a skip note", note)
	}
}

// TestEnsureManifestFallback_EntryMCPServersWin: a marketplace entry that
// declares its own mcpServers is authoritative; bin auto-detection must not
// add or replace anything.
func TestEnsureManifestFallback_EntryMCPServersWin(t *testing.T) {
	dir := t.TempDir()
	writeBinPlugin(t, dir, "widget", `{"widget":"./cli.js"}`, "cli.js")

	cp := CatalogPlugin{
		Name:       "widget",
		MCPServers: json.RawMessage(`{"declared":{"command":"echo"}}`),
	}
	inst, note := fallbackAndLoad(t, dir, cp)
	if note != "" {
		t.Errorf("note = %q, want none", note)
	}
	if len(inst.MCPConfigs) != 1 || inst.MCPConfigs[0].Name != "plugin_widget_declared" {
		t.Fatalf("MCPConfigs = %+v, want only the entry-declared server", inst.MCPConfigs)
	}
}

// TestEnsureManifestFallback_MCPJSONPresent_NoBinWiring: a root .mcp.json is
// the plugin's own MCP declaration (Claude Code convention) and Load() already
// honors it; auto-wiring the bin too would register the server twice.
func TestEnsureManifestFallback_MCPJSONPresent_NoBinWiring(t *testing.T) {
	dir := t.TempDir()
	writeBinPlugin(t, dir, "journal", `{"journal":"./cli.js"}`, "cli.js")
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"),
		[]byte(`{"mcpServers":{"journal":{"command":"${CLAUDE_PLUGIN_ROOT}/cli.js"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	inst, note := fallbackAndLoad(t, dir, CatalogPlugin{Name: "journal"})
	if note != "" {
		t.Errorf("note = %q, want none", note)
	}
	if len(inst.MCPConfigs) != 1 || inst.MCPConfigs[0].Name != "plugin_journal_journal" {
		t.Fatalf("MCPConfigs = %+v, want exactly the .mcp.json server", inst.MCPConfigs)
	}
	if inst.MCPConfigs[0].Command != filepath.Join(inst.Dir, "cli.js") {
		t.Errorf("Command = %q, want the .mcp.json (expanded) command, not node auto-wiring", inst.MCPConfigs[0].Command)
	}
}

// TestEnsureManifestFallback_NoPackageJSON_LSPShape pins that non-node
// manifest-less plugins (gopls-lsp, rust-analyzer-lsp, swift-lsp: just
// LICENSE/README, no package.json) are untouched by bin detection: zero
// components, no note — the pre-existing Claude Code parity behavior.
func TestEnsureManifestFallback_NoPackageJSON_LSPShape(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# gopls-lsp"), 0o644)
	os.WriteFile(filepath.Join(dir, "LICENSE"), []byte("MIT"), 0o644)

	inst, note := fallbackAndLoad(t, dir, CatalogPlugin{Name: "gopls-lsp"})
	if note != "" {
		t.Errorf("note = %q, want none for a plugin with no package.json", note)
	}
	if len(inst.MCPConfigs) != 0 {
		t.Errorf("MCPConfigs = %+v, want none", inst.MCPConfigs)
	}
}

// TestEnsureManifestFallback_PackageJSONNoBin: a package.json without a bin
// map (a library, not a server) contributes nothing and warrants no note.
func TestEnsureManifestFallback_PackageJSONNoBin(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"lib","version":"1.0.0"}`), 0o644)

	inst, note := fallbackAndLoad(t, dir, CatalogPlugin{Name: "lib"})
	if note != "" || len(inst.MCPConfigs) != 0 {
		t.Errorf("note = %q, MCPConfigs = %+v; want no wiring and no note", note, inst.MCPConfigs)
	}
}

// TestEnsureManifestFallback_BinEscapesDir_NoteSkip: a bin path resolving
// outside the plugin dir must never be wired.
func TestEnsureManifestFallback_BinEscapesDir_NoteSkip(t *testing.T) {
	dir := t.TempDir()
	writeBinPlugin(t, dir, "sneaky", `{"sneaky":"../../evil.js"}`)

	inst, note := fallbackAndLoad(t, dir, CatalogPlugin{Name: "sneaky"})
	if len(inst.MCPConfigs) != 0 {
		t.Errorf("MCPConfigs = %+v, want none for an escaping bin path", inst.MCPConfigs)
	}
	if note == "" || !strings.Contains(note, "not wiring") {
		t.Errorf("note = %q, want a skip note", note)
	}
}

// TestEnsureManifestFallback_MalformedPackageJSON: junk package.json must not
// fail the install — Claude Code installs the plugin regardless; we just skip
// auto-wiring silently (nothing MCP-shaped was detectable).
func TestEnsureManifestFallback_MalformedPackageJSON(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"bin": nope`), 0o644)

	inst, note := fallbackAndLoad(t, dir, CatalogPlugin{Name: "junk"})
	if note != "" || len(inst.MCPConfigs) != 0 {
		t.Errorf("note = %q, MCPConfigs = %+v; want silent skip", note, inst.MCPConfigs)
	}
}

// TestEnsureManifestFallback_EmptyBinName_NoteSkip pins a fuzz-found bug:
// a bin map whose single entry has an empty name ({"":"cli.js"}) must not be
// wired — an empty server name fails mcpconfig validation downstream.
func TestEnsureManifestFallback_EmptyBinName_NoteSkip(t *testing.T) {
	dir := t.TempDir()
	writeBinPlugin(t, dir, "oops", `{"":"./cli.js"}`, "cli.js")

	inst, note := fallbackAndLoad(t, dir, CatalogPlugin{Name: "oops"})
	if len(inst.MCPConfigs) != 0 {
		t.Errorf("MCPConfigs = %+v, want none for an empty bin name", inst.MCPConfigs)
	}
	if note == "" || !strings.Contains(note, "not wiring") {
		t.Errorf("note = %q, want a skip note", note)
	}
}

// TestEnsureManifestFallback_StringBin_NoteSkip: npm also allows "bin" to be a
// bare string, but no real plugin in the surveyed caches uses it, so serf
// deliberately does not wire that shape — skip with a note (don't guess).
func TestEnsureManifestFallback_StringBin_NoteSkip(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"),
		[]byte(`{"name":"strbin","version":"1.0.0","bin":"./cli.js"}`), 0o644)
	os.WriteFile(filepath.Join(dir, "cli.js"), []byte("x"), 0o644)

	inst, note := fallbackAndLoad(t, dir, CatalogPlugin{Name: "strbin"})
	if len(inst.MCPConfigs) != 0 {
		t.Errorf("MCPConfigs = %+v, want none for a string-form bin", inst.MCPConfigs)
	}
	if note == "" || !strings.Contains(note, "not wiring") {
		t.Errorf("note = %q, want a skip note", note)
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
	if _, err := ensureManifestFallback(dir, true, cp); err != nil {
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
