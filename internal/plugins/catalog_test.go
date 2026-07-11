package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestParseCatalog(t *testing.T) {
	root := t.TempDir()
	mj := `{
	  "name":"acme","description":"d","owner":{"name":"o","email":"o@e"},
	  "metadata":{"pluginRoot":"plugins"},
	  "plugins":[
	    {"name":"widget","description":"w","category":"dev","source":"./plugins/widget"},
	    {"name":"gadget","source":{"source":"git-subdir","url":"https://x.git","path":"g"}}
	  ]}`
	os.MkdirAll(filepath.Join(root, ".claude-plugin"), 0o755)
	os.WriteFile(filepath.Join(root, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644)

	cat, err := ParseCatalog(root)
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	if cat.Name != "acme" || len(cat.Plugins) != 2 {
		t.Fatalf("catalog = %+v", cat)
	}
	if cat.Plugins[0].Source.Kind != SourceDirectory || !cat.Plugins[0].Source.Rel {
		t.Errorf("widget source = %+v, want rel directory", cat.Plugins[0].Source)
	}
	if cat.Plugins[1].Source.Kind != SourceGitSubdir {
		t.Errorf("gadget source = %+v, want git-subdir", cat.Plugins[1].Source)
	}
	if len(cat.SkippedPlugins) != 0 {
		t.Errorf("SkippedPlugins = %v, want none", cat.SkippedPlugins)
	}
}

// TestParseCatalog_SkipsUnsupportedSourceAndReportsIt is the fix for design
// spec §7: one plugin entry with an unsupported/unknown source kind (e.g. an
// npm source, a real Claude Code source type serf's Source doesn't implement)
// must not brick the whole marketplace. Before this fix, ParseCatalog did one
// whole-file json.Unmarshal into Catalog, so a single bad Source.UnmarshalJSON
// failed the entire parse and made every OTHER plugin in the marketplace
// uninstallable too. The fix parses each plugin entry independently: a
// plugin whose source fails to decode is omitted from Plugins and its name
// recorded in SkippedPlugins, while its well-formed siblings still parse.
func TestParseCatalog_SkipsUnsupportedSourceAndReportsIt(t *testing.T) {
	root := t.TempDir()
	mj := `{
	  "name":"acme","owner":{"name":"o"},
	  "plugins":[
	    {"name":"bad-npm","description":"unsupported","source":{"source":"npm","package":"whatever"}},
	    {"name":"widget","description":"good","source":"./plugins/widget"}
	  ]}`
	os.MkdirAll(filepath.Join(root, ".claude-plugin"), 0o755)
	os.WriteFile(filepath.Join(root, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644)

	cat, err := ParseCatalog(root)
	if err != nil {
		t.Fatalf("ParseCatalog: %v, want the npm-source plugin skipped rather than the whole parse failing", err)
	}
	if len(cat.Plugins) != 1 || cat.Plugins[0].Name != "widget" {
		t.Fatalf("Plugins = %+v, want only widget", cat.Plugins)
	}
	if len(cat.SkippedPlugins) != 1 || cat.SkippedPlugins[0] != "bad-npm" {
		t.Fatalf("SkippedPlugins = %v, want [bad-npm]", cat.SkippedPlugins)
	}
}

// TestBrowse_SkipsUnsupportedSourceAndReturnsTheRest proves the fix reaches
// Manager.Browse (used by both the CLI's new `marketplace browse` and the
// hub's serf/marketplace/browse RPC): a marketplace with one unsupported-source
// plugin must still be browsable, returning every other plugin.
func TestBrowse_SkipsUnsupportedSourceAndReturnsTheRest(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755)
	mj := `{"name":"acme","owner":{"name":"o"},"plugins":[
	  {"name":"bad-npm","source":{"source":"npm","package":"x"}},
	  {"name":"widget","source":"./plugins/widget"}
	]}`
	os.WriteFile(filepath.Join(dir, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644)
	writePlugin(t, filepath.Join(dir, "plugins", "widget"), "widget", nil)

	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceDirectory, Path: dir}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	cat, err := m.Browse(context.Background(), "acme")
	if err != nil {
		t.Fatalf("Browse: %v, want the npm plugin skipped rather than Browse failing", err)
	}
	if len(cat.Plugins) != 1 || cat.Plugins[0].Name != "widget" {
		t.Fatalf("Plugins = %+v, want only widget", cat.Plugins)
	}
	if len(cat.SkippedPlugins) != 1 || cat.SkippedPlugins[0] != "bad-npm" {
		t.Fatalf("SkippedPlugins = %v, want [bad-npm]", cat.SkippedPlugins)
	}
}

func TestCatalogPlugin_ParsesMarketplaceEntryManifestFields(t *testing.T) {
	root := t.TempDir()
	mj := `{
	  "name":"acme","owner":{"name":"o"},
	  "plugins":[
	    {
	      "name":"private-journal-mcp",
	      "description":"Journal MCP server",
	      "source":{"source":"url","url":"https://example.com/x.git"},
	      "strict": false,
	      "mcpServers": {
	        "private-journal": {"command":"npx","args":["-y","private-journal-mcp"]}
	      },
	      "commands": ["./commands/"],
	      "agents": ["./agents/reviewer.md"],
	      "hooks": {"PostToolUse":[{"matcher":"Write","hooks":[{"type":"command","command":"echo hi"}]}]},
	      "skills": ["./extra-skills/"]
	    }
	  ]}`
	os.MkdirAll(filepath.Join(root, ".claude-plugin"), 0o755)
	os.WriteFile(filepath.Join(root, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644)

	cat, err := ParseCatalog(root)
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	if len(cat.Plugins) != 1 {
		t.Fatalf("Plugins = %+v, want 1", cat.Plugins)
	}
	p := cat.Plugins[0]
	if p.Strict == nil || *p.Strict != false {
		t.Errorf("Strict = %v, want a pointer to false", p.Strict)
	}
	var mcp map[string]json.RawMessage
	if err := json.Unmarshal(p.MCPServers, &mcp); err != nil || len(mcp) != 1 {
		t.Fatalf("MCPServers = %s, err %v", p.MCPServers, err)
	}
	if _, ok := mcp["private-journal"]; !ok {
		t.Errorf("MCPServers missing private-journal entry: %s", p.MCPServers)
	}
	var commands []string
	if err := json.Unmarshal(p.Commands, &commands); err != nil || len(commands) != 1 {
		t.Fatalf("Commands = %s, err %v", p.Commands, err)
	}
	var agents []string
	if err := json.Unmarshal(p.Agents, &agents); err != nil || len(agents) != 1 {
		t.Fatalf("Agents = %s, err %v", p.Agents, err)
	}
	if len(p.Hooks) == 0 {
		t.Errorf("Hooks not captured")
	}
	if len(p.Skills) == 0 {
		t.Errorf("Skills not captured")
	}
}

// TestCatalogPlugin_ManifestFieldsOmittedWhenAbsent guards an ordinary plugin
// entry (the common case, a plugin with its own plugin.json): none of the
// new fallback fields should be populated just because the struct has them.
func TestCatalogPlugin_ManifestFieldsOmittedWhenAbsent(t *testing.T) {
	root := t.TempDir()
	mj := `{"name":"acme","owner":{"name":"o"},"plugins":[{"name":"widget","source":"./plugins/widget"}]}`
	os.MkdirAll(filepath.Join(root, ".claude-plugin"), 0o755)
	os.WriteFile(filepath.Join(root, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644)

	cat, err := ParseCatalog(root)
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	p := cat.Plugins[0]
	if p.Strict != nil || p.Commands != nil || p.Agents != nil || p.Hooks != nil || p.MCPServers != nil || p.Skills != nil {
		t.Errorf("expected all manifest-fallback fields nil/absent, got %+v", p)
	}
}
