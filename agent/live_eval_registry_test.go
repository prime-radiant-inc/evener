//go:build eval

package agent

import (
	"os"
	"testing"

	"primeradiant.com/evener/agent/internal/liveeval"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/registry"
)

// liveEvalRegistry is the provider registry an explicitly opted-in live eval
// runs against, the developer's own provider layer and state home, with the state
// home it set. It skips the test when live evals are off or no provider layer
// is configured. Credentials come from credentials.toml as in every binary.
func liveEvalRegistry(t *testing.T) (*registry.Registry, string) {
	t.Helper()
	if !liveeval.Enabled(os.Getenv(liveeval.OptInEnv)) {
		t.Skipf("live eval disabled: set %s=1 to run provider-backed evals", liveeval.OptInEnv)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("live eval requires a resolvable user home: %v", err)
	}
	stateHome, providersConfig, noUserLayer := liveeval.Paths(envvars.XDGStateHome.Trimmed(), home)
	if noUserLayer {
		t.Skipf("%s is empty: no user provider layer to run a live eval against", envvars.EVENERProvidersConfig.Name)
	}
	t.Setenv(envvars.XDGStateHome.Name, stateHome)
	if _, err := os.Stat(providersConfig); err != nil {
		t.Skipf("providers.toml at %s: %v", providersConfig, err)
	}
	t.Setenv(envvars.EVENERProvidersConfig.Name, providersConfig)
	credStore, err := credentials.LoadStore(skillsLiveCredentialsPath(home, providersConfig))
	if err != nil {
		t.Fatalf("credentials store: %v", err)
	}
	r, err := registry.Load(registry.WithCredentials(skillsLiveCredentialSource{store: credStore}))
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}
	return r, stateHome
}
