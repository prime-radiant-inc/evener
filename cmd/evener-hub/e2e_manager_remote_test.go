package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/appserver"
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
// forwards that fixed reverse call over the same bridge A used to enable the
// workspace; A's real sessionOperations reads B's current saved row and sends
// the tool's read through the shared RemoteHubSource back to B.
func TestE2E_ManagerRemoteQuestionLoop(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs two hubs + daemons")
	}
	provider, err := fakellm.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	a, b := startHubStack(t, provider), startHubStack(t, provider)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	store := hubcore.NewSessionPreferencesStore(filepath.Join(a.home, "state", "evener", "index.db"))
	aID, err := store.HubID()
	if err != nil {
		t.Fatal(err)
	}
	bridge := dialManagerPeerBridge(ctx, t, b)
	if _, err := bridge.IdentifySessionToolsPeer(ctx, appwire.SessionToolsPeerParams{HubID: aID}); err != nil {
		t.Fatal(err)
	}
	source := appsource.NewRemoteHubSource("remote-b", nil, func(context.Context, string) (*appwire.Client, error) { return bridge, nil })
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{SessionPreferences: store}
	server := appserver.NewServer(appserver.ServerConfig{})
	owner := &sessionOperations{cfg: cfg, sources: sources, server: server, relays: newHubRelayFunctions(server, cfg, sources)}
	var bound *boundSessionOperations
	source.SetSessionTools(func(binding appwire.SessionBinding) (appwire.SessionHubClient, error) {
		bound = &boundSessionOperations{owner: owner, remote: source, binding: binding}
		return bound, nil
	}, store.HubID)
	human := b.dialRPC(ctx, t)
	worker, err := clientRequest[appwire.ThreadStartResponse](ctx, human, appwire.MethodThreadStart, appwire.ThreadStartParams{Harness: "evener", CWD: b.workDir, Model: b.model})
	if err != nil {
		t.Fatal(err)
	}
	workerRef, workerInstance := worker.Thread.Evener.Ref, worker.Thread.Evener.InstanceID
	t.Cleanup(func() {
		stop, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		_, _ = clientRequest[appwire.EmptyResponse](stop, human, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: workerRef})
	})
	workerID, err := appwire.ParseRef(workerRef)
	if err != nil {
		t.Fatal(err)
	}
	qualifiedWorker := (appwire.Ref{SourceID: "remote-b", ThreadID: workerID.ThreadID}).String()
	manager, err := clientRequest[appwire.ThreadStartResponse](ctx, human, appwire.MethodThreadStart, appwire.ThreadStartParams{Harness: "evener", CWD: b.workDir, Model: b.model})
	if err != nil {
		t.Fatal(err)
	}
	managerRef, instance := manager.Thread.Evener.Ref, manager.Thread.Evener.InstanceID
	t.Cleanup(func() {
		stop, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		_, _ = clientRequest[appwire.EmptyResponse](stop, human, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: managerRef})
	})
	parsed, err := appwire.ParseRef(managerRef)
	if err != nil {
		t.Fatal(err)
	}
	qualified := (appwire.Ref{SourceID: "remote-b", ThreadID: parsed.ThreadID}).String()
	set, err := source.SetManager(ctx, appwire.ThreadManagerSetParams{Ref: qualified, Enabled: true})
	if err != nil || set.View.Setting.HubID != aID {
		t.Fatalf("bound remote Manager: view=%+v err=%v", set.View, err)
	}
	if bound == nil {
		t.Fatal("enabling hub did not install the identified peer owner")
	}
	// The contract is that a remote watch is refused as unavailable while the
	// other operations stay usable, not that the refusal carries this phrasing.
	_, watchErr := bound.Watch(ctx, appwire.SessionWatchRequest{Ref: qualifiedWorker, Events: []string{"question"}})
	var watchWire appwire.WireError
	if watchErr == nil || !errors.As(watchErr, &watchWire) || watchWire.Code != appwire.CodeUnavailable {
		t.Fatalf("remote observation falsely advertised as a lease: %v", watchErr)
	}
	const marker = "REMOTE-MANAGER-BOUND-INPUT"
	_, err = clientRequest[appwire.TurnStartResponse](ctx, human, appwire.MethodTurnStart, appwire.TurnStartParams{Ref: managerRef, ExpectedInstanceID: instance, ClientMutationID: newMutationID(t), Input: []appwire.InputItem{{Type: "text", Text: marker}}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := provider.Next(ctx.Done())
	if err != nil || !first.Contains(marker) {
		t.Fatalf("manager model input: call=%v err=%v", first, err)
	}
	first.RespondToolCall("job_status", map[string]any{"hub": map[string]any{"ref": qualified}})
	next, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatal(err)
	}
	var status appwire.SessionOperationResponse
	if !managerToolResult(next, &status) || status.Read == nil || status.Read.Thread.Evener.Ref != qualified || status.Actor.ManagerRef != qualified {
		t.Fatalf("bound reverse call did not use A's source identity: actor=%+v read=%+v model=%v", status.Actor, status.Read, next.Texts())
	}
	const input = "REMOTE-MANAGER-INPUT-ON-WORKER"
	next.RespondToolCall("delegate_send", map[string]any{"to": qualifiedWorker, "input_mode": "start", "message": input, "client_mutation_id": newMutationID(t), "expected_instance_id": workerInstance})
	var workerCall *fakellm.Call
	managerDelivered := false
	for workerCall == nil || !managerDelivered {
		call, nextErr := provider.Next(ctx.Done())
		if nextErr != nil {
			turns, readErr := readTranscriptTurns(ctx, human, managerRef, "")
			t.Fatalf("remote worker provider call: %v; manager turns=%v readErr=%v", nextErr, managerItemSummary(turns), readErr)
		}
		if call.Contains(input) {
			workerCall = call
			continue
		}
		if !call.Contains(marker) {
			t.Fatalf("unexpected provider call: %v", call.Texts())
		}
		var delivery appwire.SessionOperationResponse
		if !managerToolResult(call, &delivery) || delivery.Start == nil {
			t.Fatalf("Manager tool did not accept remote input: %s", managerToolRefusal(call))
		}
		call.RespondToolCall("communicate", communicateArgs("remote input delivered"))
		managerDelivered = true
	}
	const question = "REMOTE-WORKER-QUESTION"
	workerCall.RespondToolCall("ask_user", map[string]any{"questions": []any{map[string]any{"question": question, "options": []any{map[string]any{"label": "Proceed", "detail": "Continue the task"}, map[string]any{"label": "Wait", "detail": "Stop here"}}}}})
	awaitThread(ctx, t, human, workerRef, "remote worker question", func(thread appwire.Thread) bool {
		return thread.Evener.AskPending && thread.Evener.PendingQuestion != nil && thread.Evener.PendingQuestion.Question == question
	})
	awaitThread(ctx, t, human, managerRef, "manager tool turn finished", func(thread appwire.Thread) bool { return thread.Status.Type == appwire.ThreadStatusIdle })
	const answer = "REMOTE-MANAGER-ANSWER"
	if _, err := clientRequest[appwire.TurnStartResponse](ctx, human, appwire.MethodTurnStart, appwire.TurnStartParams{
		Ref: managerRef, ExpectedInstanceID: instance, ClientMutationID: newMutationID(t), Input: []appwire.InputItem{{Type: "text", Text: answer}},
	}); err != nil {
		t.Fatal(err)
	}
	answerRound, err := provider.Next(ctx.Done())
	if err != nil || !answerRound.Contains(answer) {
		t.Fatalf("Manager answer prompt: call=%v err=%v", answerRound, err)
	}
	answerRound.RespondToolCall("job_status", map[string]any{"hub": map[string]any{"ref": qualifiedWorker}})
	questionStatus, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatal(err)
	}
	var pending appwire.SessionOperationResponse
	if !managerToolResult(questionStatus, &pending) || pending.Read == nil || !pending.Read.Thread.Evener.AskPending || pending.Read.Thread.Evener.PendingQuestion == nil || pending.Read.Thread.Evener.PendingQuestion.Question != question || pending.Read.Thread.Evener.Ref != qualifiedWorker || pending.Actor.ManagerRef != qualified {
		t.Fatalf("remote Manager did not see source-qualified worker question: actor=%+v question=%v last result=%s", pending.Actor, pending.Read != nil && pending.Read.Thread.Evener.AskPending, managerToolRefusal(questionStatus))
	}
	questionStatus.RespondToolCall("delegate_send", map[string]any{"to": qualifiedWorker, "input_mode": "start", "message": answer, "client_mutation_id": newMutationID(t), "expected_instance_id": workerInstance})
	const result = "REMOTE-WORKER-RESULT-DELIVERED"
	var answered *fakellm.Call
	managerAnswered := false
	for answered == nil || !managerAnswered {
		call, nextErr := provider.Next(ctx.Done())
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		if call.Contains(answer) && call.Contains(input) {
			answered = call
		} else if call.Contains(answer) && !call.Contains(input) {
			var sent appwire.SessionOperationResponse
			if !managerToolResult(call, &sent) || sent.Start == nil || sent.Actor.ManagerRef != qualified {
				t.Fatalf("remote answer not delivered by identified Manager: actor=%+v accepted=%v last result=%s", sent.Actor, sent.Start != nil, managerToolRefusal(call))
			}
			call.RespondToolCall("communicate", communicateArgs("remote question answered"))
			managerAnswered = true
		} else {
			t.Fatalf("unexpected provider call after remote question: %v", call.Texts())
		}
	}
	answered.RespondToolCall("communicate", communicateArgs(result))
	awaitThread(ctx, t, human, workerRef, "remote result", func(thread appwire.Thread) bool { return thread.Status.Type == appwire.ThreadStatusIdle })
	turns, err := readTranscriptTurns(ctx, human, workerRef, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	answerFound := false
	for _, turn := range turns {
		for _, item := range turn.Items {
			if item.Text == input && item.Actor != nil && item.Actor.Kind == "manager" && item.Actor.ManagerRef == managerRef {
				found = true
			}
			if item.Text == answer && item.Actor != nil && item.Actor.Kind == "manager" && item.Actor.ManagerRef == managerRef {
				answerFound = true
			}
		}
	}
	if !found || !answerFound || !workerTerminalAccepted(turns, result) {
		t.Fatalf("remote question/result history incomplete: firstInput=%v answer=%v terminalResult=%v worker=%v", found, answerFound, workerTerminalAccepted(turns, result), managerItemSummary(turns))
	}
	if _, err := source.SetManager(ctx, appwire.ThreadManagerSetParams{Ref: qualified, Enabled: false}); err != nil {
		t.Fatalf("enabling hub disable: %v", err)
	}
	if _, err := bound.Invoke(ctx, appwire.SessionOperationRequest{Read: &appwire.ThreadReadParams{Ref: qualifiedWorker}}); err == nil {
		t.Fatal("revoked Manager binding admitted next operation")
	}
	if read, err := clientRequest[appwire.ThreadReadResponse](ctx, human, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: workerRef}); err != nil || read.Thread.Evener.Ref != workerRef {
		t.Fatalf("revocation broke ordinary worker access: ref=%q err=%v", read.Thread.Evener.Ref, err)
	}
}

