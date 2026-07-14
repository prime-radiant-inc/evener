package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"

	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/identifier"
)

// RuntimeDir computes the XDG-compliant state directory for a project.
// If overrideDir is non-empty, it is returned directly without resolving the
// project. Otherwise the project is resolved and its canonical ID is used.
func RuntimeDir(workDir, overrideDir string) (identifier.Project, string, error) {
	return RuntimeDirWithStateHome(workDir, overrideDir, "")
}

// RuntimeDirWithStateHome computes the project state directory like RuntimeDir,
// but uses stateHome as the base when it is non-empty instead of xdgStateHome().
// If overrideDir is non-empty, it is returned directly without resolving the
// project. Otherwise the result is <base>/serf/projects/<Project.ID>.
func RuntimeDirWithStateHome(workDir, overrideDir, stateHome string) (identifier.Project, string, error) {
	if overrideDir != "" {
		return identifier.Project{}, overrideDir, nil
	}
	project, err := identifier.ResolveProject(workDir)
	if err != nil {
		return identifier.Project{}, "", err
	}
	base := stateHome
	if base == "" {
		base = xdgStateHome()
	}
	return project, filepath.Join(base, "serf", "projects", project.ID), nil
}

// CacheDir returns the global cache directory: $XDG_CACHE_HOME/serf/
func CacheDir() string {
	return filepath.Join(xdgCacheHome(), "serf")
}

// shortHash returns SHA256(b)[:8] as hex, used for compact tool-call
// signatures when deduplicating repeated calls.
func shortHash(b []byte) string {
	return nonProjectHash(string(b))
}

// nonProjectHash is used for compact cache and tool-call signatures. It is
// deliberately unrelated to identifier.Project identity.
func nonProjectHash(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:8])
}

// hexHash is retained for non-project cache/test callers.
func hexHash(input string) string { return nonProjectHash(input) }

// xdgStateHome returns $XDG_STATE_HOME or ~/.local/state as default.
func xdgStateHome() string {
	if v := envvars.XDGStateHome.Getenv(); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	return filepath.Join(home, ".local", "state")
}

// xdgCacheHome returns $XDG_CACHE_HOME or ~/.cache per the XDG spec.
func xdgCacheHome() string {
	if v := envvars.XDGCacheHome.Getenv(); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	return filepath.Join(home, ".cache")
}
