package plugins

import (
	"fmt"

	agentplugin "primeradiant.com/serf/agent/plugin"
)

// EnabledPluginDirs returns the plugin directories a session should load:
// the explicit --plugin-dir values first, then every installed+enabled+valid
// registry entry. Each dir is dry-run validated (agent/plugin.Load), and the
// set is deduped by plugin Manifest.Name — an explicit dir wins over a registry
// entry with the same plugin name — so the fail-hard LoadAll never sees a broken
// or duplicate-named plugin. Dropped dirs are warned to m.stderr().
func (m *Manager) EnabledPluginDirs(explicit []string) []string {
	seen := map[string]bool{} // plugin name → true if the chosen dir was explicit
	var out []string

	add := func(dir string, fromRegistry bool) {
		inst, err := agentplugin.Load(dir)
		if err != nil {
			if fromRegistry {
				_, _ = fmt.Fprintf(m.stderr(), "warning: skipping broken plugin %s: %v\n", dir, err)
			} else {
				_, _ = fmt.Fprintf(m.stderr(), "warning: skipping invalid --plugin-dir %s: %v\n", dir, err)
			}
			return
		}
		name := inst.Manifest.Name
		if wasExplicit, exists := seen[name]; exists {
			// A registry entry losing to an explicit incumbent is expected — stay silent.
			// Any other collision (explicit-vs-explicit, registry-vs-registry) is a real
			// duplicate the user should hear about.
			if !wasExplicit || !fromRegistry {
				_, _ = fmt.Fprintf(m.stderr(), "warning: duplicate plugin name %q at %s; keeping the first\n", name, dir)
			}
			return
		}
		seen[name] = !fromRegistry
		out = append(out, dir)
	}

	for _, d := range explicit {
		add(d, false)
	}

	// Deterministic order across the enabled registry entries.
	for _, item := range listOrWarn(m) {
		if item.Enabled && !item.Broken {
			add(item.InstallPath, true)
		}
	}
	return out
}

// listOrWarn returns m.List() results or nil on error (already warned by caller).
func listOrWarn(m *Manager) []ListItem {
	items, err := m.List()
	if err != nil {
		_, _ = fmt.Fprintf(m.stderr(), "warning: listing plugins: %v\n", err)
		return nil
	}
	return items
}
