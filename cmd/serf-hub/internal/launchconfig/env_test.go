package launchconfig

import (
	"testing"

	"primeradiant.com/serf/envvars"
)

type stubCreds struct {
	keys map[string]string
}

func (s stubCreds) APIKeyFor(provider string) (string, string) {
	v, ok := s.keys[provider]
	if !ok {
		return "", "absent"
	}
	return v, "file"
}

func TestToEnv_BaselineSetsRunStateAndProvider(t *testing.T) {
	parent := []string{"PATH=/usr/bin"}
	r := Resolved{Effective: Layer{Model: "anthropic/claude-1", Env: map[string]string{"FOO": "bar"}}}
	creds := stubCreds{keys: map[string]string{"anthropic": "sk-ant-FROM-FILE"}}
	got := ToEnv(EnvInputs{
		Resolved:  r,
		Provider:  "anthropic",
		Creds:     creds,
		ParentEnv: parent,
		RunDir:    "/run",
		StateDir:  "/state",
		HubToken:  "tok",
	})
	want := map[string]string{
		"PATH":              "/usr/bin",
		"FOO":               "bar",
		"SERF_HUB_SPAWNED":  "1",
		"SERF_RUN_DIR":      "/run",
		"SERF_STATE_DIR":    "/state",
		"SERF_HUB_TOKEN":    "tok",
		"ANTHROPIC_API_KEY": "sk-ant-FROM-FILE",
	}
	gotMap := envSliceToMap(got)
	for k, v := range want {
		if gotMap[k] != v {
			t.Errorf("env[%s] = %q, want %q", k, gotMap[k], v)
		}
	}
}

func TestToEnv_PerLaunchEnvBeatsCredsStore(t *testing.T) {
	r := Resolved{Effective: Layer{Env: map[string]string{"ANTHROPIC_API_KEY": "from-overrides"}}}
	creds := stubCreds{keys: map[string]string{"anthropic": "from-file"}}
	got := envSliceToMap(ToEnv(EnvInputs{
		Resolved: r, Provider: "anthropic", Creds: creds,
	}))
	if got["ANTHROPIC_API_KEY"] != "from-overrides" {
		t.Errorf("per-launch env should win: %q", got["ANTHROPIC_API_KEY"])
	}
}

func TestToEnv_OpenAICompatibleCredentialUsesAPIKeyEnv(t *testing.T) {
	r := Resolved{Effective: Layer{Env: map[string]string{
		"OPENAI_COMPATIBLE_BASE_URL": "https://compat.example.test/v1",
	}}}
	creds := stubCreds{keys: map[string]string{"openai-compatible": "compat-key"}}
	got := envSliceToMap(ToEnv(EnvInputs{
		Resolved: r,
		Provider: "openai-compatible",
		Creds:    creds,
	}))
	if got["OPENAI_COMPATIBLE_API_KEY"] != "compat-key" {
		t.Errorf("OPENAI_COMPATIBLE_API_KEY = %q, want compat-key", got["OPENAI_COMPATIBLE_API_KEY"])
	}
	if got["OPENAI_COMPATIBLE_BASE_URL"] != "https://compat.example.test/v1" {
		t.Errorf("OPENAI_COMPATIBLE_BASE_URL = %q", got["OPENAI_COMPATIBLE_BASE_URL"])
	}
}

func TestToEnv_NoProviderNoInjection(t *testing.T) {
	creds := stubCreds{keys: map[string]string{"anthropic": "x"}}
	got := envSliceToMap(ToEnv(EnvInputs{Provider: "", Creds: creds}))
	if _, ok := got["ANTHROPIC_API_KEY"]; ok {
		t.Errorf("no provider, no injection; got %v", got)
	}
}

func TestToEnv_OpenAIStoredKeyInjectsOpenAIAPIKey(t *testing.T) {
	creds := stubCreds{keys: map[string]string{"openai": "sk-FROM-FILE"}}
	got := envSliceToMap(ToEnv(EnvInputs{
		Provider:  "openai",
		Creds:     creds,
		ParentEnv: []string{"PATH=/usr/bin"},
	}))
	if got["OPENAI_API_KEY"] != "sk-FROM-FILE" {
		t.Errorf("OPENAI_API_KEY = %q, want sk-FROM-FILE", got["OPENAI_API_KEY"])
	}
}

func TestToEnv_ProvidersConfigPathSetsEnvVar(t *testing.T) {
	got := envSliceToMap(ToEnv(EnvInputs{
		ProvidersConfigPath: "/hub/.serf/providers.toml",
		ParentEnv:           []string{"PATH=/usr/bin"},
	}))
	if got["SERF_PROVIDERS_CONFIG"] != "/hub/.serf/providers.toml" {
		t.Errorf("SERF_PROVIDERS_CONFIG = %q, want /hub/.serf/providers.toml", got["SERF_PROVIDERS_CONFIG"])
	}
}

func TestToEnv_NoProvidersConfigPathDoesNotSetEnvVar(t *testing.T) {
	got := envSliceToMap(ToEnv(EnvInputs{
		ParentEnv: []string{"PATH=/usr/bin"},
	}))
	if _, ok := got["SERF_PROVIDERS_CONFIG"]; ok {
		t.Errorf("SERF_PROVIDERS_CONFIG should not be set when ProvidersConfigPath is empty, got %q", got["SERF_PROVIDERS_CONFIG"])
	}
}

func TestToEnv_RawHTTPLoggingSetsRawLogEnv(t *testing.T) {
	got := envSliceToMap(ToEnv(EnvInputs{
		Resolved: Resolved{Effective: Layer{RawHTTPLogging: ptrBool(true)}},
		ParentEnv: []string{
			"PATH=/usr/bin",
			envvars.SERFLogRawHTTP.Assignment("0"),
		},
	}))
	if got[envvars.SERFLogRawHTTP.Name] != "1" {
		t.Fatalf("%s = %q, want 1", envvars.SERFLogRawHTTP.Name, got[envvars.SERFLogRawHTTP.Name])
	}
}

func TestToEnv_RawHTTPLoggingFalseOverridesInheritedEnv(t *testing.T) {
	got := envSliceToMap(ToEnv(EnvInputs{
		Resolved: Resolved{Effective: Layer{RawHTTPLogging: ptrBool(false)}},
		ParentEnv: []string{
			"PATH=/usr/bin",
			envvars.SERFLogRawHTTP.Assignment("1"),
		},
	}))
	if got[envvars.SERFLogRawHTTP.Name] != "0" {
		t.Fatalf("%s = %q, want 0", envvars.SERFLogRawHTTP.Name, got[envvars.SERFLogRawHTTP.Name])
	}
}

func TestToEnv_RawHTTPLoggingUnsetPreservesInheritedEnv(t *testing.T) {
	got := envSliceToMap(ToEnv(EnvInputs{
		ParentEnv: []string{
			"PATH=/usr/bin",
			envvars.SERFLogRawHTTP.Assignment("1"),
		},
	}))
	if got[envvars.SERFLogRawHTTP.Name] != "1" {
		t.Fatalf("%s = %q, want inherited value", envvars.SERFLogRawHTTP.Name, got[envvars.SERFLogRawHTTP.Name])
	}
}

func envSliceToMap(env []string) map[string]string {
	out := map[string]string{}
	for _, kv := range env {
		i := 0
		for ; i < len(kv); i++ {
			if kv[i] == '=' {
				break
			}
		}
		if i >= len(kv) {
			continue
		}
		out[kv[:i]] = kv[i+1:]
	}
	return out
}
