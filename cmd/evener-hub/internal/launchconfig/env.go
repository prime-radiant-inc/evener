package launchconfig

import "primeradiant.com/evener/envvars"

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
	return envvars.ApplyOverlay(out, in.Resolved.Effective.Env)
}
