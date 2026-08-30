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
	_ "primeradiant.com/evener/llm/providers/all"
)

// launchCheckGateway starts a /models endpoint answering with status and body,
// declares it as the "gw" instance in an isolated providers.toml, and points
// every environment seam at that temp directory. It returns nothing: the
// launch check reads the environment the same way the CLI does.
func launchCheckGateway(t *testing.T, status int, body string) {
	t.Helper()
	// Isolate from any stored OAuth record or provider key on the dev machine
	// so only the declared instance is configured.
	oaitest.IsolateOpenAIAuth(t)
	for _, key := range []string{
		"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GLM_API_KEY",
		"GROK_API_KEY", "KIMI_API_KEY", "MINIMAX_API_KEY", "OPENROUTER_API_KEY",
		"OLLAMA_API_KEY", "OPENAI_COMPATIBLE_BASE_URL",
	} {
		t.Setenv(key, "")
	}
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
	// The ollama instance is always configured (its auth is optional); point it
	// at a closed port so its listing fails fast instead of reaching a real one.
	t.Setenv("OLLAMA_HOST", "127.0.0.1:1")

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
	t.Setenv("EVENER_PROVIDERS_CONFIG", cfgPath)
	t.Setenv("EVENER_CREDENTIALS_CONFIG", filepath.Join(dir, "credentials.toml"))
	t.Setenv("EVENER_STATE_DIR", t.TempDir())
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
	if out.Protocol != "evener-appwire-v3" {
		t.Fatalf("out.Protocol=%q, want \"evener-appwire-v3\"", out.Protocol)
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
