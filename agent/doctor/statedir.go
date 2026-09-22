package doctor

import (
	"os"
	"path/filepath"

	"primeradiant.com/evener/envvars"
)

var doctorUserHomeDir = os.UserHomeDir

// ResolveStateBase resolves the doctor's state base with evener's session-state
// precedence: the --state-dir flag › EVENER_STATE_DIR env › $XDG_STATE_HOME ›
// ~/.local/state. Locate (and the subcommands built on it) then auto-detect
// whether the base is an XDG state home (it holds evener/projects/* buckets) or is
// itself a single override / scratch bucket (sessions/ directly under it).
//
// Note EVENER_STATE_HOME does not exist — it was never read by evener; the real env
// knob is EVENER_STATE_DIR.
func ResolveStateBase(flagStateDir string) string {
	if flagStateDir != "" {
		return flagStateDir
	}
	if v := envvars.EVENERStateDir.Getenv(); v != "" {
		return v
	}
	if v := envvars.XDGStateHome.Getenv(); v != "" {
		return v
	}
	home, err := doctorUserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	return filepath.Join(home, ".local", "state")
}

// stateHomeForBucketDir reports the XDG-style state home that a project
// bucket directory lives under, or "" when stateBase is not laid out as
// <stateHome>/evener/projects/<project-id>.
//
// The daemon runs every session with its state dir set to that bucket
// directory — what RuntimeDir computes and EVENER_STATE_DIR carries — so a
// sweep handed such a base must up-walk to the state home before it can see
// the sibling buckets. The two path components checked (evener, projects)
// are the runtime's own structural layout (agent.RuntimeDir), not a
// bucket-naming assumption.
func stateHomeForBucketDir(stateBase string) string {
	projects := filepath.Dir(stateBase)
	if filepath.Base(projects) != "projects" {
		return ""
	}
	evener := filepath.Dir(projects)
	if filepath.Base(evener) != "evener" {
		return ""
	}
	return filepath.Dir(evener)
}
