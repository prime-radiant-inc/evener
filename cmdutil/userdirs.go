package cmdutil

import (
	"fmt"
	"os"
	"path/filepath"

	"primeradiant.com/evener/envvars"
)

// DefaultConfigRoot returns the user config root for evener:
// $XDG_CONFIG_HOME/evener, or ~/.config/evener when XDG_CONFIG_HOME is unset.
func DefaultConfigRoot() string {
	base := envvars.XDGConfigHome.Getenv()
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			home = "."
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "evener")
}

func DefaultSkillsDir() string {
	return filepath.Join(DefaultConfigRoot(), "skills")
}

func DefaultPluginsRoot() string {
	return filepath.Join(DefaultConfigRoot(), "plugins")
}

// EnsureUserConfigDirs creates the user-managed Evener extension directories.
// Runtime state remains lazy and is created by the subsystem that writes it.
func EnsureUserConfigDirs() error {
	for _, dir := range []string{
		DefaultConfigRoot(),
		DefaultSkillsDir(),
		DefaultPluginsRoot(),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create user config dir %s: %w", dir, err)
		}
	}
	return nil
}
