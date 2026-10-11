package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/execsupport/valueexpr"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// The pane's credential-test probe is the one deliberate exception to the
// hub's never-mint contract (spec §10.1, ruled 2026-09-23): the user asks
// the hub to exercise the credential, so the probe resolves the command
// once and sends the mint to the instance's endpoint. This pins that
// ruling — a probe that refused command-credentialed instances fails
// here, and so does one that stopped sending what it resolved.
func TestCredentialTestProbesCommandCredential(t *testing.T) {
	valueexpr.ResetForTest()
	t.Cleanup(valueexpr.ResetForTest)
	runs := 0
	valueexpr.RunCommand = func(string) (string, error) {
		runs++
		return "token", nil
	}
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"probe-model"}]}`))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	cfg := "[providers.gw]\nbase = \"openai-compatible\"\nbase_url = \"" + srv.URL + "/v1\"\napi_key = '''$(gw-mint)'''\n" +
		"[providers.gw.models.\"probe-model\"]\n"
	if err := os.WriteFile(tomlPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	ctrl := newTestAuthController(t, dir, t.TempDir(), tomlPath)

	loader := func(path string, _ bool) (credentialProbeClient, error) {
		r, err := registry.Load(
			registry.WithConfigPath(path),
			registry.WithStateRoot(t.TempDir()),
			registry.WithOffline(true),
			registry.WithoutCache(),
		)
		if err != nil {
			return nil, err
		}
		return llm.NewClient(llm.WithRegistry(r)), nil
	}
	resp, err := ctrl.runCredentialTest(context.Background(), "gw", "", loader)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != appwire.AuthTestStatusSuccess {
		t.Fatalf("status = %q (%s); want success", resp.Status, resp.Message)
	}
	if runs != 1 {
		t.Fatalf("the probe executed the credential command %d time(s); a user-initiated check mints exactly once", runs)
	}
	if gotAuth != "Bearer token" {
		t.Fatalf("the probe sent Authorization %q; want the minted bearer", gotAuth)
	}
}
