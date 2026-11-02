package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"

	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/envvars/userdirs"
	"primeradiant.com/evener/identifier"
)

// RuntimeDir computes the XDG-compliant state directory for a canonical
// project ID.
// If overrideDir is non-empty, it is returned directly without resolving the
// project. Otherwise the project is resolved and its canonical ID is used.
func RuntimeDir(workDir, overrideDir string) (identifier.Project, string, error) {
	return RuntimeDirWithStateHome(workDir, overrideDir, "")
}

// RuntimeDirWithStateHome computes the project state directory like RuntimeDir,
// but uses stateHome as the base when it is non-empty instead of the XDG state
// home (userdirs.StateHome). It fails with userdirs.ErrNoStateHome when neither
// is available.
// If overrideDir is non-empty, it is returned directly without resolving the
// project. Otherwise the result is <base>/evener/projects/<Project.ID>.
func RuntimeDirWithStateHome(workDir, overrideDir, stateHome string) (identifier.Project, string, error) {
	if overrideDir != "" {
		return identifier.Project{}, overrideDir, nil
	}
	project, err := identifier.ResolveProject(workDir)
	if err != nil {
		return identifier.Project{}, "", err
	}
	dir, err := RuntimeDirForProjectWithStateHome(project, stateHome)
	if err != nil {
		return identifier.Project{}, "", err
	}
	return project, dir, nil
}

// RuntimeDirForProjectWithStateHome returns the state directory for an
// already-resolved project without performing filesystem or Git resolution.
// Callers that have a Project from an earlier launch-resolution boundary use
// this to carry identity forward without resolving the active cwd again.
func RuntimeDirForProjectWithStateHome(project identifier.Project, stateHome string) (string, error) {
	if stateHome == "" {
		stateHome = envvars.XDGStateHome.Getenv()
	}
	root, err := userdirs.StateRoot(stateHome, os.UserHomeDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "projects", project.ID), nil
}

// CacheDir returns the global cache directory: $XDG_CACHE_HOME/evener/
func CacheDir() string {
	return filepath.Join(xdgCacheHome(), "evener")
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
