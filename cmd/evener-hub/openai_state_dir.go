package hub

import (
	"runtime"

	"primeradiant.com/evener/cmdutil"
)

// openAIStateDirFromEnvMap resolves evener's state root for the hub auth
// controller out of a caller-supplied environment, so a launch env can redirect
// it with XDG_STATE_HOME or by overriding the home directory. The whole chain —
// the state home, the home directory and its Windows spelling, and the
// fallback when no home can be found — lives in cmdutil and is resolved against
// the supplied env rather than the process env, so the hub lands on the same
// root the rest of evener would (#1012).
//
// It used to resolve the chain itself and had drifted from
// cmdutil.DefaultStateRoot in two places: on Windows it also accepted
// HOMEDRIVE+HOMEPATH, which os.UserHomeDir does not, and with no home at all it
// fell back to os.TempDir() rather than ".".
func openAIStateDirFromEnvMap(env map[string]string) string {
	return openAIStateDirFromLookup(runtime.GOOS, func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	})
}

// openAIStateDirFromLookup resolves the state root from a supplied environment
// lookup and OS. openAIStateDirFromEnvMap passes the host OS; taking goos as an
// argument keeps cmdutil's per-OS home spelling drivable from any host in tests.
// The chain itself is cmdutil's, so the hub keeps no resolution logic of its own.
func openAIStateDirFromLookup(goos string, lookup func(string) (string, bool)) string {
	return cmdutil.StateRootFromLookup(goos, lookup)
}
