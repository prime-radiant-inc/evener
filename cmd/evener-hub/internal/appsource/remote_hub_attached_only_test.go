package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/appwire"
)

// assertSessionUnavailableForHost fails unless err is the typed unavailable
// error the fleet view's auto-resume/refusal gates act on, naming host.
func assertSessionUnavailableForHost(t *testing.T, op, host string, err error) {
	t.Helper()
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("%s error %T=%v, want WireError", op, err, err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok || wire.Code != appwire.CodeUnavailable || data.EvenerErrorInfo != appwire.ErrorSessionUnavailable {
		t.Fatalf("%s: wire=%+v", op, wire)
	}
	if !strings.Contains(wire.Message, host) {
		t.Fatalf("%s message %q does not name host %q", op, wire.Message, host)
	}
}

// A RemoteHubSource whose host is not attached must refuse every direct call
// with the typed unavailable error instead of resolving through the dialing
// connector: component 05, §"Every other remote call is non-dialing" and
// component 06 acceptance criterion 13 (Ensure called zero times, no transport
// opened). The attached-only lookup is the client resolver; the wired
// RemoteHubClientFunc is a fallback for tests only.
func TestRemoteHubSourceUnattachedCallIsUnavailableWithoutDialing(t *testing.T) {
	var dials atomic.Int64
	dial := func(context.Context, string) (*appwire.Client, error) {
		dials.Add(1)
		return nil, errors.New("the attached-only path must never dial")
	}
	source := NewRemoteHubSource("alpha", nil, dial)
	source.SetHostClientIfAttached(func(host string) (*appwire.Client, bool) {
		if host != "alpha" {
			t.Fatalf("attached-only lookup for %q, want alpha", host)
		}
		return nil, false
	})
	ctx := context.Background()

	_, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: "alpha:t1"})
	assertSessionUnavailableForHost(t, "ReadThread", "alpha", err)

	_, err = source.StartThread(ctx, appwire.ThreadStartParams{CWD: "/tmp"})
	assertSessionUnavailableForHost(t, "StartThread", "alpha", err)

	// SubscribeThread is the notification rebind path: it must read the fresh
	// client through the attached-only lookup so a rebind on a detach edge never
	// re-dials a dormant host (component 05, §"One notification consumer per
	// client").
	_, err = source.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: "alpha:t1"})
	assertSessionUnavailableForHost(t, "SubscribeThread", "alpha", err)

	_, err = source.HostCapabilities(ctx)
	assertSessionUnavailableForHost(t, "HostCapabilities", "alpha", err)

	if got := dials.Load(); got != 0 {
		t.Fatalf("the dialing connector was called %d times; want 0", got)
	}
}

