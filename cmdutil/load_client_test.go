package cmdutil

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "primeradiant.com/evener/llm/providers/all" // register every protocol
	"primeradiant.com/evener/llm/registry"
)

// gatewayProvidersToml declares one instance, "gw", pointing at a local
// chat-completions server: the shape every LoadClient test drives.
func gatewayProvidersToml(baseURL string) string {
	return `
[providers.gw]
base     = "openai"
protocol = "openai-chat"
surface  = "generic"
base_url = "` + baseURL + `/v1"
api_key  = "sk-gw"
`
}

// oldSchemaProvidersToml is a pre-registry file (spec §14.1).
const oldSchemaProvidersToml = `
schema = 1
default = "work"

[instances.work]
type = "openai"
api_style = "responses"
api_key = "sk-work"
`

// gatewayServer serves one OpenRouter-shaped /models listing: two chat ids,
// one of which advertises a context length.
func gatewayServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
			{"id": "glm-5.2-nvfp4", "context_length": 262144},
			{"id": "glm-5.2-flash"},
		}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// writeGatewayConfig points EVENER_PROVIDERS_CONFIG at a providers.toml
// declaring the gw instance, and returns the path.
func writeGatewayConfig(t *testing.T, baseURL string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.toml")
	if err := os.WriteFile(path, []byte(gatewayProvidersToml(baseURL)), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("EVENER_PROVIDERS_CONFIG", path)
	t.Setenv("EVENER_CREDENTIALS_CONFIG", filepath.Join(dir, "credentials.toml"))
	t.Setenv("EVENER_STATE_DIR", t.TempDir())
	return path
}

// TestLoadClient_ListsTheDeclaredInstanceLive drives the whole loader: the
// client LoadClient builds has no overrides, so listing "gw" reaches the
// declared endpoint, records the listing on the registry, and every id the
// server served resolves on the instance afterwards.
func TestLoadClient_ListsTheDeclaredInstanceLive(t *testing.T) {
	srv := gatewayServer(t)
	writeGatewayConfig(t, srv.URL)

	client, err := LoadClient("")
	if err != nil {
		t.Fatalf("LoadClient: %v", err)
	}
	listing, err := client.Models(t.Context(), "gw")
	if err != nil {
		t.Fatalf("Models(gw): %v", err)
	}
	if !listing.Live {
		t.Fatal("Models(gw).Live = false; the declared endpoint answered, so the listing is live")
	}
	ids := map[string]bool{}
	for _, m := range listing.Models {
		ids[m.ModelID] = true
	}
	for _, want := range []string{"glm-5.2-nvfp4", "glm-5.2-flash"} {
		if !ids[want] {
			t.Fatalf("listing ids = %v, want it to contain %q", ids, want)
		}
	}
	if refs := client.Registry().FindModel("glm-5.2-nvfp4"); len(refs) != 1 || refs[0].Instance != "gw" {
		t.Fatalf("FindModel = %v, want the gw instance", refs)
	}
}

// TestResolveProfile_TakesTheServedWindow pins that the profile the loader
// hands the session carries the facts the instance advertised: a
// generic-surface row whose window is the server's context_length.
func TestResolveProfile_TakesTheServedWindow(t *testing.T) {
	srv := gatewayServer(t)
	writeGatewayConfig(t, srv.URL)

	client, err := LoadClient("")
	if err != nil {
		t.Fatalf("LoadClient: %v", err)
	}
	if _, err := client.Models(t.Context(), "gw"); err != nil {
		t.Fatalf("Models(gw): %v", err)
	}
	p, err := ResolveProfile(client, "gw/glm-5.2-nvfp4")
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	if p.ID() != "gw" || p.Surface() != registry.SurfaceGeneric {
		t.Fatalf("profile = %s on surface %s, want gw on generic", p.ID(), p.Surface())
	}
	if got := p.ContextWindowSize(); got != 262_144 {
		t.Fatalf("ContextWindowSize() = %d, want the served 262144", got)
	}
	// BuildResolveProfile is the same resolution, as a SessionConfig closure.
	q, err := BuildResolveProfile(client)("gw/glm-5.2-nvfp4")
	if err != nil || q.ContextWindowSize() != p.ContextWindowSize() {
		t.Fatalf("BuildResolveProfile: %v (window %d)", err, q.ContextWindowSize())
	}
}

// TestLoadClient_CodexAllowlistIsTheOneUnservableRef pins §7.3 through the
// loader: openai-codex is a curated implicit id that resolves without an OAuth
// record, and the only reference that fails outright is a model off its
// transport's allowlist.
func TestLoadClient_CodexAllowlistIsTheOneUnservableRef(t *testing.T) {
	srv := gatewayServer(t)
	writeGatewayConfig(t, srv.URL)

	client, err := LoadClient("")
	if err != nil {
		t.Fatalf("LoadClient: %v", err)
	}
	if !client.CanServe("openai-codex", "gpt-5.6") {
		t.Fatal("an allowlisted Codex model must be servable without an OAuth record")
	}
	if client.CanServe("openai-codex", "not-on-the-allowlist") {
		t.Fatal("CanServe = true for a model off the Codex allowlist")
	}
	_, err = client.Resolve("openai-codex/not-on-the-allowlist")
	if err == nil || !strings.Contains(err.Error(), "unknown model on the Codex transport") {
		t.Fatalf("Resolve error = %v, want the Codex allowlist message", err)
	}
}

// TestLoadClientAt_ReadsTheExplicitPath pins that the explicit-path loader
// ignores EVENER_PROVIDERS_CONFIG, which is what lets the hub inspect a file
// without changing process-wide environment.
func TestLoadClientAt_ReadsTheExplicitPath(t *testing.T) {
	srv := gatewayServer(t)
	dir := t.TempDir()
	wanted := filepath.Join(dir, "selected.toml")
	if err := os.WriteFile(wanted, []byte(gatewayProvidersToml(srv.URL)), 0o600); err != nil {
		t.Fatalf("WriteFile selected: %v", err)
	}
	other := filepath.Join(dir, "other.toml")
	if err := os.WriteFile(other, []byte(oldSchemaProvidersToml), 0o600); err != nil {
		t.Fatalf("WriteFile other: %v", err)
	}
	t.Setenv("EVENER_PROVIDERS_CONFIG", other)
	t.Setenv("EVENER_CREDENTIALS_CONFIG", filepath.Join(dir, "credentials.toml"))
	t.Setenv("EVENER_STATE_DIR", t.TempDir())

	client, err := LoadClientAt(wanted, "")
	if err != nil {
		t.Fatalf("LoadClientAt: %v", err)
	}
	if _, err := ResolveProfile(client, "gw/glm-5.2-nvfp4"); err != nil {
		t.Fatalf("ResolveProfile on the explicit path: %v", err)
	}
}

// TestLoadClient_OldSchemaFileIsReported pins §14.1: a pre-registry
// providers.toml is a load failure the CLI exits with, not a silent skip.
func TestLoadClient_OldSchemaFileIsReported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.toml")
	if err := os.WriteFile(path, []byte(oldSchemaProvidersToml), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("EVENER_PROVIDERS_CONFIG", path)
	t.Setenv("EVENER_CREDENTIALS_CONFIG", filepath.Join(dir, "credentials.toml"))
	t.Setenv("EVENER_STATE_DIR", t.TempDir())

	if _, err := LoadClient(""); !errors.Is(err, registry.ErrOldSchema) {
		t.Fatalf("LoadClient on an old-schema file = %v, want registry.ErrOldSchema", err)
	}
}
