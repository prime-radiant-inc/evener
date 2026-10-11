package hub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// recordingStartSource records the wire params it was asked to start, and how
// many starts it was asked for, so a test can prove a refused request never
// reached the source.
type recordingStartSource struct {
	*scriptedAppSource
	gotParams  appwire.ThreadStartParams
	startCalls int
}

func (s *recordingStartSource) StartThread(_ context.Context, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	s.startCalls++
	s.gotParams = params
	return appwire.ThreadStartResponse{Thread: appwire.Thread{ID: "spawned-" + s.ID(), Source: s.ID()}}, nil
}

// TestHubThreadStartRoutesExplicitSource covers the explicit Source precedence:
// a named source receives thread/start, even when Harness would route elsewhere,
// and receives the normalized Source the hub resolved rather than the verbatim
// padded field.
func TestHubThreadStartRoutesExplicitSource(t *testing.T) {
	sources := appsource.NewRegistry()
	source := &recordingStartSource{scriptedAppSource: &scriptedAppSource{id: "remote-a"}}
	sources.Add(source)

	resp, err := hubThreadStart(t.Context(), hubcore.WebConfig{}, sources, appwire.ThreadStartParams{
		Source:  "  remote-a  ",
		Harness: "evener",
		CWD:     "/tmp",
	})
	if err != nil {
		t.Fatalf("hubThreadStart: %v", err)
	}
	if resp.Thread.ID != "spawned-remote-a" {
		t.Fatalf("thread = %+v, want the remote source's thread", resp.Thread)
	}
	if got := source.gotParams.Source; got != "remote-a" {
		t.Fatalf("forwarded params.Source = %q, want the trimmed routing value", got)
	}
}

// TestHubThreadStartUnknownSourceUnavailable covers the existing error shape for
// an unavailable non-local source.
func TestHubThreadStartUnknownSourceUnavailable(t *testing.T) {
	_, err := hubThreadStart(t.Context(), hubcore.WebConfig{}, appsource.NewRegistry(), appwire.ThreadStartParams{Source: "missing"})
	if err == nil {
		t.Fatal("hubThreadStart accepted an unknown source")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("err = %v, want CodeUnavailable", err)
	}
	if wire.Message != "spawn source is not available: missing" {
		t.Fatalf("message = %q, want the existing unavailable shape", wire.Message)
	}
}

// TestHubThreadStartEmptySourceDefaultsLocal covers the empty case: with no
// Source (and no harness route) thread/start takes the local spawn path instead
// of a source lookup. A nil Spawner is the observable local-path sentinel.
func TestHubThreadStartEmptySourceDefaultsLocal(t *testing.T) {
	for name, params := range map[string]appwire.ThreadStartParams{
		"empty":        {},
		"whitespace":   {Source: "   "},
		"explicit-loc": {Source: "local"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := hubThreadStart(t.Context(), hubcore.WebConfig{}, appsource.NewRegistry(), params)
			if err == nil || !strings.Contains(err.Error(), "spawner not configured") {
				t.Fatalf("err = %v, want local spawner path (spawner not configured)", err)
			}
		})
	}
}

// TestHubThreadStartRefusesHarnessNamingHostSource pins component 06's write
// contract (docs/superpowers/specs/2026-09-14-multi-host-06-fleet-view.md,
// §"Write contract (session targeting)"): a harness value that names a
// registered non-local source must be refused InvalidParams rather than routed
// — on the legacy launchSourceID fallback path (empty Source) and when it is
// forwarded alongside a set Source alike. The fallback itself is retained: a
// harness that names no registered source still resolves as before (an
// unavailable lookup, never a silent fall-through to the local spawner).
func TestHubThreadStartRefusesHarnessNamingHostSource(t *testing.T) {
	for name, params := range map[string]appwire.ThreadStartParams{
		"fallback (empty Source)": {Harness: "remote-a", CWD: "/tmp"},
		"forwarded with Source":   {Source: "remote-b", Harness: "remote-a", CWD: "/tmp"},
	} {
		t.Run(name, func(t *testing.T) {
			sources := appsource.NewRegistry()
			harnessNamed := &recordingStartSource{scriptedAppSource: &scriptedAppSource{id: "remote-a"}}
			forwardTarget := &recordingStartSource{scriptedAppSource: &scriptedAppSource{id: "remote-b"}}
			sources.Add(harnessNamed)
			sources.Add(forwardTarget)

			_, err := hubThreadStart(t.Context(), hubcore.WebConfig{}, sources, params)
			assertWireCode(t, err, appwire.CodeInvalidParams)
			if !strings.Contains(err.Error(), "remote-a") {
				t.Fatalf("refusal %q does not name the harness value", err)
			}
			if harnessNamed.startCalls != 0 || forwardTarget.startCalls != 0 {
				t.Fatalf("a harness naming the host source was routed (%d calls to remote-a, %d to remote-b), want a refusal", harnessNamed.startCalls, forwardTarget.startCalls)
			}
		})
	}

	// The retained fallback: a harness naming no registered source still
	// resolves through launchSourceID — an unavailable lookup here — rather
	// than falling through to the local spawner in silence.
	sources := appsource.NewRegistry()
	sources.Add(&recordingStartSource{scriptedAppSource: &scriptedAppSource{id: "remote-a"}})
	_, err := hubThreadStart(t.Context(), hubcore.WebConfig{}, sources, appwire.ThreadStartParams{Harness: "claude", CWD: "/tmp"})
	assertWireCode(t, err, appwire.CodeUnavailable)
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Message != "spawn source is not available: claude" {
		t.Fatalf("err = %v, want the retained fallback's unavailable resolution", err)
	}
}

