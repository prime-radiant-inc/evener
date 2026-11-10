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
		VoicePresentationContextV1: true,
		Send:                       true,
		Steer:                      true,
		Interrupt:                  true,
		Compact:                    true,
		Clear:                      true,
		ForkFromTurn:               true,
		Shutdown:                   true,
		ChangeModel:                true,
		ChangeVisionModel:          true,
		Queue:                      true,
		Goal:                       true,
		SharedNotes:                true,
		Rename:                     true,
		SkillInput:                 true,
		CommandInput:               true,
		StopSubagent:               true,
		PageBefore:                 true,
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

// Remote turn methods reach the peer's public hub input, not its journal-backed
// voice transport. Even an explicit peer claim cannot make that path preserve
// trusted presentation evidence, so ordinary Send and Steer remain available
// while the voice capability stays masked.
func TestRemoteHostVoicePresentationStaysMaskedAtPublicHubBoundary(t *testing.T) {
	features := allRemoteThreadFeatures()
	features.VoicePresentationContextV1 = true
	registry := remoteCapabilityFixture(t, appwire.ThreadStatusIdle, features)
	if got := listRemoteThreadCapabilities(t, registry); got.VoicePresentationContextV1 || !got.Send || !got.Steer {
		t.Fatalf("remote capabilities = %+v, want ordinary Send and Steer without voice presentation", got)
	}

	peer := newHubAppServer(hubcore.WebConfig{}, appsource.NewRegistry())
	defer peer.Shutdown(t.Context())
	transport, err := peer.NewInProcessTransport(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	client := appwire.NewClient(transport)
	client.Start(t.Context())
	defer client.Close()
	mustInitializeHubClient(t, client)
	if !client.Features().VoicePresentationContextV1 {
		t.Fatal("peer does not advertise its own voice presentation support")
	}
	source := appsource.NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})
	input := []appwire.InputItem{{Type: "text", Text: "REFERENCE-149", VoicePresentation: &appwire.VoicePresentationContext{
		Version: 1, DeliveryID: "delivery", BindingID: "binding", Epoch: "epoch", InstanceID: "instance",
		CanonicalSourceItemID: "source", OriginatingRequestID: "request", FrontierID: "frontier",
		RunGeneration: 1, PresentationGeneration: 1, GeneratedTranscript: "SPEECH-151", SampleRateHz: 24000,
		Items: []appwire.VoicePresentationItem{{ResponseID: "response", ItemID: "item", GeneratedSamples: 480, QueuedSamples: 480, PlayedSamples: 240}},
	}}}
	for _, method := range []string{appwire.MethodTurnStart, appwire.MethodTurnSteer} {
		t.Run(method, func(t *testing.T) {
			if err := appwire.ValidateVoicePresentationInput(input, method, "human", "instance"); err != nil {
				t.Fatalf("invalid presentation fixture: %v", err)
			}
			// The controller's private identity cannot cross the peer transport.
			ctx := context.WithValue(t.Context(), voicePresentationContextKey{}, true)
			var err error
			if method == appwire.MethodTurnStart {
				_, err = source.StartTurn(ctx, appwire.TurnStartParams{Ref: "host:thread", ExpectedInstanceID: "instance", ClientMutationID: "mutation", Input: input})
			} else {
				_, err = source.SteerTurn(ctx, appwire.TurnSteerParams{Ref: "host:thread", ExpectedInstanceID: "instance", ClientMutationID: "mutation", Input: input})
			}
			wire, ok := wireErrorFromError(err)
			if !ok || wire.Code != appwire.CodeInvalidParams {
				t.Fatalf("forwarded presentation reached session routing: %v", err)
			}
		})
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
