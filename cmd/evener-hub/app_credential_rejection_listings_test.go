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
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/llm"
)

// listingGateway serves /models as a provider would: a listing while status
// is 200, and the provider's refusal (with body text that must never reach a
// status) otherwise. It returns the providers.toml naming it as instance
// "gw" and the status to set.
func listingGateway(t *testing.T) (tomlPath string, status *atomic.Int32) {
	t.Helper()
	status = &atomic.Int32{}
	status.Store(http.StatusUnauthorized)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		code := int(status.Load())
		w.WriteHeader(code)
		if code != http.StatusOK {
			_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided: ` + rejectionSecret + `","type":"invalid_request_error"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-live"}]}`))
	}))
	t.Cleanup(srv.Close)
	tomlPath = filepath.Join(t.TempDir(), "providers.toml")
	cfg := "[providers.gw]\nbase = \"openai-compatible\"\nbase_url = \"" + srv.URL + "/v1\"\napi_key = \"test-key\"\n"
	if err := os.WriteFile(tomlPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return tomlPath, status
}

func newListingController(t *testing.T) (*hubInstancesController, *atomic.Int32) {
	t.Helper()
	tomlPath, status := listingGateway(t)
	ctl := newTestInstancesController(t, tomlPath, filepath.Dir(tomlPath), t.TempDir(), nil)
	if err := ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	return ctl, status
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

const gwRejected = "The provider rejected this credential (HTTP 401)."

func TestCredentialRejection_RefreshModelsRecordsAndClearsIt(t *testing.T) {
	ctl, status := newListingController(t)
	if err := ctl.RefreshModels(context.Background(), appwire.InstanceRefreshModelsParams{Name: "gw"}); err == nil {
		t.Fatal("precondition: RefreshModels succeeded against a gateway refusing the key")
	}
	if got := gwError(t, ctl.auth); got != gwRejected {
		t.Fatalf("error after a refused refresh = %q, want %q", got, gwRejected)
	}
	status.Store(http.StatusOK)
	if err := ctl.RefreshModels(context.Background(), appwire.InstanceRefreshModelsParams{Name: "gw"}); err != nil {
		t.Fatalf("RefreshModels: %v", err)
	}
	if got := gwError(t, ctl.auth); got != "" {
		t.Fatalf("error after a successful refresh = %q, want none", got)
	}
}

// A listing that fails for a reason other than the credential says nothing
// about it.
func TestCredentialRejection_ARefreshThatFailsOtherwiseRecordsNothing(t *testing.T) {
	ctl, status := newListingController(t)
	status.Store(http.StatusServiceUnavailable)
	_ = ctl.RefreshModels(context.Background(), appwire.InstanceRefreshModelsParams{Name: "gw"})
	if got := gwError(t, ctl.auth); got != "" {
		t.Fatalf("error after a 503 = %q, want none", got)
	}
}

// The background prefetch lists every instance on a timer, so a rejected key
// shows as an error without anyone pressing Test.
func TestCredentialRejection_TheLivePrefetchRecordsIt(t *testing.T) {
	ctl, _ := newListingController(t)
	prefetchAllLiveModels(context.Background(), ctl.reg, ctl.auth, func() {})
	if got := gwError(t, ctl.auth); got != gwRejected {
		t.Fatalf("error after the prefetch = %q, want %q", got, gwRejected)
	}
}

// The model picker's live pass lists every instance through its own client;
// its outcome lands in the web server's auth controller.
func TestCredentialRejection_ThePickersLiveListingRecordsIt(t *testing.T) {
	ctl, _ := newListingController(t)
	server := NewWebServer(hubcore.WebConfig{
		Registry:            ctl.reg,
		ProvidersConfigPath: ctl.providersConfigPath,
		HubStateRoot:        ctl.auth.stateDir,
		CredsStore:          ctl.auth.creds,
	})
	// The picker dials the same configuration the hub holds.
	oldLoadClient := liveModelLoadClient
	liveModelLoadClient = func(string) (*llm.Client, error) { return LiveRegistryClient(ctl.reg.Get()), nil }
	t.Cleanup(func() { liveModelLoadClient = oldLoadClient })

	server.fetchLiveModels(context.Background())

	if got := gwError(t, server.auth); got != gwRejected {
		t.Fatalf("error after the picker's listing = %q, want %q", got, gwRejected)
	}
}
