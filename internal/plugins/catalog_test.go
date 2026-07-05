package plugins

import (
	"context"
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
