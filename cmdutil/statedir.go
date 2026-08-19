package cmdutil

import (
	"os"
	"path/filepath"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/identifier"
)

// DefaultStateRoot returns evener's machine-generated state root:
// $XDG_STATE_HOME/evener, or ~/.local/state/evener when XDG_STATE_HOME is
// unset (or ./.local/state/evener if the home directory can't be resolved).
//
// It holds machine-generated, non-config state: the auth token, the past-
// session index, the hub lock, and the daemon rendezvous/log directory. It is
// the evener-wide counterpart to DefaultConfigRoot (user-editable config, e.g.
// providers.toml) and to agent.RuntimeDir (per-project session state, also
// under $XDG_STATE_HOME/evener). EVENER_STATE_DIR does NOT override this root:
// that variable is a per-invocation project/session state override (see
// cmd/evener/run.go, cmd/evener-hub/spawn.go), a different concept from this
// evener-wide root, and XDG_STATE_HOME is already the standard override for
// it.
func DefaultStateRoot() string {
	base := envvars.XDGStateHome.Getenv()
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			home = "."
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "evener")
}

// ResolveStateKeyDir is retained for source compatibility with callers that
// need a resolved project path. It uses the shared identifier policy and
// returns the canonical path on success; on resolution failure it returns the
// input unchanged because this no-error API cannot report the error.
func ResolveStateKeyDir(workDir string) string {
	if project, err := identifier.ResolveProject(workDir); err == nil {
		return project.CanonicalPath
	}
	return workDir
}

// DefaultProjectStateDir computes the default per-project runtime state
// directory for workDir, for use when no explicit --state-dir flag or
// EVENER_STATE_DIR override is set: $XDG_STATE_HOME/evener/projects/<Project.ID>/.
func DefaultProjectStateDir(workDir string) (identifier.Project, string, error) {
	return agent.RuntimeDir(workDir, "")
}
