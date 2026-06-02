package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/mcpconfig"
)

func TestValidatePluginName(t *testing.T) {
	valid := []string{
		"my-plugin",
		"a",
		"test-123",
		"a-b-c",
		"plugin42",
	}
	for _, name := range valid {
		if err := validatePluginName(name); err != nil {
			t.Errorf("validatePluginName(%q) returned error: %v", name, err)
		}
	}

	invalid := []struct {
		name string
		desc string
	}{
		{"", "empty"},
		{"has spaces", "spaces"},
		{"UPPER", "uppercase"},
		{"under_score", "underscore"},
		{"-leading", "leading hyphen"},
		{"trailing-", "trailing hyphen"},
	}
	for _, tt := range invalid {
		if err := validatePluginName(tt.name); err == nil {
			t.Errorf("validatePluginName(%q) [%s]: expected error, got nil", tt.name, tt.desc)
		}
	}
}

func TestParsePluginManifest_Minimal(t *testing.T) {
	data := []byte(`{"name": "my-plugin"}`)
	m, err := ParsePluginManifest(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Name != "my-plugin" {
		t.Errorf("Name = %q, want %q", m.Name, "my-plugin")
	}
}

func TestParsePluginManifest_FullMetadata(t *testing.T) {
	data := []byte(`{
		"name": "full-plugin",
		"version": "1.2.3",
		"description": "A full plugin",
		"author": {"name": "Jesse", "email": "j@example.com", "url": "https://example.com"},
		"homepage": "https://example.com/full-plugin",
		"repository": "https://github.com/example/full-plugin",
		"license": "MIT",
		"keywords": ["test", "full"],
		"commands": ["/greet"],
		"agents": {"helper": {"description": "helps"}},
		"hooks": {"on-start": "echo hi"},
		"mcpServers": {"srv": {"command": "run-it"}}
	}`)
	m, err := ParsePluginManifest(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Name != "full-plugin" {
		t.Errorf("Name = %q, want %q", m.Name, "full-plugin")
	}
	if m.Version != "1.2.3" {
		t.Errorf("Version = %q, want %q", m.Version, "1.2.3")
	}
	if m.Description != "A full plugin" {
		t.Errorf("Description = %q, want %q", m.Description, "A full plugin")
	}
	if m.Homepage != "https://example.com/full-plugin" {
		t.Errorf("Homepage = %q", m.Homepage)
	}
	if m.Repository != "https://github.com/example/full-plugin" {
		t.Errorf("Repository = %q", m.Repository)
	}
	if m.License != "MIT" {
		t.Errorf("License = %q, want %q", m.License, "MIT")
	}
	if len(m.Keywords) != 2 || m.Keywords[0] != "test" || m.Keywords[1] != "full" {
		t.Errorf("Keywords = %v, want [test full]", m.Keywords)
	}
	if m.Author == nil {
		t.Error("Author is nil, expected object")
	}
	if m.Commands == nil {
		t.Error("Commands is nil, expected data")
	}
	if m.Agents == nil {
		t.Error("Agents is nil, expected data")
	}
	if m.Hooks == nil {
		t.Error("Hooks is nil, expected data")
	}
	if m.MCPServers == nil {
		t.Error("MCPServers is nil, expected data")
	}
}

func TestParsePluginManifest_AuthorString(t *testing.T) {
	data := []byte(`{"name": "a", "author": "Jesse"}`)
	m, err := ParsePluginManifest(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Author == nil {
		t.Fatal("Author is nil, expected string value")
	}
}

func TestParsePluginManifest_MissingName(t *testing.T) {
	data := []byte(`{"version": "1.0.0"}`)
	_, err := ParsePluginManifest(data)
	if err == nil {
		t.Fatal("expected error for missing name")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("error %q should mention 'name'", err.Error())
	}
}

func TestParsePluginManifest_InvalidNames(t *testing.T) {
	names := []string{
		"has spaces",
		"UPPERCASE",
		"-leading",
		"trailing-",
		"under_score",
	}
	for _, name := range names {
		data := []byte(`{"name": "` + name + `"}`)
		_, err := ParsePluginManifest(data)
		if err == nil {
			t.Errorf("ParsePluginManifest with name %q: expected error, got nil", name)
		}
	}
}

func TestParsePluginManifest_InvalidJSON(t *testing.T) {
	_, err := ParsePluginManifest([]byte(`{invalid`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestExpandPluginRoot(t *testing.T) {
	tests := []struct {
		input     string
		pluginDir string
		want      string
	}{
		{
			"${CLAUDE_PLUGIN_ROOT}/bin/tool",
			"/home/user/.plugins/my-plugin",
			"/home/user/.plugins/my-plugin/bin/tool",
		},
		{
			"${PLUGIN_ROOT}/bin/tool",
			"/home/user/.plugins/my-plugin",
			"/home/user/.plugins/my-plugin/bin/tool",
		},
		{
			"no variable here",
			"/some/dir",
			"no variable here",
		},
		{
			"${CLAUDE_PLUGIN_ROOT}",
			"/plugins/x",
			"/plugins/x",
		},
		{
			"prefix-${CLAUDE_PLUGIN_ROOT}-suffix",
			"/p",
			"prefix-/p-suffix",
		},
	}
	for _, tt := range tests {
		got := expandPluginRoot(tt.input, tt.pluginDir)
		if got != tt.want {
			t.Errorf("expandPluginRoot(%q, %q) = %q, want %q", tt.input, tt.pluginDir, got, tt.want)
		}
	}
}

// makePluginDir creates a temp dir with a .claude-plugin/plugin.json manifest.
// Returns the resolved (EvalSymlinks) path to the plugin dir.
func makePluginDir(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	metaDir := filepath.Join(dir, ".claude-plugin")
	if err := os.MkdirAll(metaDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	manifest := `{"name": "` + name + `"}`
	if err := os.WriteFile(filepath.Join(metaDir, "plugin.json"), []byte(manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return dir
}

func TestLoadPlugin_Valid(t *testing.T) {
	dir := makePluginDir(t, "test-plugin")
	lp, err := LoadPlugin(dir)
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}
	if lp.Manifest.Name != "test-plugin" {
		t.Errorf("Name = %q, want %q", lp.Manifest.Name, "test-plugin")
	}
	if lp.Dir != dir {
		t.Errorf("Dir = %q, want %q", lp.Dir, dir)
	}
	// Dir should be absolute
	if !filepath.IsAbs(lp.Dir) {
		t.Errorf("Dir %q is not absolute", lp.Dir)
	}
}

func TestLoadPlugin_PrefersCodexManifest(t *testing.T) {
	dir := makePluginDir(t, "claude-plugin")
	metaDir := filepath.Join(dir, ".codex-plugin")
	if err := os.MkdirAll(metaDir, 0755); err != nil {
		t.Fatalf("mkdir .codex-plugin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metaDir, "plugin.json"), []byte(`{"name":"codex-plugin","hooks":"./hooks/hooks-codex.json"}`), 0644); err != nil {
		t.Fatalf("write codex manifest: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "hooks"), 0755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	hooks := `{"hooks":{"SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":"\"${PLUGIN_ROOT}/hooks/session-start-codex\""}]}]}}`
	if err := os.WriteFile(filepath.Join(dir, "hooks", "hooks-codex.json"), []byte(hooks), 0644); err != nil {
		t.Fatalf("write codex hooks: %v", err)
	}

	lp, err := LoadPlugin(dir)
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}
	if lp.Manifest.Name != "codex-plugin" {
		t.Fatalf("manifest name = %q, want codex-plugin", lp.Manifest.Name)
	}
	got := lp.Hooks[HookSessionStart]
	if len(got) != 1 {
		t.Fatalf("SessionStart hook count = %d, want 1", len(got))
	}
	wantCommand := filepath.Join(dir, "hooks", "session-start-codex")
	if got[0].Command != `"`+wantCommand+`"` {
		t.Fatalf("hook command = %q, want quoted %q", got[0].Command, wantCommand)
	}
}

func TestLoadPlugin_MissingManifest(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadPlugin(dir)
	if err == nil {
		t.Fatal("expected error for missing manifest")
	}
}

func TestLoadPlugin_NonExistentDir(t *testing.T) {
	_, err := LoadPlugin("/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Fatal("expected error for non-existent dir")
	}
}

func TestLoadPlugins_Multiple(t *testing.T) {
	dir1 := makePluginDir(t, "plugin-a")
	dir2 := makePluginDir(t, "plugin-b")

	plugins, err := LoadPlugins([]string{dir1, dir2})
	if err != nil {
		t.Fatalf("LoadPlugins: %v", err)
	}
	if len(plugins) != 2 {
		t.Fatalf("got %d plugins, want 2", len(plugins))
	}
	if plugins[0].Manifest.Name != "plugin-a" {
		t.Errorf("plugins[0].Name = %q, want %q", plugins[0].Manifest.Name, "plugin-a")
	}
	if plugins[1].Manifest.Name != "plugin-b" {
		t.Errorf("plugins[1].Name = %q, want %q", plugins[1].Manifest.Name, "plugin-b")
	}
}

func TestLoadPlugins_DuplicateName(t *testing.T) {
	dir1 := makePluginDir(t, "same-name")
	dir2 := makePluginDir(t, "same-name")

	_, err := LoadPlugins([]string{dir1, dir2})
	if err == nil {
		t.Fatal("expected error for duplicate plugin name")
	}
	if !strings.Contains(err.Error(), "same-name") {
		t.Errorf("error %q should mention duplicate name", err.Error())
	}
}

func TestLoadPlugins_Empty(t *testing.T) {
	plugins, err := LoadPlugins(nil)
	if err != nil {
		t.Fatalf("LoadPlugins(nil): %v", err)
	}
	if len(plugins) != 0 {
		t.Errorf("got %d plugins, want 0", len(plugins))
	}
}

func TestResolveComponentDirs_DefaultOnly(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	// Create the default "commands" subdir
	defaultDir := filepath.Join(dir, "commands")
	if err := os.MkdirAll(defaultDir, 0755); err != nil {
		t.Fatal(err)
	}

	dirs := resolveComponentDirs(dir, "commands", nil)
	if len(dirs) != 1 {
		t.Fatalf("got %d dirs, want 1", len(dirs))
	}
	if dirs[0] != defaultDir {
		t.Errorf("dirs[0] = %q, want %q", dirs[0], defaultDir)
	}
}

func TestResolveComponentDirs_DefaultPlusCustomString(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	// Create both default and custom dirs
	defaultDir := filepath.Join(dir, "commands")
	customDir := filepath.Join(dir, "my-cmds")
	os.MkdirAll(defaultDir, 0755)
	os.MkdirAll(customDir, 0755)

	dirs := resolveComponentDirs(dir, "commands", "./my-cmds")
	if len(dirs) != 2 {
		t.Fatalf("got %d dirs, want 2: %v", len(dirs), dirs)
	}
	if dirs[0] != defaultDir {
		t.Errorf("dirs[0] = %q, want %q", dirs[0], defaultDir)
	}
	if dirs[1] != customDir {
		t.Errorf("dirs[1] = %q, want %q", dirs[1], customDir)
	}
}

func TestResolveComponentDirs_DefaultPlusCustomArray(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	defaultDir := filepath.Join(dir, "agents")
	custom1 := filepath.Join(dir, "extra1")
	custom2 := filepath.Join(dir, "extra2")
	os.MkdirAll(defaultDir, 0755)
	os.MkdirAll(custom1, 0755)
	os.MkdirAll(custom2, 0755)

	override := []any{"./extra1", "./extra2"}
	dirs := resolveComponentDirs(dir, "agents", override)
	if len(dirs) != 3 {
		t.Fatalf("got %d dirs, want 3: %v", len(dirs), dirs)
	}
	if dirs[0] != defaultDir {
		t.Errorf("dirs[0] = %q, want %q", dirs[0], defaultDir)
	}
	if dirs[1] != custom1 {
		t.Errorf("dirs[1] = %q, want %q", dirs[1], custom1)
	}
	if dirs[2] != custom2 {
		t.Errorf("dirs[2] = %q, want %q", dirs[2], custom2)
	}
}

func TestResolveComponentDirs_CustomDirDoesNotExist(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)

	// No default dir, custom doesn't exist either — caller decides what to do
	dirs := resolveComponentDirs(dir, "commands", "./nonexistent")
	if len(dirs) != 1 {
		t.Fatalf("got %d dirs, want 1: %v", len(dirs), dirs)
	}
	expected := filepath.Join(dir, "nonexistent")
	if dirs[0] != expected {
		t.Errorf("dirs[0] = %q, want %q", dirs[0], expected)
	}
}

// keys returns sorted map keys for test diagnostics.
func keys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func TestDiscoverPluginSkills(t *testing.T) {
	dir := makePluginDir(t, "my-plugin")
	// Create a skill
	skillDir := filepath.Join(dir, "skills", "my-skill")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: my-skill\ndescription: A test skill\n---\nSkill body"), 0644)

	plugin, err := LoadPlugin(dir)
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}

	// Skills should be namespaced
	if _, ok := plugin.Skills["my-plugin:my-skill"]; !ok {
		t.Errorf("expected 'my-plugin:my-skill', got keys: %v", keys(plugin.Skills))
	}
	// Verify the meta has correct values
	meta := plugin.Skills["my-plugin:my-skill"]
	if meta.Description != "A test skill" {
		t.Errorf("Description = %q", meta.Description)
	}
}

func TestDiscoverPluginSkills_NoSkillsDir(t *testing.T) {
	dir := makePluginDir(t, "empty-plugin")
	plugin, err := LoadPlugin(dir)
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}
	if len(plugin.Skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(plugin.Skills))
	}
}

func TestDiscoverPluginSkills_MultipleSkills(t *testing.T) {
	dir := makePluginDir(t, "multi-plugin")
	for _, name := range []string{"skill-a", "skill-b"} {
		skillDir := filepath.Join(dir, "skills", name)
		os.MkdirAll(skillDir, 0755)
		os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
			[]byte("---\nname: "+name+"\ndescription: desc-"+name+"\n---\nbody"), 0644)
	}

	plugin, err := LoadPlugin(dir)
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}
	if len(plugin.Skills) != 2 {
		t.Fatalf("expected 2 skills, got %d: %v", len(plugin.Skills), keys(plugin.Skills))
	}
	if _, ok := plugin.Skills["multi-plugin:skill-a"]; !ok {
		t.Error("missing multi-plugin:skill-a")
	}
	if _, ok := plugin.Skills["multi-plugin:skill-b"]; !ok {
		t.Error("missing multi-plugin:skill-b")
	}
}

func TestResolveComponentDirs_MissingDefaultDir(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	// Don't create the default dir, but create a custom one
	customDir := filepath.Join(dir, "my-agents")
	os.MkdirAll(customDir, 0755)

	dirs := resolveComponentDirs(dir, "agents", "./my-agents")
	// Default doesn't exist on disk, so only custom is returned
	if len(dirs) != 1 {
		t.Fatalf("got %d dirs, want 1: %v", len(dirs), dirs)
	}
	if dirs[0] != customDir {
		t.Errorf("dirs[0] = %q, want %q", dirs[0], customDir)
	}
}

func TestDiscoverPluginMCPConfigs_FromFile(t *testing.T) {
	pluginDir := t.TempDir()
	pluginDir, _ = filepath.EvalSymlinks(pluginDir)
	os.WriteFile(filepath.Join(pluginDir, ".mcp.json"),
		[]byte(`{"mcpServers": {"my-server": {"command": "echo", "args": ["hello"]}}}`), 0644)

	configs, err := discoverPluginMCPConfigs(pluginDir, nil, "test-plugin")
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 {
		t.Fatalf("got %d configs, want 1", len(configs))
	}
	if configs[0].Name != "plugin_test-plugin_my-server" {
		t.Errorf("Name = %q", configs[0].Name)
	}
	if configs[0].Command != "echo" {
		t.Errorf("Command = %q", configs[0].Command)
	}
	if len(configs[0].Args) != 1 || configs[0].Args[0] != "hello" {
		t.Errorf("Args = %v", configs[0].Args)
	}
}

func TestDiscoverPluginMCPConfigs_ExpandsRoot(t *testing.T) {
	pluginDir := t.TempDir()
	pluginDir, _ = filepath.EvalSymlinks(pluginDir)
	os.WriteFile(filepath.Join(pluginDir, ".mcp.json"),
		[]byte(`{"mcpServers": {"srv": {"command": "${CLAUDE_PLUGIN_ROOT}/server"}}}`), 0644)

	configs, err := discoverPluginMCPConfigs(pluginDir, nil, "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 {
		t.Fatalf("got %d configs, want 1", len(configs))
	}
	if configs[0].Command != pluginDir+"/server" {
		t.Errorf("Command = %q, want %q", configs[0].Command, pluginDir+"/server")
	}
}

func TestDiscoverPluginMCPConfigs_InlineManifest(t *testing.T) {
	pluginDir := t.TempDir()
	pluginDir, _ = filepath.EvalSymlinks(pluginDir)
	inline := json.RawMessage(`{"inline-srv": {"command": "inline-cmd"}}`)

	configs, err := discoverPluginMCPConfigs(pluginDir, inline, "myplugin")
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 {
		t.Fatalf("got %d configs, want 1", len(configs))
	}
	if configs[0].Name != "plugin_myplugin_inline-srv" {
		t.Errorf("Name = %q", configs[0].Name)
	}
	if configs[0].Command != "inline-cmd" {
		t.Errorf("Command = %q", configs[0].Command)
	}
}

func TestDiscoverPluginMCPConfigs_NoConfig(t *testing.T) {
	pluginDir := t.TempDir()
	configs, err := discoverPluginMCPConfigs(pluginDir, nil, "empty")
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 0 {
		t.Fatalf("got %d configs, want 0", len(configs))
	}
}

func TestDiscoverPluginMCPConfigs_FilePlusInline(t *testing.T) {
	pluginDir := t.TempDir()
	pluginDir, _ = filepath.EvalSymlinks(pluginDir)
	os.WriteFile(filepath.Join(pluginDir, ".mcp.json"),
		[]byte(`{"mcpServers": {"file-srv": {"command": "from-file"}}}`), 0644)
	inline := json.RawMessage(`{"inline-srv": {"command": "from-inline"}}`)

	configs, err := discoverPluginMCPConfigs(pluginDir, inline, "combo")
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 2 {
		t.Fatalf("got %d configs, want 2", len(configs))
	}
	byName := map[string]mcpconfig.ServerConfig{}
	for _, c := range configs {
		byName[c.Name] = c
	}
	if _, ok := byName["plugin_combo_file-srv"]; !ok {
		t.Error("missing file-srv")
	}
	if _, ok := byName["plugin_combo_inline-srv"]; !ok {
		t.Error("missing inline-srv")
	}
}

func TestDiscoverPluginMCPConfigs_InlineShadowsFile(t *testing.T) {
	pluginDir := t.TempDir()
	pluginDir, _ = filepath.EvalSymlinks(pluginDir)
	os.WriteFile(filepath.Join(pluginDir, ".mcp.json"),
		[]byte(`{"mcpServers": {"srv": {"command": "from-file"}}}`), 0644)
	inline := json.RawMessage(`{"srv": {"command": "from-inline"}}`)

	configs, err := discoverPluginMCPConfigs(pluginDir, inline, "shadow")
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 {
		t.Fatalf("got %d configs, want 1", len(configs))
	}
	// Inline should shadow file (inline is higher priority)
	if configs[0].Command != "from-inline" {
		t.Errorf("Command = %q, want from-inline (inline shadows file)", configs[0].Command)
	}
}

func TestLoadPlugin_WithMCPConfigs(t *testing.T) {
	dir := makePluginDir(t, "mcp-plugin")
	os.WriteFile(filepath.Join(dir, ".mcp.json"),
		[]byte(`{"mcpServers": {"my-mcp": {"command": "mcp-server"}}}`), 0644)

	plugin, err := LoadPlugin(dir)
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}
	if len(plugin.MCPConfigs) != 1 {
		t.Fatalf("got %d MCP configs, want 1", len(plugin.MCPConfigs))
	}
	if plugin.MCPConfigs[0].Name != "plugin_mcp-plugin_my-mcp" {
		t.Errorf("Name = %q", plugin.MCPConfigs[0].Name)
	}
	if plugin.MCPConfigs[0].Command != "mcp-server" {
		t.Errorf("Command = %q", plugin.MCPConfigs[0].Command)
	}
}

func TestLoadPlugin_WithInlineMCPServers(t *testing.T) {
	dir := makePluginDir(t, "inline-mcp")
	// Overwrite manifest with inline mcpServers
	metaDir := filepath.Join(dir, ".claude-plugin")
	manifest := `{"name": "inline-mcp", "mcpServers": {"isrv": {"command": "inline-server"}}}`
	os.WriteFile(filepath.Join(metaDir, "plugin.json"), []byte(manifest), 0644)

	plugin, err := LoadPlugin(dir)
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}
	if len(plugin.MCPConfigs) != 1 {
		t.Fatalf("got %d MCP configs, want 1", len(plugin.MCPConfigs))
	}
	if plugin.MCPConfigs[0].Name != "plugin_inline-mcp_isrv" {
		t.Errorf("Name = %q", plugin.MCPConfigs[0].Name)
	}
}
