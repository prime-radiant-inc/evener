package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	agentplugin "primeradiant.com/serf/agent/plugin"
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

// ensureManifestFallback makes a manifest-less plugin installable by
// honoring its marketplace entry as a fallback manifest — the fix for
// serf's real root cause (a bare MCP/npm-package plugin with no
// .claude-plugin/plugin.json, e.g. private-journal-mcp in
// superpowers-marketplace, had its entry's manifest-shaped fields silently
// dropped and could not install). See the plan's Global Constraints for why
// this triggers on manifest-ABSENCE alone rather than gating on the entry's
// Strict field.
//
// If dir already has a plugin.json (either flavor), this is a no-op — an
// existing manifest is always authoritative; the entry's fields are only
// ever a fallback, never a merge (strict:false's
// entry-supplements/conflicts-with-an-existing-plugin.json behavior is out
// of scope for v1).
//
// If dir has no plugin.json:
//   - and the entry declares no usable component (cp.HasManifestFields()),
//     this returns a clear, honest, plugin-named error — no misleading
//     .codex-plugin path, no Load() call at all.
//   - and the entry does declare components, and dir is a cache directory
//     serf materialized (staged), it writes a synthesized
//     .claude-plugin/plugin.json built from the entry's fields, so every
//     later Load() of dir (this install's own validation, a future
//     session's EnabledPluginDirs(), `serf plugin list`'s Broken check,
//     serf-doctor) finds an ordinary on-disk manifest and needs no
//     special-casing.
//   - and the entry declares components but dir is NOT a cache directory
//     (staged=false — a directory-source plugin referenced in place, a
//     local-dev/test convenience), it returns a distinct clear error: serf
//     will not write a generated file into a directory it does not own.
func ensureManifestFallback(dir string, staged bool, cp CatalogPlugin) error {
	if hasPluginManifest(dir) {
		return nil
	}
	if !cp.HasManifestFields() {
		return fmt.Errorf("plugin %q: source has no plugin manifest and the marketplace entry declares no components (commands/agents/hooks/mcpServers)", cp.Name)
	}
	if !staged {
		return fmt.Errorf("plugin %q: source has no plugin manifest; the marketplace entry declares components, but serf only synthesizes a fallback manifest into a materialized cache install, not a directory source referenced in place", cp.Name)
	}

	manifest := agentplugin.Manifest{
		Name:        cp.Name,
		Description: cp.Description,
		Commands:    cp.Commands,
		Agents:      cp.Agents,
		Hooks:       cp.Hooks,
		MCPServers:  cp.MCPServers,
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("synthesizing manifest for plugin %q: %w", cp.Name, err)
	}
	manifestDir := filepath.Join(dir, ".claude-plugin")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		return fmt.Errorf("synthesizing manifest for plugin %q: %w", cp.Name, err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), data, 0o644); err != nil {
		return fmt.Errorf("synthesizing manifest for plugin %q: %w", cp.Name, err)
	}
	return nil
}
