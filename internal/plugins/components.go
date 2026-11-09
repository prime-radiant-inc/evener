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
		return agentplugin.Load(target.Path)
	}
	items, err := m.List(ctx)
	if err != nil {
		return agentplugin.Instance{}, err
	}
	for _, item := range items {
		if item.Plugin == target.Plugin && item.Marketplace == target.Marketplace {
			return agentplugin.Load(item.InstallPath)
		}
	}
	return agentplugin.Instance{}, fmt.Errorf("%s: %w", registryKey(target.Plugin, target.Marketplace), ErrNotInstalled)
}
