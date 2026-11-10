package plugins

import (
	"context"
	"fmt"

	agentplugin "primeradiant.com/evener/agent/plugin"
)

// InspectTarget names one plugin to load for display: an installed
// plugin/marketplace pair, or a directory path — exactly one of the two.
type InspectTarget struct {
	Plugin      string
	Marketplace string
	Path        string
}

// Inspect loads the one plugin target names — the registry entry's
// InstallPath for an installed pair, target.Path directly otherwise — and
// returns plugin.Load's result. It performs no fail-soft: mapping any error to
// a display-level error is the hub handler's job.
func (m *Manager) Inspect(ctx context.Context, target InspectTarget) (agentplugin.Instance, error) {
	if target.Path != "" {
		return pluginLoad(target.Path)
	}
	if _, err := m.loadMigratedMarketplaces(ctx, installAcquireLock); err != nil {
		return agentplugin.Instance{}, err
	}
	registry, err := m.loadRegistry()
	if err != nil {
		return agentplugin.Instance{}, err
	}
	key := registryKey(target.Plugin, target.Marketplace)
	if entries := registry.Plugins[key]; len(entries) > 0 {
		return pluginLoad(entries[0].InstallPath)
	}
	return agentplugin.Instance{}, fmt.Errorf("%s: %w", key, ErrNotInstalled)
}
