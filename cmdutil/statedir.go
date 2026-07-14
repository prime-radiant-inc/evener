package cmdutil

import (
	"os"
	"path/filepath"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/identifier"
)

// DefaultStateRoot returns the serf state root: $SERF_STATE_DIR when set,
// otherwise ~/.serf (or ./.serf if the home directory can't be resolved).
//
// It is the single knob that redirects all home-based serf state — the provider
// config (providers.toml) and credentials. `serf run` / `serf serve`, tests,
// sandboxed runs, and multi-instance setups all honor it, so cmd/serf and
// cmd/serf-hub resolve the identical path.
func DefaultStateRoot() string {
	if dir := envvars.SERFStateDir.Getenv(); dir != "" {
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".serf")
	}
	return ".serf"
}

// ResolveStateKeyDir is retained for source compatibility with callers that
// need a resolved project path. It uses the shared identifier policy and
// returns the canonical path on success; on resolution failure it returns the
// input unchanged because this legacy no-error API cannot report the error.
func ResolveStateKeyDir(workDir string) string {
	if project, err := identifier.ResolveProject(workDir); err == nil {
		return project.CanonicalPath
	}
	return workDir
}

// DefaultProjectStateDir computes the default per-project runtime state
// directory for workDir, for use when no explicit --state-dir flag or
// SERF_STATE_DIR override is set: $XDG_STATE_HOME/serf/projects/<Project.ID>/.
func DefaultProjectStateDir(workDir string) (identifier.Project, string, error) {
	return agent.RuntimeDir(workDir, "")
}
