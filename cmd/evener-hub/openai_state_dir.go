package hub

import (
	"strings"

	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/envvars"
)

// openAIStateDirFromEnvMap resolves evener's state root for the hub auth
// controller out of a caller-supplied environment, so a launch env can redirect
// it with XDG_STATE_HOME. That override is all this resolves; everything under
// it — the home directory, its Windows spelling, and the fallback when no home
// can be found — is cmdutil.DefaultStateRoot's job, so the hub lands on the
// same root as the rest of evener.
//
// It used to resolve the whole chain itself and had drifted from
// cmdutil.DefaultStateRoot in two places: on Windows it read
// USERPROFILE/HOMEDRIVE+HOMEPATH out of the supplied env instead of letting
// os.UserHomeDir find the home directory, and with no home at all it fell back
// to os.TempDir() rather than ".".
func openAIStateDirFromEnvMap(env map[string]string) string {
	if stateHome := strings.TrimSpace(env[envvars.XDGStateHome.Name]); stateHome != "" {
		return authopenai.DefaultStateDirWithStateHome(stateHome)
	}
	return cmdutil.DefaultStateRoot()
}
