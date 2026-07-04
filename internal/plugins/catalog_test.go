package plugins

import (
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
}
