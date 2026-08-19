package cmdutil

import (
	"fmt"
	"os"
	"path/filepath"

	"primeradiant.com/evener/envvars"
)

// checkLegacyDataDirs refuses to let a fresh Evener binary create an empty
// replacement for a Serf directory that still holds real data.
//
// cmd/evener-migrate treats an existing destination as "already migrated"
// and silently skips it. If any Evener binary runs before the user runs
// evener-migrate, EnsureUserConfigDirs (and the hub's own state-root
// mkdir, right behind it) would create an empty ~/.evener/$XDG evener dir
// first, and the user's real ~/.serf data would then be stranded forever
// with no error ever surfacing. This check runs before any of those
// directories are created, so it can catch the condition while it's still
// fixable.
//
// It compares the same three global locations evener-migrate moves — the
// home state root, the XDG config root, and the XDG state root — using the
// identical base-directory resolution (home, $XDG_CONFIG_HOME,
// $XDG_STATE_HOME) so the two tools always agree on what "migrated" means.
// (cmd/evener-migrate is a deliberately dependency-light standalone binary
// and does not import cmdutil, so that resolution is duplicated here rather
// than shared — see cmd/evener-migrate/main.go.) Per-project .serf
// directories are not checked: that migration is opt-in, run by the user
// from a project root when ready, not something a binary launch should
// block on.
func checkLegacyDataDirs() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil //nolint:nilerr // no home directory to compare against; DefaultStateRoot/DefaultConfigRoot already degrade gracefully in this case, so let that fallback behavior proceed rather than erroring here too
	}

	type pair struct {
		legacy, target string
	}
	var pairs []pair

	if envvars.EVENERStateDir.Getenv() == "" {
		pairs = append(pairs, pair{
			legacy: filepath.Join(home, ".serf"),
			target: filepath.Join(home, ".evener"),
		})
	}

	configBase := envvars.XDGConfigHome.Getenv()
	if configBase == "" {
		configBase = filepath.Join(home, ".config")
	}
	pairs = append(pairs, pair{
		legacy: filepath.Join(configBase, "serf"),
		target: filepath.Join(configBase, "evener"),
	})

	stateBase := envvars.XDGStateHome.Getenv()
	if stateBase == "" {
		stateBase = filepath.Join(home, ".local", "state")
	}
	pairs = append(pairs, pair{
		legacy: filepath.Join(stateBase, "serf"),
		target: filepath.Join(stateBase, "evener"),
	})

	for _, p := range pairs {
		if _, err := os.Lstat(p.legacy); err != nil {
			continue // no legacy data at this location
		}
		if _, err := os.Lstat(p.target); err == nil {
			continue // already migrated (or the user created it deliberately)
		}
		return fmt.Errorf(
			"found legacy Serf data at %s, not yet migrated to %s: run evener-migrate before continuing, or create %s yourself to start fresh without migrating",
			p.legacy, p.target, p.target)
	}
	return nil
}
