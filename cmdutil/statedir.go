package cmdutil

import (
	"os"
	"path/filepath"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/identifier"
)

// DefaultStateRoot returns the serf state root: $EVENER_STATE_DIR when set,
// otherwise ~/.evener (or ./.evener if the home directory can't be resolved).
//
// It is the single knob that redirects all home-based serf state — the provider
// config (providers.toml) and credentials. `serf run` / `serf serve`, tests,
// sandboxed runs, and multi-instance setups all honor it, so cmd/evener and
// cmd/evener-hub resolve the identical path.
func DefaultStateRoot() string {
	if dir := envvars.EVENERStateDir.Getenv(); dir != "" {
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".evener")
	}
	return ".evener"
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
