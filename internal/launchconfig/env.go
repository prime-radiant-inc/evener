package launchconfig

import (
	"sort"
	"strings"
)

// CredentialResolver is the slice of internal/credentials.Store that
// ToEnv depends on. Decoupled to a small interface so tests don't need
// to construct a real store.
type CredentialResolver interface {
	// APIKeyFor returns the API key value plus the source label
	// ("file", "env", "oauth", "absent"). Empty value means absent.
	APIKeyFor(provider string) (string, string)
}

// EnvInputs bundles everything ToEnv needs.
type EnvInputs struct {
	Resolved            Resolved
	Provider            string
	Creds               CredentialResolver
	ParentEnv           []string // typically os.Environ()
	RunDir              string
	StateDir            string
	HubToken            string
	ProvidersConfigPath string // if set, passed as SERF_PROVIDERS_CONFIG to spawned children
}

// providerEnvVar maps provider name → the canonical env var that serf
// reads for that provider.
var providerEnvVar = map[string]string{
	"openai":               "OPENAI_API_KEY",
	"anthropic":            "ANTHROPIC_API_KEY",
	"google":               "GEMINI_API_KEY",
	"gemini":               "GEMINI_API_KEY",
	"minimax":              "MINIMAX_API_KEY",
	"openrouter":           "OPENROUTER_API_KEY",
	"openrouter-anthropic": "OPENROUTER_API_KEY",
	"kimi":                 "KIMI_API_KEY",
	"glm":                  "GLM_API_KEY",
	"openai-compatible":    "OPENAI_COMPATIBLE_API_KEY",
}

// ToEnv produces the env slice for the spawned `serf serve`. Order of
// precedence per the spec §4.5:
//  1. Per-launch env from Resolved.Effective.Env (last-write-wins).
//  2. The matching credential env var (from Creds).
//  3. Parent process env (typically os.Environ()).
//  4. Provider-specific on-disk OAuth state — handled by serf itself.
//
// Items earlier in the priority list are applied later in setEnv so they
// overwrite earlier writes.
func ToEnv(in EnvInputs) []string {
	out := append([]string{}, in.ParentEnv...)
	out = setEnv(out, "SERF_HUB_SPAWNED", "1")
	if in.RunDir != "" {
		out = setEnv(out, "SERF_RUN_DIR", in.RunDir)
	}
	if in.StateDir != "" {
		out = setEnv(out, "SERF_STATE_DIR", in.StateDir)
	}
	if in.HubToken != "" {
		out = setEnv(out, "SERF_HUB_TOKEN", in.HubToken)
	}
	if in.ProvidersConfigPath != "" {
		out = setEnv(out, "SERF_PROVIDERS_CONFIG", in.ProvidersConfigPath)
	}

	// 2. Credentials store value.
	if envKey, ok := providerEnvVar[strings.ToLower(in.Provider)]; ok && in.Creds != nil {
		if v, _ := in.Creds.APIKeyFor(strings.ToLower(in.Provider)); v != "" {
			out = setEnv(out, envKey, v)
		}
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
