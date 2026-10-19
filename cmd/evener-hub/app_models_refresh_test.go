package hub

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/registry"
)

// Exercise the actual websocket dispatch, launch-check child and HTTP provider.
// In particular, a provider diagnostic is a successful RPC, not a transport error.
func TestModelListExplicitRefreshRecoversProvider(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "evener")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "../evener")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		switch key {
		case "PATH", "TMPDIR", "GOCACHE", "GOMODCACHE", "GOROOT", "CGO_ENABLED":
		default:
			t.Setenv(key, "")
		}
	}
	for key, value := range map[string]string{"HOME": filepath.Join(root, "home"), "XDG_CONFIG_HOME": filepath.Join(root, "config"), "XDG_STATE_HOME": filepath.Join(root, "state"), "EVENER_OFFLINE": "1"} {
		t.Setenv(key, value)
	}
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprintf("providerFailure=%t", failure), func(t *testing.T) {
			var recovered atomic.Bool
			var calls atomic.Int64
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/unavailable/api/tags" {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				if r.URL.Path != "/v1/models" {
					http.NotFound(w, r)
					return
				}
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if !recovered.Load() {
					if failure {
						http.Error(w, `{"error":{"message":"fixture unavailable"}}`, http.StatusServiceUnavailable)
						return
					}
					fmt.Fprint(w, `{"data":[]}`)
					return
				}
				fmt.Fprint(w, `{"data":[{"id":"refresh-model-sentinel","object":"model"}]}`)
			}))
			defer provider.Close()
			t.Setenv("OLLAMA_BASE_URL", provider.URL+"/unavailable")
			config := filepath.Join(t.TempDir(), "providers.toml")
			creds := filepath.Join(t.TempDir(), "credentials.toml")
			t.Setenv("EVENER_PROVIDERS_CONFIG", config)
			t.Setenv("EVENER_CREDENTIALS_CONFIG", creds)
			if err := os.WriteFile(config, []byte(fmt.Sprintf("[providers.refreshfixture]\nbase = \"openai-compatible\"\nbase_url = %q\n", provider.URL+"/v1")), 0600); err != nil {
				t.Fatal(err)
			}
			store, err := credentials.LoadStore(creds)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Set("refreshfixture", "fixture-key-not-a-secret"); err != nil {
				t.Fatal(err)
			}
			reg := hubcore.NewProviderRegistry(func(opts ...registry.Option) (*registry.Registry, *credentials.Store, error) {
				return cmdutil.LoadRegistry(append(opts, registry.WithOffline(true))...)
			})
			if err := reg.Reload(); err != nil {
				t.Fatal(err)
			}
			hub, _ := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Registry: reg, CredsStore: store, Spawner: &HubSpawner{EvenerBinary: binary, Registry: reg, ProvidersConfigPath: config, CredentialsPath: creds}})
			defer hub.Close()
			client := dialHubRPC(t, hub)
			defer client.Close()
			if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
				t.Fatal(err)
			}
			list := func(refresh bool) appwire.ModelListResponse {
				t.Helper()
				var resp appwire.ModelListResponse
				if err := client.Request(t.Context(), appwire.MethodModelList, appwire.ModelListParams{Refresh: refresh}, &resp); err != nil {
					t.Fatal(err)
				}
				return resp
			}
			before := list(false)
			if len(before.Data) != 0 {
				t.Fatalf("initial models = %+v", before.Data)
			}
			if calls.Load() == 0 {
				t.Fatal("launch check never contacted fixture provider")
			}
			if failure && len(before.Diagnostics) == 0 {
				t.Fatal("provider failure did not produce a diagnostic")
			}
			prior := calls.Load()
			recovered.Store(true)
			cached := list(false)
			if len(cached.Data) != 0 || calls.Load() != prior {
				t.Fatal("ordinary read bypassed the cache")
			}
			fresh := list(true)
			found := false
			for _, model := range fresh.Data {
				if model.Provider == "refreshfixture" && model.Model == "refresh-model-sentinel" {
					found = true
				}
			}
			if !found || calls.Load() <= prior {
				t.Fatalf("explicit retry retained stale provider result: models=%+v calls=%d before=%d", fresh.Data, calls.Load(), prior)
			}
			after := calls.Load()
			if len(list(false).Data) != len(fresh.Data) || calls.Load() != after {
				t.Fatal("refresh did not populate normal cached reads")
			}
		})
	}
}
