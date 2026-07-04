package plugins

import (
	"io"
	"os"
	"path/filepath"
	"time"

	"primeradiant.com/serf/envvars"
)

// Manager owns all on-disk plugin state under Root (~/.config/serf/plugins).
type Manager struct {
	Root   string           // store root
	Now    func() time.Time // injectable clock; defaults to time.Now
	Stderr io.Writer        // warnings sink; defaults to os.Stderr
}

// NewManager returns a Manager rooted at root, or DefaultRoot() when root == "".
func NewManager(root string) *Manager {
	if root == "" {
		root = DefaultRoot()
	}
	return &Manager{Root: root, Now: time.Now, Stderr: os.Stderr}
}

// DefaultRoot is ~/.config/serf/plugins, honoring XDG_CONFIG_HOME the same way
// the rest of serf does (envvars.XDGConfigHome).
func DefaultRoot() string {
	dir := envvars.XDGConfigHome.Getenv()
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "serf", "plugins")
}

func (m *Manager) registryPath() string   { return filepath.Join(m.Root, "installed_plugins.json") }
func (m *Manager) marketplacesFile() string {
	return filepath.Join(m.Root, "known_marketplaces.json")
}
func (m *Manager) marketplacesDir() string { return filepath.Join(m.Root, "marketplaces") }
func (m *Manager) cacheDir() string        { return filepath.Join(m.Root, "cache") }
func (m *Manager) lockPath() string        { return filepath.Join(m.Root, ".lock") }

func (m *Manager) marketplaceDir(name string) string {
	return filepath.Join(m.marketplacesDir(), name)
}

func (m *Manager) pluginCacheDir(marketplace, plugin, sha string) string {
	return filepath.Join(m.cacheDir(), marketplace, plugin, sha)
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}
