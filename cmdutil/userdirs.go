package cmdutil

import (
	"fmt"
	"os"
	"path/filepath"

	"primeradiant.com/evener/envvars/userdirs"
)

// DefaultConfigRoot returns the user config root for evener:
// $XDG_CONFIG_HOME/evener, or ~/.config/evener when XDG_CONFIG_HOME is unset.
func DefaultConfigRoot() string {
	if root := userdirs.DefaultConfigRoot(); root != "" {
		return root
	}
	return filepath.Join(".", ".config", "evener")
}

func DefaultSkillsDir() string {
	return filepath.Join(DefaultConfigRoot(), "skills")
}

func DefaultPluginsRoot() string {
	return filepath.Join(DefaultConfigRoot(), "plugins")
}

// EnsureUserConfigDirs creates the user-managed Evener extension directories.
// Runtime state remains lazy and is created by the subsystem that writes it.
//
// It is the first side-effecting call in every product binary's startup
// (cmd/evener, cmd/evener-hub, cmd/evener-tui), so checkLegacyDataDirs runs
// here, before anything creates a directory a stranded ~/.serf could be
// mistaken as already migrated into.
func EnsureUserConfigDirs() error {
	if err := checkLegacyDataDirs(); err != nil {
		return err
	}
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
