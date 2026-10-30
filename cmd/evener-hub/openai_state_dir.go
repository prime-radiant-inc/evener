package hub

import (
	"runtime"

	"primeradiant.com/evener/cmdutil"
)

// openAIStateDirFromEnvMap resolves evener's state root for the hub auth
// controller out of a caller-supplied environment, so a launch env can redirect
// it with XDG_STATE_HOME or by overriding the home directory. The whole chain —
// the state home, the home directory and its Windows spelling, and the error
// when no home can be found — lives in cmdutil and userdirs and is resolved
// against the supplied env rather than the process env, so the hub lands on the
// same root the rest of evener would (#1012).
func openAIStateDirFromEnvMap(env map[string]string) (string, error) {
	return openAIStateDirFromLookup(runtime.GOOS, func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	})
}

// openAIStateDirFromLookup resolves the state root from a supplied environment
// lookup and OS. openAIStateDirFromEnvMap passes the host OS; taking goos as an
// argument keeps cmdutil's per-OS home spelling drivable from any host in tests.
// The chain itself is cmdutil's, so the hub keeps no resolution logic of its own.
func openAIStateDirFromLookup(goos string, lookup func(string) (string, bool)) (string, error) {
	return cmdutil.StateRootFromLookup(goos, lookup)
}
