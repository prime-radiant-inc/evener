package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A relative source is copied from the marketplace clone, and its sha is the
// clone's last commit touching the plugin's folder.
func TestFetchPluginSource_RelativeCopiesFromMarketplace(t *testing.T) {
	mktRoot := t.TempDir()
	// a plugin living at <mktRoot>/plugins/widget
	writePlugin(t, filepath.Join(mktRoot, "plugins", "widget"), "widget", nil)
	folderCommit := makeGitRepo(t, mktRoot, "README.md", "mkt")
	gitIn(t, mktRoot, "commit", "--allow-empty", "-qm", "elsewhere")

	dst := filepath.Join(t.TempDir(), "out")
	sha, err := fetchPluginSource(context.Background(),
		Source{Kind: SourceDirectory, Path: "./plugins/widget", Rel: true}, mktRoot, dst)
	if err != nil {
		t.Fatalf("fetchPluginSource: %v", err)
	}
	if sha != folderCommit {
		t.Errorf("relative source sha = %q, want the folder's last commit %q", sha, folderCommit)
	}
	if _, err := os.Stat(filepath.Join(dst, ".claude-plugin", "plugin.json")); err != nil {
		t.Fatalf("plugin.json not copied: %v", err)
	}
}
