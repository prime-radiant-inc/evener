package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

// Mutation caught: hashing raw selector order or duplicates replaces a durable
// registration, drops pending sends, cancels its subscription and forgets dedup.
func TestHubWatchEquivalentSelectorsPreserveDurableState(t *testing.T) {
	t.Parallel()
	for _, initial := range [][]string{{"approval", "question"}, {"question", "approval", "question"}} {
		t.Run(initial[0], func(t *testing.T) {
			t.Parallel()
			root := newMessagingRoot(t, newMessagingAdapter(), nil)
			jm := root.jobManager
			original := append([]string(nil), initial...)
			first, err := jm.configureHubWatch("local:worker", "instance", "boot", "local:manager", initial, true)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(initial, original) {
				t.Fatal("initial selectors mutated")
			}
			subscription, cancel := context.WithCancel(t.Context())
			defer cancel()
			if !root.installHubWatchSubscription(jm, first.WatchID, cancel) {
				t.Fatal("subscription not installed")
			}
			question := appwire.SessionWatchUpdate{Event: "question", Metadata: appwire.SessionObservationMetadata{Ref: "local:worker", InstanceID: "instance", GenerationID: "boot", EntryID: "question:A", TurnID: "A", CausalKnown: true}}
			if err := jm.onHubObservation(first.WatchID, question); err != nil {
				t.Fatal(err)
			}
			if err := jm.suspendHubWatch(first.WatchID); err != nil {
				t.Fatal(err)
			}
			before := loadWatchSendRecord(t, jm)
			if len(before.Pending) != 2 {
				t.Fatalf("pending=%d, want question and interruption", len(before.Pending))
			}
			for _, selectors := range [][]string{{"question", "approval"}, {"approval", "question", "approval"}, {"question", "question", "approval", "approval"}} {
				input := append([]string(nil), selectors...)
				next, err := jm.configureHubWatch("local:worker", "instance", "boot", "local:manager", selectors, true)
				if err != nil {
					t.Fatal(err)
				}
				after := loadWatchSendRecord(t, jm)
				// Report all three concrete effects on RED, not only the changed ID.
				samePending := reflect.DeepEqual(before.Pending, after.Pending)
				jm.mu.Lock()
				_, cfg, present := jm.watchConfigByIDLocked(first.WatchID)
				intact := present && cfg.hubTurnBound && cfg.hubTurnID == "A" && cfg.hubSuspended && len(cfg.hubSeen) == 1 && reflect.DeepEqual(cfg.events, []string{"approval", "question"})
				jm.mu.Unlock()
				if next.WatchID != first.WatchID || next.ReplacedExisting || !samePending || !intact || subscription.Err() != nil {
					t.Fatalf("equivalent selectors replaced durable state: same_id=%v replaced=%v same_pending=%v evidence_intact=%v subscription_canceled=%v", next.WatchID == first.WatchID, next.ReplacedExisting, samePending, intact, subscription.Err() != nil)
				}
				if !reflect.DeepEqual(input, selectors) {
					t.Fatal("replacement selectors mutated")
				}
				if err := jm.onHubObservation(first.WatchID, question); err != nil {
					t.Fatal(err)
				}
				if got := loadWatchSendRecord(t, jm); !reflect.DeepEqual(before.Pending, got.Pending) {
					t.Fatal("duplicate observation changed pending identity")
				}
			}
			registered, cleared, dropped, pending := 0, 0, 0, 0
			for _, e := range loadJobStoreEvents(t, jm) {
				switch e.Kind {
				case jobstore.EventWatchRegistered:
					registered++
				case jobstore.EventWatchCleared:
					cleared++
				case jobstore.EventWatchSendDropped:
					dropped++
				case jobstore.EventWatchSendPending:
					pending++
				}
			}
			if registered != 1 || cleared != 0 || dropped != 0 || pending != 2 {
				t.Fatalf("journal registered=%d cleared=%d dropped=%d pending=%d", registered, cleared, dropped, pending)
			}
			// Real changes must still replace and discard the old registration's sends.
			changed, err := jm.configureHubWatch("local:worker", "instance", "boot", "local:manager", []string{"approval"}, true)
			if err != nil || changed.WatchID == first.WatchID || !changed.ReplacedExisting {
				t.Fatalf("changed selectors=%+v err=%v", changed, err)
			}
			if got := loadWatchSendRecord(t, jm); len(got.Pending) != 0 {
				t.Fatalf("changed selectors retained pending=%d", len(got.Pending))
			}
			if !root.installHubWatchSubscription(jm, changed.WatchID, func() {}) {
				t.Fatal("replacement subscription not installed")
			}
			if subscription.Err() == nil {
				t.Fatal("genuine replacement retained old subscription")
			}
			completion, err := jm.configureHubWatch("local:worker", "instance", "boot", "local:manager", []string{"approval"}, false)
			if err != nil || completion.WatchID == changed.WatchID || !completion.ReplacedExisting {
				t.Fatalf("changed completion=%+v err=%v", completion, err)
			}
		})
	}
}

