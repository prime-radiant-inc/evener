package hub

import (
	"context"
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// scriptedForceStopHost wires a controller WebConfig with one configured host
// "h1" whose RemoteHubSource forwards to a scripted remote hub (no SSH, no
// network: newScriptedRemoteHub's in-memory AppWire transport). online controls
// the component-05 attachment signal.
func scriptedForceStopHost(t *testing.T, online bool) (hubcore.WebConfig, *appsource.Registry, func() []remoteHubCall) {
	t.Helper()
	client, calls := newScriptedRemoteHub(t, func(method string, _ json.RawMessage) any {
		if method == appwire.MethodInitialize {
			return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"}
		}
		return appwire.EmptyResponse{}
	})
	hosts, err := hostreg.New([]hostreg.Host{{Name: "h1", SSH: "h1.example"}})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	source := appsource.NewRemoteHubSource("h1", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})
	source.SetHostOnline(func() bool { return online })
	sources := appsource.NewRegistry()
	sources.Add(source)
	return hubcore.WebConfig{RemoteHostRegistry: hosts}, sources, calls
}

// forceStopCalls narrows a recorded scripted-remote call log to the force-stop
// requests (the log also carries the client's initialize).
func forceStopCalls(calls []remoteHubCall) []remoteHubCall {
	var out []remoteHubCall
	for _, call := range calls {
		if call.method == appwire.MethodEvenerThreadForceStop {
			out = append(out, call)
		}
	}
	return out
}

// TestForceStopRemoteHostTranslatesRefAndForwards drives forceStopThread with a
// host-namespaced ref and asserts it resolves the host through the registry,
// translates the ref into the host's own "local:" spelling, and issues
// evener/thread/forceStop on that host's client — never through
// evener/host/request.
func TestForceStopRemoteHostTranslatesRefAndForwards(t *testing.T) {
	cfg, sources, calls := scriptedForceStopHost(t, true)

	if err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "h1:t1"}, sources); err != nil {
		t.Fatalf("remote force stop: %v", err)
	}

	got := forceStopCalls(calls())
	if len(got) != 1 {
		t.Fatalf("forceStop calls = %+v, want exactly one on the host's client", calls())
	}
	var params struct {
		Ref string `json:"ref"`
	}
	if err := json.Unmarshal(got[0].params, &params); err != nil {
		t.Fatalf("decode forwarded params: %v", err)
	}
	if params.Ref != "local:t1" {
		t.Fatalf("forwarded ref = %q, want %q", params.Ref, "local:t1")
	}
	for _, call := range calls() {
		if call.method == appwire.MethodEvenerHostRequest {
			t.Fatalf("force stop went through evener/host/request: %+v", call)
		}
	}
}

// TestForceStopRemoteHostRefusals pins the two typed refusals the spec names:
// an unknown host is InvalidParams, and a configured-but-unattached host is
// Unavailable — neither forwards anything.
func TestForceStopRemoteHostRefusals(t *testing.T) {
	t.Run("unknown host is invalid params", func(t *testing.T) {
		cfg, sources, calls := scriptedForceStopHost(t, true)

		err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "nope:t1"}, sources)
		assertWireCode(t, err, appwire.CodeInvalidParams)
		if got := forceStopCalls(calls()); len(got) != 0 {
			t.Fatalf("unknown host forwarded: %+v", got)
		}
	})

	t.Run("unattached host is unavailable", func(t *testing.T) {
		cfg, sources, calls := scriptedForceStopHost(t, false)

		err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "h1:t1"}, sources)
		assertWireCode(t, err, appwire.CodeUnavailable)
		if got := forceStopCalls(calls()); len(got) != 0 {
			t.Fatalf("unattached host forwarded: %+v", got)
		}
	})

	t.Run("host with no source is unavailable", func(t *testing.T) {
		hosts, err := hostreg.New([]hostreg.Host{{Name: "h1", SSH: "h1.example"}})
		if err != nil {
			t.Fatalf("hostreg.New: %v", err)
		}
		cfg := hubcore.WebConfig{RemoteHostRegistry: hosts}

		err = forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "h1:t1"}, appsource.NewRegistry())
		assertWireCode(t, err, appwire.CodeUnavailable)
	})
}

// TestForceStopRemoteHostOriginGuardRefusesBeforeForwarding asserts the
// shared host-routing origin guard refuses a remote-originated force-stop typed
// before any request reaches the host's client.
func TestForceStopRemoteHostOriginGuardRefusesBeforeForwarding(t *testing.T) {
	cfg, sources, calls := scriptedForceStopHost(t, true)
	ctx := withHostRoutingOrigin(t.Context(), hostRoutingOriginBridge)

	err := forceStopThread(ctx, cfg, appwire.ThreadForceStopParams{Ref: "h1:t1"}, sources)
	assertWireCode(t, err, appwire.CodeInvalidParams)
	if got := forceStopCalls(calls()); len(got) != 0 {
		t.Fatalf("remote-originated force stop forwarded: %+v", got)
	}
}

// TestForceStopLocalRefNeverRoutesToRemoteHost proves the local path is
// unchanged: a local ref follows the shipped local ownership path and never
// consults the host registry or forwards to a remote client.
func TestForceStopLocalRefNeverRoutesToRemoteHost(t *testing.T) {
	cfg, sources, calls := scriptedForceStopHost(t, true)

	err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:owner"}, sources)
	// No local ownership configured: the shipped local path refuses Unavailable.
	assertWireCode(t, err, appwire.CodeUnavailable)
	if got := forceStopCalls(calls()); len(got) != 0 {
		t.Fatalf("local ref forwarded to a remote host: %+v", got)
	}
}
