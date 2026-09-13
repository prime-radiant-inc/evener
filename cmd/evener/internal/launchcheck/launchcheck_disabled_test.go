package launchcheck

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// launchCheckGatewayWithConfig is launchCheckGateway with a caller-supplied
// [providers.gw] body, so tests can add rows the default helper cannot.
func launchCheckGatewayWithConfig(t *testing.T, gwExtra string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-live"}]}`))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "providers.toml")
	cfg := "[providers.gw]\nbase     = \"openai-compatible\"\nbase_url = \"" + srv.URL + "/v1\"\napi_key  = \"test-key\"\n" + gwExtra
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
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

func TestLaunchCheckModelsOmitDisabled(t *testing.T) {
	launchCheckGatewayWithConfig(t, "[providers.gw.models.\"gpt-live\"]\ndisabled = true\n")

	var stdout, stderr bytes.Buffer
	if err := RunLaunchCheck([]string{"--protocol", appwire.ProtocolVersion, "--models", "--json"}, &stdout, &stderr); err != nil {
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
	for _, m := range out.Models {
		if m.Provider == "gw" && m.Model == "gpt-live" {
			t.Fatalf("disabled model listed: %+v", out.Models)
		}
	}
}

func TestLaunchCheckRejectsDisabledModel(t *testing.T) {
	launchCheckGatewayWithConfig(t, "[providers.gw.models.\"gpt-live\"]\ndisabled = true\n")

	var stdout, stderr bytes.Buffer
	err := RunLaunchCheck([]string{"--protocol", appwire.ProtocolVersion, "--model", "gw/gpt-live", "--json"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected disabled model rejection")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("error=%v, want it to name the disablement", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout=%q, want empty on failure", stdout.String())
	}
}
