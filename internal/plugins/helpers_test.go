package plugins

import "path/filepath"

func (m *Manager) pluginCacheDir(marketplace, plugin, sha string) string {
	return filepath.Join(m.cacheDir(), marketplace, plugin, sha)
}

// bundledPluginPath names where a bundled plugin's published copy lives. It is
// the unchecked join, for tests naming a path; prepareBundledStore derives the
// same path through storePath so an unresolved root is refused.
func (m *Manager) bundledPluginPath(name, digest string) string {
	return filepath.Join(m.bundledDir(), bundledPluginDirName(name, digest))
}

// registryPath and marketplacesFile are the unchecked joins, for tests naming
// a path to plant a file at. Production reads and writes go through
// loadRegistry/saveRegistry and loadMarketplaces/saveMarketplaces, which
// derive the same paths through storePath.
func (m *Manager) registryPath() string { return filepath.Join(m.Root, registryFileName) }
func (m *Manager) marketplacesFile() string {
	return filepath.Join(m.Root, marketplacesFileName)
}
