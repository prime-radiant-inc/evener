package doctor

import (
	"os"
	"path/filepath"
)

// ResolveStateBase resolves the doctor's state base with serf's session-state
// precedence: the --state-dir flag › SERF_STATE_DIR env › $XDG_STATE_HOME ›
// ~/.local/state. Locate (and the subcommands built on it) then auto-detect
// whether the base is an XDG state home (it holds serf/projects/* buckets) or is
// itself a single override / scratch bucket (sessions/ directly under it).
//
// Note SERF_STATE_HOME does not exist — it was never read by serf; the real env
// knob is SERF_STATE_DIR.
func ResolveStateBase(flagStateDir string) string {
	if flagStateDir != "" {
		return flagStateDir
	}
	if v := os.Getenv("SERF_STATE_DIR"); v != "" {
		return v
	}
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	return filepath.Join(home, ".local", "state")
}
