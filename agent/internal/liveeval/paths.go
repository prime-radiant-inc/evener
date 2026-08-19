// Package liveeval contains the shared setup policy for provider-backed evals.
package liveeval

import (
	"path/filepath"
	"strings"

	"primeradiant.com/evener/envvars"
)

// OptInEnv is the environment variable that explicitly enables provider-backed
// evals. Build tags select the eval sources; this variable permits live calls.
const OptInEnv = "EVENER_LIVE_TESTS"

// Enabled reports whether a caller explicitly opted into live provider calls.
func Enabled(value string) bool {
	return value == "1"
}

// Paths resolves the state-home and provider-config roots used by live evals.
// A configured state home wins; otherwise it follows the user's home rather
// than naming one developer's machine. Provider configuration follows the
// runtime precedence: EVENER_PROVIDERS_CONFIG, then
// $XDG_CONFIG_HOME/evener/providers.toml, then ~/.config/evener/providers.toml
// — cmdutil.DefaultConfigRoot's resolution, duplicated here so this
// low-level eval-harness package stays free of a dependency on the cmd
// helper layer (see appwire/frame_recorder.go for the same tradeoff). The
// returned state path is the XDG base, not the child "evener" directory that
// the auth layer appends.
func Paths(stateHome, userHome string) (string, string) {
	stateHome = strings.TrimSpace(stateHome)
	userHome = strings.TrimSpace(userHome)
	if stateHome == "" {
		stateHome = filepath.Join(userHome, ".local", "state")
	}

	providerPath := envvars.EVENERProvidersConfig.Trimmed()
	if providerPath == "" {
		configHome := envvars.XDGConfigHome.Trimmed()
		if configHome == "" {
			configHome = filepath.Join(userHome, ".config")
		}
		providerPath = filepath.Join(configHome, "evener", "providers.toml")
	}
	return stateHome, providerPath
}
