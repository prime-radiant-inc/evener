package plugins

import (
	"os"
	"path/filepath"
	"testing"
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
