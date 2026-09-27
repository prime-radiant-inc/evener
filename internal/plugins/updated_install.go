package plugins

import "path/filepath"

// UpdatedInstallDir finds the current revision of an already-selected cached
// plugin. The old path pins the store, marketplace and registry plugin identity;
// a same-name plugin elsewhere is never a replacement. An empty result means
// there is no unambiguous replacement in that cache.
//
// Enabled is a launch default, not a revocation of a session's selection. This
// lookup preserves that selection, including explicitly selected default-off
// plugins. It only reads the registry; it neither installs nor loads components.
func UpdatedInstallDir(previous string) (string, error) {
	if !filepath.IsAbs(previous) {
		return "", nil
	}
	previous = filepath.Clean(previous)
	pluginDir := filepath.Dir(previous)
	marketplaceDir := filepath.Dir(pluginDir)
	cacheDir := filepath.Dir(marketplaceDir)
	if filepath.Base(cacheDir) != cacheDirName {
		return "", nil
	}
	registry, err := NewManager(filepath.Dir(cacheDir)).loadRegistry()
	if err != nil {
		return "", err
	}
	entries := registry.Plugins[registryKey(filepath.Base(pluginDir), filepath.Base(marketplaceDir))]
	if len(entries) != 1 || !filepath.IsAbs(entries[0].InstallPath) {
		return "", nil
	}
	// Discovery records physical paths, while the registry can retain a
	// configured symlink to the same store. Compare those physical identities.
	current, err := filepath.EvalSymlinks(entries[0].InstallPath)
	if err != nil {
		return "", err
	}
	if current == previous || filepath.Dir(current) != pluginDir {
		return "", nil
	}
	return current, nil
}
