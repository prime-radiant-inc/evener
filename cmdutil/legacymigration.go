package cmdutil

import (
	"fmt"
	"os"
	"path/filepath"

	"primeradiant.com/evener/envvars"
)

// homeRootFiles are the basenames evener-migrate moves out of a legacy
// (~/.serf) or interim (~/.evener) home root into their final XDG homes:
// providers.toml/credentials.toml/hub.toml/launch.toml into the config root,
// auth-token/index.db/hub.lock/run/deletions into the state root (see
// cmd/evener-migrate). checkLegacyDataDirs below uses the identical list to
// decide whether either home root still holds real, unmigrated data, so the
// two tools always agree on what "migrated" means.
var homeRootFiles = []string{
	"providers.toml",
	"credentials.toml",
	"hub.toml",
	"launch.toml",
	"auth-token",
	"index.db",
	"hub.lock",
	"run",
	"deletions",
}

// checkLegacyDataDirs refuses to let a fresh Evener binary create an empty
// replacement for a Serf/home-root directory that still holds real data.
//
// cmd/evener-migrate treats an existing destination as "already migrated"
// and silently skips it. If any Evener binary runs before the user runs
// evener-migrate, EnsureUserConfigDirs (and the hub's own state-root
// mkdir, right behind it) would create an empty replacement XDG directory
// first, and the user's real legacy data would then be stranded forever
// with no error ever surfacing. This check runs before any of those
// directories are created, so it can catch the condition while it's still
// fixable.
//
// It checks four locations:
//   - the XDG config root (~/.config/serf → ~/.config/evener)
//   - the XDG state root (~/.local/state/serf → ~/.local/state/evener)
//   - the legacy home root (~/.serf), which pre-rename held the files
//     homeRootFiles now names
//   - the interim home root (~/.evener), the post-rename/pre-consolidation
//     location for the same files
//
// The first two are whole-directory comparisons (their contents move as a
// unit and nothing else populates the destination first). The home-root
// checks cannot use the same directory-level comparison: both
// ~/.config/evener and ~/.local/state/evener legitimately pre-exist for
// unrelated content (skills/plugins; auth/continuation/projects state)
// on any machine that has used Evener before, so their mere existence is
// not evidence the home-root files were migrated. Checking for the known
// home-root filenames still present in ~/.serf or ~/.evener is the signal
// that doesn't false-positive on that pre-existing content.
//
// Per-project .serf directories are not checked: that migration is opt-in,
// run by the user from a project root when ready, not something a binary
// launch should block on.
func checkLegacyDataDirs() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil //nolint:nilerr // no home directory to compare against; DefaultStateRoot/DefaultConfigRoot already degrade gracefully in this case, so let that fallback behavior proceed rather than erroring here too
	}

	type pair struct {
		legacy, target string
	}
	var pairs []pair

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

	for _, root := range []string{
		filepath.Join(home, ".serf"),
		filepath.Join(home, ".evener"),
	} {
		if name, found := firstUnmigratedHomeRootFile(root); found {
			return fmt.Errorf(
				"found unmigrated Evener data at %s (e.g. %s): run evener-migrate before continuing, or remove %s yourself to start fresh without migrating",
				root, filepath.Join(root, name), root)
		}
	}
	return nil
}

// firstUnmigratedHomeRootFile reports the first homeRootFiles entry still
// present directly under root, if any.
func firstUnmigratedHomeRootFile(root string) (string, bool) {
	for _, name := range homeRootFiles {
		if _, err := os.Lstat(filepath.Join(root, name)); err == nil {
			return name, true
		}
	}
	return "", false
}