func awaitManagerRemoteStatus(t *testing.T, parent context.Context, ch <-chan appwire.Notification, ref, status string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	for {
		select {
		case n, ok := <-ch:
			if !ok {
				t.Fatal("subscription ended before status notification")
			}
			if n.Method != appwire.NotifyThreadStatusChanged {
				continue
			}
			var update appwire.ThreadStatusChangedParams
			if err := json.Unmarshal(n.Params, &update); err != nil {
				t.Fatal(err)
			}
			if update.Ref == ref && update.Status.Type == status {
				return
			}
		case <-ctx.Done():
			t.Fatalf("waiting for %s status on %s: %v", status, ref, ctx.Err())
		}
	}
}

// One remote relay attaches for the worker while two human subscribers and two
// distinct identified Manager bindings use that worker. A human unsubscribe
// cannot end either the remote relay or the other human subscription. Manager
// observation leases are intentionally not asserted here: remote job_watch is
// unsupported, while Manager status reads remain authorized.
func TestManagerRemoteFanoutAndRevocation(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs two hubs + daemons")
	}
	provider, err := fakellm.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	a, b := startHubStack(t, provider), startHubStack(t, provider)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	store := hubcore.NewSessionPreferencesStore(filepath.Join(a.home, "state", "evener", "index.db"))
	aID, err := store.HubID()
	if err != nil {
		t.Fatal(err)
	}
	bridge := dialManagerPeerBridge(ctx, t, b)
	if _, err := bridge.IdentifySessionToolsPeer(ctx, appwire.SessionToolsPeerParams{HubID: aID}); err != nil {
		t.Fatal(err)
	}
	source := appsource.NewRemoteHubSource("remote-b", nil, func(context.Context, string) (*appwire.Client, error) { return bridge, nil })
	sources := appsource.NewRegistry()
	sources.Add(source)
	server := appserver.NewServer(appserver.ServerConfig{})
	owner := &sessionOperations{cfg: hubcore.WebConfig{SessionPreferences: store}, sources: sources, server: server}
	owner.relays = newHubRelayFunctions(server, owner.cfg, sources)
	bindings := map[string]*boundSessionOperations{}
	source.SetSessionTools(func(binding appwire.SessionBinding) (appwire.SessionHubClient, error) {
		bound := &boundSessionOperations{owner: owner, remote: source, binding: binding}
		bindings[binding.Ref] = bound
		return bound, nil
	}, store.HubID)
	human, otherHuman := b.dialRPC(ctx, t), b.dialRPC(ctx, t)
	start := func() (string, string) {
		t.Helper()
		response, err := clientRequest[appwire.ThreadStartResponse](ctx, human, appwire.MethodThreadStart, appwire.ThreadStartParams{Harness: "evener", CWD: b.workDir, Model: b.model})
		if err != nil {
			t.Fatal(err)
		}
		localRef := response.Thread.Evener.Ref
		t.Cleanup(func() {
			stop, done := context.WithTimeout(context.Background(), 20*time.Second)
			defer done()
			_, _ = clientRequest[appwire.EmptyResponse](stop, human, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: localRef})
		})
		parsed, err := appwire.ParseRef(localRef)
		if err != nil {
			t.Fatal(err)
		}
		return localRef, (appwire.Ref{SourceID: "remote-b", ThreadID: parsed.ThreadID}).String()
	}
	worker, remoteWorker := start()
	workerState, err := clientRequest[appwire.ThreadReadResponse](ctx, human, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: worker})
	if err != nil {
		t.Fatal(err)
	}
	workerInstance := workerState.Thread.Evener.InstanceID
	_, managerOne := start()
	_, managerTwo := start()
	for _, ref := range []string{managerOne, managerTwo} {
		if _, err := source.SetManager(ctx, appwire.ThreadManagerSetParams{Ref: ref, Enabled: true}); err != nil {
			t.Fatalf("enable %s: %v", ref, err)
		}
	}
	if len(bindings) != 2 {
		t.Fatalf("same bridge lost a Manager: %d bindings", len(bindings))
	}
	for _, ref := range []string{managerOne, managerTwo} {
		response, err := bindings[ref].Invoke(ctx, appwire.SessionOperationRequest{Read: &appwire.ThreadReadParams{Ref: remoteWorker}})
		if err != nil || response.Read == nil || response.Actor.ManagerRef != ref || response.Read.Thread.Evener.Ref != remoteWorker {
			t.Fatalf("Manager %s lost worker status: actor=%+v read=%+v err=%v", ref, response.Actor, response.Read, err)
		}
	}
	for _, client := range []*appwire.Client{human, otherHuman} {
		if _, err := clientRequest[appwire.ThreadReadResponse](ctx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: worker, Subscribe: true}); err != nil {
			t.Fatal(err)
		}
	}
	remoteStream, err := source.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: remoteWorker})
	if err != nil {
		t.Fatal(err)
	}
	const first = "FANOUT-REMOTE-WORKER-ONE"
	if _, err := clientRequest[appwire.TurnStartResponse](ctx, human, appwire.MethodTurnStart, appwire.TurnStartParams{Ref: worker, ExpectedInstanceID: workerInstance, ClientMutationID: newMutationID(t), Input: []appwire.InputItem{{Type: "text", Text: first}}}); err != nil {
		t.Fatal(err)
	}
	call, err := provider.Next(ctx.Done())
	if err != nil || !call.Contains(first) {
		t.Fatalf("first worker turn: call=%v err=%v", call, err)
	}
	awaitManagerRemoteStatus(t, ctx, human.Notifications(), worker, appwire.ThreadStatusActive)
	awaitManagerRemoteStatus(t, ctx, otherHuman.Notifications(), worker, appwire.ThreadStatusActive)
	awaitManagerRemoteStatus(t, ctx, remoteStream, remoteWorker, appwire.ThreadStatusActive)
	if _, err := clientRequest[appwire.EmptyResponse](ctx, human, appwire.MethodThreadUnsubscribe, appwire.ThreadUnsubscribeParams{Ref: worker}); err != nil {
		t.Fatal(err)
	}
	call.RespondToolCall("communicate", communicateArgs("first fanout complete"))
	awaitThread(ctx, t, otherHuman, worker, "first fanout completion", func(thread appwire.Thread) bool { return thread.Status.Type == appwire.ThreadStatusIdle })
	const second = "FANOUT-REMOTE-WORKER-TWO"
	if _, err := clientRequest[appwire.TurnStartResponse](ctx, human, appwire.MethodTurnStart, appwire.TurnStartParams{Ref: worker, ExpectedInstanceID: workerInstance, ClientMutationID: newMutationID(t), Input: []appwire.InputItem{{Type: "text", Text: second}}}); err != nil {
		t.Fatal(err)
	}
	call, err = provider.Next(ctx.Done())
	if err != nil || !call.Contains(second) {
		t.Fatalf("second worker turn: call=%v err=%v", call, err)
	}
	awaitManagerRemoteStatus(t, ctx, otherHuman.Notifications(), worker, appwire.ThreadStatusActive)
	awaitManagerRemoteStatus(t, ctx, remoteStream, remoteWorker, appwire.ThreadStatusActive)
	for _, ref := range []string{managerOne, managerTwo} {
		if read, err := bindings[ref].Invoke(ctx, appwire.SessionOperationRequest{Read: &appwire.ThreadReadParams{Ref: remoteWorker}}); err != nil || read.Read == nil || read.Actor.ManagerRef != ref {
			t.Fatalf("Manager status after human unsubscribe %s: %+v err=%v", ref, read, err)
		}
	}
	call.RespondToolCall("communicate", communicateArgs("second fanout complete"))
	if _, err := source.SetManager(ctx, appwire.ThreadManagerSetParams{Ref: managerOne, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := bindings[managerOne].Invoke(ctx, appwire.SessionOperationRequest{Read: &appwire.ThreadReadParams{Ref: remoteWorker}}); err == nil {
		t.Fatal("revoked Manager still admitted")
	}
	// Disabling and re-enabling on the same bridge must give a usable Manager
	// again. The hosting hub only learns a binding is finished when its context
	// ends, and this bridge never disconnects, so nothing retires the old one
	// unless the re-enable itself supersedes it.
	if _, err := source.SetManager(ctx, appwire.ThreadManagerSetParams{Ref: managerOne, Enabled: true}); err != nil {
		t.Fatalf("re-enable on the same bridge: %v", err)
	}
	// Disabling tears down the workspace's relay transport, so the re-enable's
	// first reads can land while it is re-dialing. That is a recovery boundary,
	// not a failure: wait for the Manager to become usable instead of sampling
	// one instant of it.
	deadline := time.Now().Add(20 * time.Second)
	var reenabled appwire.SessionOperationResponse
	for {
		response, err := bindings[managerOne].Invoke(ctx, appwire.SessionOperationRequest{Read: &appwire.ThreadReadParams{Ref: remoteWorker}})
		if err == nil && response.Read != nil && response.Actor.ManagerRef == managerOne {
			reenabled = response
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("re-enabled Manager never read the worker: %+v err=%v", response.Read, err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("re-enabled Manager never read the worker before the test deadline")
		case <-time.After(100 * time.Millisecond):
		}
	}
	if reenabled.Actor.ManagerRef != managerOne {
		t.Fatalf("re-enabled Manager acted as %q", reenabled.Actor.ManagerRef)
	}
	if read, err := bindings[managerTwo].Invoke(ctx, appwire.SessionOperationRequest{Read: &appwire.ThreadReadParams{Ref: remoteWorker}}); err != nil || read.Read == nil || read.Actor.ManagerRef != managerTwo {
		t.Fatalf("re-enable displaced an independent Manager: %+v err=%v", read, err)
	}
	if read, err := bindings[managerTwo].Invoke(ctx, appwire.SessionOperationRequest{Read: &appwire.ThreadReadParams{Ref: remoteWorker}}); err != nil || read.Read == nil || read.Actor.ManagerRef != managerTwo {
		t.Fatalf("revocation displaced independent Manager: %+v err=%v", read, err)
	}
}

func TestManagerRemoteReconnectRestoresBinding(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and reconnects two hubs")
	}
	provider, err := fakellm.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	a, b := startHubStack(t, provider), startHubStack(t, provider)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	store := hubcore.NewSessionPreferencesStore(filepath.Join(a.home, "state", "evener", "index.db"))
	aID, err := store.HubID()
	if err != nil {
		t.Fatal(err)
	}
	bridge := dialManagerPeerBridge(ctx, t, b)
	if _, err := bridge.IdentifySessionToolsPeer(ctx, appwire.SessionToolsPeerParams{HubID: aID}); err != nil {
		t.Fatal(err)
	}
	source := appsource.NewRemoteHubSource("remote-b", nil, func(context.Context, string) (*appwire.Client, error) { return bridge, nil })
	sources := appsource.NewRegistry()
	sources.Add(source)
	server := appserver.NewServer(appserver.ServerConfig{})
	owner := &sessionOperations{cfg: hubcore.WebConfig{SessionPreferences: store}, sources: sources, server: server}
	owner.relays = newHubRelayFunctions(server, owner.cfg, sources)
	var bound *boundSessionOperations
	source.SetSessionTools(func(binding appwire.SessionBinding) (appwire.SessionHubClient, error) {
		bound = &boundSessionOperations{owner: owner, remote: source, binding: binding}
		return bound, nil
	}, store.HubID)
	human := b.dialRPC(ctx, t)
	start := func() (string, string, string) {
		t.Helper()
		response, err := clientRequest[appwire.ThreadStartResponse](ctx, human, appwire.MethodThreadStart, appwire.ThreadStartParams{Harness: "evener", CWD: b.workDir, Model: b.model})
		if err != nil {
			t.Fatal(err)
		}
		ref, instance := response.Thread.Evener.Ref, response.Thread.Evener.InstanceID
		t.Cleanup(func() {
			stop, done := context.WithTimeout(context.Background(), 20*time.Second)
			defer done()
			_, _ = clientRequest[appwire.EmptyResponse](stop, human, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: ref})
		})
		parsed, err := appwire.ParseRef(ref)
		if err != nil {
			t.Fatal(err)
		}
		return ref, instance, (appwire.Ref{SourceID: "remote-b", ThreadID: parsed.ThreadID}).String()
	}
	_, _, remoteWorker := start()
	manager, instance, remoteManager := start()
	set, err := source.SetManager(ctx, appwire.ThreadManagerSetParams{Ref: remoteManager, Enabled: true})
	if err != nil || set.View.Setting.HubID != aID || set.View.Setting.Revision == 0 {
		t.Fatalf("initial saved preference: %+v err=%v", set.View, err)
	}
	oldBinding := bound.binding.BindingID
	if err := bridge.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := bound.Invoke(ctx, appwire.SessionOperationRequest{Read: &appwire.ThreadReadParams{Ref: remoteWorker}}); err == nil {
		t.Fatal("disconnected Manager silently retained its old binding")
	}
	bridge = dialManagerPeerBridge(ctx, t, b)
	if err := source.RestoreManagerBindings(ctx); err == nil {
		t.Fatal("fresh bridge restored without identifying its peer")
	}
	if _, err := bridge.IdentifySessionToolsPeer(ctx, appwire.SessionToolsPeerParams{HubID: aID}); err != nil {
		t.Fatal(err)
	}
	if err := source.RestoreManagerBindings(ctx); err != nil || bound == nil || bound.binding.BindingID == oldBinding || bound.binding.PreferenceRevision != set.View.Setting.Revision {
		t.Fatalf("reconnect failed live-row restoration: binding=%+v old=%q err=%v", bound, oldBinding, err)
	}
	const marker = "RECONNECTED-REMOTE-MANAGER"
	if _, err := clientRequest[appwire.TurnStartResponse](ctx, human, appwire.MethodTurnStart, appwire.TurnStartParams{Ref: manager, ExpectedInstanceID: instance, ClientMutationID: newMutationID(t), Input: []appwire.InputItem{{Type: "text", Text: marker}}}); err != nil {
		t.Fatal(err)
	}
	request, err := provider.Next(ctx.Done())
	if err != nil || !request.Contains(marker) {
		t.Fatalf("reconnected Manager model call: %v %v", request, err)
	}
	request.RespondToolCall("job_status", map[string]any{"hub": map[string]any{"ref": remoteWorker}})
	result, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatal(err)
	}
	var status appwire.SessionOperationResponse
	if !managerToolResult(result, &status) || status.Read == nil || status.Read.Thread.Evener.Ref != remoteWorker || status.Actor.ManagerRef != remoteManager {
		t.Fatalf("B daemon did not use restored Manager binding: %+v", status)
	}
	result.RespondToolCall("communicate", communicateArgs("reconnected Manager read worker"))
	setting, err := hubcore.NewSessionPreferencesStore(filepath.Join(b.home, "state", "evener", "index.db")).Manager(manager)
	if err != nil || !setting.Enabled || setting.HubID != aID || setting.Revision != set.View.Setting.Revision {
		t.Fatalf("reconnect changed saved authority: %+v err=%v", setting, err)
	}
}

