package launchconfig

import (
	"testing"
)

func checkToEnv_BaselineSetsRunStateAndToken(t *testing.T) {
	parent := []string{"PATH=/usr/bin"}
	r := Resolved{Effective: Layer{Model: "anthropic/claude-1", Env: map[string]string{"FOO": "bar"}}}
	got := ToEnv(EnvInputs{
		Resolved:  r,
		ParentEnv: parent,
		RunDir:    "/run",
		StateDir:  "/state",
		HubToken:  "tok",
	})
	want := map[string]string{
		"PATH":               "/usr/bin",
		"FOO":                "bar",
		"EVENER_HUB_SPAWNED": "1",
		"EVENER_RUN_DIR":     "/run",
		"EVENER_STATE_DIR":   "/state",
		"EVENER_HUB_TOKEN":   "tok",
	}
	gotMap := envSliceToMap(got)
	for k, v := range want {
		if gotMap[k] != v {
			t.Errorf("env[%s] = %q, want %q", k, gotMap[k], v)
		}
	}
}

func checkToEnv_PerLaunchEnvBeatsParentEnv(t *testing.T) {
	r := Resolved{Effective: Layer{Env: map[string]string{"ANTHROPIC_API_KEY": "from-overrides"}}}
	got := envSliceToMap(ToEnv(EnvInputs{
		Resolved:  r,
		ParentEnv: []string{"ANTHROPIC_API_KEY=from-parent"},
	}))
	if got["ANTHROPIC_API_KEY"] != "from-overrides" {
		t.Errorf("per-launch env should win: %q", got["ANTHROPIC_API_KEY"])
	}
}

// checkToEnv_AddsOnlyItsOwnControls is the guard against re-introducing
// credential injection: the child resolves every provider credential itself,
// from the registry and the store it is pointed at, so the only keys the hub
// adds beyond the parent environment are its own controls and the per-launch
// env the user authored (spec §10).
func checkToEnv_AddsOnlyItsOwnControls(t *testing.T) {
	parent := []string{"PATH=/usr/bin"}
	got := envSliceToMap(ToEnv(EnvInputs{
		Resolved:            Resolved{Effective: Layer{Env: map[string]string{"FOO": "bar"}}},
		ParentEnv:           parent,
		RunDir:              "/run",
		StateDir:            "/state",
		HubToken:            "tok",
		ProvidersConfigPath: "/cfg/providers.toml",
		CredentialsPath:     "/cfg/credentials.toml",
	}))
	allowed := map[string]bool{
		"PATH": true, "FOO": true,
		"EVENER_HUB_SPAWNED": true, "EVENER_RUN_DIR": true, "EVENER_STATE_DIR": true,
		"EVENER_HUB_TOKEN": true, "EVENER_PROVIDERS_CONFIG": true, "EVENER_CREDENTIALS_CONFIG": true,
	}
	for k := range got {
		if !allowed[k] {
			t.Errorf("child env carries unexpected %s: the hub injects no provider credentials", k)
		}
	}
}

func checkToEnv_ProvidersConfigPathSetsEnvVar(t *testing.T) {
	got := envSliceToMap(ToEnv(EnvInputs{
		ProvidersConfigPath: "/hub/.evener/providers.toml",
		ParentEnv:           []string{"PATH=/usr/bin"},
	}))
	if got["EVENER_PROVIDERS_CONFIG"] != "/hub/.evener/providers.toml" {
		t.Errorf("EVENER_PROVIDERS_CONFIG = %q, want /hub/.evener/providers.toml", got["EVENER_PROVIDERS_CONFIG"])
	}
}

func checkToEnv_NoProvidersConfigPathDoesNotSetEnvVar(t *testing.T) {
	got := envSliceToMap(ToEnv(EnvInputs{
		ParentEnv: []string{"PATH=/usr/bin"},
	}))
	if _, ok := got["EVENER_PROVIDERS_CONFIG"]; ok {
		t.Errorf("EVENER_PROVIDERS_CONFIG should not be set when ProvidersConfigPath is empty, got %q", got["EVENER_PROVIDERS_CONFIG"])
	}
}

// checkToEnv_NoUserLayerSetsEmptyProvidersConfig is spec §10's third state:
// a hub whose providers.toml failed to load hands every child a present but
// empty EVENER_PROVIDERS_CONFIG, replacing whatever it inherited, so the
// child computes the same implicit-only instance set the hub is running on.
func checkToEnv_NoUserLayerSetsEmptyProvidersConfig(t *testing.T) {
	got := ToEnv(EnvInputs{
		NoUserLayer:         true,
		ProvidersConfigPath: "/hub/.evener/providers.toml",
		ParentEnv:           []string{"PATH=/usr/bin", "EVENER_PROVIDERS_CONFIG=/inherited/providers.toml"},
	})
	found := false
	for _, kv := range got {
		if kv == "EVENER_PROVIDERS_CONFIG=" {
			found = true
		}
		if kv == "EVENER_PROVIDERS_CONFIG=/inherited/providers.toml" {
			t.Error("the inherited EVENER_PROVIDERS_CONFIG survived; it must be replaced")
		}
	}
	if !found {
		t.Errorf("EVENER_PROVIDERS_CONFIG= (present, empty) missing from %v", got)
	}
}

func checkToEnv_CredentialsPathSetsEnvVar(t *testing.T) {
	got := envSliceToMap(ToEnv(EnvInputs{
		CredentialsPath: "/hub/.evener/credentials.toml",
		ParentEnv:       []string{"PATH=/usr/bin"},
	}))
	if got["EVENER_CREDENTIALS_CONFIG"] != "/hub/.evener/credentials.toml" {
		t.Errorf("EVENER_CREDENTIALS_CONFIG = %q, want /hub/.evener/credentials.toml", got["EVENER_CREDENTIALS_CONFIG"])
	}
}

func checkToEnv_DoesNotIntroduceObsoleteRawHTTPLogging(t *testing.T) {
	got := envSliceToMap(ToEnv(EnvInputs{
		ParentEnv: []string{"PATH=/usr/bin"},
	}))
	if _, ok := got["EVENER_LOG_RAW_HTTP"]; ok {
		t.Fatal("child environment unexpectedly introduced the obsolete raw HTTP logging control")
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
