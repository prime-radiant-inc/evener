package plugins

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/envvars/userdirs"
)

var pluginUserHomeDir = os.UserHomeDir

// Manager owns all on-disk plugin state under Root (~/.config/evener/plugins).
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

// DefaultRoot is ~/.config/evener/plugins, honoring XDG_CONFIG_HOME the same way
// the rest of evener does (envvars.XDGConfigHome).
func DefaultRoot() string {
	return userdirs.Subdir(userdirs.ConfigRoot(envvars.XDGConfigHome.Getenv(), pluginUserHomeDir), "plugins")
}

// The store's own files and directories, named once so storePath and the
// plain joins below cannot drift apart.
const (
	registryFileName     = "installed_plugins.json"
	marketplacesFileName = "known_marketplaces.json"
	bundledDirName       = "bundled"
	cacheDirName         = "cache"
	marketplacesDirName  = "marketplaces"
)

// storePath derives a path inside the store, refusing a root that resolves
// against whatever directory the process happens to be in — an empty root
// (none could be resolved) or a relative one.
//
// Deriving the path is what refuses, so a caller cannot forget: List and
// ListMarketplaces used to hand back whatever installed_plugins.json or
// known_marketplaces.json the working directory happened to hold, because a
// reader has no store lock to inherit a check from. Every access that reaches
// the filesystem without holding the lock derives here — the registry and
// marketplaces accessors, the bundled store, Doctor's writability probe, and
// the marketplaces stat that seeding does before it locks.
//
// The plain joins below stay unchecked, and the invariant that keeps them safe
// is that each is only reached once the root is known to be usable: under the
// store lock, which acquireStoreLock would not have granted otherwise (the
// cache, marketplace and plugin directories in install, gc and the marketplace
// verbs); past Doctor's own refusal (doctorOrphanCacheDirs walks cacheDir with
// no lock at all); or in a test naming a path to plant a file at. A new caller
// that fits none of those derives here instead.
func (m *Manager) storePath(parts ...string) (string, error) {
	if err := m.storeRootError(); err != nil {
		return "", err
	}
	return filepath.Join(append([]string{m.Root}, parts...)...), nil
}

// registryPath and marketplacesFile are the unchecked joins, for tests naming
// a path to plant a file at. Production reads and writes go through
// loadRegistry/saveRegistry and loadMarketplaces/saveMarketplaces, which
// derive the same paths through storePath.
func (m *Manager) registryPath() string { return filepath.Join(m.Root, registryFileName) }
func (m *Manager) marketplacesFile() string {
	return filepath.Join(m.Root, marketplacesFileName)
}
func (m *Manager) marketplacesDir() string { return filepath.Join(m.Root, marketplacesDirName) }
func (m *Manager) cacheDir() string        { return filepath.Join(m.Root, cacheDirName) }
func (m *Manager) lockPath() string        { return filepath.Join(m.Root, ".lock") }

// bundledDir is evener's content-addressed cache of the plugins the running
// binary ships.
func (m *Manager) bundledDir() string { return filepath.Join(m.Root, "bundled") }

// bundledLockName is the lock file the bundled cache keeps beside the copies
// it holds. Named here because the sweep and every store listing has to know
// it is not a plugin.
const bundledLockName = ".lock"

// bundledLockPath excludes bundled publishers from each other and from nobody
// else. Publishing a bundled copy is a classify, set-aside, stage and rename
// sequence that touches only bundledDir, so it has no business waiting on the
// store lock, which install, upgrade, gc, catalog and marketplace refresh hold
// across git fetches.
func (m *Manager) bundledLockPath() string {
	return filepath.Join(m.bundledDir(), bundledLockName)
}

func (m *Manager) marketplaceDir(name string) string {
	return filepath.Join(m.marketplacesDir(), name)
}

func (m *Manager) pluginCacheDir(marketplace, plugin, sha string) string {
	return filepath.Join(m.cacheDir(), marketplace, plugin, sha)
}

// validNameComponent rejects a marketplace/plugin name that is unsafe to use as
// a filesystem path segment (traversal, absolute, separators, empty). Names come
// from untrusted marketplace.json and caller input.
func validNameComponent(kind, name string) error {
	if name == "" || name == "." || name == ".." ||
		strings.ContainsRune(name, '/') || strings.ContainsRune(name, '\\') ||
		!filepath.IsLocal(name) {
		return fmt.Errorf("invalid %s name %q: must be a single non-traversing path component", kind, name)
	}
	return nil
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Manager) stderr() io.Writer {
	if m.Stderr != nil {
		return m.Stderr
	}
	return os.Stderr
}
