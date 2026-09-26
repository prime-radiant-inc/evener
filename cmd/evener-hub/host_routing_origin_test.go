package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
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

// The dial guard alone does not stop a remote-originated request from riding an
// ALREADY-ATTACHED remote source: evener/host/request resolves the host through
// the attached-only lookup and forwards over the live client without ever
// dialing. Before this round a peer hub could therefore reach a second host
// through this hub — depth 2 — and bypass the depth-1 topology cap. The shared
// dispatch guard at the remote-client seam (appsource.guardRemoteDispatch,
// reached by every RemoteHubSource call through resolveClient) refuses it typed
// and sends nothing to the remote.
func TestHostAdminRequestRefusesRemoteOriginatedForwarding(t *testing.T) {
	controller, _, calls := scriptedHostAdmin(t, true, func(string, json.RawMessage) hostAdminReply {
		return okReply()
	})

	_, err := controller.Request(
		withHostRoutingOrigin(context.Background(), hostRoutingOriginBridge),
		appwire.HostRequestParams{Host: "m4", Method: appwire.MethodEvenerInstanceList},
	)
	assertWireCode(t, err, appwire.CodeInvalidParams)
	if !strings.Contains(err.Error(), hostRoutingOriginBridge) {
		t.Fatalf("refusal %q does not name the origin %q", err, hostRoutingOriginBridge)
	}
	// Only the harness's own initialize: the forwarded request never reached the
	// remote host's client.
	if got := calls(); len(got) != 1 {
		t.Fatalf("remote-originated proxy call was forwarded: remote calls = %+v, want only initialize", got)
	}

	// The identical call from a LOCAL (unmarked) request still forwards, so the
	// guard refuses only remote-originated dispatches.
	out, err := controller.Request(context.Background(), appwire.HostRequestParams{
		Host:   "m4",
		Method: appwire.MethodEvenerInstanceList,
	})
	if err != nil {
		t.Fatalf("local-originated proxy call: %v", err)
	}
	if string(out) != `{"ok":true}` {
		t.Fatalf("local-originated result = %s, want the remote's own result", out)
	}
	if got := calls(); len(got) != 2 {
		t.Fatalf("local-originated call was not forwarded: remote calls = %+v", got)
	}
}

// A remote-originated request must still read this hub's own CACHED remote
// state: the snapshot rows and fleet manifests are local data, so no remote hub
// is contacted and nothing about the guard may refuse them. The cache path
// (remoteThreadFetch) never reaches a RemoteHubSource, so this pins that the
// dispatch guard covers only real dispatches.
func TestRemoteOriginatedRequestStillReadsCachedSnapshotRows(t *testing.T) {
	cache := &hubcore.RemoteThreadCache{}
	cache.StoreSnapshot([]appwire.Thread{{ID: "alpha-thread", Source: "m4"}}, true)
	cfg := hubcore.WebConfig{
		RemoteHosts:       []hostreg.Host{{Name: "m4", SSH: "m4.example"}},
		RemoteThreadCache: cache,
		RemoteHostClient: func(context.Context, string) (*appwire.Client, error) {
			t.Error("the cached-state read reached the dialing connector")
			return nil, errUnexpectedRemoteContact
		},
	}
	// Built directly rather than through NewWebServer: the cache read touches
	// only cfg, and a full hub server would start the admin fan-out, whose
	// background subscription has nothing to do with this assertion.
	web := &WebServer{cfg: cfg}

	rows := web.remoteTreeThreads(withHostRoutingOrigin(context.Background(), hostRoutingOriginBridge))
	if len(rows) != 1 || rows[0].ID != "alpha-thread" {
		t.Fatalf("cached snapshot rows = %+v, want the cached host row", rows)
	}
}

// errUnexpectedRemoteContact names a remote-hub contact a test asserts must not
// happen.
var errUnexpectedRemoteContact = errors.New("unexpected remote contact")
