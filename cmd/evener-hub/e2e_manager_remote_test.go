package hub

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/e2ecap"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// The hubs and bearer-authenticated /rpc connections are real. Only the model provider is scripted. The bridge marker denotes an
// honest cooperating hub, not cryptographic proof of a particular peer.
func TestManagerRemotePeerIdentity(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs two hubs + daemon")
	}
	provider, err := fakellm.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	hubA, hubB := startHubStack(t, provider), startHubStack(t, provider)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	idA, err := hubcore.NewHubIdentityStore(filepath.Join(hubA.home, "state", "evener", "index.db")).HubID()
	if err != nil {
		t.Fatal(err)
	}
	idB, err := hubcore.NewHubIdentityStore(filepath.Join(hubB.home, "state", "evener", "index.db")).HubID()
	if err != nil || idB == idA {
		t.Fatalf("two persisted hub identities: A=%q B=%q err=%v", idA, idB, err)
	}
	human := hubB.dialRPC(ctx, t)

	// A human cannot assert a peer identity even if it holds the hub token.
	if _, err := human.IdentifySessionToolsPeer(ctx, appwire.SessionToolsPeerParams{HubID: idA}); err == nil {
		t.Fatal("human identity accepted")
	}
	bridge := dialManagerPeerBridge(ctx, t, hubB)
	identified, err := bridge.IdentifySessionToolsPeer(ctx, appwire.SessionToolsPeerParams{HubID: idA})
	if err != nil || identified.HubID != idB {
		t.Fatalf("identify A->B: response=%+v err=%v", identified, err)
	}
	// Repetition on one connection is idempotent, never a new owner selection.
	if repeated, err := bridge.IdentifySessionToolsPeer(ctx, appwire.SessionToolsPeerParams{HubID: idA}); err != nil || repeated != identified {
		t.Fatalf("repeat identify: response=%+v err=%v", repeated, err)
	}
	if _, err := bridge.IdentifySessionToolsPeer(ctx, appwire.SessionToolsPeerParams{HubID: idB}); err == nil {
		t.Fatal("conflicting reidentify accepted")
	}
	fresh := dialManagerPeerBridge(ctx, t, hubB)
	again, err := fresh.IdentifySessionToolsPeer(ctx, appwire.SessionToolsPeerParams{HubID: idA})
	if err != nil || again.HubID != idB {
		t.Fatalf("reconnect identify: response=%+v err=%v", again, err)
	}
	// Neither a human nor an identified peer can change a session's Manager bit.
	for _, client := range []*appwire.Client{human, bridge, fresh} {
		for _, enabled := range []bool{false, true} {
			var response appwire.EmptyResponse
			err := client.Request(ctx, "evener/thread/manager/set", map[string]any{"ref": "local:workspace", "enabled": enabled, "hubId": idA}, &response)
			var wire appwire.WireError
			if !errors.As(err, &wire) || wire.Code != appwire.CodeMethodNotFound {
				t.Fatalf("toggle remains callable: enabled=%t error=%v", enabled, err)
			}
		}
	}

}

func dialManagerPeerBridge(ctx context.Context, t *testing.T, stack hubStack) *appwire.Client {
	t.Helper()
	header := http.Header{}
	header.Set("Authorization", "Bearer "+stack.token)
	header.Set(bridgeOriginHeader, "1")
	transport, err := appwire.DialWebSocketWithHeaders(ctx, "ws://"+stack.addr+"/rpc", nil, header)
	if err != nil {
		t.Fatal(err)
	}
	client := appwire.NewClient(transport)
	t.Cleanup(func() { _ = client.Close() })
	client.Start(context.Background())
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ClientInfo: appwire.ClientInfo{Name: "evener-hub", Version: "test"}}); err != nil {
		t.Fatal(err)
	}
	if !client.Features().SessionTools {
		t.Fatal("peer did not negotiate SessionTools")
	}
	return client
}

