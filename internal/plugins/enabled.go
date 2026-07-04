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
	seen := map[string]bool{} // plugin name → already chosen
	var out []string

	add := func(dir string, fromRegistry bool) {
		inst, err := agentplugin.Load(dir)
		if err != nil {
			if fromRegistry {
				fmt.Fprintf(m.stderr(), "warning: skipping broken plugin %s: %v\n", dir, err)
			} else {
				fmt.Fprintf(m.stderr(), "warning: skipping invalid --plugin-dir %s: %v\n", dir, err)
			}
			return
		}
		name := inst.Manifest.Name
		if seen[name] {
			if fromRegistry {
				return // explicit already won; silently drop the registry dup
			}
			fmt.Fprintf(m.stderr(), "warning: duplicate plugin name %q at %s; keeping the first\n", name, dir)
			return
		}
		seen[name] = true
		out = append(out, dir)
	}

	for _, d := range explicit {
		add(d, false)
	}

	// Deterministic order across the enabled registry entries.
	for _, item := range mustList(m) {
		if item.Enabled && !item.Broken {
			add(item.InstallPath, true)
		}
	}
	return out
}

// mustList returns m.List() results or nil on error (already warned by caller).
func mustList(m *Manager) []ListItem {
	items, err := m.List()
	if err != nil {
		fmt.Fprintf(m.stderr(), "warning: listing plugins: %v\n", err)
		return nil
	}
	return items
}
