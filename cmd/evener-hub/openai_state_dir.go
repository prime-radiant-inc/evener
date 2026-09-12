package hub

import (
	"runtime"

	"primeradiant.com/evener/cmdutil"
)

// openAIStateDirFromEnvMap resolves evener's state root for the hub auth
// controller out of a caller-supplied environment, so a launch env can redirect
// it with XDG_STATE_HOME or by overriding the home directory. The whole chain —
// the state home, the home directory and its Windows spellings, and the
// fallback when no home can be found — lives in cmdutil and is resolved against
// the supplied env rather than the process env, so the hub lands on the same
// root the rest of evener would (#1012).
//
// It used to resolve the chain itself and had drifted from
// cmdutil.DefaultStateRoot in two places: on Windows it used a different home
// spelling, and with no home at all it fell back to os.TempDir() rather than
// ".".
func openAIStateDirFromEnvMap(env map[string]string) string {
	return cmdutil.StateRootFromLookup(runtime.GOOS, func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	})
}
