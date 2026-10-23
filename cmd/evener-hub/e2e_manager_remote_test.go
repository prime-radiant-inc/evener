package hub

import (
	"context"
	"encoding/json"
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

// The hubs, daemon, bearer-authenticated /rpc connections and preference row
// are real. Only the model provider is scripted. The bridge marker denotes an
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
	idA, err := hubcore.NewSessionPreferencesStore(filepath.Join(hubA.home, "state", "evener", "index.db")).HubID()
	if err != nil {
		t.Fatal(err)
	}
	idB, err := hubcore.NewSessionPreferencesStore(filepath.Join(hubB.home, "state", "evener", "index.db")).HubID()
	if err != nil || idB == idA {
		t.Fatalf("two persisted hub identities: A=%q B=%q err=%v", idA, idB, err)
	}
	human := hubB.dialRPC(ctx, t)
	started, err := clientRequest[appwire.ThreadStartResponse](ctx, human, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness: "evener", CWD: hubB.workDir, Model: hubB.model,
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := started.Thread.Evener.Ref
	if ref == "" || started.Thread.Evener.InstanceID == "" {
		t.Fatalf("empty daemon identity: %+v", started.Thread.Evener)
	}
	t.Cleanup(func() {
		shutdown, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		_, err := clientRequest[appwire.EmptyResponse](shutdown, human, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: ref})
		if err != nil {
			t.Errorf("shutdown remote Manager: %v", err)
		}
	})

	// A human cannot assert a peer identity even if it holds the hub token.
	if _, err := human.IdentifySessionToolsPeer(ctx, appwire.SessionToolsPeerParams{HubID: idA}); err == nil {
		t.Fatal("human identity accepted")
	}
	bridge := dialManagerPeerBridge(ctx, t, hubB)
	if _, err := clientRequest[appwire.ThreadManagerSetResponse](ctx, bridge, appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: ref, Enabled: true}); err == nil {
		t.Fatal("unbound set accepted")
	}
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
	set, err := clientRequest[appwire.ThreadManagerSetResponse](ctx, bridge, appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: ref, Enabled: true})
	if err != nil || !set.View.Setting.Enabled || set.View.Setting.HubID != idA {
		t.Fatalf("peer-selected authority: response=%+v err=%v", set, err)
	}
	// The hosting hub's UI reads this same row, but cannot replace A.
	if _, err := clientRequest[appwire.ThreadManagerSetResponse](ctx, human, appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: ref, Enabled: true}); err == nil {
		t.Fatal("hosting human replaced enabling authority")
	}
	fresh := dialManagerPeerBridge(ctx, t, hubB)
	if _, err := clientRequest[appwire.ThreadManagerSetResponse](ctx, fresh, appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: ref, Enabled: false}); err == nil {
		t.Fatal("new connection inherited former identity before exchange")
	}
	again, err := fresh.IdentifySessionToolsPeer(ctx, appwire.SessionToolsPeerParams{HubID: idA})
	if err != nil || again.HubID != idB {
		t.Fatalf("reconnect identify: response=%+v err=%v", again, err)
	}
	if _, err := clientRequest[appwire.ThreadManagerSetResponse](ctx, fresh, appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: ref, Enabled: false}); err != nil {
		t.Fatalf("owner cannot disable after reconnect: %v", err)
	}
	// Supplying an extra identity as a human operation argument cannot re-enable.
	var forged json.RawMessage
	if err := human.Request(ctx, appwire.MethodEvenerThreadManagerSet, map[string]any{"ref": ref, "enabled": true, "hubId": idA}, &forged); err == nil {
		var result appwire.ThreadManagerSetResponse
		if err := json.Unmarshal(forged, &result); err != nil || result.View.Setting.HubID != idB {
			t.Fatalf("caller-supplied hubId established authority: response=%+v err=%v", result, err)
		}
	}
}

// An actual B daemon asks its Manager tool over B's daemon connection. B
// Shared remote source input runs through a real hosting hub and daemon. The
// Manager path must use these same source methods and their mutation receipts.
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
	if !caps.Send || !caps.Queue || !caps.Steer || !caps.SkillInput || !caps.CommandInput || caps.Clear || caps.Interrupt || caps.ChangeModel {
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
