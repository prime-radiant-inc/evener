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

// catalogPlugin finds a named plugin's entry + its marketplace ref, lazily
// fetching a seeded-but-unfetched marketplace first. Callers (Install/Upgrade)
// already hold m.lockPath(), which ensureFetched requires.
func (m *Manager) catalogPlugin(ctx context.Context, marketplace, plugin string) (MarketplaceRef, CatalogPlugin, error) {
	ref, err := m.ensureFetched(ctx, marketplace)
	if err != nil {
		return MarketplaceRef{}, CatalogPlugin{}, err
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
	return MarketplaceRef{}, CatalogPlugin{}, fmt.Errorf("plugin %q in marketplace %q: %w", plugin, marketplace, ErrPluginNotFound)
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
	_ = os.RemoveAll(staging)
	sha, err = fetchPluginSource(ctx, cp.Source, m.catalogRoot(ref), staging)
	if err != nil {
		_ = os.RemoveAll(staging)
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
	_ = os.RemoveAll(final)
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		_ = os.RemoveAll(staging)
		return "", err
	}
	if err := os.Rename(staging, final); err != nil {
		_ = os.RemoveAll(staging)
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

	if err := validNameComponent("marketplace", marketplace); err != nil {
		return InstallEntry{}, err
	}
	if err := validNameComponent("plugin", plugin); err != nil {
		return InstallEntry{}, err
	}

	ref, cp, err := m.catalogPlugin(ctx, marketplace, plugin)
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
			_ = os.RemoveAll(dir)
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
//
// Upgrade always upgrades once called, regardless of the plugin's AutoUpgrade
// flag: it is the explicit-consent path (`serf plugin upgrade`, UpdateAll).
// The auto-upgrade daemon uses upgradeAuto instead, which re-checks the flag
// under the same lock immediately before acting — see upgradeLocked.
func (m *Manager) Upgrade(ctx context.Context, plugin, marketplace string) (InstallEntry, error) {
	release, err := acquireLock(m.lockPath(), 30*time.Second)
	if err != nil {
		return InstallEntry{}, err
	}
	defer release()
	entry, _, _, err := m.upgradeLocked(ctx, plugin, marketplace, false)
	return entry, err
}

// upgradeLocked contains the actual mechanics of Upgrade. The caller MUST
// already hold m.lockPath() for the duration of this call (Upgrade and
// upgradeAuto both acquire it before calling in).
//
// If requireAutoUpgrade is true, the plugin's CURRENT AutoUpgrade flag and
// git-backed-ness are read fresh from the registry — under the lock the
// caller is holding, immediately before any fetch — and the upgrade is
// skipped (skipped=true, no error) if the plugin is no longer eligible. This
// is what lets the auto-upgrade daemon honor a SetAutoUpgrade(false) (or a
// switch to a directory/relative source) that lands after a sweep started
// but before it reached this plugin's turn; requireAutoUpgrade=false (the
// explicit-consent path) never skips on the flag.
//
// changed reports whether the sha actually moved, computed entirely from this
// call's own fresh prev/after comparison — never from a caller-supplied
// snapshot — so callers can trust it even when another sweep or an explicit
// upgrade races this one.
func (m *Manager) upgradeLocked(ctx context.Context, plugin, marketplace string, requireAutoUpgrade bool) (entry InstallEntry, changed, skipped bool, err error) {
	if err := validNameComponent("marketplace", marketplace); err != nil {
		return InstallEntry{}, false, false, err
	}
	if err := validNameComponent("plugin", plugin); err != nil {
		return InstallEntry{}, false, false, err
	}

	key := registryKey(plugin, marketplace)
	reg, err := LoadRegistry(m.registryPath())
	if err != nil {
		return InstallEntry{}, false, false, err
	}
	entries, ok := reg.Plugins[key]
	if !ok || len(entries) == 0 {
		return InstallEntry{}, false, false, fmt.Errorf("%s: %w", key, ErrNotInstalled)
	}
	prev := entries[0]

	if requireAutoUpgrade && (!prev.AutoUpgrade || prev.Source.Rel || prev.Source.Kind == SourceDirectory) {
		return prev, false, true, nil
	}

	ref, cp, err := m.catalogPlugin(ctx, marketplace, plugin)
	if err != nil {
		return InstallEntry{}, false, false, err
	}
	staging, sha, staged, err := m.stagePlugin(ctx, marketplace, plugin, ref, cp)
	if err != nil {
		return InstallEntry{}, false, false, err
	}
	if !staged || sha == prev.GitCommitSha {
		if staged {
			_ = os.RemoveAll(staging)
		}
		return prev, false, false, nil
	}

	final, err := m.commitStaged(marketplace, plugin, staging, sha)
	if err != nil {
		return InstallEntry{}, false, false, err
	}
	if err := validatePluginDir(final); err != nil {
		_ = os.RemoveAll(final)
		return InstallEntry{}, false, false, fmt.Errorf("upgraded plugin failed validation: %w", err)
	}

	prev.InstallPath = final
	prev.GitCommitSha = sha
	prev.Version = computeVersion(pluginManifestVersion(final), cp.Source.Ref, sha)
	prev.LastUpdated = m.now().UTC()
	prev.Source = cp.Source
	reg.Plugins[key] = []InstallEntry{prev}
	if err := SaveRegistry(m.registryPath(), reg); err != nil {
		return InstallEntry{}, false, false, err
	}
	return prev, true, false, nil
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
		return fmt.Errorf("%s: %w", key, ErrNotInstalled)
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
		return fmt.Errorf("%s: %w", key, ErrNotInstalled)
	}
	if len(entries) > 0 {
		p := entries[0].InstallPath
		if strings.HasPrefix(p, m.cacheDir()+string(os.PathSeparator)) {
			_ = os.RemoveAll(p)
		}
	}
	delete(reg.Plugins, key)
	return SaveRegistry(m.registryPath(), reg)
}

type ListItem struct {
	Plugin      string `json:"plugin"`
	Marketplace string `json:"marketplace"`
	Version     string `json:"version"`
	Enabled     bool   `json:"enabled"`
	// serf:naming-ignore: matches Claude Code plugin/marketplace JSON schema
	AutoUpgrade bool `json:"autoUpgrade"`
	Broken      bool `json:"broken"`
	// serf:naming-ignore: matches Claude Code plugin/marketplace JSON schema
	InstallPath string `json:"installPath"`
	// serf:naming-ignore: matches Claude Code plugin/marketplace JSON schema
	GitCommitSha string `json:"gitCommitSha"`
	// serf:naming-ignore: matches Claude Code plugin/marketplace JSON schema
	InstalledAt time.Time `json:"installedAt"`
	// serf:naming-ignore: matches Claude Code plugin/marketplace JSON schema
	LastUpdated time.Time `json:"lastUpdated"`
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
	// Non-nil even when nothing is installed: callers (cmd/serf/plugincmd.go's
	// `list --json`) JSON-encode this directly, and a nil slice would encode as
	// `null` instead of `[]`.
	out := []ListItem{}
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
