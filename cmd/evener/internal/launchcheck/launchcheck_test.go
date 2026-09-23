package launchcheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/internal/valueexpr"
	"primeradiant.com/evener/llm"
	_ "primeradiant.com/evener/llm/providers/all"
	"primeradiant.com/evener/llm/registry"
)

// launchCheckRegistry installs a client built from the given providers.toml
// body on the launchCheckLoadClient seam.
//
// The client is the real one — cmdutil.LoadRegistry over the real providers
// file, no mocks — but its environment is a fixed table rather than the
// machine's. That matters because launchCheckModels lists every visible
// instance and implicit instances are conjured from the environment: an
// ambient TOGETHER_API_KEY would otherwise put api.together.ai in the loop.
// The one variable the table answers is OLLAMA_HOST, whose instance needs no
// credential and is therefore always visible; it points at a closed port so
// its listing fails instantly instead of reaching a real daemon.
func launchCheckRegistry(t *testing.T, cfg string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "providers.toml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	stateRoot := t.TempDir()
	env := map[string]string{"OLLAMA_HOST": "127.0.0.1:1"}

	withLaunchCheckLoadClient(t, func(stateDir string) (*llm.Client, error) {
		r, _, err := cmdutil.LoadRegistry(
			registry.WithConfigPath(cfgPath),
			registry.WithStateRoot(stateRoot),
			registry.WithOffline(true), registry.WithoutCache(),
			registry.WithEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok }),
		)
		if err != nil {
			return nil, err
		}
		return cmdutil.NewRegistryClient(r, stateDir), nil
	})
}

// launchCheckGateway starts a /models endpoint answering with status and body,
// declares it as the keyed "gw" instance in an isolated providers.toml, and
// installs a client built from that file on the launchCheckLoadClient seam.
func launchCheckGateway(t *testing.T, status int, body string, gwExtra ...string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	launchCheckRegistry(t, "[providers.gw]\nbase     = \"openai-compatible\"\nbase_url = \""+srv.URL+"/v1\"\napi_key  = \"test-key\"\n"+strings.Join(gwExtra, ""))
}

// launchCheckKeylessInstance declares an explicit instance on the openai
// preset with no key in the fixture's fixed environment: an explicit instance
// stays visible without a credential, so its listing runs and fails at the
// credential check before any request can leave the process.
func launchCheckKeylessInstance(t *testing.T) {
	t.Helper()
	launchCheckRegistry(t, "[providers.gw]\nbase = \"openai\"\n")
}

// decodeDiagnostics runs the launch check for the models contract and decodes
// the diagnostics it printed to stdout.
func decodeDiagnostics(t *testing.T) []appwire.ModelListDiagnostic {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := RunLaunchCheck([]string{
		"--protocol", appwire.ProtocolVersion,
		"--models",
		"--json",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runLaunchCheck: %v stderr=%s", err, stderr.String())
	}
	var out struct {
		Diagnostics []appwire.ModelListDiagnostic `json:"diagnostics"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout.String(), err)
	}
	return out.Diagnostics
}

// gwDiagnostic returns the gw row of the models contract's diagnostics,
// failing the test when the listing produced none.
func gwDiagnostic(t *testing.T) appwire.ModelListDiagnostic {
	t.Helper()
	for _, got := range decodeDiagnostics(t) {
		if got.Provider == "gw" {
			return got
		}
	}
	t.Fatalf("diagnostics missing the gw entry")
	return appwire.ModelListDiagnostic{}
}

func TestLaunchCheckReportsProtocolAndValidatedModel(t *testing.T) {
	launchCheckGateway(t, http.StatusOK, `{"data":[{"id":"glm-5"}]}`)

	var stdout, stderr bytes.Buffer
	err := RunLaunchCheck([]string{
		"--protocol", appwire.ProtocolVersion,
		"--model", "gw/glm-5",
		"--json",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runLaunchCheck: %v stderr=%s", err, stderr.String())
	}
	var out struct {
		Protocol    string   `json:"protocol"`
		Provider    string   `json:"provider"`
		Model       string   `json:"model"`
		LaunchFlags []string `json:"launch_flags"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout.String(), err)
	}
	if out.Protocol != appwire.ProtocolVersion || out.Provider != "gw" || out.Model != "glm-5" {
		t.Fatalf("launch check output=%+v", out)
	}
	if !slices.Contains(out.LaunchFlags, "api-log") {
		t.Fatalf("launch_flags=%v, want it to advertise api-log", out.LaunchFlags)
	}
	// Literal check: catches a change to the ProtocolVersion constant value.
	if out.Protocol != "evener-appwire-v5" {
		t.Fatalf("out.Protocol=%q, want \"evener-appwire-v5\"", out.Protocol)
	}
}

