package plugins

import (
	"os"
	"path/filepath"
)

// hasPluginManifest reports whether dir already has a plugin.json under
// either recognized manifest directory — the same two paths
// agent/plugin.Load tries (.claude-plugin/ first, .codex-plugin/ as
// fallback). Mirrors pluginManifestVersion's directory list (validate.go) so
// this stays in lock-step with Load's own fallback order.
func hasPluginManifest(dir string) bool {
	for _, mf := range []string{".claude-plugin", ".codex-plugin"} {
		if _, err := os.Stat(filepath.Join(dir, mf, "plugin.json")); err == nil {
			return true
		}
	}
	return false
}
