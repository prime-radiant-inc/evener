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

// allRemoteThreadFeatures is the handshake feature set every action-forwarding
// remote hub advertises. Tests vary it per scenario.
func allRemoteThreadFeatures() appwire.FeatureSet {
	return appwire.FeatureSet{
		ThreadList:      true,
		ThreadTurnsList: true,
		TurnStart:       true,
		TurnSteer:       true,
		ThreadClear:     true,
		ThreadShutdown:  true,
		ForkFromTurn:    true,
	}
}

// allRemoteDaemonCapabilities is what a healthy remote session advertises for
// its own daemon.
func allRemoteDaemonCapabilities() appwire.ThreadCapabilities {
	return appwire.ThreadCapabilities{
		Send:              true,
		Steer:             true,
		Interrupt:         true,
		Compact:           true,
		Clear:             true,
		ForkFromTurn:      true,
		Shutdown:          true,
		ChangeModel:       true,
		ChangeVisionModel: true,
		Queue:             true,
		Goal:              true,
		SharedNotes:       true,
		Rename:            true,
		SkillInput:        true,
		CommandInput:      true,
		StopSubagent:      true,
		PageBefore:        true,
	}
}

// remoteCapabilityFixture builds the production source registry around one
// configured host whose remote hub advertises features in its initialize
// handshake.
func remoteCapabilityFixture(t *testing.T, status string, clientFeatures appwire.FeatureSet) *appsource.Registry {
	t.Helper()
	client, _ := newScriptedRemoteHub(t, func(method string, _ json.RawMessage) any {
		switch method {
		case appwire.MethodInitialize:
			return appwire.InitializeResponse{
				ProtocolVersion: appwire.ProtocolVersion,
				SourceID:        "local",
				Features:        clientFeatures,
			}
		case appwire.MethodThreadList:
			return appwire.ThreadListResponse{Data: []appwire.Thread{{
				ID:     "t1",
				Source: "local",
				Status: appwire.ThreadStatus{Type: status},
				Evener: appwire.EvenerThread{Ref: "local:t1", Capabilities: allRemoteDaemonCapabilities()},
			}}}
		default:
			return appwire.EmptyResponse{}
		}
	})
	cfg := hubcore.WebConfig{
		RemoteHosts: []hostreg.Host{{Name: "h1", SSH: "h1.example"}},
		RemoteHostClient: func(context.Context, string) (*appwire.Client, error) {
			return client, nil
		},
		RemoteHostClientIfAttached: func(string) (*appwire.Client, bool) {
			return client, true
		},
	}
	return newHubSourceRegistry(cfg)
}

