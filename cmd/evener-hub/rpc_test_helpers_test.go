package hub

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// mustInitializeHubClient completes the initialize handshake a hub RPC client
// performs before its first request, failing the test on any error.
func mustInitializeHubClient(t *testing.T, client *appwire.Client) {
	t.Helper()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
}

// dialHubRPCInitialized dials the hub's RPC endpoint, closes the client when
// the test ends, and completes the initialize handshake, failing the test on
// any error. It is the dialHubRPC equivalent for tests that immediately
// initialize; tests that need the connection in some other state keep
// dialHubRPC.
func dialHubRPCInitialized(t *testing.T, hub *httptest.Server) *appwire.Client {
	t.Helper()
	client := dialHubRPC(t, hub)
	t.Cleanup(func() { _ = client.Close() })
	mustInitializeHubClient(t, client)
	return client
}

// requireWireError asserts err carries an appwire wire error of the wanted
// code, failing the test when it does not, and returns the error for
// follow-up assertions on its other fields.
func requireWireError(t *testing.T, err error, want int) appwire.WireError {
	t.Helper()
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != want {
		t.Fatalf("error = %v, want wire error code %d", err, want)
	}
	return wire
}

// newRebuiltPastIndex builds the past-thread index over the given glob and
// rebuilds it, failing the test if the rebuild reports an error.
func newRebuiltPastIndex(t testing.TB, glob string) *hubcore.PastIndex {
	t.Helper()
	past := hubcore.NewPastIndex(glob)
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	return past
}

// newWebSourceTestServer starts a hub web server over the given sources on a
// fresh httptest listener and returns the running server. The server closes
// when the test ends. Tests that need the *WebServer itself (to wire an
// onChange hook or add sources after startup) build it inline instead.
func newWebSourceTestServer(t *testing.T, sources ...appsource.Source) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	for _, source := range sources {
		web.sources.Add(source)
	}
	srv.Config.Handler = web.Handler()
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}
