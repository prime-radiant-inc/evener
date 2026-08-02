// Package liveeval contains the shared setup policy for provider-backed evals.
package liveeval

import (
	"path/filepath"
	"strings"

	"primeradiant.com/serf/envvars"
)

// OptInEnv is the environment variable that explicitly enables provider-backed
// evals. Build tags select the eval sources; this variable permits live calls.
const OptInEnv = "SERF_LIVE_TESTS"

// Enabled reports whether a caller explicitly opted into live provider calls.
func Enabled(value string) bool {
	return value == "1"
}

// Paths resolves the state-home and provider-config roots used by live evals.
// A configured state home wins; otherwise it follows the user's home rather
// than naming one developer's machine. Provider configuration follows the
// runtime precedence: SERF_PROVIDERS_CONFIG, SERF_STATE_DIR/providers.toml,
// then ~/.serf/providers.toml. The returned state path is the XDG base, not
// the child "serf" directory that the auth layer appends.
func Paths(stateHome, userHome string) (string, string) {
	stateHome = strings.TrimSpace(stateHome)
	userHome = strings.TrimSpace(userHome)
	if stateHome == "" {
		stateHome = filepath.Join(userHome, ".local", "state")
	}

	providerPath := envvars.SERFProvidersConfig.Trimmed()
	if providerPath == "" {
		stateRoot := envvars.SERFStateDir.Trimmed()
		if stateRoot == "" {
			stateRoot = filepath.Join(userHome, ".serf")
		}
		providerPath = filepath.Join(stateRoot, "providers.toml")
	}
	return stateHome, providerPath
}
