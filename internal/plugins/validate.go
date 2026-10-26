package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	agentplugin "primeradiant.com/evener/agent/plugin"
)

// validatePluginDir returns nil iff the plugin at dir loads cleanly — manifest
// plus every component (agents, commands, hooks, mcp, skills) parses. It reuses
// the exact loader the session uses, so "installs" and "loads" agree. A session
// runs a plugin around a component file it skips, but installing or upgrading
// to a revision with one is refused, which keeps the working revision in place.
func validatePluginDir(dir string) error {
	instance, err := agentplugin.Load(dir)
	if err != nil {
		return err
	}
	if len(instance.SkippedComponents) > 0 {
		skipped := instance.SkippedComponents[0]
		return fmt.Errorf("%s file %q: %s", skipped.Kind, filepath.Base(skipped.File), skipped.Reason)
	}
	return nil
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
