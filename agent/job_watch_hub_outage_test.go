package agent

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/appwire"
)

// A binding outage longer than a handful of failed state reads must not end a
// watch whose connection comes back (job-control.md: a restored connection
// retains the watch). The old recovery loop counted five consecutive failed
// state reads as exhausted connection recovery, so an outage of roughly
// twenty seconds under load tore down every armed watch with an
// "observation unreachable" end notice while the binding was still coming
// back. The watch stays armed through the outage here, and the completion
// observed on the restored connection still delivers.
func TestManagerWatchSurvivesBindingOutageUntilConnectionRestores(t *testing.T) {
	const ref, instance, generation = "local:worker", "worker-instance", "7@worker"
	ctx := t.Context()
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) {
		cfg.Manager = true
		cfg.testOnly.hubWatchRetryDelay = time.Millisecond
		cfg.testOnly.hubWatchStateOutageBudget = 5 * time.Second
	})
	snapshot := appwire.SessionWatchUpdate{
		Snapshot: &appwire.ThreadReadResponse{BootGeneration: generation, Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: ref, InstanceID: instance}}},
		Metadata: appwire.SessionObservationMetadata{Ref: ref, InstanceID: instance, GenerationID: generation},
	}
	first := make(chan appwire.SessionWatchUpdate, 1)
	first <- snapshot
	base := &managerRecoverClient{
		view:         appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true},
		streams:      make(chan chan appwire.SessionWatchUpdate, 2),
		watches:      make(chan struct{}, 2),
		offlineReads: make(chan struct{}, 16),
		ended:        make(chan struct{}, 2),
	}
	base.streams <- first
	root.SetHubClient(base)
	res, err := root.configureHubWatchRuntime(ctx, map[string]any{"operation": "create", "hub": map[string]any{"ref": ref, "events": []string{"communicate"}, "completion": true}})
	if err != nil {
		t.Fatal(err)
	}
	watchID := res.(tool.StateResult).State.(jobWatchToolResult).WatchID
	<-base.watches
	jm := root.jobManager
	if jm == nil {
		t.Fatal("missing journal owner")
	}

	// Outage: the stream dies and every state read fails. The watch must keep
	// retrying through more failures than the old five-read limit, staying
	// armed for the whole outage window.
	base.mu.Lock()
	base.offline = true
	base.mu.Unlock()
	close(first)
	for range 7 {
		select {
		case <-base.offlineReads:
		case <-time.After(5 * time.Second):
			if !managerHubWatchPresent(jm, watchID) {
				t.Fatal("binding outage ended the watch before the connection restored; a restored connection must retain the watch")
			}
			t.Fatal("state reads stopped failing while the binding was still offline")
		}
	}
	if !managerHubWatchPresent(jm, watchID) {
		t.Fatal("binding outage ended the watch before the connection restored; a restored connection must retain the watch")
	}

	// Restore: the binding is reinstalled and answers again. The signal lands
	// before any state read can succeed against the restored binding, so no
	// retry can resolve against the old binding identity and end the watch as
	// an unexpected subscription end; the watch must re-anchor and still
	// complete on the terminal observation.
	second := make(chan appwire.SessionWatchUpdate, 4)
	second <- snapshot
	base.streams <- second
	root.HubBindingChanged()
	base.mu.Lock()
	base.offline = false
	base.mu.Unlock()
	awaitWithin(t, 5*time.Second, "rebind after connection restore", func() { <-base.watches })
	accepted := true
	terminal := appwire.SessionObservationMetadata{Ref: ref, InstanceID: instance, GenerationID: generation, EntryID: "terminal:A", TurnID: "A", CausalKnown: true, TerminalAccepted: &accepted, EndTurn: true, EndReason: "done"}
	second <- appwire.SessionWatchUpdate{Event: "communicate", Metadata: terminal}

	awaitWithin(t, 5*time.Second, "watch completion after outage recovery", func() { <-base.ended })
	if got := countWatchSends(t, jm, watchID, instance+"/"+generation+"/terminal:A:communicate:completed"); got != 1 {
		t.Fatalf("completion deliveries after outage recovery=%d, want 1", got)
	}
}