func TestE2E_ManagerRemoteSharedInput(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs hub + daemon")
	}
	provider, err := fakellm.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	host := startHubStack(t, provider)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	client := host.dialRPC(ctx, t)
	started, err := clientRequest[appwire.ThreadStartResponse](ctx, client, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness: "evener", CWD: host.workDir, Model: host.model,
		LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off"},
	})
	if err != nil {
		t.Fatal(err)
	}
	localRef, instance := started.Thread.Evener.Ref, started.Thread.Evener.InstanceID
	t.Cleanup(func() {
		shutdown, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		if _, err := clientRequest[appwire.EmptyResponse](shutdown, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: localRef}); err != nil {
			t.Errorf("remote worker shutdown: %v", err)
		}
	})
	parsed, err := appwire.ParseRef(localRef)
	if err != nil {
		t.Fatal(err)
	}
	ref := appwire.Ref{SourceID: "remote-b", ThreadID: parsed.ThreadID}.String()
	source := appsource.NewRemoteHubSource("remote-b", nil, func(context.Context, string) (*appwire.Client, error) { return client, nil })
	read, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref})
	if err != nil {
		t.Fatal(err)
	}
	caps := read.Thread.Evener.Capabilities
	wantCaps := appwire.ThreadCapabilities{
		Send: true, Steer: true, Interrupt: true, Compact: true, Clear: true,
		ForkFromTurn: true, Shutdown: true, ChangeModel: true, ChangeVisionModel: true,
		Queue: true, Goal: true, SharedNotes: true, Rename: true,
		SkillInput: true, CommandInput: true, StopSubagent: true,
	}
	if caps != wantCaps {
		t.Fatalf("shared remote input qualification: %+v", caps)
	}
	for _, item := range []appwire.InputItem{
		{Type: "skill", Name: "invalid", Text: "skill selections cannot carry text"},
		{Type: "command", Name: "invalid", Text: "command selections cannot carry text"},
		{Type: "unsupported", Text: "not a qualified input mode"},
	} {
		invalid, err := source.StartTurn(ctx, appwire.TurnStartParams{Ref: ref, ExpectedInstanceID: instance, ClientMutationID: newMutationID(t), Input: []appwire.InputItem{item}})
		if err == nil || invalid.Receipt.Disposition == appwire.MutationDispositionApplied {
			t.Fatalf("unsupported or malformed remote input %q was accepted: receipt=%+v err=%v", item.Type, invalid.Receipt, err)
		}
	}
	const opening, steer, queued = "REMOTE-HUMAN-OPENING", "REMOTE-HUMAN-STEER", "REMOTE-HUMAN-QUEUE"
	openingID := newMutationID(t)
	openingRequest := appwire.TurnStartParams{Ref: ref, ExpectedInstanceID: instance, ClientMutationID: openingID, Input: []appwire.InputItem{{Type: "text", Text: opening}}}
	sent, err := source.StartTurn(ctx, openingRequest)
	if err != nil || sent.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("remote Send receipt=%+v err=%v", sent.Receipt, err)
	}
	replay, err := source.StartTurn(ctx, openingRequest)
	if err != nil || replay.Receipt.Disposition != appwire.MutationDispositionReplayed || replay.Receipt.ClientMutationID != openingID {
		t.Fatalf("remote replay ID changed: receipt=%+v err=%v", replay.Receipt, err)
	}
	openingRequest.Input = []appwire.InputItem{{Type: "text", Text: "MUST-NOT-REPLAY-DIFFERENT-INPUT"}}
	if changed, err := source.StartTurn(ctx, openingRequest); err == nil || changed.Receipt.Disposition == appwire.MutationDispositionApplied {
		t.Fatalf("mutation ID was accepted for a different payload: receipt=%+v err=%v", changed.Receipt, err)
	}
	first, err := provider.Next(ctx.Done())
	if err != nil || !first.Contains(opening) {
		t.Fatalf("remote Send did not reach daemon/provider: call=%v err=%v", first, err)
	}
	stale, err := source.QueueTurn(ctx, appwire.TurnQueueParams{Ref: ref, ExpectedInstanceID: "stale-instance", ClientMutationID: newMutationID(t), Input: []appwire.InputItem{{Type: "text", Text: "MUST-NOT-APPEAR"}}})
	if err == nil || stale.Receipt.Disposition == appwire.MutationDispositionApplied {
		t.Fatalf("stale instance admitted input: receipt=%+v err=%v", stale.Receipt, err)
	}
	steered, err := source.SteerTurn(ctx, appwire.TurnSteerParams{Ref: ref, ExpectedInstanceID: instance, ClientMutationID: newMutationID(t), Input: []appwire.InputItem{{Type: "text", Text: steer}}})
	if err != nil || steered.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("remote Steer receipt=%+v err=%v", steered.Receipt, err)
	}
	queuedResult, err := source.QueueTurn(ctx, appwire.TurnQueueParams{Ref: ref, ExpectedInstanceID: instance, ClientMutationID: newMutationID(t), Input: []appwire.InputItem{{Type: "text", Text: queued}}})
	if err != nil || queuedResult.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("remote Queue receipt=%+v err=%v", queuedResult.Receipt, err)
	}
	first.RespondToolCall("read_file", map[string]any{"file_path": host.readableFile})
	second, err := provider.Next(ctx.Done())
	if err != nil || !second.Contains(steer) {
		t.Fatalf("remote Steer did not reach next model round: call=%v err=%v", second, err)
	}
	second.RespondToolCall("communicate", communicateArgs("steering received"))
	third, err := provider.Next(ctx.Done())
	if err != nil || !third.Contains(queued) {
		t.Fatalf("remote Queue did not reach next turn: call=%v err=%v", third, err)
	}
	third.RespondToolCall("communicate", communicateArgs("queue received"))
	turns, err := readTranscriptTurns(ctx, client, localRef, "")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, turn := range turns {
		for _, item := range turn.Items {
			if item.Text == opening || item.Text == steer || item.Text == queued {
				seen[item.Text] = true
				if item.Actor != nil && item.Actor.Kind != "human" {
					t.Fatalf("human remote input actor changed: %+v", item.Actor)
				}
			}
		}
	}
	if !seen[opening] || !seen[steer] || !seen[queued] {
		t.Fatalf("remote history lost input: seen=%v", seen)
	}
}
