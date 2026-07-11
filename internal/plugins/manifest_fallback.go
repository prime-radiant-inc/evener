package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
//   - and dir is a cache directory serf materialized (staged), it writes a
//     synthesized .claude-plugin/plugin.json built from the entry's fields,
//     so every later Load() of dir (this install's own validation, a future
//     session's EnabledPluginDirs(), `serf plugin list`'s Broken check,
//     serf-doctor) finds an ordinary on-disk manifest and needs no
//     special-casing. The entry may declare no components at all — Claude
//     Code installs that shape fine (e.g. private-journal-mcp in
//     superpowers-marketplace: a bare npm MCP-server repo with no plugin
//     scaffolding and a components-free entry), with the plugin
//     contributing only whatever conventional dirs Load() auto-discovers —
//     so the synthesized manifest may carry just the entry's
//     name/description.
//   - and dir is NOT a cache directory (staged=false — a directory-source
//     plugin referenced in place, a local-dev/test convenience), it returns
//     a clear error: serf will not write a generated file into a directory
//     it does not own.
//
// The synthesized manifest goes beyond Claude Code's zero-component parity in
// one way: if the entry declares no mcpServers of its own, detectNPMBinServer
// inspects the source's package.json bin map and, when it unambiguously names
// an existing script, wires it as a stdio MCP server — see that function for
// the rules. The returned note (empty when nothing noteworthy happened) is an
// install-time message for the user explaining why an MCP-server-shaped
// plugin was NOT auto-wired; it is never an error.
func ensureManifestFallback(dir string, staged bool, cp CatalogPlugin) (note string, err error) {
	if hasPluginManifest(dir) {
		return "", nil
	}
	if !staged {
		return "", fmt.Errorf("plugin %q: source has no plugin manifest; serf only synthesizes a fallback manifest into a materialized cache install, not a directory source referenced in place", cp.Name)
	}

	mcpServers := cp.MCPServers
	if len(mcpServers) == 0 {
		mcpServers, note = detectNPMBinServer(dir, cp.Name)
	}

	manifest := agentplugin.Manifest{
		Name:        cp.Name,
		Description: cp.Description,
		Commands:    cp.Commands,
		Agents:      cp.Agents,
		Hooks:       cp.Hooks,
		MCPServers:  mcpServers,
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", fmt.Errorf("synthesizing manifest for plugin %q: %w", cp.Name, err)
	}
	manifestDir := filepath.Join(dir, ".claude-plugin")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		return "", fmt.Errorf("synthesizing manifest for plugin %q: %w", cp.Name, err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), data, 0o644); err != nil {
		return "", fmt.Errorf("synthesizing manifest for plugin %q: %w", cp.Name, err)
	}
	return note, nil
}

// detectNPMBinServer auto-wires the MCP server of a bare npm MCP-server repo
// (e.g. private-journal-mcp: no plugin scaffolding, just a package.json whose
// bin map points at the built server script). It returns an mcpServers JSON
// object to embed in the synthesized manifest, or a note explaining why an
// MCP-shaped repo was skipped, or neither for repos that are not MCP-shaped
// at all (no package.json, no bin — the LSP plugins land here silently).
//
// Wiring rules (deliberately conservative — never guess):
//   - a root .mcp.json is the plugin's own MCP declaration and Load() already
//     honors it; skip silently so the server isn't registered twice.
//   - bin must be the map form with exactly one entry, or the entry whose
//     name equals the plugin name when there are several.
//   - the bin path must stay inside the plugin dir and the file must exist in
//     the cached artifact. A missing target (an unbuilt TypeScript repo whose
//     dist/ only appears after `npm install` runs the prepare script — the
//     real private-journal-mcp cache ships this shape) gets a note, not a
//     broken server: serf never builds at install time.
//
// The wired command is `node ${CLAUDE_PLUGIN_ROOT}/<bin path>`; Load() expands
// the root placeholder to the installed plugin dir.
func detectNPMBinServer(dir, pluginName string) (servers json.RawMessage, note string) {
	if _, err := os.Stat(filepath.Join(dir, ".mcp.json")); err == nil {
		return nil, "" // Load() picks up the plugin's own .mcp.json
	}
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil, "" // not an npm repo
	}
	var pkg struct {
		Bin json.RawMessage `json:"bin"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil || len(pkg.Bin) == 0 {
		return nil, "" // malformed or bin-less package.json: nothing detectable
	}
	var bins map[string]string
	if err := json.Unmarshal(pkg.Bin, &bins); err != nil || len(bins) == 0 {
		return nil, fmt.Sprintf("plugin %q: package.json bin is not a map of names to paths; not wiring an MCP server", pluginName)
	}

	binName, binPath := pluginName, bins[pluginName]
	if binPath == "" {
		if len(bins) != 1 {
			return nil, fmt.Sprintf("plugin %q: package.json declares %d bins and none matches the plugin name; not wiring an MCP server", pluginName, len(bins))
		}
		for n, p := range bins {
			binName, binPath = n, p
		}
	}

	if binName == "" || binPath == "" {
		return nil, fmt.Sprintf("plugin %q: package.json bin entry has an empty name or path; not wiring an MCP server", pluginName)
	}

	rel := filepath.Clean(filepath.FromSlash(binPath))
	if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Sprintf("plugin %q: package.json bin %q points outside the plugin directory; not wiring an MCP server", pluginName, binPath)
	}
	if fi, err := os.Stat(filepath.Join(dir, rel)); err != nil || fi.IsDir() {
		return nil, fmt.Sprintf("plugin %q: package.json bin target %q does not exist in the installed source (unbuilt repo?); not wiring an MCP server", pluginName, binPath)
	}

	entry := map[string]any{
		"command": "node",
		"args":    []string{"${CLAUDE_PLUGIN_ROOT}/" + filepath.ToSlash(rel)},
	}
	out, err := json.Marshal(map[string]any{binName: entry})
	if err != nil {
		return nil, "" // cannot happen for this shape; fail safe by not wiring
	}
	return out, ""
}
