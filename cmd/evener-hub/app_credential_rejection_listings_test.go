package hub

// The hub's own model listings send an instance's credential too, so they
// feed the credential-rejection record (#3539) the way Test connection does:
// the manual refresh (evener/instance/refreshModels), the background live
// prefetch every instance gets, and the model picker's live pass. Each test
// runs a real listing against a real HTTP gateway that answers the way a
// provider refusing the key does.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// listingGateway is a provider's /models endpoint: a listing while status is
// 200, and the provider's refusal (with body text that must never reach a
// status) otherwise. hits counts the listings it served, and lastKey is the
// bearer key the latest one sent.
type listingGateway struct {
	status  atomic.Int32
	hits    atomic.Int32
	lastKey atomic.Value
}

// newListingController serves a listingGateway as instance "gw", whose key
// is stored in credentials.toml, so replacing it is a real credential write.
func newListingController(t *testing.T) (*hubInstancesController, *listingGateway) {
	t.Helper()
	gw := &listingGateway{}
	gw.status.Store(http.StatusUnauthorized)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			http.NotFound(w, r)
			return
		}
		gw.hits.Add(1)
		gw.lastKey.Store(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		w.Header().Set("Content-Type", "application/json")
		code := int(gw.status.Load())
		w.WriteHeader(code)
		if code != http.StatusOK {
			_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided: ` + rejectionSecret + `","type":"invalid_request_error"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-live"}]}`))
	}))
	t.Cleanup(srv.Close)
	tomlPath := filepath.Join(t.TempDir(), "providers.toml")
	cfg := "[providers.gw]\nbase = \"openai-compatible\"\nbase_url = \"" + srv.URL + "/v1\"\n"
	if err := os.WriteFile(tomlPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	ctl := newTestInstancesController(t, tomlPath, filepath.Dir(tomlPath), t.TempDir(), nil)
	if err := ctl.auth.creds.Set("gw", "sk-original"); err != nil {
		t.Fatal(err)
	}
	if err := ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	return ctl, gw
}

func gwError(t *testing.T, auth *hubAuthController) string {
	t.Helper()
	status, err := auth.Status(appwire.AuthStatusParams{Provider: "gw"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if strings.Contains(status.Error, rejectionSecret) {
		t.Fatalf("status carries provider text: %q", status.Error)
	}
	return status.Error
}

// newListingWebServer is a web server over ctl's configuration, whose
// picker records into its own auth controller.
func newListingWebServer(ctl *hubInstancesController) *WebServer {
	return NewWebServer(hubcore.WebConfig{
		Registry:            ctl.reg,
		ProvidersConfigPath: ctl.providersConfigPath,
		HubStateRoot:        ctl.auth.stateDir,
		CredsStore:          ctl.auth.creds,
	})
}

const gwRejected = "The provider rejected this credential (HTTP 401)."

func TestCredentialRejection_RefreshModelsRecordsAndClearsIt(t *testing.T) {
	ctl, gw := newListingController(t)
	if err := ctl.RefreshModels(context.Background(), appwire.InstanceRefreshModelsParams{Name: "gw"}); err == nil {
		t.Fatal("precondition: RefreshModels succeeded against a gateway refusing the key")
	}
	if got := gwError(t, ctl.auth); got != gwRejected {
		t.Fatalf("error after a refused refresh = %q, want %q", got, gwRejected)
	}
	gw.status.Store(http.StatusOK)
	if err := ctl.RefreshModels(context.Background(), appwire.InstanceRefreshModelsParams{Name: "gw"}); err != nil {
		t.Fatalf("RefreshModels: %v", err)
	}
	if got := gwError(t, ctl.auth); got != "" {
		t.Fatalf("error after a successful refresh = %q, want none", got)
	}
}

// A listing that fails for a reason other than the credential (a server
// error, an endpoint with no listing) says nothing about it: a rejection
// recorded before it stands.
func TestCredentialRejection_ARefreshThatFailsOtherwiseKeepsIt(t *testing.T) {
	for _, code := range []int32{http.StatusServiceUnavailable, http.StatusNotFound} {
		t.Run(http.StatusText(int(code)), func(t *testing.T) {
			ctl, gw := newListingController(t)
			_ = ctl.RefreshModels(context.Background(), appwire.InstanceRefreshModelsParams{Name: "gw"})
			if gwError(t, ctl.auth) != gwRejected {
				t.Fatal("precondition: the 401 was not recorded")
			}
			gw.status.Store(code)
			_ = ctl.RefreshModels(context.Background(), appwire.InstanceRefreshModelsParams{Name: "gw"})
			if got := gwError(t, ctl.auth); got != gwRejected {
				t.Fatalf("error after a %d = %q, want the rejection kept", code, got)
			}
		})
	}
}

// The startup prefetch lists every instance once, so a key rejected at hub
// start shows as an error without anyone pressing Test.
// It is announced from the prefetch's goroutine like any other change, so
// clients re-read the status.
func TestCredentialRejection_TheLivePrefetchRecordsIt(t *testing.T) {
	ctl, gw := newListingController(t)
	gw.status.Store(http.StatusForbidden)
	var mu sync.Mutex
	var announced []string
	ctl.auth.credentialRejectionChanged = func(name string) {
		mu.Lock()
		defer mu.Unlock()
		announced = append(announced, name)
	}
	prefetchAllLiveModels(context.Background(), ctl.reg, ctl.auth, func() {})
	if got, want := gwError(t, ctl.auth), "The provider rejected this credential (HTTP 403)."; got != want {
		t.Fatalf("error after the prefetch = %q, want %q", got, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(announced, ",") != "gw" {
		t.Fatalf("announced = %v, want the rejection of gw", announced)
	}
}

// The model picker's live pass lists every instance through its own client;
// its outcome lands in the web server's auth controller.
func TestCredentialRejection_ThePickersLiveListingRecordsIt(t *testing.T) {
	ctl, _ := newListingController(t)
	server := newListingWebServer(ctl)
	// The picker dials the same configuration the hub holds.
	oldLoadClient := liveModelLoadClient
	liveModelLoadClient = func(string) (*llm.Client, error) { return LiveRegistryClient(ctl.reg.Get()), nil }
	t.Cleanup(func() { liveModelLoadClient = oldLoadClient })

	server.fetchLiveModels(context.Background())

	if got := gwError(t, server.auth); got != gwRejected {
		t.Fatalf("error after the picker's listing = %q, want %q", got, gwRejected)
	}
}

// The picker's client reads every credential when it is built, before the
// loop lists any instance. A credential write that lands after that read has
// replaced the key the listing sends, so the listing's rejection must not be
// recorded against the new one: each probe starts before the client exists.
func TestCredentialRejection_ThePickerVoidsAListingOfAReplacedKey(t *testing.T) {
	ctl, gw := newListingController(t)
	server := newListingWebServer(ctl)
	oldLoadClient := liveModelLoadClient
	liveModelLoadClient = func(string) (*llm.Client, error) {
		// As the real loader does (cmdutil.LoadRegistry), the picker's client
		// reads credentials.toml from disk into a store of its own.
		store, err := credentials.LoadStore(ctl.auth.creds.Path())
		if err != nil {
			return nil, err
		}
		reg, err := registry.Load(append(
			testProbeRegistryOptions(ctl.auth.stateDir, store, func(string) (string, bool) { return "", false }),
			registry.WithConfigPath(ctl.providersConfigPath),
		)...)
		if err != nil {
			return nil, err
		}
		// A new key is written, and the hub's registry reloaded, once the
		// client holds the old one.
		if _, err := server.auth.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "gw", Value: "sk-replacement"}); err != nil {
			t.Errorf("ApiKeySet: %v", err)
		}
		return LiveRegistryClient(reg), nil
	}
	t.Cleanup(func() { liveModelLoadClient = oldLoadClient })

	server.fetchLiveModels(context.Background())

	if sent, _ := gw.lastKey.Load().(string); sent != "sk-original" {
		t.Fatalf("precondition: the listing sent %q, want the replaced key sk-original", sent)
	}
	if got := gwError(t, server.auth); got != "" {
		t.Fatalf("error = %q: the replaced key's rejection landed on the key written after the client read it", got)
	}
}