func listRemoteThreadCapabilities(t *testing.T, registry *appsource.Registry) appwire.ThreadCapabilities {
	t.Helper()
	source, ok := registry.Source("h1")
	if !ok {
		t.Fatal("production registry has no source for h1")
	}
	resp, err := source.ListThreads(t.Context(), appwire.ThreadListParams{SourceIDs: []string{"h1"}})
	if err != nil {
		t.Fatalf("list remote threads: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("remote threads = %d, want 1", len(resp.Data))
	}
	return resp.Data[0].Evener.Capabilities
}

// The capability mask must advertise the actions the peer advertises and the
// controller forwards, not the static Shutdown-only set that left remote
// sessions with no Send, Steer or Queue controls.
func TestRemoteHostEffectiveThreadCapabilitiesFollowThePeerHandshake(t *testing.T) {
	features := allRemoteThreadFeatures()
	registry := remoteCapabilityFixture(t, appwire.ThreadStatusIdle, features)

	want := appwire.ThreadCapabilities{
		Send:              true,
		Steer:             true,
		Interrupt:         true,
		Compact:           true,
		Clear:             true,
		ForkFromTurn:      true,
		Shutdown:          true,
		ChangeModel:       true,
		ChangeVisionModel: true,
		Queue:             true,
		Goal:              true,
		SharedNotes:       true,
		Rename:            true,
		SkillInput:        true,
		CommandInput:      true,
		StopSubagent:      true,
	}
	if got := listRemoteThreadCapabilities(t, registry); got != want {
		t.Fatalf("remote thread capabilities = %+v, want %+v", got, want)
	}
}

// A hub-level feature the peer disclaims stays masked even when its daemon
// claims the action, while the actions with no hub-level feature bit follow the
// daemon's own claim.
func TestRemoteHostCapabilitiesMaskDisclaimedHubFeatures(t *testing.T) {
	registry := remoteCapabilityFixture(t, appwire.ThreadStatusIdle, appwire.FeatureSet{})

	got := listRemoteThreadCapabilities(t, registry)
	if got.Send || got.Steer || got.Clear || got.ForkFromTurn || got.Shutdown {
		t.Fatalf("disclaimed hub features survived the mask: %+v", got)
	}
	want := appwire.ThreadCapabilities{
		Interrupt:         true,
		Compact:           true,
		ChangeModel:       true,
		ChangeVisionModel: true,
		Queue:             true,
		Goal:              true,
		SharedNotes:       true,
		Rename:            true,
		SkillInput:        true,
		CommandInput:      true,
		StopSubagent:      true,
	}
	if got != want {
		t.Fatalf("effective capabilities = %+v, want %+v", got, want)
	}
}

// A remote session whose daemon is not running is the composer case from
// #4874: the client offers its follow-up Send only when the thread advertises
// Send, and the first Send resumes the session on its own host.
func TestRemoteHostShutDownSessionAdvertisesSend(t *testing.T) {
	features := allRemoteThreadFeatures()
	registry := remoteCapabilityFixture(t, appwire.ThreadStatusNotLoaded, features)

	got := listRemoteThreadCapabilities(t, registry)
	if !got.Send {
		t.Fatalf("notLoaded remote session capabilities = %+v, want Send advertised", got)
	}
}

// An explicit Connect reads nothing from the remote hub beyond the attach
// itself: the thread mask comes from the client's initialize response, so no
// hub-scope surface is read to warm it.
func TestHostAttachReadsNoHubScopeSurfaces(t *testing.T) {
	features := allRemoteThreadFeatures()
	client, calls := newScriptedRemoteHub(t, func(method string, _ json.RawMessage) any {
		switch method {
		case appwire.MethodInitialize:
			return appwire.InitializeResponse{
				ProtocolVersion: appwire.ProtocolVersion,
				SourceID:        "local",
				Features:        features,
			}
		case appwire.MethodThreadList:
			return appwire.ThreadListResponse{Data: []appwire.Thread{{
				ID:     "t1",
				Source: "local",
				Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
				Evener: appwire.EvenerThread{Ref: "local:t1", Capabilities: allRemoteDaemonCapabilities()},
			}}}
		default:
			return appwire.EmptyResponse{}
		}
	})
	source := appsource.NewRemoteHubSource("h1", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})
	sources := appsource.NewRegistry()
	sources.Add(source)
	hosts, err := hostreg.New([]hostreg.Host{{Name: "h1", SSH: "h1.example"}})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	cfg := hubcore.WebConfig{
		RemoteHosts: []hostreg.Host{{Name: "h1", SSH: "h1.example"}},
		RemoteHostClient: func(context.Context, string) (*appwire.Client, error) {
			return client, nil
		},
	}
	if _, err := hubHostAttach(t.Context(), cfg, sources, hosts, appwire.HostAttachParams{Host: "h1"}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	hubScope := map[string]bool{
		appwire.MethodEvenerLaunchGetLayer: true,
		appwire.MethodEvenerLaunchResolve:  true,
		appwire.MethodModelList:            true,
		appwire.MethodEvenerPluginList:     true,
		appwire.MethodEvenerAuthList:       true,
		appwire.MethodEvenerInstanceList:   true,
	}
	for _, call := range calls() {
		if hubScope[call.method] {
			t.Errorf("attach read a hub-scope surface: %s", call.method)
		}
	}
	got := listRemoteThreadCapabilities(t, sources)
	if !got.Send || !got.Steer || !got.Queue {
		t.Fatalf("thread mask did not follow the client's features: %+v", got)
	}
}
