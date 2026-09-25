package llm_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/llm"
	_ "primeradiant.com/evener/llm/providers/all"
	"primeradiant.com/evener/llm/registry"
)

// The live listing fetches through the transport the hub describes: the
// launch a bare instance name makes, default row's overrides included. A
// fetch that resolved the provider-level shape while the listing and the
// identity describe the row's would pull rows from an endpoint the launch
// never contacts.
func TestListLiveFetchesThroughTheDefaultRowTransport(t *testing.T) {
	var providerHits, rowHits int
	respond := func(hits *int) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			*hits++
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[]}`)
		})
	}
	providerServer := httptest.NewServer(respond(&providerHits))
	t.Cleanup(providerServer.Close)
	rowServer := httptest.NewServer(respond(&rowHits))
	t.Cleanup(rowServer.Close)

	dir := t.TempDir()
	path := filepath.Join(dir, "providers.toml")
	cfg := "[providers.row-gateway]\n" +
		"base = \"openai\"\n" +
		"api_key = \"k\"\n" +
		"base_url = \"" + providerServer.URL + "\"\n" +
		"default_model = \"house-model\"\n" +
		"[providers.row-gateway.models.\"house-model\"]\n" +
		"base_url = \"" + rowServer.URL + "\"\n"
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := registry.Load(
		registry.WithConfigPath(path),
		registry.WithStateRoot(t.TempDir()),
		registry.WithOffline(true),
		registry.WithoutCache(),
	)
	if err != nil {
		t.Fatal(err)
	}
	client := llm.NewClient(llm.WithRegistry(r))
	if _, ok, err := client.ListLive(context.Background(), "row-gateway"); err != nil || !ok {
		t.Fatalf("ListLive: ok=%v err=%v", ok, err)
	}
	if rowHits == 0 {
		t.Fatal("the live fetch never contacted the default row's transport")
	}
	if providerHits != 0 {
		t.Fatalf("the live fetch hit the provider-level URL %d time(s); the listing and the identity describe the default row's launch, and the fetch must match", providerHits)
	}
}
