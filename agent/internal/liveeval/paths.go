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

// Paths resolves the state home and the provider-config path used by live
// evals under the runtime's tri-state rule (spec §10): EVENER_PROVIDERS_CONFIG
// unset → $XDG_CONFIG_HOME/evener/providers.toml (~/.config fallback);
// present and empty → no user layer (noUserLayer = true, providerPath "");
// set → that path. A configured state home wins over the user's home rather
// than naming one developer's machine; the returned state path is the XDG
// base, not the child "evener" directory the auth layer appends.
//
// This mirrors cmdutil.ProvidersConfigPath, duplicated here so this low-level
// eval-harness package stays free of a dependency on the cmd helper layer
// (see appwire/frame_recorder.go for the same tradeoff).
func Paths(stateHome, userHome string) (string, string, bool) {
	stateHome = strings.TrimSpace(stateHome)
	userHome = strings.TrimSpace(userHome)
	if stateHome == "" {
		stateHome = filepath.Join(userHome, ".local", "state")
	}

	providerPath, ok := envvars.EVENERProvidersConfig.LookupEnv()
	switch {
	case ok && strings.TrimSpace(providerPath) == "":
		return stateHome, "", true
	case ok:
		return stateHome, providerPath, false
	}
	configHome := envvars.XDGConfigHome.Trimmed()
	if configHome == "" {
		configHome = filepath.Join(userHome, ".config")
	}
	return stateHome, filepath.Join(configHome, "evener", "providers.toml"), false
}
