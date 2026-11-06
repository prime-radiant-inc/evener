package agent

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/appwire"
)

// hubWatchTestHangGuard sits far above every wait in the hub-watch tests:
// each completes in milliseconds once its seam-scaled event fires, so this
// ceiling exists only to fail a wedged producer instead of hanging the
// suite, never to pace the test.
const hubWatchTestHangGuard = 5 * time.Second

// A binding outage must not end a watch whose connection comes back
// (job-control.md: a restored connection retains the watch). The watch stays
// armed through the outage here, and the completion observed on the restored
// connection still delivers.
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
		case <-time.After(hubWatchTestHangGuard):
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
	awaitWithin(t, hubWatchTestHangGuard, "rebind after connection restore", func() { <-base.watches })
	accepted := true
	terminal := appwire.SessionObservationMetadata{Ref: ref, InstanceID: instance, GenerationID: generation, EntryID: "terminal:A", TurnID: "A", CausalKnown: true, TerminalAccepted: &accepted, EndTurn: true, EndReason: "done"}
	second <- appwire.SessionWatchUpdate{Event: "communicate", Metadata: terminal}

	awaitWithin(t, hubWatchTestHangGuard, "watch completion after outage recovery", func() { <-base.ended })
	if got := countWatchSends(t, jm, watchID, instance+"/"+generation+"/terminal:A:communicate:completed"); got != 1 {
		t.Fatalf("completion deliveries after outage recovery=%d, want 1", got)
	}
}

// A stream-end blip that rebinds inside the outage budget is not a loss the
// Manager must act on, so it owes no interruption notice (job-control.md):
// the frame read as an incident on healthy lanes through the 2026-11-05/06
// binding blips, once per watch per blip. The watch suspends quietly while
// recovery is in progress, and the terminal observation on the rebound
// stream proves the episode recovered: it still delivers exactly once, with
// no interruption frame anywhere in the episode.
func TestManagerWatchStreamEndReboundInsideBudgetDeliversNoInterruptionNotice(t *testing.T) {
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

	// The blip: the stream dies and the binding stops answering. The watch
	// must stay armed while recovery is in progress.
	base.mu.Lock()
	base.offline = true
	base.mu.Unlock()
	close(first)
	for range 2 {
		select {
		case <-base.offlineReads:
		case <-time.After(hubWatchTestHangGuard):
			if !managerHubWatchPresent(jm, watchID) {
				t.Fatal("stream-end blip ended the watch before the binding restored")
			}
			t.Fatal("state reads stopped failing while the binding was still offline")
		}
	}
	if !managerHubWatchPresent(jm, watchID) {
		t.Fatal("stream-end blip ended the watch before the binding restored")
	}

	// The blip ends on its own: the binding answers again inside the budget
	// and the rebind succeeds. The terminal observation on the rebound stream
	// must still deliver.
	second := make(chan appwire.SessionWatchUpdate, 4)
	second <- snapshot
	base.streams <- second
	root.HubBindingChanged()
	base.mu.Lock()
	base.offline = false
	base.mu.Unlock()
	awaitWithin(t, hubWatchTestHangGuard, "rebind after the binding restored", func() { <-base.watches })
	accepted := true
	terminal := appwire.SessionObservationMetadata{Ref: ref, InstanceID: instance, GenerationID: generation, EntryID: "terminal:A", TurnID: "A", CausalKnown: true, TerminalAccepted: &accepted, EndTurn: true, EndReason: "done"}
	second <- appwire.SessionWatchUpdate{Event: "communicate", Metadata: terminal}

	awaitWithin(t, hubWatchTestHangGuard, "watch completion after the stream-end blip rebound", func() { <-base.ended })
	if got := countWatchSends(t, jm, watchID, instance+"/"+generation+"/terminal:A:communicate:completed"); got != 1 {
		t.Fatalf("completion deliveries after the blip rebound=%d, want 1", got)
	}
	// The episode is closed once the completion delivered: recovery succeeded,
	// and any interruption frame would have been recorded at the suspend, long
	// before this point.
	if got := countWatchSends(t, jm, watchID, ref+"#interruption"); got != 0 {
		t.Fatalf("interruption notices across the rebound blip=%d, want 0", got)
	}
}

// The outage budget's other half: a binding outage that outlasts the budget
// ends the watch durably with re-registration guidance, never an inferred
// verdict. The durable end notice is the one notice the loss delivers
// (job-control.md): the interruption frame stays silent, because recovery
// never succeeded.
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
	awaitWithin(t, hubWatchTestHangGuard, "durable end after the outage outlasted the budget", func() { <-base.ended })
	if managerHubWatchPresent(jm, watchID) {
		t.Fatal("outage past the budget left the watch armed")
	}
	if got := countWatchSends(t, jm, watchID, ref+"#interruption"); got != 0 {
		t.Fatalf("interruption notices for the exhausted episode=%d, want 0", got)
	}
	if got := countWatchSends(t, jm, watchID, ref+"#ended"); got != 1 {
		t.Fatalf("durable end notices=%d, want 1", got)
	}
}

// The outage budget's end decision must follow a fresh read, not a sleep: a
// binding that answers again inside the budget retains the watch even when its
// answer lands in the final backoff window, after the last scheduled read and
// before the deadline.
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
		case <-time.After(hubWatchTestHangGuard):
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
	// window after the last scheduled read (about 1.5s) that the deadline-
	// capped backoff must still re-read, with margin for a loaded runner.
	time.Sleep(200 * time.Millisecond)
	base.mu.Lock()
	base.offline = false
	base.mu.Unlock()
	awaitWithin(t, hubWatchTestHangGuard, "rebind after the binding answered inside the budget", func() { <-base.watches })
	accepted := true
	terminal := appwire.SessionObservationMetadata{Ref: ref, InstanceID: instance, GenerationID: generation, EntryID: "terminal:A", TurnID: "A", CausalKnown: true, TerminalAccepted: &accepted, EndTurn: true, EndReason: "done"}
	second <- appwire.SessionWatchUpdate{Event: "communicate", Metadata: terminal}
	awaitWithin(t, hubWatchTestHangGuard, "watch completion after in-budget recovery", func() { <-base.ended })
	if got := countWatchSends(t, jm, watchID, instance+"/"+generation+"/terminal:A:communicate:completed"); got != 1 {
		t.Fatalf("completion deliveries after in-budget recovery=%d, want 1", got)
	}
}
