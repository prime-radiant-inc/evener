// Package liveeval contains the shared setup policy for provider-backed evals.
package liveeval

import (
	"path/filepath"
	"strings"
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
// than naming one developer's machine. The returned state path is the XDG base,
// not the child "serf" directory that the auth layer appends.
func Paths(stateHome, userHome string) (string, string) {
	stateHome = strings.TrimSpace(stateHome)
	userHome = strings.TrimSpace(userHome)
	if stateHome == "" {
		stateHome = filepath.Join(userHome, ".local", "state")
	}
	return stateHome, filepath.Join(userHome, ".serf", "providers.toml")
}