// This is a real SSH/AppWire attach: the test never calls the restoration
// method. The private localhost host is confined to its own config, port and
// state directory, just like the disposable-host deploy fixture.
func TestManagerRemoteSSHAutomaticReconnect(t *testing.T) {
	if testing.Short() {
		t.Skip("live SSH reconnect test")
	}
	probe, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	if err := exec.CommandContext(probe, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=3", "localhost", "true").Run(); err != nil {
		t.Skipf("localhost SSH not available: %v", err)
	}
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	provider, err := fakellm.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	a := startHubStack(t, provider)
	host := newHostSSH(t, "localhost", "")
	hostDir := filepath.Join(os.TempDir(), "evener-manager-ssh-"+hostDeployRunID())
	host.prepareDeployDir(hostDir)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if stopHostListener(t, host, addr, hostDir+"/"+hostDeployToml) {
			// Stopping the listener does not stop the session daemons the hub
			// started over SSH; deleting the directory underneath them leaves
			// them running with no owner.
			if err := host.tryRun(hostDaemonReapCommand(hostDir)); err != nil {
				t.Errorf("reap SSH-hosted daemons: %v", err)
				return
			}
			if err := host.tryRun("rm -rf " + shellquote.RemoteWord(hostDir)); err != nil {
				t.Errorf("clean up private SSH host: %v", err)
			}
		}
	})
	host.writeFile(hostDir+"/"+hostDeployToml, hostDeployConfig(hostDir, addr))
	host.mustRun("cp " + shellquote.RemoteWord(filepath.Join(a.binDir, "evener")) + " " + shellquote.RemoteWord(hostDir+"/bin/evener-real"))
	host.mustRun("mkdir -p " + shellquote.RemoteWord(hostDir+"/home/config/evener") + " " + shellquote.RemoteWord(hostDir+"/workspace"))
	host.writeFile(hostDir+"/home/config/evener/providers.toml", []byte(fmt.Sprintf("default = \"fake\"\n[providers.fake]\nbase = \"openai-compatible\"\nbase_url = %q\napi_key = \"fakellm-not-a-secret\"\n", provider.BaseURL())))
	host.writeFile(hostDir+"/bin/evener", []byte(fmt.Sprintf("#!/bin/sh\nexport HOME=%s XDG_CONFIG_HOME=%s XDG_STATE_HOME=%s XDG_CACHE_HOME=%s EVENER_PROVIDERS_CONFIG=%s\nexec %s \"$@\"\n", shellquote.RemoteWord(hostDir+"/home"), shellquote.RemoteWord(hostDir+"/home/config"), shellquote.RemoteWord(hostDir+"/home/state"), shellquote.RemoteWord(hostDir+"/home/cache"), shellquote.RemoteWord(hostDir+"/home/config/evener/providers.toml"), shellquote.RemoteWord(hostDir+"/bin/evener-real"))))
	host.mustRun("chmod 700 " + shellquote.RemoteWord(hostDir+"/bin/evener"))
	sshDir := hostDeploySSHWrapper(t, hostDir)
	t.Setenv("PATH", sshDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	aHuman := a.dialRPC(ctx, t)
	entry := appwire.HostEntry{Name: "manager-ssh", Address: "localhost", EvenerPath: hostDir + "/bin/evener", ConfigPath: hostDir + "/" + hostDeployToml, Addr: addr}
	row, err := hostAddCommittedRow(ctx, aHuman, appwire.HostAddParams{Entry: entry})
	if err != nil {
		t.Fatal(err)
	}
	awaitHostAttachedWithin(ctx, t, aHuman, "manager-ssh", hostDeployAttachTimeout)
	token, err := os.ReadFile(hostDir + "/state/auth-token")
	if err != nil {
		t.Fatalf("private host token: %v", err)
	}
	b := hubStack{addr: addr, token: strings.TrimSpace(string(token)), workDir: hostDir + "/workspace", model: "fake/" + fakellm.ModelID}
	bHuman := b.dialRPC(ctx, t)
	var models json.RawMessage
	modelErr := bHuman.Request(ctx, appwire.MethodModelList, appwire.EmptyParams{}, &models)
	if modelErr != nil || !strings.Contains(string(models), `"provider":"fake"`) {
		t.Fatalf("private SSH host did not load fake provider: %v", modelErr)
	}
	started, err := clientRequest[appwire.ThreadStartResponse](ctx, bHuman, appwire.MethodThreadStart, appwire.ThreadStartParams{Harness: "evener", CWD: b.workDir, Model: b.model})
	if err != nil {
		t.Fatal(err)
	}
	ref := started.Thread.Evener.Ref
	parsed, err := appwire.ParseRef(ref)
	if err != nil {
		t.Fatal(err)
	}
	remote := (appwire.Ref{SourceID: "manager-ssh", ThreadID: parsed.ThreadID}).String()
	t.Cleanup(func() {
		stop, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_, _ = clientRequest[appwire.EmptyResponse](stop, bHuman, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: ref})
	})
	setting, err := clientRequest[appwire.ThreadManagerSetResponse](ctx, aHuman, appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: remote, Enabled: true})
	if err != nil || !setting.View.Setting.Enabled {
		t.Fatalf("enable over SSH: %+v %v", setting, err)
	}
	assertManager := func(marker string) {
		t.Helper()
		if _, err := clientRequest[appwire.TurnStartResponse](ctx, bHuman, appwire.MethodTurnStart, appwire.TurnStartParams{Ref: ref, ExpectedInstanceID: started.Thread.Evener.InstanceID, ClientMutationID: newMutationID(t), Input: []appwire.InputItem{{Type: "text", Text: marker}}}); err != nil {
			t.Fatal(err)
		}
		request, err := provider.Next(ctx.Done())
		if err != nil || !request.Contains(marker) {
			t.Fatalf("Manager model request: %v %v", request, err)
		}
		request.RespondToolCall("job_status", map[string]any{"hub": map[string]any{"ref": remote}})
		result, err := provider.Next(ctx.Done())
		var operation appwire.SessionOperationResponse
		if err != nil || !managerToolResult(result, &operation) || operation.Read == nil || operation.Actor.ManagerRef != remote {
			var tool map[string]any
			var messages []string
			if result != nil {
				managerToolResult(result, &tool)
				for _, raw := range result.Body["messages"].([]any) {
					message, _ := raw.(map[string]any)
					if message["role"] == "tool" {
						messages = append(messages, fmt.Sprintf("%v:%.200s", message["tool_call_id"], message["content"]))
					}
				}
			}
			t.Fatalf("SSH Manager operation: %+v tool=%v toolResults=%v err=%v", operation, tool, messages, err)
		}
		result.RespondToolCall("communicate", communicateArgs("SSH Manager read complete"))
		awaitThread(ctx, t, bHuman, ref, "SSH Manager turn finished", func(thread appwire.Thread) bool {
			return thread.Status.Type == appwire.ThreadStatusIdle
		})
	}
	assertManager("SSH-MANAGER-FIRST")
	if !stopHostListener(t, host, addr, hostDir+"/"+hostDeployToml) {
		t.Fatal("could not stop test-owned hosting hub for reconnect")
	}
	awaitHostAttachedWithin(ctx, t, aHuman, "manager-ssh", hostDeployAttachTimeout)
	if _, err := clientRequest[appwire.ThreadReadResponse](ctx, bHuman, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: ref}); err == nil {
		t.Fatal("old hosting connection remained live after hub restart")
	}
	bHuman = b.dialRPC(ctx, t)
	assertManager("SSH-MANAGER-RESTORED")
	removed, err := clientRequest[appwire.HostMutationResult](ctx, aHuman, appwire.MethodEvenerHostRemove, appwire.HostRemoveParams{
		Name: entry.Name, MutationID: newMutationID(t), ExpectedGeneration: row.Generation, ExpectedIncarnationID: row.IncarnationID,
	})
	if err != nil || removed.HostMutationCommittedRemoved == nil {
		t.Fatalf("remove attached SSH host: %+v err=%v", removed, err)
	}
	if _, err := clientRequest[appwire.ThreadReadResponse](ctx, aHuman, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: remote}); err == nil {
		t.Fatal("removed host stayed reachable through Manager source")
	}
	aID, err := hubcore.NewSessionPreferencesStore(filepath.Join(a.home, "state", "evener", "index.db")).HubID()
	if err != nil {
		t.Fatal(err)
	}
	control := dialManagerPeerBridge(ctx, t, b)
	if _, err := control.IdentifySessionToolsPeer(ctx, appwire.SessionToolsPeerParams{HubID: aID}); err != nil {
		t.Fatal(err)
	}
	disabled, err := clientRequest[appwire.ThreadManagerSetResponse](ctx, control, appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: ref, Enabled: false})
	if err != nil || disabled.View.Setting.Enabled {
		t.Fatalf("identified enabling hub could not disable preference while SSH bridge is down: %+v err=%v", disabled, err)
	}
	if err := control.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := hostAddCommittedRow(ctx, aHuman, appwire.HostAddParams{Entry: entry}); err != nil {
		t.Fatal(err)
	}
	awaitHostAttachedWithin(ctx, t, aHuman, entry.Name, hostDeployAttachTimeout)
	read, err := clientRequest[appwire.ThreadReadResponse](ctx, aHuman, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: remote})
	if err != nil || read.Thread.Evener.ManagerSetting == nil || read.Thread.Evener.ManagerSetting.Enabled {
		t.Fatalf("changed authority revived on SSH reconnect: setting=%+v err=%v", read.Thread.Evener.ManagerSetting, err)
	}
	if _, err := clientRequest[appwire.TurnStartResponse](ctx, bHuman, appwire.MethodTurnStart, appwire.TurnStartParams{
		Ref: ref, ExpectedInstanceID: started.Thread.Evener.InstanceID, ClientMutationID: newMutationID(t), Input: []appwire.InputItem{{Type: "text", Text: "SSH-MANAGER-DISABLED"}},
	}); err != nil {
		t.Fatal(err)
	}
	request, err := provider.Next(ctx.Done())
	if err != nil || !request.Contains("SSH-MANAGER-DISABLED") {
		t.Fatalf("disabled Manager model request: %v err=%v", request, err)
	}
	request.RespondToolCall("job_status", map[string]any{"hub": map[string]any{"ref": remote}})
	result, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatal(err)
	}
	var lastTool string
	for _, raw := range result.Body["messages"].([]any) {
		message, _ := raw.(map[string]any)
		if message["role"] == "tool" {
			lastTool, _ = message["content"].(string)
		}
	}
	if !strings.Contains(lastTool, "arguments did not match the schema") {
		t.Fatalf("disabled Manager retained its remote tool surface: %s", lastTool)
	}
	result.RespondText("Manager mode disabled")
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

