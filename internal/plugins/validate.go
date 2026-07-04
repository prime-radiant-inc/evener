package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"

	agentplugin "primeradiant.com/serf/agent/plugin"
)

// validatePluginDir returns nil iff the plugin at dir loads cleanly — manifest
// plus every component (agents, hooks, mcp, skills) parses. It reuses the exact
// loader the session uses, so "installs" and "loads" agree.
func validatePluginDir(dir string) error {
	_, err := agentplugin.Load(dir)
	return err
}

// pluginManifestVersion returns the plugin.json "version" (best effort; "" on
// any error or absence), for computeVersion.
func pluginManifestVersion(dir string) string {
	for _, mf := range []string{".claude-plugin", ".codex-plugin"} {
		data, err := os.ReadFile(filepath.Join(dir, mf, "plugin.json"))
		if err != nil {
			continue
		}
		var m struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(data, &m) == nil {
			return m.Version
		}
	}
	return ""
}
