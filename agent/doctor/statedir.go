package doctor

import (
	"os"

	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/envvars/userdirs"
)

var doctorUserHomeDir = os.UserHomeDir

// ResolveStateBase resolves the doctor's state base with evener's session-state
// precedence: the --state-dir flag › EVENER_STATE_DIR env › $XDG_STATE_HOME ›
// ~/.local/state; it fails with userdirs.ErrNoStateHome when none resolves.
// Locate (and the subcommands built on it) then auto-detect the base's shape (see resolveBuckets): an XDG state home (it holds
// evener/projects/* buckets), a project bucket directory (the daemon's
// per-session state dir — see userdirs.StateHomeForBucketDir), or a single
// override / scratch bucket (sessions/ directly under it).
//
// Note EVENER_STATE_HOME does not exist — it was never read by evener; the real env
// knob is EVENER_STATE_DIR.
func ResolveStateBase(flagStateDir string) (string, error) {
	if flagStateDir != "" {
		return flagStateDir, nil
	}
	if v := envvars.EVENERStateDir.Getenv(); v != "" {
		return v, nil
	}
	return userdirs.StateHome(envvars.XDGStateHome.Getenv(), doctorUserHomeDir)
}
