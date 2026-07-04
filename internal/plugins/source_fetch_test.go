package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFetchPluginSource_RelativeCopiesFromMarketplace(t *testing.T) {
	mktRoot := t.TempDir()
	// a plugin living at <mktRoot>/plugins/widget
	writePlugin(t, filepath.Join(mktRoot, "plugins", "widget"), "widget", nil)

	dst := filepath.Join(t.TempDir(), "out")
	sha, err := fetchPluginSource(context.Background(),
		Source{Kind: SourceDirectory, Path: "./plugins/widget", Rel: true}, mktRoot, dst)
	if err != nil {
		t.Fatalf("fetchPluginSource: %v", err)
	}
	if sha != "" {
		t.Errorf("relative source sha = %q, want empty", sha)
	}
	if _, err := os.Stat(filepath.Join(dst, ".claude-plugin", "plugin.json")); err != nil {
		t.Fatalf("plugin.json not copied: %v", err)
	}
}
