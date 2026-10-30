package cmdutil

import (
	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/envvars/userdirs"
	"primeradiant.com/evener/identifier"
)

// StateRootFromLookup resolves evener's machine-generated state root
// (userdirs.StateRoot) out of a caller-supplied environment lookup instead of
// the process environment. A caller holding a launch environment that is not
// the process environment — the hub auth controller is handed one — resolves
// the one canonical chain with it, so the hub and the rest of evener cannot
// drift apart (#1012). It returns userdirs.ErrNoStateHome when the lookup
// yields neither XDG_STATE_HOME nor a home directory.
//
// goos selects the home-directory spelling the way os.UserHomeDir does, so
// callers and tests can pin Windows behavior from any host. XDG_STATE_HOME is
// used verbatim, matching the rendezvous and appwire callers that read it raw.
func StateRootFromLookup(goos string, lookup func(string) (string, bool)) (string, error) {
	stateHome, _ := lookup(envvars.XDGStateHome.Name)
	return userdirs.StateRoot(stateHome, func() (string, error) {
		return userHomeDirFromLookup(goos, lookup), nil
	})
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
