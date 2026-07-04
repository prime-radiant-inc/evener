package plugins

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func registryKey(plugin, marketplace string) string { return plugin + "@" + marketplace }

// catalogPlugin finds a named plugin's entry + its marketplace ref.
func (m *Manager) catalogPlugin(marketplace, plugin string) (MarketplaceRef, CatalogPlugin, error) {
	mk, err := m.loadMarketplaces()
	if err != nil {
		return MarketplaceRef{}, CatalogPlugin{}, err
	}
	ref, ok := mk[marketplace]
	if !ok {
		return MarketplaceRef{}, CatalogPlugin{}, fmt.Errorf("marketplace %q not found", marketplace)
	}
	cat, err := ParseCatalog(m.catalogRoot(ref))
	if err != nil {
		return MarketplaceRef{}, CatalogPlugin{}, err
	}
	for _, p := range cat.Plugins {
		if p.Name == plugin {
			return ref, p, nil
		}
	}
	return MarketplaceRef{}, CatalogPlugin{}, fmt.Errorf("plugin %q not found in marketplace %q", plugin, marketplace)
}

// materialize fetches a plugin's source into the cache (or references it in
// place for a directory marketplace) and returns its dir + resolved sha.
func (m *Manager) materialize(ctx context.Context, marketplace, plugin string, ref MarketplaceRef, cp CatalogPlugin) (dir, sha string, err error) {
	if ref.Source.Kind == SourceDirectory {
		// referenced in place: resolve relative to the marketplace root, no copy.
		root := ref.InstallLocation
		if cp.Source.Rel {
			return filepath.Join(root, cp.Source.Path), "", nil
		}
		if cp.Source.Kind == SourceDirectory {
			return cp.Source.Path, "", nil
		}
	}
	// staging → sha → move to cache/<mkt>/<plugin>/<sha>/
	staging := m.pluginCacheDir(marketplace, plugin, ".staging")
	os.RemoveAll(staging)
	sha, err = fetchPluginSource(ctx, cp.Source, m.catalogRoot(ref), staging)
	if err != nil {
		os.RemoveAll(staging)
		return "", "", err
	}
	key := sha
	if key == "" {
		key = "unknown"
	}
	final := m.pluginCacheDir(marketplace, plugin, key)
	os.RemoveAll(final)
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		os.RemoveAll(staging)
		return "", "", err
	}
	if err := os.Rename(staging, final); err != nil {
		os.RemoveAll(staging)
		return "", "", err
	}
	return final, sha, nil
}

// Install installs plugin from marketplace, enabled.
func (m *Manager) Install(ctx context.Context, plugin, marketplace string) (InstallEntry, error) {
	release, err := acquireLock(m.lockPath(), 30*time.Second)
	if err != nil {
		return InstallEntry{}, err
	}
	defer release()

	ref, cp, err := m.catalogPlugin(marketplace, plugin)
	if err != nil {
		return InstallEntry{}, err
	}
	dir, sha, err := m.materialize(ctx, marketplace, plugin, ref, cp)
	if err != nil {
		return InstallEntry{}, err
	}
	if err := validatePluginDir(dir); err != nil {
		if strings.HasPrefix(dir, m.cacheDir()) {
			os.RemoveAll(dir)
		}
		return InstallEntry{}, fmt.Errorf("installed plugin failed validation: %w", err)
	}

	now := m.now().UTC()
	entry := InstallEntry{
		InstallPath:  dir,
		Version:      computeVersion(pluginManifestVersion(dir), cp.Source.Ref, sha),
		GitCommitSha: sha,
		InstalledAt:  now,
		LastUpdated:  now,
		Enabled:      true,
		AutoUpgrade:  false,
		Source:       cp.Source,
	}
	reg, err := LoadRegistry(m.registryPath())
	if err != nil {
		return InstallEntry{}, err
	}
	reg.Plugins[registryKey(plugin, marketplace)] = []InstallEntry{entry}
	if err := SaveRegistry(m.registryPath(), reg); err != nil {
		return InstallEntry{}, err
	}
	return entry, nil
}