// The capability probe reads ProtocolVersion/SourceID/Features from the attach
// handshake seam and OS/Arch/HubVersion from the preflight seam, over the
// attached-only client: component 05, §"Capability probe". ProtocolVersion,
// SourceID, and Features are handshake-only — the connection-scoped initialize
// cannot be re-run on the already-initialized channel, and no other wire call
// reports them. HubVersion is preflight-owned: the handshake's
// ServerInfo.Version is the hub's package constant ("0.1.0"), not the running
// build, so copying it would clobber the facts seam's buildinfo.Version().
func TestHostCapabilitiesReadsAttachedHandshakeFacts(t *testing.T) {
	client, _ := newScriptedClient(t, capabilityReply("gpt-x", "pl"))
	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})
	source.SetHostClientIfAttached(func(string) (*appwire.Client, bool) { return client, true })
	source.SetHostFacts(func(context.Context, string, *appwire.Client) (HostFacts, error) {
		return HostFacts{
			ProtocolVersion: "preflight-proto",
			HubVersion:      "preflight-ver",
			OS:              "linux",
			Arch:            "amd64",
			Features:        appwire.FeatureSet{ThreadList: true},
		}, nil
	})
	source.SetHostHandshake(func(host string, got *appwire.Client) (appwire.InitializeResponse, bool) {
		if host != "host" {
			t.Fatalf("handshake lookup for %q, want host", host)
		}
		if got != client {
			t.Fatalf("handshake lookup for a different client generation")
		}
		return appwire.InitializeResponse{
			ProtocolVersion: "handshake-proto",
			ServerInfo:      appwire.ServerInfo{Name: "remote", Version: "9.9.9"},
			SourceID:        "local",
			Features:        appwire.FeatureSet{TurnStart: true},
		}, true
	})

	caps, err := source.HostCapabilities(t.Context())
	if err != nil {
		t.Fatalf("HostCapabilities: %v", err)
	}
	if caps.ProtocolVersion != "handshake-proto" {
		t.Fatalf("ProtocolVersion = %q, want handshake-proto", caps.ProtocolVersion)
	}
	// HubVersion stays facts-owned. The handshake stubs a deliberately distinct
	// ServerInfo.Version ("9.9.9"), so a value of "9.9.9" here would prove the
	// probe copied the hub's package constant over the running build the facts
	// seam reported.
	if caps.HubVersion != "preflight-ver" {
		t.Fatalf("HubVersion = %q, want the facts seam value preflight-ver (not the handshake ServerInfo.Version 9.9.9)", caps.HubVersion)
	}
	if caps.HubSourceID != "local" {
		t.Fatalf("HubSourceID = %q, want local", caps.HubSourceID)
	}
	if !caps.Features.TurnStart || caps.Features.ThreadList {
		t.Fatalf("Features = %+v, want the handshake set", caps.Features)
	}
	if caps.OS != "linux" || caps.Arch != "amd64" {
		t.Fatalf("os/arch = %q/%q, want the preflight facts", caps.OS, caps.Arch)
	}
}

// A handshake seam that reports false — no live channel installed, or a channel
// that is not the generation the probe resolved — must refuse with the typed
// SessionUnavailable and cache nothing. Falling through would cache a snapshot
// that pairs this client's wire reads with no handshake facts, and a later call
// on the same client would serve that partial snapshot instead of refusing the
// generation change.
func TestHostCapabilitiesHandshakeFalseRefusesWithoutCaching(t *testing.T) {
	client, _ := newScriptedClient(t, capabilityReply("gpt-x", "pl"))
	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})
	source.SetHostClientIfAttached(func(string) (*appwire.Client, bool) { return client, true })
	source.SetHostFacts(func(context.Context, string, *appwire.Client) (HostFacts, error) {
		return HostFacts{
			ProtocolVersion: "preflight-proto",
			HubVersion:      "preflight-ver",
			OS:              "linux",
			Arch:            "amd64",
		}, nil
	})
	source.SetHostHandshake(func(string, *appwire.Client) (appwire.InitializeResponse, bool) {
		return appwire.InitializeResponse{}, false
	})

	_, err := source.HostCapabilities(context.Background())
	assertSessionUnavailableForHost(t, "HostCapabilities", "host", err)

	if source.probe != nil {
		t.Fatalf("a refused handshake cached a probe: %+v", source.probe)
	}
}

// When the host is attached, the same calls resolve through the attached-only
// client (the wired dialer stays untouched), so the lookup is a real
// replacement and not merely a refusal path.
func TestRemoteHubSourceAttachedCallUsesAttachedOnlyClient(t *testing.T) {
	var dials atomic.Int64
	dial := func(context.Context, string) (*appwire.Client, error) {
		dials.Add(1)
		return nil, errors.New("the attached-only path must never dial")
	}
	source := NewRemoteHubSource("alpha", nil, dial)
	live, calls := newScriptedClient(t, func(string, json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "t1"}}}
	})
	source.SetHostClientIfAttached(func(string) (*appwire.Client, bool) { return live, true })

	resp, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{Ref: "alpha:t1"})
	if err != nil {
		t.Fatalf("ReadThread: %v", err)
	}
	if resp.Thread.ID != "t1" {
		t.Fatalf("ReadThread thread = %q, want t1", resp.Thread.ID)
	}
	var reads int
	for _, call := range calls() {
		if call.method == appwire.MethodThreadRead {
			reads++
		}
	}
	if reads != 1 {
		t.Fatalf("attached client served %d thread/read calls, want 1", reads)
	}
	if got := dials.Load(); got != 0 {
		t.Fatalf("the dialing connector was called %d times; want 0", got)
	}
}
