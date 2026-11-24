package launchconfig

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/llm/registry"
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

// The daemon and launch-check resolve providers from this env, so a
// checkout-controlled project layer must not be able to choose either the
// provider file or the Copilot endpoint. This drives the env the hub actually
// hands the child into a real registry load and resolves the Copilot route it
// produces.
func checkToEnv_ProjectLayerCannotRedirectProviderResolution(t *testing.T) {
	hubProviders := filepath.Join(t.TempDir(), "hub-providers.toml")
	checkoutProviders := filepath.Join(t.TempDir(), "checkout-providers.toml")
	for path, baseURL := range map[string]string{
		hubProviders:      "https://hub.example.test",
		checkoutProviders: "https://checkout.example.test",
	} {
		body := "[providers.cp]\nbase = \"github-copilot\"\nbase_url = \"" + baseURL + "\"\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	project, diags := mergeLayers(map[LayerName]Layer{LayerProject: {Env: map[string]string{
		"EVENER_PROVIDERS_CONFIG": checkoutProviders,
		"GITHUB_COPILOT_BASE_URL": "https://checkout.example.test",
	}}})
	if len(diags) != 2 {
		t.Fatalf("project-layer diagnostics = %v, want both keys refused", diags)
	}
	env := envSliceToMap(ToEnv(EnvInputs{
		Resolved:            project,
		ParentEnv:           []string{"PATH=/usr/bin"},
		ProvidersConfigPath: hubProviders,
		StateDir:            "/state",
	}))
	if got := resolvedCopilotBaseURL(t, env); got != "https://hub.example.test" {
		t.Fatalf("Copilot base URL = %q, want the hub provider file's endpoint", got)
	}

	// The parent process and the per-launch layer are user choices; both still
	// select the provider file and the endpoint.
	parent := envSliceToMap(ToEnv(EnvInputs{
		Resolved:  Resolved{Effective: Layer{Env: map[string]string{}}},
		ParentEnv: []string{"PATH=/usr/bin", "GITHUB_COPILOT_BASE_URL=https://parent.example.test"},
	}))
	if got := parent["GITHUB_COPILOT_BASE_URL"]; got != "https://parent.example.test" {
		t.Fatalf("parent-process GITHUB_COPILOT_BASE_URL = %q, want it preserved", got)
	}

	launchProviders := filepath.Join(t.TempDir(), "launch-providers.toml")
	if err := os.WriteFile(launchProviders,
		[]byte("[providers.cp]\nbase = \"github-copilot\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	launch, diags := mergeLayers(map[LayerName]Layer{LayerLaunch: {Env: map[string]string{
		"EVENER_PROVIDERS_CONFIG": launchProviders,
		"GITHUB_COPILOT_BASE_URL": "https://launch.example.test",
	}}})
	if len(diags) != 0 {
		t.Fatalf("launch-layer diagnostics = %v, want none", diags)
	}
	env = envSliceToMap(ToEnv(EnvInputs{
		Resolved:            launch,
		ParentEnv:           []string{"PATH=/usr/bin"},
		ProvidersConfigPath: hubProviders,
		StateDir:            "/state",
	}))
	if got := env["EVENER_PROVIDERS_CONFIG"]; got != launchProviders {
		t.Fatalf("per-launch EVENER_PROVIDERS_CONFIG = %q, want %q", got, launchProviders)
	}
	if got := resolvedCopilotBaseURL(t, env); got != "https://launch.example.test" {
		t.Fatalf("Copilot base URL = %q, want the per-launch endpoint", got)
	}
}

// resolvedCopilotBaseURL loads the provider file the child env selects and
// resolves the Copilot route the child would launch, without materializing a
// credential.
func resolvedCopilotBaseURL(t *testing.T, env map[string]string) string {
	t.Helper()
	reg, err := registry.Load(
		registry.WithStateRoot(t.TempDir()),
		registry.WithOffline(true), registry.WithoutCache(),
		registry.WithEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok }),
	)
	if err != nil {
		t.Fatalf("registry load: %v", err)
	}
	res, err := reg.ResolveInstanceTransport("cp")
	if err != nil {
		t.Fatalf("resolve cp transport: %v", err)
	}
	return res.Transport.BaseURL
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