// Restart only the hub, preserving its HOME, saved preferences, and daemon.
// The original stack's cleanup still reaps its original process and daemons.
func restartManagerHub(t *testing.T, stack hubStack) hubStack {
	t.Helper()
	old, err := os.FindProcess(stack.pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Kill(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stack.home, "hub-restarted.log")
	log, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(stack.binDir, "evener")
	hub := exec.Command(bin, "hub", "--addr", "127.0.0.1:0", "--evener", bin)
	hub.Env = append(os.Environ(),
		"HOME="+stack.home,
		"XDG_CONFIG_HOME="+filepath.Join(stack.home, "config"),
		"XDG_STATE_HOME="+filepath.Join(stack.home, "state"),
		"XDG_CACHE_HOME="+filepath.Join(stack.home, "cache"),
	)
	hub.Stdout, hub.Stderr = log, log
	if err := hub.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = hub.Process.Kill()
		_ = hub.Wait()
		_ = log.Close()
		if t.Failed() {
			if body, err := os.ReadFile(path); err == nil {
				t.Logf("restarted hub log:\n%s", redactHubStackLog(body))
			}
		}
	})
	stack.addr = awaitHubListening(t, path)
	stack.pid = hub.Process.Pid
	token, err := os.ReadFile(filepath.Join(stack.home, "state", "evener", "auth-token"))
	if err != nil {
		t.Fatal(err)
	}
	stack.token = strings.TrimSpace(string(token))
	return stack
}

