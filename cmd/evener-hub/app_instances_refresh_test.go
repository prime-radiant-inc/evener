package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

// refreshGateway writes a providers.toml with a gw instance pointed at a
// /models endpoint serving body, for live-refresh tests.
func refreshGateway(t *testing.T, body string) (tomlPath string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	tomlPath = filepath.Join(dir, "providers.toml")
	cfg := "[providers.gw]\nbase = \"openai-compatible\"\nbase_url = \"" + srv.URL + "/v1\"\napi_key = \"test-key\"\n"
	if err := os.WriteFile(tomlPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return tomlPath
}

func TestInstances_RefreshModelsFetchesLiveIDs(t *testing.T) {
	tomlPath := refreshGateway(t, `{"data":[{"id":"gpt-live"},{"id":"text-embedding-3-small"}]}`)
	dir := filepath.Dir(tomlPath)
	ctl := newTestInstancesController(t, tomlPath, dir, t.TempDir(), nil)
	if err := ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	resp, err := ctl.RefreshModels(context.Background(), appwire.InstanceRefreshModelsParams{Name: "gw"})
	if err != nil {
		t.Fatalf("RefreshModels: %v", err)
	}
	got := entry(t, resp, "gw")
	if !slices.ContainsFunc(got.Models, func(m appwire.InstanceModelEntry) bool { return m.ID == "gpt-live" && !m.Disabled }) {
		t.Fatalf("entry models = %+v, want live gpt-live listed", got.Models)
	}
	if slices.ContainsFunc(got.Models, func(m appwire.InstanceModelEntry) bool { return m.ID == "text-embedding-3-small" }) {
		t.Fatalf("entry models = %+v, want the embedding id filtered out", got.Models)
	}
}

func TestInstances_RefreshModelsFailureKeepsCatalog(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	cfg := "[providers.dead]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:9/v1\"\napi_key = \"test-key\"\n"
	if err := os.WriteFile(tomlPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	ctl := newTestInstancesController(t, tomlPath, dir, t.TempDir(), nil)
	if err := ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	// A closed port fails the fetch: the error — not a silently
	// catalog-only list — is what the sheet toasts.
	if _, err := ctl.RefreshModels(context.Background(), appwire.InstanceRefreshModelsParams{Name: "dead"}); err == nil {
		t.Fatal("unreachable endpoint must error")
	}
	if _, err := ctl.RefreshModels(context.Background(), appwire.InstanceRefreshModelsParams{Name: "nope"}); err == nil {
		t.Fatal("unknown instance must be refused")
	}
}
