package launchconfig

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
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

// A project TOML env key may itself contain '='. envvars.Set serializes that
// key as "key=value", and a child splits each environment entry at the first
// '=', so the part before '=' becomes the variable's name in the child. A
// malformed project key could therefore alias a reserved name the exact check
// refused. This drives the project layer through real TOML parsing and ToEnv
// into a child process, then resolves the provider file from what that child
// observed.
func checkToEnv_ProjectMalformedEnvNameCannotAliasReservedNames(t *testing.T) {
	writeProviders := func(path, baseURL string) {
		t.Helper()
		body := "[providers.cp]\nbase = \"github-copilot\"\nbase_url = \"" + baseURL + "\"\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	hubProviders := filepath.Join(t.TempDir(), "hub-providers.toml")
	writeProviders(hubProviders, "https://hub.example.test")

	// The malformed key's trailing value lands after a separator the child
	// keeps, so the checkout names a file carrying that trailing '='.
	checkoutProviders := filepath.Join(t.TempDir(), "checkout-providers.toml") + "="
	writeProviders(checkoutProviders, "https://checkout.example.test")

	projectFile := filepath.Join(t.TempDir(), "launch.local.toml")
	malformedProvidersKey := "EVENER_PROVIDERS_CONFIG=" + strings.TrimSuffix(checkoutProviders, "=")
	malformedCopilotKey := "GITHUB_COPILOT_BASE_URL=https://checkout.example.test/"
	body := "[env]\n" +
		fmt.Sprintf("%q = \"\"\n", malformedProvidersKey) +
		fmt.Sprintf("%q = \"\"\n", malformedCopilotKey) +
		"\"PROJECT_KEPT\" = \"kept\"\n"
	if err := os.WriteFile(projectFile, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	project, err := loadLayerFS(afero.NewOsFs(), projectFile)
	if err != nil {
		t.Fatalf("load project layer: %v", err)
	}
	resolved, diags := mergeLayers(map[LayerName]Layer{LayerProject: project})
	env := ToEnv(EnvInputs{
		Resolved:            resolved,
		ParentEnv:           []string{"PATH=/usr/bin"},
		ProvidersConfigPath: hubProviders,
		StateDir:            "/state",
	})

	// A real child is the oracle for the names it receives: exec dedups the
	// environment and the child splits each entry at the first '='.
	cmd := exec.Command("sh", "-c", `printf '%s\n' "$EVENER_PROVIDERS_CONFIG" "$GITHUB_COPILOT_BASE_URL" "$PROJECT_KEPT"`)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("child environment: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("child output = %q, want three values", out)
	}
	if lines[0] != hubProviders {
		t.Fatalf("child EVENER_PROVIDERS_CONFIG = %q, want the hub provider file %q: a malformed project key aliased the reserved name", lines[0], hubProviders)
	}
	if lines[1] != "" {
		t.Fatalf("child GITHUB_COPILOT_BASE_URL = %q, want unset: a malformed project key aliased the reserved name", lines[1])
	}
	if lines[2] != "kept" {
		t.Fatalf("child PROJECT_KEPT = %q, want the valid project key applied", lines[2])
	}
	if len(diags) != 2 {
		t.Fatalf("project-layer diagnostics = %v, want both malformed names rejected", diags)
	}
	for _, diag := range diags {
		if diag.Layer != LayerProject || !strings.HasPrefix(diag.Field, EnvField+".") {
			t.Fatalf("diagnostic = %+v, want the malformed project env name named", diag)
		}
	}

	// What the child observed is the environment the registry resolves.
	childEnv := map[string]string{"EVENER_PROVIDERS_CONFIG": lines[0]}
	if lines[1] != "" {
		childEnv["GITHUB_COPILOT_BASE_URL"] = lines[1]
	}
	if host := resolvedCopilotHost(t, childEnv); host != "hub.example.test" {
		t.Fatalf("Copilot endpoint host = %q, want the hub provider file's host", host)
	}
}

func resolvedCopilotHost(t *testing.T, env map[string]string) string {
	t.Helper()
	baseURL := resolvedCopilotBaseURL(t, env)
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" {
		t.Fatalf("Copilot base URL = %q: parse error %v", baseURL, err)
	}
	return u.Hostname()
}

// checkToEnv_ProjectEndpointExclusionsKeepTrustedValues pins the layering
// around an endpoint input the project layer may not choose: a per-launch
// override still wins, a trusted layer's value is restored, an inherited
// parent value survives, and every unrelated project key is untouched.
func checkToEnv_ProjectEndpointExclusionsKeepTrustedValues(t *testing.T) {
	const key = "CUSTOM_ENDPOINT_URL"
	resolved, diags := mergeLayers(map[LayerName]Layer{
		LayerGlobal:  {Env: map[string]string{key: "https://global.example.test/v1", "GLOBAL_KEPT": "global"}},
		LayerRepo:    {Env: map[string]string{key: "https://repo.example.test/v1", "REPO_KEPT": "repo"}},
		LayerProject: {Env: map[string]string{key: "https://checkout.example.test/v1", "PROJECT_KEPT": "kept"}},
	})
	if len(diags) != 0 {
		t.Fatalf("merge diagnostics = %v, want none for an ordinary endpoint variable", diags)
	}
	env := envSliceToMap(ToEnv(EnvInputs{
		Resolved:             resolved,
		ParentEnv:            []string{key + "=https://parent.example.test/v1"},
		ProjectEnvExclusions: []string{key},
	}))
	if got := env[key]; got != "https://repo.example.test/v1" {
		t.Fatalf("%s = %q, want the trusted repo layer's value restored over the global one", key, got)
	}
	if env["PROJECT_KEPT"] != "kept" || env["GLOBAL_KEPT"] != "global" {
		t.Fatalf("unrelated env keys = %v, want the project and global keys preserved", env)
	}

	resolved, diags = mergeLayers(map[LayerName]Layer{
		LayerGlobal:  {Env: map[string]string{key: "https://global.example.test/v1"}},
		LayerProject: {Env: map[string]string{key: "https://checkout.example.test/v1"}},
		LayerLaunch:  {Env: map[string]string{key: "https://launch.example.test/v1"}},
	})
	if len(diags) != 0 {
		t.Fatalf("merge diagnostics = %v, want none", diags)
	}
	env = envSliceToMap(ToEnv(EnvInputs{
		Resolved:             resolved,
		ProjectEnvExclusions: []string{key},
	}))
	if got := env[key]; got != "https://launch.example.test/v1" {
		t.Fatalf("%s = %q, want the per-launch override kept", key, got)
	}

	resolved, diags = mergeLayers(map[LayerName]Layer{
		LayerProject: {Env: map[string]string{key: "https://checkout.example.test/v1"}},
	})
	if len(diags) != 0 {
		t.Fatalf("merge diagnostics = %v, want none", diags)
	}
	env = envSliceToMap(ToEnv(EnvInputs{
		Resolved:             resolved,
		ParentEnv:            []string{key + "=https://parent.example.test/v1"},
		ProjectEnvExclusions: []string{key},
	}))
	if got := env[key]; got != "https://parent.example.test/v1" {
		t.Fatalf("%s = %q, want the inherited parent value kept", key, got)
	}
}

// TestToEnvProjectEnvExclusionsFromAlternateProviderFile derives the excluded
// endpoint inputs from the trusted alternate provider file the child reads
// rather than from a hub registry that never saw it. The project layer may not
// choose the variable, while the global, trusted repo, per-launch and parent
// values all still reach the child, and an unrelated project key survives.
func TestToEnvProjectEnvExclusionsFromAlternateProviderFile(t *testing.T) {
	const (
		key      = "COPILOT_UPSTREAM_URL"
		trusted  = "https://trusted.example.test"
		checkout = "https://checkout.example.test"
		launch   = "https://launch.example.test"
	)
	providers := filepath.Join(t.TempDir(), "providers.toml")
	if err := os.WriteFile(providers, []byte("[providers.cp]\nbase = \"github-copilot\"\nvars_env = { BASE_URL = \""+key+"\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.Load(
		registry.WithConfigPath(providers),
		registry.WithStateRoot(t.TempDir()),
		registry.WithOffline(true), registry.WithoutCache(),
	)
	if err != nil {
		t.Fatalf("registry load: %v", err)
	}
	exclusions := reg.EndpointEnvNames()
	found := false
	for _, name := range exclusions {
		if name == key {
			found = true
		}
	}
	if !found {
		t.Fatalf("endpoint exclusions = %v, want the alternate file's %s", exclusions, key)
	}

	projectEnv := map[string]string{key: checkout, "PROJECT_KEPT": "kept"}
	cases := []struct {
		name   string
		layers map[LayerName]Layer
		parent []string
		want   string
	}{
		{
			name: "global override",
			layers: map[LayerName]Layer{
				LayerGlobal:  {Env: map[string]string{key: trusted, "GLOBAL_KEPT": "global"}},
				LayerProject: {Env: projectEnv},
			},
			parent: []string{"PATH=/usr/bin"},
			want:   trusted,
		},
		{
			name: "trusted repo override",
			layers: map[LayerName]Layer{
				LayerGlobal:  {Env: map[string]string{key: "https://global.example.test", "GLOBAL_KEPT": "global"}},
				LayerRepo:    {Env: map[string]string{key: trusted}},
				LayerProject: {Env: projectEnv},
			},
			parent: []string{"PATH=/usr/bin"},
			want:   trusted,
		},
		{
			name: "per-launch override",
			layers: map[LayerName]Layer{
				LayerProject: {Env: projectEnv},
				LayerLaunch:  {Env: map[string]string{key: launch}},
			},
			parent: []string{"PATH=/usr/bin"},
			want:   launch,
		},
		{
			name: "parent override",
			layers: map[LayerName]Layer{
				LayerProject: {Env: projectEnv},
			},
			parent: []string{"PATH=/usr/bin", key + "=" + trusted},
			want:   trusted,
		},
		{
			name: "project only",
			layers: map[LayerName]Layer{
				LayerProject: {Env: projectEnv},
			},
			parent: []string{"PATH=/usr/bin"},
			want:   "https://api.githubcopilot.com",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolved, diags := mergeLayers(tc.layers)
			if len(diags) != 0 {
				t.Fatalf("merge diagnostics = %v, want none", diags)
			}
			env := envSliceToMap(ToEnv(EnvInputs{
				Resolved:             resolved,
				ParentEnv:            tc.parent,
				ProvidersConfigPath:  providers,
				ProjectEnvExclusions: exclusions,
			}))
			if got := resolvedCopilotBaseURL(t, env); got != tc.want {
				t.Fatalf("Copilot base URL = %q, want %q: the project layer chose the endpoint input", got, tc.want)
			}
			if env["PROJECT_KEPT"] != "kept" {
				t.Fatalf("PROJECT_KEPT = %q, want the unrelated project key preserved", env["PROJECT_KEPT"])
			}
		})
	}
}