func TestManagerRemoteAuthorityRestartOrder(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and restarts both hubs")
	}
	for _, order := range [][]string{{"hosting", "enabling"}, {"enabling", "hosting"}} {
		t.Run(strings.Join(order, "-then-"), func(t *testing.T) {
			provider, err := fakellm.New()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(provider.Close)
			a, b := startHubStack(t, provider), startHubStack(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			aStore := hubcore.NewSessionPreferencesStore(filepath.Join(a.home, "state", "evener", "index.db"))
			bStore := hubcore.NewSessionPreferencesStore(filepath.Join(b.home, "state", "evener", "index.db"))
			aID, err := aStore.HubID()
			if err != nil {
				t.Fatal(err)
			}
			human := b.dialRPC(ctx, t)
			manager, err := clientRequest[appwire.ThreadStartResponse](ctx, human, appwire.MethodThreadStart, appwire.ThreadStartParams{Harness: "evener", CWD: b.workDir, Model: b.model})
			if err != nil {
				t.Fatal(err)
			}
			ref := manager.Thread.Evener.Ref
			bridge := dialManagerPeerBridge(ctx, t, b)
			if _, err := bridge.IdentifySessionToolsPeer(ctx, appwire.SessionToolsPeerParams{HubID: aID}); err != nil {
				t.Fatal(err)
			}
			if _, err := clientRequest[appwire.ThreadManagerSetResponse](ctx, bridge, appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: ref, Enabled: true}); err != nil {
				t.Fatal(err)
			}
			for _, side := range order {
				switch side {
				case "hosting":
					_ = bridge.Close()
					_ = human.Close()
					b = restartManagerHub(t, b)
				case "enabling":
					a = restartManagerHub(t, a)
				}
				id, err := hubcore.NewSessionPreferencesStore(filepath.Join(a.home, "state", "evener", "index.db")).HubID()
				setting, rowErr := bStore.Manager(ref)
				if err != nil || rowErr != nil || id != aID || !setting.Enabled || setting.HubID != aID {
					t.Fatalf("saved authority after %s restart: A=%q want=%q row=%+v errors=%v,%v", side, id, aID, setting, err, rowErr)
				}
			}
			human = b.dialRPC(ctx, t)
			read, err := clientRequest[appwire.ThreadReadResponse](ctx, human, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: ref})
			if err != nil || read.Thread.Evener.ManagerSetting == nil || !read.Thread.Evener.Manager || read.Thread.Evener.ManagerEditable || read.Thread.Evener.ManagerSetting.HubID != aID {
				t.Fatalf("hosting UI after restarts: read=%+v err=%v", read.Thread.Evener, err)
			}
			if _, err := clientRequest[appwire.ThreadManagerSetResponse](ctx, human, appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: ref, Enabled: true}); err == nil {
				t.Fatal("hosting hub displaced the enabling authority after restarts")
			}
			fresh := dialManagerPeerBridge(ctx, t, b)
			if _, err := clientRequest[appwire.ThreadManagerSetResponse](ctx, fresh, appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: ref, Enabled: false}); err == nil {
				t.Fatal("fresh bridge inherited identity without identify")
			}
			if _, err := fresh.IdentifySessionToolsPeer(ctx, appwire.SessionToolsPeerParams{HubID: aID}); err != nil {
				t.Fatal(err)
			}
			if _, err := clientRequest[appwire.ThreadManagerSetResponse](ctx, fresh, appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: ref, Enabled: false}); err != nil {
				t.Fatalf("restored enabling authority cannot disable: %v", err)
			}
		})
	}
}

