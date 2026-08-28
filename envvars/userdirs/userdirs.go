// Package userdirs resolves Evener's user-level configuration paths.
package userdirs

import (
	"os"
	"path/filepath"

	"primeradiant.com/evener/envvars"
)

// ConfigRoot resolves the Evener config root from an XDG config base and a
// caller-supplied home-directory lookup. A failed home lookup produces an
// empty path so callers that cannot safely fall back do not scan a relative
// directory by accident.
func ConfigRoot(xdgConfigHome string, userHomeDir func() (string, error)) string {
	base := xdgConfigHome
	if base == "" {
		home, err := userHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "evener")
}

// Subdir derives a child directory or file from a config root. It preserves an
// unavailable root as unavailable instead of turning it into a relative path.
func Subdir(root, name string) string {
	if root == "" {
		return ""
	}
	return filepath.Join(root, name)
}

// DefaultConfigRoot resolves the user config root from the process environment.
func DefaultConfigRoot() string {
	return ConfigRoot(envvars.XDGConfigHome.Getenv(), os.UserHomeDir)
}
