package plugins

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	agentplugin "primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/internal/bundled"
)

// InspectTarget names one plugin to load for display: an installed
// plugin/marketplace pair, or a directory path — exactly one of the two.
type InspectTarget struct {
	Plugin      string
	Marketplace string
	Path        string
}

// Inspect loads the one plugin target names — the registry entry's
// InstallPath for an installed pair, target.Path otherwise — and returns
// plugin.Load's result. An unpublished bundled preview path is loaded through
// private staging without publication. It performs no fail-soft: mapping any
// error to a display-level error is the hub handler's job.
func (m *Manager) Inspect(ctx context.Context, target InspectTarget) (agentplugin.Instance, error) {
	if target.Path != "" {
		if name := m.unpublishedBundledName(target.Path); name != "" {
			return m.inspectBundledPreview(ctx, name)
		}
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

// Only a missing current build's content-addressed bundled destination can
// name an unpublished preview. Existing broken directories, installed cache
// paths and missing revisions from other builds must keep their load errors.
func (m *Manager) unpublishedBundledName(path string) string {
	store, err := m.storePath(bundledDirName)
	if err != nil || filepath.Dir(filepath.Clean(path)) != store {
		return ""
	}
	name, digest, ok := splitBundledDigestName(filepath.Base(path))
	if !ok {
		return ""
	}
	current, err := bundledPluginDigest(name)
	if err != nil || current != digest {
		return ""
	}
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	return name
}

func (m *Manager) inspectBundledPreview(ctx context.Context, name string) (instance agentplugin.Instance, err error) {
	dest, staging, _, err := m.prepareBundledStore(ctx, name, previewBundledStore)
	if err != nil {
		return agentplugin.Instance{}, err
	}
	if staging == nil {
		return pluginLoad(dest)
	}
	defer staging.release()
	defer func() { err = errors.Join(err, removeBundledPreview(staging.dir)) }()
	if err := copyBundledPayload(staging.payload, mustSubFS(bundled.Plugins(), name)); err != nil {
		return agentplugin.Instance{}, fmt.Errorf("stage bundled plugin %s for inspection: %w", name, err)
	}
	return pluginLoad(staging.payload)
}