func TestLaunchCheckRejectsPreviousProtocolVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := RunLaunchCheck([]string{"--protocol", "evener-appwire-v2", "--json"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "unsupported appwire protocol") {
		t.Fatalf("RunLaunchCheck error = %v, want previous-protocol rejection", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("RunLaunchCheck wrote a contract for an incompatible protocol: %q", stdout.String())
	}
}

func TestLaunchCheckListsLiveModelsFromConfiguredProviders(t *testing.T) {
	launchCheckGateway(t, http.StatusOK, `{"data":[{"id":"gpt-live"},{"id":"text-embedding-3-small"}]}`)

	var stdout, stderr bytes.Buffer
	err := RunLaunchCheck([]string{
		"--protocol", appwire.ProtocolVersion,
		"--models",
		"--json",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runLaunchCheck: %v stderr=%s", err, stderr.String())
	}
	var out struct {
		Models []struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"models"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout.String(), err)
	}
	gw := map[string]bool{}
	for _, m := range out.Models {
		if m.Provider == "gw" {
			gw[m.Model] = true
		}
	}
	if !gw["gpt-live"] {
		t.Fatalf("models=%+v, want the served gpt-live", out.Models)
	}
	if gw["text-embedding-3-small"] {
		t.Fatalf("models=%+v, want the embedding id filtered out", out.Models)
	}
}

func TestLaunchCheckReportsModelEnumerationDiagnostics(t *testing.T) {
	launchCheckGateway(t, http.StatusForbidden, `{"error":"forbidden"}`)

	got := gwDiagnostic(t)
	// The picker prints the message inline ("gw — <message>"), so a listing
	// failure reports its class, not the endpoint's prose.
	if got.Source != "provider" || got.Title != "Provider error" || got.Message != "HTTP 403" {
		t.Fatalf("diagnostic=%+v", got)
	}
}

// The picker prints a provider's listing diagnostic inline under the model
// list. An endpoint that answers 404 with an HTML error page must not put
// that page's content in the list: the line stops at the status.
func TestLaunchCheckDiagnosticStopsA404PageAtTheStatusLine(t *testing.T) {
	page := "<html><head><title>404 Not Found</title></head><body>nginx: no such path</body></html>"
	launchCheckGateway(t, http.StatusNotFound, page)

	if got := gwDiagnostic(t); got.Message != "HTTP 404" {
		t.Fatalf("diagnostic message=%q, want the status line without the page content", got.Message)
	}
}

// A keyless explicit instance fails its listing at the credential check. The
// diagnostic must report the class ("no credential"), not the registry's
// whole remediation warning, which the picker would print under the list.
func TestLaunchCheckDiagnosticCompactsAMissingCredential(t *testing.T) {
	launchCheckKeylessInstance(t)

	if got := gwDiagnostic(t); got.Message != "no credential" {
		t.Fatalf("diagnostic message=%q, want the compact no-credential class", got.Message)
	}
}

// The google protocol buries a classified HTTP error under a ConfigurationError
// (reclassifyGemini's regional-Vertex remap, whose message quotes the provider
// verbatim). The picker's line must still stop at the wrapped status, not the
// wrapped prose.
func TestLaunchCheckDiagnosticFindsAStatusUnderAConfigurationWrapper(t *testing.T) {
	inner := llm.ClassifyHTTPError("models.list", http.StatusNotFound, nil,
		[]byte("Publisher model `projects/p/locations/us-central1/models/gemini-x` was not found"),
		registry.Resolved{Instance: "vtx"})
	err := &llm.ConfigurationError{
		Message: "a global-only model under a regional location needs `global`; provider said: " + inner.Error(),
		Cause:   inner,
	}

	if got := launchCheckModelDiagnostic("vtx", err).Message; got != "HTTP 404" {
		t.Fatalf("diagnostic message=%q, want the wrapped HTTP 404 class", got)
	}
}

// A joined error's branches must not hide a status. The errors.Join node
// answers only Unwrap() []error — which errors.Unwrap cannot descend into —
// and errors.As stops at the first llm.Error in branch order, so a status
// behind a status-zero sibling in the join needs a branch-aware walk.
func TestLaunchCheckDiagnosticFindsAStatusBehindAJoinedSibling(t *testing.T) {
	inner := llm.ClassifyHTTPError("models.list", http.StatusNotFound, nil,
		[]byte("Publisher model `projects/p/locations/us-central1/models/gemini-x` was not found"),
		registry.Resolved{Instance: "vtx"})
	err := &llm.ConfigurationError{
		Message: "a global-only model under a regional location needs `global`; provider said: " + inner.Error(),
		Cause:   errors.Join(&llm.ConfigurationError{Message: "regional endpoint unusable"}, inner),
	}

	if got := launchCheckModelDiagnostic("vtx", err).Message; got != "HTTP 404" {
		t.Fatalf("diagnostic message=%q, want the status from the joined sibling branch", got)
	}
}

// An exhausted allowance is its own class, more specific than the status it
// arrives on (429, or a provider's 403 billing-cycle exhaustion): the line
// names the spent allowance, and the distinct title must survive the
// compaction too.
func TestLaunchCheckDiagnosticNamesAnExhaustedAllowance(t *testing.T) {
	body := []byte(`{"error":{"code":"usage_limit_reached","message":"The usage limit has been reached"}}`)
	err := llm.ClassifyHTTPError("models.list", http.StatusTooManyRequests, nil, body, registry.Resolved{Instance: "gw"})

	diag := launchCheckModelDiagnostic("gw", err)
	if diag.Message != "usage limit reached" {
		t.Fatalf("diagnostic message=%q, want the exhausted-allowance class", diag.Message)
	}
	if diag.Title != "Usage limit reached" {
		t.Fatalf("diagnostic title=%q, want the distinct usage-limit title", diag.Title)
	}
}

// The quota class must not swallow every 429: an ordinary rate limit keeps
// the bare status, or a throttled listing would read as a spent allowance.
func TestLaunchCheckDiagnosticKeepsAnOrdinaryRateLimitAtTheStatus(t *testing.T) {
	body := []byte(`{"error":{"code":"rate_limit_exceeded","message":"Rate limit reached for gpt-4o"}}`)
	err := llm.ClassifyHTTPError("models.list", http.StatusTooManyRequests, nil, body, registry.Resolved{Instance: "gw"})

	if got := launchCheckModelDiagnostic("gw", err).Message; got != "HTTP 429" {
		t.Fatalf("diagnostic message=%q, want the bare status for an ordinary rate limit", got)
	}
}

func TestLaunchCheckModelDiagnosticRedactsEnvSecrets(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	t.Setenv("OPENAI_API_KEY", "sk-launch-secret")
	diag := launchCheckModelDiagnostic("openai", errors.New("provider rejected credential sk-launch-secret in https://sk-launch-secret@example.test"))
	if strings.Contains(diag.Message, "sk-launch-secret") {
		t.Fatalf("diagnostic leaked secret: %+v", diag)
	}
	if !strings.Contains(diag.Message, "[redacted]") {
		t.Fatalf("diagnostic did not redact secret: %+v", diag)
	}
}

func TestLaunchCheckRejectsModelMissingFromLiveProviderList(t *testing.T) {
	launchCheckGateway(t, http.StatusOK, `{"data":[{"id":"gpt-live"}]}`)

	var stdout, stderr bytes.Buffer
	err := RunLaunchCheck([]string{
		"--protocol", appwire.ProtocolVersion,
		"--model", "gw/gpt-stale",
		"--json",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected stale model rejection")
	}
	if !strings.Contains(err.Error(), "model gw/gpt-stale is not available") {
		t.Fatalf("error=%v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout=%q, want empty on failure", stdout.String())
	}
}

func TestLaunchCheckAcceptsModelWhenProviderCannotEnumerateModels(t *testing.T) {
	launchCheckGateway(t, http.StatusForbidden, `{"error":"forbidden"}`)

	var stdout, stderr bytes.Buffer
	err := RunLaunchCheck([]string{
		"--protocol", appwire.ProtocolVersion,
		"--model", "gw/gpt-5.5",
		"--json",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runLaunchCheck: %v stderr=%s", err, stderr.String())
	}
	var out struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout.String(), err)
	}
	if out.Provider != "gw" || out.Model != "gpt-5.5" {
		t.Fatalf("launch check output=%+v", out)
	}
}

func TestLaunchCheckRejectsProtocolMismatch(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := RunLaunchCheck([]string{
		"--protocol", "evener-appwire-v0",
		"--model", "gw/free",
		"--json",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected protocol mismatch")
	}
	if !strings.Contains(err.Error(), "unsupported appwire protocol") {
		t.Fatalf("error=%v", err)
	}
}

// A model ref naming an instance the registry does not have is rejected by
// the resolver's own error.
func TestLaunchCheckRejectsUnknownInstance(t *testing.T) {
	launchCheckGateway(t, http.StatusOK, `{"data":[{"id":"glm-5"}]}`)

	var stdout, stderr bytes.Buffer
	err := RunLaunchCheck([]string{
		"--protocol", appwire.ProtocolVersion,
		"--model", "missing/some-model",
		"--json",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected unknown instance error")
	}
	if !strings.Contains(err.Error(), "unknown instance") {
		t.Fatalf("error=%v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout=%q, want empty on failure", stdout.String())
	}
}

// TestLaunchCheckSeesOnlyTheDeclaredInstances pins the suite's hermeticity:
// the launch check lists every visible registry instance, and implicit
// instances are conjured from the environment, so a provider key that happens
// to be exported on the machine running the tests would put a real endpoint in
// the loop. The fixture's client must see the declared gateway and nothing
// else that could be reached over the network.
func TestLaunchCheckSeesOnlyTheDeclaredInstances(t *testing.T) {
	// A key the old hand-listed sweep did not clear.
	t.Setenv("TOGETHER_API_KEY", "sk-ambient-must-not-leak")
	launchCheckGateway(t, http.StatusOK, `{"data":[{"id":"glm-5"}]}`)

	client, err := launchCheckLoadClient("")
	if err != nil {
		t.Fatalf("launchCheckLoadClient: %v", err)
	}
	for _, inst := range client.Registry().Instances() {
		if inst.Hidden {
			continue
		}
		if inst.Name != "gw" && inst.Name != "ollama" {
			t.Errorf("visible instance %q (base URL %q) came from the ambient environment; the launch check would list it over the network",
				inst.Name, inst.BaseURL)
		}
	}
}

// TestLaunchCheckCarriesResolvedWarnings proves the resolved-row notes the
// registry attaches (a global-only Gemini under a regional Vertex location)
// survive into the launch contract's models, which is the hub picker's primary
// source. A config built on the vertex preset with models_endpoint "-" makes
// the listing registry-only: no network, but the rows resolve with warnings.
func TestLaunchCheckCarriesResolvedWarnings(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "providers.toml")
	if err := os.WriteFile(cfgPath, []byte(`
[providers.vtx]
base = "google-vertex"
models_endpoint = "-"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	stateRoot := t.TempDir()
	env := map[string]string{
		"GOOGLE_VERTEX_PROJECT":  "p",
		"GOOGLE_VERTEX_LOCATION": "us-central1",
		"OLLAMA_HOST":            "127.0.0.1:1",
	}

	old := launchCheckLoadClient
	t.Cleanup(func() { launchCheckLoadClient = old })
	launchCheckLoadClient = func(stateDir string) (*llm.Client, error) {
		r, _, err := cmdutil.LoadRegistry(
			registry.WithConfigPath(cfgPath),
			registry.WithStateRoot(stateRoot),
			registry.WithOffline(true), registry.WithoutCache(),
			registry.WithEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok }),
		)
		if err != nil {
			return nil, err
		}
		return cmdutil.NewRegistryClient(r, stateDir), nil
	}

	var stdout, stderr bytes.Buffer
	err := RunLaunchCheck([]string{
		"--protocol", appwire.ProtocolVersion,
		"--models",
		"--json",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runLaunchCheck: %v stderr=%s", err, stderr.String())
	}
	var out struct {
		Models []struct {
			Provider string   `json:"provider"`
			Model    string   `json:"model"`
			Warnings []string `json:"warnings"`
		} `json:"models"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout.String(), err)
	}
	for _, m := range out.Models {
		if m.Provider != "vtx" || m.Model != "gemini-3.5-flash" {
			continue
		}
		if !slices.ContainsFunc(m.Warnings, func(w string) bool {
			return strings.Contains(w, "regional Vertex location")
		}) {
			t.Fatalf("gemini-3.5-flash warnings=%v, want the regional-location note", m.Warnings)
		}
		return
	}
	t.Fatalf("models=%+v, want vtx/gemini-3.5-flash", out.Models)
}

// The launch check validates that a ref resolves — a read, not a launch:
// a command-bearing credential is the child's first request to spend
// (spec §10.1), and a preflight that minted would prompt the user's
// password manager with no session launched.
func TestLaunchCheckNeverMintsCommandCredentials(t *testing.T) {
	valueexpr.ResetForTest()
	t.Cleanup(valueexpr.ResetForTest)
	runs := 0
	valueexpr.RunCommand = func(string) (string, error) {
		runs++
		return "token", nil
	}
	client := launchCheckClient(t, map[string]registry.Provider{
		"gw": {
			Base: "openai-compatible", APIKey: "$(gw-mint)",
			Transport: registry.Transport{BaseURL: "http://127.0.0.1:9/v1"},
			Models:    map[string]registry.Model{"house-model": {}},
		},
	})
	oldLoad := launchCheckLoadClient
	launchCheckLoadClient = func(string) (*llm.Client, error) { return client, nil }
	t.Cleanup(func() { launchCheckLoadClient = oldLoad })

	if err := validateLaunchCheckProfile(cmdutil.ModelRef{Provider: "gw", Model: "house-model"}); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("the launch check executed the credential command %d time(s); the child's first request owns the mint (spec §10.1)", runs)
	}
}

// The launch contract's model list serves a command-credentialed
// instance's registry rows — every advertised fact, no credential
// materialized — and leaves its live listing to the child, mirroring the
// hub picker (spec §10.1).
func TestLaunchCheckModelsServesCommandCredentialedRowsWithoutMinting(t *testing.T) {
	valueexpr.ResetForTest()
	t.Cleanup(valueexpr.ResetForTest)
	runs := 0
	valueexpr.RunCommand = func(string) (string, error) {
		runs++
		return "token", nil
	}
	client := launchCheckClient(t, map[string]registry.Provider{
		"gw": {
			Base: "openai-compatible", APIKey: "$(gw-mint)",
			Transport: registry.Transport{BaseURL: "http://127.0.0.1:9/v1"},
			Models:    map[string]registry.Model{"house-model": {}},
		},
	})
	oldLoad := launchCheckLoadClient
	launchCheckLoadClient = func(string) (*llm.Client, error) { return client, nil }
	t.Cleanup(func() { launchCheckLoadClient = oldLoad })

	models, _, err := launchCheckModels()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range models {
		if m.Provider == "gw" && m.Model == "house-model" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the launch contract's model list dropped the command-credentialed instance's registry rows: %+v", models)
	}
	if runs != 0 {
		t.Fatalf("the model list executed the credential command %d time(s); skipping the live fetch must skip the mint", runs)
	}
}