type overflowRecoveryPeer struct {
	initial          appwire.SessionWatchUpdate
	stalled, healthy chan appwire.SessionWatchUpdate
	started          chan context.Context
}

func (*overflowRecoveryPeer) State(context.Context) (appwire.ManagerView, error) {
	return appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true}, nil
}
func (*overflowRecoveryPeer) Invoke(context.Context, appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	return appwire.SessionOperationResponse{}, appwire.InvalidRequest("unused")
}
func (p *overflowRecoveryPeer) Watch(ctx context.Context, r appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
	p.started <- ctx
	if r.Ref == "local:stalled" {
		return p.stalled, nil
	}
	p.healthy <- p.initial
	return p.healthy, nil
}

// Mutation caught: channel closure ends instead of suspends a durable watch,
// binding replacement cannot rearm it, or replay drops pending/dedup identity.
// The stalled consumer is deliberately unread at the public client boundary.
// The second watch uses the real agent consumer, journal and rebind owner.
func TestManagerWatchOverflowRebindPreservesPendingCompletion(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(c *SessionConfig) { c.Manager = true })
	server := appserver.NewServer(appserver.ServerConfig{})
	type installation struct {
		ctx    context.Context
		client appwire.SessionHubClient
	}
	bound := make(chan installation, 1)
	server.InstallSessionTools(func(ctx context.Context, _ appwire.SessionBinding, c appwire.SessionHubClient) error {
		bound <- installation{ctx, c}
		return nil
	})
	endpoint := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	t.Cleanup(endpoint.Close)
	meta := appwire.SessionObservationMetadata{Ref: "local:worker", InstanceID: "instance", GenerationID: "boot"}
	initial := appwire.SessionWatchUpdate{Metadata: meta, Snapshot: &appwire.ThreadReadResponse{BootGeneration: "boot", Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: "local:worker", InstanceID: "instance", ActiveTurnID: "A", AskPending: true}}}}
	connect := func() (installation, *overflowRecoveryPeer) {
		t.Helper()
		transport, err := appwire.DialWebSocket(t.Context(), "ws"+endpoint.URL[len("http"):], endpoint.Client())
		if err != nil {
			t.Fatal(err)
		}
		client := appwire.NewClient(transport)
		t.Cleanup(func() { _ = client.Close() })
		peer := &overflowRecoveryPeer{initial: initial, stalled: make(chan appwire.SessionWatchUpdate), healthy: make(chan appwire.SessionWatchUpdate, 4), started: make(chan context.Context, 2)}
		client.SetSessionTools(appwire.SessionBinding{Ref: "local:manager", InstanceID: "manager-instance", HubID: "hub", BindingID: "binding"}, peer)
		client.Start(t.Context())
		if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
			t.Fatal(err)
		}
		if err := client.BindSessionTools(t.Context()); err != nil {
			t.Fatal(err)
		}
		installed := <-bound
		root.SetHubClient(installed.client)
		return installed, peer
	}
	first, peer := connect()
	if _, err := root.configureHubWatchRuntime(t.Context(), map[string]any{"operation": "create", "hub": map[string]any{"ref": "local:worker", "events": []string{"question"}, "completion": true}}); err != nil {
		t.Fatal(err)
	}
	healthyCtx := <-peer.started
	jm := root.jobManager
	// TRIPWIRE: durable pending state has no public completion signal.
	waitForCondition(t, 30*time.Second, "initial pending question", func() bool { return len(loadWatchSendRecord(t, jm).Pending) == 1 })
	before := loadWatchSendRecord(t, jm)
	var id string
	for _, pending := range before.Pending {
		id = pending.Key.WatchID
	}
	stalled, err := first.client.Watch(t.Context(), appwire.SessionWatchRequest{Ref: "local:stalled"})
	if err != nil {
		t.Fatal(err)
	}
	stalledCtx := <-peer.started
	for range cap(stalled) + 1 {
		select {
		case peer.stalled <- appwire.SessionWatchUpdate{Event: "status"}:
		case <-t.Context().Done():
			t.Fatal("stalled producer not admitted")
		}
	}
	awaitSignal(t, first.ctx.Done(), "overflow disconnect")
	awaitSignal(t, healthyCtx.Done(), "healthy producer cancellation")
	awaitSignal(t, stalledCtx.Done(), "stalled producer cancellation")
	_, replacement := connect()
	var reboundCtx context.Context
	select {
	case reboundCtx = <-replacement.started: // Existing consumeHubWatch issued Watch itself.
	case <-t.Context().Done():
		t.Fatal("original watch did not rebind")
	}
	if !managerHubWatchPresent(jm, id) {
		t.Fatal("rebind replaced original durable registration")
	}
	accepted := true
	terminal := meta
	terminal.EntryID, terminal.TurnID, terminal.CausalKnown = "terminal:A", "A", true
	terminal.TerminalAccepted, terminal.EndTurn, terminal.EndReason = &accepted, true, "done"
	replacement.healthy <- appwire.SessionWatchUpdate{Event: "communicate", Metadata: terminal}
	replacement.healthy <- appwire.SessionWatchUpdate{Event: "communicate", Metadata: terminal}
	// Consumer cancellation follows its completion send, not just the earlier
	// registration removal, so journal assertions cannot outrun that send.
	awaitSignal(t, reboundCtx.Done(), "rebound consumer completion")
	if managerHubWatchPresent(jm, id) {
		t.Fatal("completed watch remains active")
	}
	after := loadWatchSendRecord(t, jm)
	for key, pending := range before.Pending {
		got := after.Pending[key]
		if got == nil || got.DeliveryID != pending.DeliveryID || got.Key != pending.Key {
			t.Fatal("overflow recovery lost pending question identity")
		}
	}
	questions, completions, registrations := 0, 0, 0
	for _, e := range loadJobStoreEvents(t, jm) {
		if e.Kind == jobstore.EventWatchRegistered && e.WatchID == id {
			registrations++
		}
		if e.Kind != jobstore.EventWatchSendPending || e.WatchSend == nil || e.WatchSend.Key.WatchID != id {
			continue
		}
		switch e.WatchSend.TriggerIdentity {
		case "instance/boot/question:A:question":
			questions++
		case "instance/boot/terminal:A:communicate:completed":
			completions++
		}
		if e.WatchSend.EndNotice {
			t.Fatal("overflow permanently ended healthy durable watch")
		}
	}
	if registrations != 1 || questions != 1 || completions != 1 {
		t.Fatalf("registrations=%d questions=%d completions=%d", registrations, questions, completions)
	}
}
