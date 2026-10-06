package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A relative source is copied from the marketplace clone, and its sha is the
// id of the tree the clone holds at the plugin's folder.
func TestFetchPluginSource_RelativeCopiesFromMarketplace(t *testing.T) {
	mktRoot := t.TempDir()
	// a plugin living at <mktRoot>/plugins/widget
	writePlugin(t, filepath.Join(mktRoot, "plugins", "widget"), "widget", nil)
	makeGitRepo(t, mktRoot, "README.md", "mkt")
	gitIn(t, mktRoot, "commit", "--allow-empty", "-qm", "elsewhere")
	folderTree := gitIn(t, mktRoot, "rev-parse", "HEAD:plugins/widget")

	dst := filepath.Join(t.TempDir(), "out")
	sha, err := fetchPluginSource(context.Background(),
		Source{Kind: SourceDirectory, Path: "./plugins/widget", Rel: true}, mktRoot, dst)
	if err != nil {
		t.Fatalf("fetchPluginSource: %v", err)
	}
	if sha != folderTree {
		t.Errorf("relative source sha = %q, want the folder's tree %q", sha, folderTree)
	}
	if _, err := os.Stat(filepath.Join(dst, ".claude-plugin", "plugin.json")); err != nil {
		t.Fatalf("plugin.json not copied: %v", err)
	}
}
