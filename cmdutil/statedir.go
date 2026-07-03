package cmdutil

import (
	"os"
	"path/filepath"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/internal/gitpath"
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

// DefaultProjectStateDir computes the default per-project runtime state
// directory for workDir, for use when no explicit --state-dir flag or
// SERF_STATE_DIR override is set: $XDG_STATE_HOME/serf/projects/<hash>/,
// keyed by the git origin URL when present, otherwise by the resolved main
// repository root (gitpath.ResolveMainRepoRootLocal), falling back to
// workDir itself when it is not inside a git repository.
//
// Keying on the main root rather than the raw workDir matters for
// origin-less repos: RuntimeDir falls back to its workDir argument when
// originURL is empty, so without this resolution step, launching from a
// linked worktree would compute a different project state dir (and hence a
// different worktreeRoot) than launching from the main checkout. See
// docs/superpowers/specs/2026-07-02-native-worktree-tools-design.md §1
// ("Runtime state keying at launch").
func DefaultProjectStateDir(workDir string) string {
	originURL := GitOriginURLFromDir(workDir)
	keyDir := gitpath.ResolveMainRepoRootLocal(workDir)
	if keyDir == "" {
		keyDir = workDir
	}
	return agent.RuntimeDir(originURL, keyDir, "")
}
