package cmdutil

import (
	"os"
	"path/filepath"

	"primeradiant.com/serf/envvars"
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
