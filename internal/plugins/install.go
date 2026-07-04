package plugins

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// stagePlugin resolves a plugin's source. For a directory-marketplace it returns
// the in-place dir (staged=false, empty sha, no copy). Otherwise it fetches into
// a staging dir (staged=true) and returns that dir + the resolved sha; the caller
// must either commitStaged it or remove it.
func (m *Manager) stagePlugin(ctx context.Context, marketplace, plugin string, ref MarketplaceRef, cp CatalogPlugin) (dir, sha string, staged bool, err error) {
	if ref.Source.Kind == SourceDirectory {
		root := ref.InstallLocation
		if cp.Source.Rel {
			return filepath.Join(root, cp.Source.Path), "", false, nil
		}
		if cp.Source.Kind == SourceDirectory {
			return cp.Source.Path, "", false, nil
		}
	}
	staging := m.pluginCacheDir(marketplace, plugin, ".staging")
	os.RemoveAll(staging)
	sha, err = fetchPluginSource(ctx, cp.Source, m.catalogRoot(ref), staging)
	if err != nil {
		os.RemoveAll(staging)
		return "", "", false, err
	}
	return staging, sha, true, nil
}

// commitStaged moves a staging dir into its final sha-keyed cache location and
// returns that path, replacing any existing dir at that sha.
func (m *Manager) commitStaged(marketplace, plugin, staging, sha string) (string, error) {
	key := sha
	if key == "" {
		key = "unknown"
	}
	final := m.pluginCacheDir(marketplace, plugin, key)
	os.RemoveAll(final)
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		os.RemoveAll(staging)
		return "", err
	}
	if err := os.Rename(staging, final); err != nil {
		os.RemoveAll(staging)
		return "", err
	}
	return final, nil
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
	dir, sha, staged, err := m.stagePlugin(ctx, marketplace, plugin, ref, cp)
	if err != nil {
		return InstallEntry{}, err
	}
	if staged {
		final, cerr := m.commitStaged(marketplace, plugin, dir, sha)
		if cerr != nil {
			return InstallEntry{}, cerr
		}
		dir = final
	}
	if err := validatePluginDir(dir); err != nil {
		if strings.HasPrefix(dir, m.cacheDir()+string(os.PathSeparator)) {
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

// Upgrade re-resolves plugin from its marketplace. If the sha changed it
// materializes a NEW sha-dir, validates, and repoints the registry — never
// deleting the old dir (the gc sweep reclaims it) so live sessions keep working.
// If the sha is unchanged (or the source is an in-place directory) it is a true
// no-op: the staging clone is discarded and the live dir is never touched.
func (m *Manager) Upgrade(ctx context.Context, plugin, marketplace string) (InstallEntry, error) {
	release, err := acquireLock(m.lockPath(), 30*time.Second)
	if err != nil {
		return InstallEntry{}, err
	}
	defer release()

	key := registryKey(plugin, marketplace)
	reg, err := LoadRegistry(m.registryPath())
	if err != nil {
		return InstallEntry{}, err
	}
	entries, ok := reg.Plugins[key]
	if !ok || len(entries) == 0 {
		return InstallEntry{}, fmt.Errorf("%s is not installed", key)
	}
	prev := entries[0]

	ref, cp, err := m.catalogPlugin(marketplace, plugin)
	if err != nil {
		return InstallEntry{}, err
	}
	staging, sha, staged, err := m.stagePlugin(ctx, marketplace, plugin, ref, cp)
	if err != nil {
		return InstallEntry{}, err
	}
	if !staged || sha == prev.GitCommitSha {
		if staged {
			os.RemoveAll(staging)
		}
		return prev, nil
	}

	final, err := m.commitStaged(marketplace, plugin, staging, sha)
	if err != nil {
		return InstallEntry{}, err
	}
	if err := validatePluginDir(final); err != nil {
		os.RemoveAll(final)
		return InstallEntry{}, fmt.Errorf("upgraded plugin failed validation: %w", err)
	}

	prev.InstallPath = final
	prev.GitCommitSha = sha
	prev.Version = computeVersion(pluginManifestVersion(final), cp.Source.Ref, sha)
	prev.LastUpdated = m.now().UTC()
	prev.Source = cp.Source
	reg.Plugins[key] = []InstallEntry{prev}
	if err := SaveRegistry(m.registryPath(), reg); err != nil {
		return InstallEntry{}, err
	}
	return prev, nil
}

func (m *Manager) mutateEntry(plugin, marketplace string, fn func(*InstallEntry)) error {
	release, err := acquireLock(m.lockPath(), 30*time.Second)
	if err != nil {
		return err
	}
	defer release()
	key := registryKey(plugin, marketplace)
	reg, err := LoadRegistry(m.registryPath())
	if err != nil {
		return err
	}
	entries, ok := reg.Plugins[key]
	if !ok || len(entries) == 0 {
		return fmt.Errorf("%s is not installed", key)
	}
	e := entries[0]
	fn(&e)
	reg.Plugins[key] = []InstallEntry{e}
	return SaveRegistry(m.registryPath(), reg)
}

func (m *Manager) SetEnabled(plugin, marketplace string, enabled bool) error {
	return m.mutateEntry(plugin, marketplace, func(e *InstallEntry) { e.Enabled = enabled })
}

func (m *Manager) SetAutoUpgrade(plugin, marketplace string, on bool) error {
	return m.mutateEntry(plugin, marketplace, func(e *InstallEntry) { e.AutoUpgrade = on })
}

// Remove deletes the registry entry and its cache dir. A plugin referenced in
// place (directory-source marketplace) leaves the source untouched.
func (m *Manager) Remove(plugin, marketplace string) error {
	release, err := acquireLock(m.lockPath(), 30*time.Second)
	if err != nil {
		return err
	}
	defer release()
	key := registryKey(plugin, marketplace)
	reg, err := LoadRegistry(m.registryPath())
	if err != nil {
		return err
	}
	entries, ok := reg.Plugins[key]
	if !ok {
		return fmt.Errorf("%s is not installed", key)
	}
	if len(entries) > 0 {
		p := entries[0].InstallPath
		if strings.HasPrefix(p, m.cacheDir()+string(os.PathSeparator)) {
			os.RemoveAll(p)
		}
	}
	delete(reg.Plugins, key)
	return SaveRegistry(m.registryPath(), reg)
}

type ListItem struct {
	Plugin       string
	Marketplace  string
	Version      string
	Enabled      bool
	AutoUpgrade  bool
	Broken       bool
	InstallPath  string
	GitCommitSha string
	InstalledAt  time.Time
	LastUpdated  time.Time
}

func splitKey(key string) (plugin, marketplace string) {
	if i := strings.LastIndex(key, "@"); i >= 0 {
		return key[:i], key[i+1:]
	}
	return key, ""
}

func (m *Manager) List() ([]ListItem, error) {
	reg, err := LoadRegistry(m.registryPath())
	if err != nil {
		return nil, err
	}
	var out []ListItem
	for key, entries := range reg.Plugins {
		if len(entries) == 0 {
			continue
		}
		e := entries[0]
		plugin, marketplace := splitKey(key)
		out = append(out, ListItem{
			Plugin:       plugin,
			Marketplace:  marketplace,
			Version:      e.Version,
			Enabled:      e.Enabled,
			AutoUpgrade:  e.AutoUpgrade,
			Broken:       validatePluginDir(e.InstallPath) != nil,
			InstallPath:  e.InstallPath,
			GitCommitSha: e.GitCommitSha,
			InstalledAt:  e.InstalledAt,
			LastUpdated:  e.LastUpdated,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Plugin != out[j].Plugin {
			return out[i].Plugin < out[j].Plugin
		}
		return out[i].Marketplace < out[j].Marketplace
	})
	return out, nil
}

// UpdateAll upgrades every installed, git-backed plugin (directory/relative
// sources are inherently current and skipped). Failures are collected but do
// not stop the others.
func (m *Manager) UpdateAll(ctx context.Context) ([]InstallEntry, error) {
	reg, err := LoadRegistry(m.registryPath())
	if err != nil {
		return nil, err
	}
	var keys []string
	for key := range reg.Plugins {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var updated []InstallEntry
	var errs []string
	for _, key := range keys {
		entries := reg.Plugins[key]
		if len(entries) == 0 {
			continue
		}
		e := entries[0]
		if e.Source.Rel || e.Source.Kind == SourceDirectory {
			continue
		}
		plugin, marketplace := splitKey(key)
		entry, err := m.Upgrade(ctx, plugin, marketplace)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", key, err))
			continue
		}
		updated = append(updated, entry)
	}
	if len(errs) > 0 {
		return updated, fmt.Errorf("some upgrades failed:\n%s", strings.Join(errs, "\n"))
	}
	return updated, nil
}