// Two authenticated bridge connections compete for one hosting preference row.
// The loser and hosting UI must see the winner, never independent booleans.
func TestManagerRemoteAuthority(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs three hubs + daemon")
	}
	provider, err := fakellm.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	a, b, c := startHubStack(t, provider), startHubStack(t, provider), startHubStack(t, provider)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	store := func(h hubStack) *hubcore.SessionPreferencesStore {
		return hubcore.NewSessionPreferencesStore(filepath.Join(h.home, "state", "evener", "index.db"))
	}
	aID, err := store(a).HubID()
	if err != nil {
		t.Fatal(err)
	}
	cID, err := store(c).HubID()
	if err != nil || aID == cID {
		t.Fatalf("distinct controller IDs: A=%q C=%q err=%v", aID, cID, err)
	}
	human := b.dialRPC(ctx, t)
	started, err := clientRequest[appwire.ThreadStartResponse](ctx, human, appwire.MethodThreadStart, appwire.ThreadStartParams{Harness: "evener", CWD: b.workDir, Model: b.model})
	if err != nil {
		t.Fatal(err)
	}
	ref := started.Thread.Evener.Ref
	t.Cleanup(func() {
		shutdown, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		if _, err := clientRequest[appwire.EmptyResponse](shutdown, human, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: ref}); err != nil {
			t.Errorf("manager shutdown: %v", err)
		}
	})
	bridgeA, bridgeC := dialManagerPeerBridge(ctx, t, b), dialManagerPeerBridge(ctx, t, b)
	for _, peer := range []struct {
		client *appwire.Client
		id     string
	}{{bridgeA, aID}, {bridgeC, cID}} {
		if _, err := peer.client.IdentifySessionToolsPeer(ctx, appwire.SessionToolsPeerParams{HubID: peer.id}); err != nil {
			t.Fatalf("identify %q: %v", peer.id, err)
		}
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	type outcome struct {
		id   string
		view appwire.ManagerView
		err  error
	}
	results := make(chan outcome, 2)
	for _, peer := range []struct {
		client *appwire.Client
		id     string
	}{{bridgeA, aID}, {bridgeC, cID}} {
		wg.Add(1)
		go func(client *appwire.Client, id string) {
			defer wg.Done()
			<-start
			response, err := clientRequest[appwire.ThreadManagerSetResponse](ctx, client, appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: ref, Enabled: true})
			results <- outcome{id: id, view: response.View, err: err}
		}(peer.client, peer.id)
	}
	close(start)
	wg.Wait()
	close(results)
	wins := 0
	var winner string
	for result := range results {
		if result.err == nil {
			wins++
			winner = result.id
		} else if result.view.Setting.HubID != "" && result.view.Setting.HubID != aID && result.view.Setting.HubID != cID {
			t.Fatalf("losing operation reported foreign authority: %+v", result)
		}
	}
	setting, err := store(b).Manager(ref)
	if err != nil || wins != 1 || !setting.Enabled || setting.HubID != winner {
		t.Fatalf("concurrent authority: wins=%d winner=%q saved=%+v err=%v", wins, winner, setting, err)
	}
	read, err := clientRequest[appwire.ThreadReadResponse](ctx, human, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: ref})
	if err != nil || !read.Thread.Evener.Manager || read.Thread.Evener.ManagerEditable || read.Thread.Evener.ManagerSetting == nil || read.Thread.Evener.ManagerSetting.HubID != winner {
		t.Fatalf("hosting UI disagrees with saved row: setting=%+v err=%v", read.Thread.Evener, err)
	}
	if _, err := clientRequest[appwire.ThreadManagerSetResponse](ctx, human, appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: ref, Enabled: true}); err == nil {
		t.Fatal("hosting hub took over enabled Manager")
	}
	loser, owner := bridgeA, bridgeC
	if winner == aID {
		loser, owner = bridgeC, bridgeA
	}
	if _, err := clientRequest[appwire.ThreadManagerSetResponse](ctx, loser, appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: ref, Enabled: false}); err == nil {
		t.Fatal("non-owner disabled enabled Manager")
	}
	setting, err = store(b).Manager(ref)
	if err != nil || !setting.Enabled || setting.HubID != winner {
		t.Fatalf("non-owner changed saved authority: %+v err=%v", setting, err)
	}
	if _, err := clientRequest[appwire.ThreadManagerSetResponse](ctx, owner, appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: ref, Enabled: false}); err != nil {
		t.Fatalf("owner could not disable Manager: %v", err)
	}
}

// Shared remote source input runs through a real hosting hub and daemon. The
// Manager path must use these same source methods and their mutation receipts.
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
