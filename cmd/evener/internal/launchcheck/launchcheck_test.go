package launchcheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/llm"
	_ "primeradiant.com/evener/llm/providers/all"
	"primeradiant.com/evener/llm/registry"
)

// launchCheckGateway starts a /models endpoint answering with status and body,
// declares it as the "gw" instance in an isolated providers.toml, and installs
// a client built from that file on the launchCheckLoadClient seam.
//
// The client is the real one — cmdutil.LoadRegistry over the real providers
// file, no mocks — but its environment is a fixed table rather than the
// machine's. That matters because launchCheckModels lists every visible
// instance and implicit instances are conjured from the environment: an
// ambient TOGETHER_API_KEY would otherwise put api.together.ai in the loop.
// The one variable the table answers is OLLAMA_HOST, whose instance needs no
// credential and is therefore always visible; it points at a closed port so
// its listing fails instantly instead of reaching a real daemon.
func launchCheckGateway(t *testing.T, status int, body string) {
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

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "providers.toml")
	if err := os.WriteFile(cfgPath, []byte(`
[providers.gw]
base     = "openai-compatible"
base_url = "`+srv.URL+`/v1"
api_key  = "test-key"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	stateRoot := t.TempDir()
	env := map[string]string{"OLLAMA_HOST": "127.0.0.1:1"}

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
		Protocol string `json:"protocol"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout.String(), err)
	}
	if out.Protocol != appwire.ProtocolVersion || out.Provider != "gw" || out.Model != "glm-5" {
		t.Fatalf("launch check output=%+v", out)
	}
	// Literal check: catches a change to the ProtocolVersion constant value.
	if out.Protocol != "evener-appwire-v4" {
		t.Fatalf("out.Protocol=%q, want \"evener-appwire-v4\"", out.Protocol)
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
		Diagnostics []struct {
			Provider string `json:"provider"`
			Source   string `json:"source"`
			Title    string `json:"title"`
			Message  string `json:"message"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout.String(), err)
	}
	var found bool
	for _, got := range out.Diagnostics {
		if got.Provider == "gw" {
			found = true
			if got.Source != "provider" || got.Title != "Provider error" || !strings.Contains(got.Message, "403") {
				t.Fatalf("diagnostic=%+v", got)
			}
		}
	}
	if !found {
		t.Fatalf("diagnostics missing the gw entry: %+v", out.Diagnostics)
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
