package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

// The hub's /rpc edge stamps the cooperative bridge marker into the request
// context before the AppWire edge accepts the connection, so a handler reads the
// request's routing origin rather than re-inspecting a header (component 05,
// §"The origin signal is an explicit bridge marker on the connection"). A
// connection presenting the marker is remote-originated; one without it — the
// local browser, TUI, or CLI — is local.
func TestServeAppWireRPCStampsBridgeOrigin(t *testing.T) {
	// A catalog-less probe method that reports the origin the handler context
	// carries. It is registered on the real appserver the hub's edge wraps, so
	// the test exercises the production wrapper rather than a reimplementation.
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "test-hub", Version: "0", SourceID: "local"})
	appserver.HandleTyped(server.Router(), "test/origin", func(ctx context.Context, _ struct{}) (string, error) {
		return hostRoutingOrigin(ctx), nil
	})
	ws := &WebServer{appRPC: server}
	httpSrv := httptest.NewServer(http.HandlerFunc(ws.serveAppWireRPC))
	defer httpSrv.Close()
	url := "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/rpc"

	origin := func(t *testing.T, header http.Header) string {
		t.Helper()
		transport, err := appwire.DialWebSocketWithHeaders(t.Context(), url, httpSrv.Client(), header)
		if err != nil {
			t.Fatalf("dial %s: %v", url, err)
		}
		client := appwire.NewClient(transport)
		client.Start(t.Context())
		t.Cleanup(func() { _ = client.Close() })
		if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
			t.Fatalf("initialize: %v", err)
		}
		var out string
		if err := client.Request(t.Context(), "test/origin", struct{}{}, &out); err != nil {
			t.Fatalf("test/origin: %v", err)
		}
		return out
	}

	marked := http.Header{}
	marked.Set(bridgeOriginHeader, "1")
	if got := origin(t, marked); got != hostRoutingOriginBridge {
		t.Fatalf("bridge-marked request origin = %q, want %q", got, hostRoutingOriginBridge)
	}
	if got := origin(t, nil); got != "" {
		t.Fatalf("unmarked request origin = %q, want local (empty)", got)
	}
}
