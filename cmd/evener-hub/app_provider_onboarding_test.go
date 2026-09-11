package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func TestZeroProviderOnboarding_SaveCheckListAtHTTPBoundary(t *testing.T) {
	const key = "fixture-saved-key-sentinel"
	const replacement = "fixture-replacement-key-sentinel"
	const rejection = "fixture-provider-response-sentinel"
	type request struct{ method, path, key string }
	requests := make(chan request, 8)
	var reject atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- request{r.Method, r.URL.Path, r.Header.Get("X-Api-Key")}
		w.Header().Set("Content-Type", "application/json")
		if reject.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"type":"authentication_error","message":"` + rejection + ` ` + r.Header.Get("X-Api-Key") + `"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"fixture-model","type":"model","display_name":"Fixture model"}],"has_more":false}`))
	}))
	t.Cleanup(server.Close)
	// The only provider boundary is loopback. A registry environment override
	// redirects the implicit provider before any credential exists.
	env := map[string]string{"ANTHROPIC_BASE_URL": server.URL + "/v1"}
	f := newInstancesFixture(t, env)
	// Reopen the real persisted store and reload the real registry for each
	// probe; inject only fixture-owned filesystem/environment roots, not a
	// fake client or a precomputed listing.
	f.ctl.auth.credentialTestLoader = func(path string, noUserLayer bool) (credentialProbeClient, error) {
		store, err := credentials.LoadStore(f.credsPath)
		if err != nil {
			return nil, err
		}
		opts := testProbeRegistryOptions(f.stateDir, store, func(name string) (string, bool) { v, ok := env[name]; return v, ok })
		if noUserLayer {
			opts = append(opts, registry.WithNoUserLayer())
		} else {
			opts = append(opts, registry.WithConfigPath(path))
		}
		r, err := registry.Load(opts...)
		if err != nil {
			return nil, err
		}
		return llm.NewClient(llm.WithRegistry(r)), nil
	}
	list := f.ctl.List()
	setup := providerSetup(t, list, "anthropic").Setup
	if setup == nil || setup.ActiveSource != "none" || setup.BaseURL != server.URL+"/v1" {
		t.Fatalf("missing pre-key discovery: %+v", setup)
	}
	if slices.ContainsFunc(list.Instances, func(i appwire.InstanceEntry) bool { return i.Name == "anthropic" }) {
		t.Fatal("uncredentialed Anthropic is launch-ready")
	}
	check := func(want string) {
		t.Helper()
		resp, err := f.ctl.auth.TestCredentials(context.Background(), appwire.AuthTestParams{Provider: "anthropic"})
		if err != nil || resp.Status != want {
			t.Fatalf("check=%+v err=%v, want %s", resp, err, want)
		}
		requireNoProviderSecrets(t, resp, key, replacement, rejection)
	}
	assertNoRequest := func() {
		t.Helper()
		select {
		case <-requests:
			t.Fatal("unexpected provider request")
		default:
		}
	}
	assertRequest := func(path, credential string) {
		t.Helper()
		select {
		case got := <-requests:
			if got.method != http.MethodGet || got.path != path || got.key != credential {
				t.Fatal("model-list request did not use the saved destination and credential")
			}
		default:
			t.Fatal("check returned without a provider model-list request")
		}
		assertNoRequest()
	}
	check(appwire.AuthTestStatusMissing)
	assertNoRequest()
	saved, err := f.ctl.auth.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "anthropic", Value: key})
	if err != nil || saved.ActiveSource != "store" || !saved.HasStoredFile {
		t.Fatalf("save=%+v err=%v", saved, err)
	}
	store, err := credentials.LoadStore(f.credsPath)
	if err != nil {
		t.Fatal(err)
	}
	if actual, ok := store.Get("anthropic"); !ok || actual != key {
		t.Fatal("key save did not persist the submitted credential")
	}
	list = f.ctl.List()
	if e := entry(t, list, "anthropic"); e.ActiveSource != "store" || e.BaseURL != server.URL+"/v1" {
		t.Fatalf("save/reload membership incorrect: %+v", e)
	}
	if s := providerSetup(t, list, "anthropic").Setup; s == nil || !s.HasStoredFile || s.ActiveSource != "store" {
		t.Fatalf("catalogue did not refresh after save: %+v", s)
	}
	requireNoProviderSecrets(t, saved, key)
	requireNoProviderSecrets(t, list, key)
	check(appwire.AuthTestStatusSuccess)
	assertRequest("/v1/models", key)

	// A directly stored instance key is intentionally not inherited auth.
	// Switch to the curated environment source before testing the endpoint
	// inheritance boundary (registry.effectiveAPIKeyEnv).
	cleared, err := f.ctl.auth.ApiKeyClear(appwire.AuthApiKeyClearParams{Provider: "anthropic"})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.HasStoredFile || cleared.ActiveSource != "none" {
		t.Fatalf("saved direct key was not cleared: %+v", cleared)
	}
	env["ANTHROPIC_API_KEY"] = key
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatal(err)
	}
	if e := entry(t, f.ctl.List(), "anthropic"); e.ActiveSource != "env:ANTHROPIC_API_KEY" {
		t.Fatalf("curated environment credential did not resolve: %+v", e)
	}
	check(appwire.AuthTestStatusSuccess)
	assertRequest("/v1/models", key)

	// An authored destination change must not inherit that curated key.
	changedURL := server.URL + "/changed/v1"
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "anthropic", BaseURL: changedURL}); err != nil {
		t.Fatal(err)
	}
	list = f.ctl.List()
	setup = providerSetup(t, list, "anthropic").Setup
	if setup == nil || setup.BaseURL != changedURL || setup.ActiveSource != "none" || setup.HasStoredFile {
		t.Fatalf("changed destination inherited curated credential: %+v", setup)
	}
	check(appwire.AuthTestStatusMissing)
	assertNoRequest()

	// Explicit replacement under the authored instance opts into the new
	// destination. Provider rejection remains a typed, secret-free result.
	saved, err = f.ctl.auth.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "anthropic", Value: replacement})
	if err != nil {
		t.Fatal(err)
	}
	requireNoProviderSecrets(t, saved, replacement)
	reject.Store(true)
	check(appwire.AuthTestStatusAuthRejected)
	assertRequest("/changed/v1/models", replacement)
	requireNoProviderSecrets(t, f.ctl.List(), key, replacement, rejection)
}
