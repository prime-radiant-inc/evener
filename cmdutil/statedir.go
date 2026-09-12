package cmdutil

import (
	"os"
	"path/filepath"
	"runtime"

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
	return StateRootFromLookup(runtime.GOOS, os.LookupEnv)
}

// StateRootFromLookup resolves the same chain as DefaultStateRoot out of a
// caller-supplied environment lookup instead of the process environment. A
// caller holding a launch environment that is not the process environment —
// the hub auth controller is handed one — resolves the one canonical chain
// with it, so the hub and the rest of evener cannot drift apart (#1012).
//
// goos selects the home-directory spelling the way os.UserHomeDir does, so
// callers and tests can pin Windows behavior from any host. XDG_STATE_HOME is
// used verbatim, matching DefaultStateRoot's previous behavior and the
// rendezvous/appwire mirrors that read it raw.
func StateRootFromLookup(goos string, lookup func(string) (string, bool)) string {
	base := ""
	if stateHome, ok := lookup(envvars.XDGStateHome.Name); ok {
		base = stateHome
	}
	if base == "" {
		home := userHomeDirFromLookup(goos, lookup)
		if home == "" {
			home = "."
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "evener")
}

// userHomeDirFromLookup resolves the home directory from a supplied environment
// lookup using os.UserHomeDir's rules: USERPROFILE on Windows, home on Plan 9,
// HOME elsewhere, and the /sdcard and / constants os.UserHomeDir returns on
// Android and iOS. It returns an empty string when os.UserHomeDir returns an
// error.
//
// It deliberately has no HOMEDRIVE/HOMEPATH arm: os.UserHomeDir has none, and
// the hub's old arm for those was the Windows drift #1012 removed.
func userHomeDirFromLookup(goos string, lookup func(string) (string, bool)) string {
	name := envvars.Home.Name
	switch goos {
	case "windows":
		name = envvars.UserProfile.Name
	case "plan9":
		name = "home"
	}
	if home, ok := lookup(name); ok && home != "" {
		return home
	}
	switch goos {
	case "android":
		return "/sdcard"
	case "ios":
		return "/"
	}
	return ""
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
