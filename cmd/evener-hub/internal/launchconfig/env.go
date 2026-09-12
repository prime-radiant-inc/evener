package launchconfig

import (
	"sort"
	"strings"

	"primeradiant.com/evener/envvars"
)

// EnvInputs bundles everything ToEnv needs.
type EnvInputs struct {
	Resolved  Resolved
	ParentEnv []string // typically os.Environ()
	RunDir    string
	StateDir  string
	HubToken  string
	// ProvidersConfigPath is passed as EVENER_PROVIDERS_CONFIG so the child
	// reads the same user layer the hub does. NoUserLayer overrides it.
	ProvidersConfigPath string
	// NoUserLayer sets EVENER_PROVIDERS_CONFIG to the empty string —
	// spec §10's third state, "no user layer" — replacing any inherited
	// value. It is how a hub whose providers.toml failed to load still
	// spawns children that resolve the implicit instance set.
	NoUserLayer bool
	// CredentialsPath is passed as EVENER_CREDENTIALS_CONFIG so the child
	// resolves credentials out of the same credentials.toml the hub's pane
	// writes.
	CredentialsPath string
}

// ToEnv produces the env slice for the spawned `evener serve`. Order of
// precedence per the spec §4.5:
//  1. Per-launch env from Resolved.Effective.Env (last-write-wins).
//  2. Parent process env (typically os.Environ()).
//
// Items earlier in the priority list are applied later in setEnv so they
// overwrite earlier writes. Provider credentials are not among them: the
// child resolves its own from the registry, the credentials.toml named by
// EVENER_CREDENTIALS_CONFIG, and the environment (spec §10).
func ToEnv(in EnvInputs) []string {
	out := append([]string{}, in.ParentEnv...)
	out = setEnv(out, envvars.EVENERHubSpawned.Name, "1")
	if in.RunDir != "" {
		out = setEnv(out, envvars.EVENERRunDir.Name, in.RunDir)
	}
	if in.StateDir != "" {
		out = setEnv(out, envvars.EVENERStateDir.Name, in.StateDir)
	}
	if in.HubToken != "" {
		out = setEnv(out, envvars.EVENERHubToken.Name, in.HubToken)
	}
	switch {
	case in.NoUserLayer:
		out = setEnv(out, envvars.EVENERProvidersConfig.Name, "")
	case in.ProvidersConfigPath != "":
		out = setEnv(out, envvars.EVENERProvidersConfig.Name, in.ProvidersConfigPath)
	}
	if in.CredentialsPath != "" {
		out = setEnv(out, envvars.EVENERCredentialsConfig.Name, in.CredentialsPath)
	}

	// 1. Per-launch env: applied last so it wins, in sorted key order
	//    for determinism.
	keys := make([]string, 0, len(in.Resolved.Effective.Env))
	for k := range in.Resolved.Effective.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = setEnv(out, k, in.Resolved.Effective.Env[k])
	}
	return out
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}
