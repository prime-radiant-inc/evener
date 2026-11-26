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
