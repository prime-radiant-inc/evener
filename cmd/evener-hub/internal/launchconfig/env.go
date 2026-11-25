package launchconfig

import (
	"maps"

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
	// value. It is how a hub with no usable user configuration still
	// spawns children that resolve the implicit instance set.
	NoUserLayer bool
	// CredentialsPath is passed as EVENER_CREDENTIALS_CONFIG so the child
	// resolves credentials out of the same credentials.toml the hub's pane
	// writes.
	CredentialsPath string
	// ProjectEnvExclusions names child environment variables that feed
	// provider endpoint resolution. The checkout-controlled project layer
	// may not choose them: its value is dropped, and the global or trusted
	// repo layer's value (or a per-launch override) stands instead. Every
	// other project key is preserved.
	ProjectEnvExclusions []string
}

// ToEnv produces the env slice for the spawned `evener serve`. Order of
// precedence per the spec §4.5:
//  1. Per-launch env from Resolved.Effective.Env (last-write-wins).
//  2. Parent process env (typically os.Environ()).
//
// Items earlier in the priority list are applied last so they
// overwrite earlier writes. Provider credentials are not among them: the
// child resolves its own from the registry, the credentials.toml named by
// EVENER_CREDENTIALS_CONFIG, and the environment (spec §10).
func ToEnv(in EnvInputs) []string {
	out := append([]string{}, in.ParentEnv...)
	out = envvars.Set(out, envvars.EVENERHubSpawned.Name, "1")
	if in.RunDir != "" {
		out = envvars.Set(out, envvars.EVENERRunDir.Name, in.RunDir)
	}
	if in.StateDir != "" {
		out = envvars.Set(out, envvars.EVENERStateDir.Name, in.StateDir)
	}
	if in.HubToken != "" {
		out = envvars.Set(out, envvars.EVENERHubToken.Name, in.HubToken)
	}
	switch {
	case in.NoUserLayer:
		out = envvars.Set(out, envvars.EVENERProvidersConfig.Name, "")
	case in.ProvidersConfigPath != "":
		out = envvars.Set(out, envvars.EVENERProvidersConfig.Name, in.ProvidersConfigPath)
	}
	if in.CredentialsPath != "" {
		out = envvars.Set(out, envvars.EVENERCredentialsConfig.Name, in.CredentialsPath)
	}

	// 1. Per-launch env: applied last so it wins, in sorted key order
	//    for determinism.
	return envvars.ApplyOverlay(out, endpointEnvWithoutProjectChoices(in.Resolved, in.ProjectEnvExclusions))
}

// endpointEnvWithoutProjectChoices returns the effective env with the project
// layer's values for excluded endpoint inputs removed. A per-launch value
// still wins, and the global or trusted repo layer's value is restored when it
// had one, so only a checkout-controlled choice is refused.
func endpointEnvWithoutProjectChoices(resolved Resolved, exclusions []string) map[string]string {
	if len(exclusions) == 0 {
		return resolved.Effective.Env
	}
	project := resolved.Layers[LayerProject].Env
	if len(project) == 0 {
		return resolved.Effective.Env
	}
	excluded := make(map[string]struct{}, len(exclusions))
	for _, key := range exclusions {
		excluded[key] = struct{}{}
	}
	out := resolved.Effective.Env
	copied := false
	for key := range project {
		if _, ok := excluded[key]; !ok {
			continue
		}
		if _, fromLaunch := resolved.Layers[LayerLaunch].Env[key]; fromLaunch {
			continue
		}
		if !copied {
			out = maps.Clone(resolved.Effective.Env)
			copied = true
		}
		restored := false
		// mergeLayers refuses a credential-looking key at every layer; a raw
		// layer map still holds the refused entry, so the restore must not
		// reintroduce one.
		if !isCredentialEnvKey(key) {
			for _, name := range []LayerName{LayerRepo, LayerGlobal} {
				if value, ok := resolved.Layers[name].Env[key]; ok {
					out[key] = value
					restored = true
					break
				}
			}
		}
		if !restored {
			delete(out, key)
		}
	}
	return out
}