// The outage budget's other half: a binding outage that outlasts the budget
// ends the watch durably with re-registration guidance, never an inferred
// verdict, and the loss itself owes exactly one interruption notice
// (job-control.md).
func TestManagerWatchOutagePastBudgetEndsDurably(t *testing.T) {
	const ref, instance, generation = "local:worker", "worker-instance", "7@worker"
	ctx := t.Context()
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) {
		cfg.Manager = true
		cfg.testOnly.hubWatchRetryDelay = time.Millisecond
		cfg.testOnly.hubWatchStateOutageBudget = 50 * time.Millisecond
	})
	first := make(chan appwire.SessionWatchUpdate, 1)
	first <- appwire.SessionWatchUpdate{
		Snapshot: &appwire.ThreadReadResponse{BootGeneration: generation, Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: ref, InstanceID: instance}}},
		Metadata: appwire.SessionObservationMetadata{Ref: ref, InstanceID: instance, GenerationID: generation},
	}
	base := &managerRecoverClient{
		view:         appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true},
		streams:      make(chan chan appwire.SessionWatchUpdate, 1),
		watches:      make(chan struct{}, 1),
		offlineReads: make(chan struct{}, 16),
		ended:        make(chan struct{}, 2),
	}
	base.streams <- first
	root.SetHubClient(base)
	res, err := root.configureHubWatchRuntime(ctx, map[string]any{"operation": "create", "hub": map[string]any{"ref": ref, "events": []string{"communicate"}, "completion": true}})
	if err != nil {
		t.Fatal(err)
	}
	watchID := res.(tool.StateResult).State.(jobWatchToolResult).WatchID
	<-base.watches
	jm := root.jobManager
	if jm == nil {
		t.Fatal("missing journal owner")
	}

	// The binding goes offline for the rest of the test; the outage outlasts
	// the 50ms budget, so connection recovery is exhausted.
	base.mu.Lock()
	base.offline = true
	base.mu.Unlock()
	close(first)
	awaitWithin(t, 5*time.Second, "durable end after the outage outlasted the budget", func() { <-base.ended })
	if managerHubWatchPresent(jm, watchID) {
		t.Fatal("outage past the budget left the watch armed")
	}
	if got := countWatchSends(t, jm, watchID, ref+"#interruption"); got != 1 {
		t.Fatalf("interruption notices for the loss episode=%d, want 1", got)
	}
	if got := countWatchSends(t, jm, watchID, ref+"#ended"); got != 1 {
		t.Fatalf("durable end notices=%d, want 1", got)
	}
}

// The outage budget's end decision must follow a fresh read, not a sleep: a
// binding that answers again inside the budget retains the watch even when its
// answer lands in the final backoff window, after the last scheduled read and
// before the deadline. Ending there reused the previous read's error and
// slept past the deadline without ever observing the recovery.
func TestManagerWatchBindingAnsweredInsideBudgetRetainsWatch(t *testing.T) {
	const ref, instance, generation = "local:worker", "worker-instance", "7@worker"
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) {
		cfg.Manager = true
		cfg.testOnly.hubWatchRetryDelay = 100 * time.Millisecond
		cfg.testOnly.hubWatchStateOutageBudget = 2500 * time.Millisecond
	})
	snapshot := appwire.SessionWatchUpdate{
		Snapshot: &appwire.ThreadReadResponse{BootGeneration: generation, Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: ref, InstanceID: instance}}},
		Metadata: appwire.SessionObservationMetadata{Ref: ref, InstanceID: instance, GenerationID: generation},
	}
	first := make(chan appwire.SessionWatchUpdate, 1)
	first <- snapshot
	base := &managerRecoverClient{
		view:         appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true},
		streams:      make(chan chan appwire.SessionWatchUpdate, 2),
		watches:      make(chan struct{}, 2),
		offlineReads: make(chan struct{}, 16),
		ended:        make(chan struct{}, 2),
	}
	base.streams <- first
	root.SetHubClient(base)
	res, err := root.configureHubWatchRuntime(t.Context(), map[string]any{"operation": "create", "hub": map[string]any{"ref": ref, "events": []string{"communicate"}, "completion": true}})
	if err != nil {
		t.Fatal(err)
	}
	watchID := res.(tool.StateResult).State.(jobWatchToolResult).WatchID
	<-base.watches
	jm := root.jobManager
	if jm == nil {
		t.Fatal("missing journal owner")
	}

	// Outage. With the 100ms base the failed reads land at roughly 0, 100,
	// 300, 700 and 1500ms, and the next backoff would sleep well past the
	// 2.5s deadline.
	base.mu.Lock()
	base.offline = true
	base.mu.Unlock()
	close(first)
	for range 5 {
		select {
		case <-base.offlineReads:
		case <-time.After(5 * time.Second):
			t.Fatal("state reads stopped failing while the binding was still offline")
		}
	}
	// A rebind signal arrives now, while the binding is still down, so the
	// loop consumes it here and every later wakeup is the backoff timer's.
	second := make(chan appwire.SessionWatchUpdate, 4)
	second <- snapshot
	base.streams <- second
	root.HubBindingChanged()
	// The binding answers again at roughly 1.7s: inside the budget, in the
	// window after the last scheduled read (about 1.5s) that an unreformed
	// loop would have slept through, with margin for a loaded runner.
	time.Sleep(200 * time.Millisecond)
	base.mu.Lock()
	base.offline = false
	base.mu.Unlock()
	awaitWithin(t, 5*time.Second, "rebind after the binding answered inside the budget", func() { <-base.watches })
	accepted := true
	terminal := appwire.SessionObservationMetadata{Ref: ref, InstanceID: instance, GenerationID: generation, EntryID: "terminal:A", TurnID: "A", CausalKnown: true, TerminalAccepted: &accepted, EndTurn: true, EndReason: "done"}
	second <- appwire.SessionWatchUpdate{Event: "communicate", Metadata: terminal}
	awaitWithin(t, 5*time.Second, "watch completion after in-budget recovery", func() { <-base.ended })
	if got := countWatchSends(t, jm, watchID, instance+"/"+generation+"/terminal:A:communicate:completed"); got != 1 {
		t.Fatalf("completion deliveries after in-budget recovery=%d, want 1", got)
	}
}
