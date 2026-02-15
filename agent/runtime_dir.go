package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// RuntimeDir computes the XDG-compliant state directory for a project.
// If overrideDir is non-empty, it is returned directly.
// Otherwise: $XDG_STATE_HOME/serf/projects/<hash>/
// where <hash> is derived from originURL (if non-empty) or workDir.
func RuntimeDir(originURL, workDir, overrideDir string) string {
	if overrideDir != "" {
		return overrideDir
	}
	base := xdgStateHome()
	key := originURL
	if key == "" {
		key = workDir
	}
	return filepath.Join(base, "serf", "projects", hexHash(key))
}

// CacheDir returns the global cache directory: $XDG_CACHE_HOME/serf/
func CacheDir() string {
	return filepath.Join(xdgCacheHome(), "serf")
}

// hexHash returns SHA256(input)[:8] as hex (16 hex chars).
func hexHash(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:8])
}

// xdgStateHome returns $XDG_STATE_HOME or ~/.local/state as default.
func xdgStateHome() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	return filepath.Join(home, ".local", "state")
}

// xdgCacheHome returns $XDG_CACHE_HOME or ~/.local/cache as default.
func xdgCacheHome() string {
	if v := os.Getenv("XDG_CACHE_HOME"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	return filepath.Join(home, ".local", "cache")
}