// TestHubThreadStartRefusesRemoteOriginatedHostRouting pins the recipient-side
// half of the runtime loop guard (design §2 "Topology"; component 05,
// §"The receiving hub must reject a non-local resolution for a
// remote-originated thread/start"): a request that arrived over a peer hub's
// attach bridge is served from local state only, so a spawn resolving to any
// non-local source — from a set Source or the harness fallback — is refused
// typed before it is routed. A local-originated request with the same fields
// still routes, and a remote-originated request resolving to local still takes
// the local spawn path.
func TestHubThreadStartRefusesRemoteOriginatedHostRouting(t *testing.T) {
	remoteOrigin := withHostRoutingOrigin(context.Background(), hostRoutingOriginBridge)

	for name, params := range map[string]appwire.ThreadStartParams{
		"explicit Source":      {Source: "remote-a", CWD: "/tmp"},
		"unregistered harness": {Harness: "not-a-host", CWD: "/tmp"},
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			sources := appsource.NewRegistry()
			source := &recordingStartSource{scriptedAppSource: &scriptedAppSource{id: "remote-a"}}
			sources.Add(source)

			_, err := hubThreadStart(remoteOrigin, hubcore.WebConfig{}, sources, params)
			assertWireCode(t, err, appwire.CodeInvalidParams)
			if !strings.Contains(err.Error(), hostRoutingOriginBridge) {
				t.Fatalf("refusal %q does not name the origin %q", err, hostRoutingOriginBridge)
			}
			if source.startCalls != 0 {
				t.Fatalf("remote-originated spawn was routed to another source (%d StartThread calls), want a refusal", source.startCalls)
			}
		})
	}

	// A preserved harness naming one of this hub's own configured hosts is
	// refused under a remote origin too, by the harness rule — it must never
	// resolve to that source and be routed onward.
	t.Run("refuses a preserved harness naming a configured host", func(t *testing.T) {
		sources := appsource.NewRegistry()
		source := &recordingStartSource{scriptedAppSource: &scriptedAppSource{id: "remote-a"}}
		sources.Add(source)

		_, err := hubThreadStart(remoteOrigin, hubcore.WebConfig{}, sources, appwire.ThreadStartParams{Harness: "remote-a", CWD: "/tmp"})
		assertWireCode(t, err, appwire.CodeInvalidParams)
		if source.startCalls != 0 {
			t.Fatalf("remote-originated spawn was routed to another source (%d StartThread calls), want a refusal", source.startCalls)
		}
	})

	t.Run("local resolution still spawns locally", func(t *testing.T) {
		_, err := hubThreadStart(remoteOrigin, hubcore.WebConfig{}, appsource.NewRegistry(), appwire.ThreadStartParams{Source: "local", CWD: "/tmp"})
		if err == nil || !strings.Contains(err.Error(), "spawner not configured") {
			t.Fatalf("err = %v, want the local spawner path", err)
		}
	})

	t.Run("local-originated request still routes", func(t *testing.T) {
		sources := appsource.NewRegistry()
		source := &recordingStartSource{scriptedAppSource: &scriptedAppSource{id: "remote-a"}}
		sources.Add(source)
		if _, err := hubThreadStart(t.Context(), hubcore.WebConfig{}, sources, appwire.ThreadStartParams{Source: "remote-a", CWD: "/tmp"}); err != nil {
			t.Fatalf("local-originated spawn: %v", err)
		}
		if source.startCalls != 1 {
			t.Fatalf("local-originated spawn calls = %d, want 1", source.startCalls)
		}
	})
}

// TestHubRPCThreadStartRefusesBridgeOriginatedFanOut drives the recipient-side
// refusal through the real /rpc edge: a connection presenting the cooperative
// bridge marker is stamped remote-originated (web.go), and its thread/start
// naming a configured host is refused typed before any source is called. The
// identical request on an unmarked (local) connection still routes.
func TestHubRPCThreadStartRefusesBridgeOriginatedFanOut(t *testing.T) {
	source := &recordingStartSource{scriptedAppSource: &scriptedAppSource{id: "remote-a"}}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	dial := func(t *testing.T, header http.Header) *appwire.Client {
		t.Helper()
		transport, err := appwire.DialWebSocketWithHeaders(t.Context(), "ws"+strings.TrimPrefix(srv.URL, "http")+"/rpc", srv.Client(), header)
		if err != nil {
			t.Fatalf("dial hub rpc: %v", err)
		}
		client := appwire.NewClient(transport)
		client.Start(t.Context())
		t.Cleanup(func() { _ = client.Close() })
		if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		return client
	}

	bridged := http.Header{}
	bridged.Set(bridgeOriginHeader, "1")
	_, err := dial(t, bridged).ThreadStart(t.Context(), appwire.ThreadStartParams{Source: "remote-a", CWD: "/tmp"})
	assertWireCode(t, err, appwire.CodeInvalidParams)
	if !strings.Contains(err.Error(), hostRoutingOriginBridge) {
		t.Fatalf("refusal %q does not name the origin %q", err, hostRoutingOriginBridge)
	}
	if source.startCalls != 0 {
		t.Fatalf("bridge-originated spawn reached the host source (%d StartThread calls), want a refusal", source.startCalls)
	}

	if _, err := dial(t, nil).ThreadStart(t.Context(), appwire.ThreadStartParams{Source: "remote-a", CWD: "/tmp"}); err != nil {
		t.Fatalf("local-originated thread/start: %v", err)
	}
	if source.startCalls != 1 {
		t.Fatalf("local-originated spawn calls = %d, want 1", source.startCalls)
	}
}
