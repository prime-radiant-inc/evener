package agent

import (
	"testing"
	"time"

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
		cfg.testOnly.hubWatchStateOutageBudget = time.Second
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
	if _, err := root.configureHubWatchRuntime(ctx, map[string]any{"operation": "create", "hub": map[string]any{"ref": ref, "events": []string{"communicate"}, "completion": true}}); err != nil {
		t.Fatal(err)
	}
	<-base.watches
	jm := root.jobManager
	if jm == nil {
		t.Fatal("missing journal owner")
	}
	jm.mu.Lock()
	var watchID string
	for _, cfg := range jm.watches {
		if cfg.hubRef == ref {
			watchID = cfg.watchID
		}
	}
	jm.mu.Unlock()
	if watchID == "" {
		t.Fatal("registration did not arm a watch")
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

	// Restore: the binding answers again and is reinstalled. The watch must
	// re-anchor on it and still complete on the terminal observation.
	base.mu.Lock()
	base.offline = false
	base.mu.Unlock()
	second := make(chan appwire.SessionWatchUpdate, 4)
	second <- snapshot
	base.streams <- second
	root.HubBindingChanged()
	select {
	case <-base.watches:
	case <-ctx.Done():
		t.Fatal("restored connection did not re-anchor the watch subscription")
	}
	accepted := true
	terminal := appwire.SessionObservationMetadata{Ref: ref, InstanceID: instance, GenerationID: generation, EntryID: "terminal:A", TurnID: "A", CausalKnown: true, TerminalAccepted: &accepted, EndTurn: true, EndReason: "done"}
	second <- appwire.SessionWatchUpdate{Event: "communicate", Metadata: terminal}

	deadline := time.Now().Add(5 * time.Second)
	for managerHubWatchPresent(jm, watchID) {
		if time.Now().After(deadline) {
			t.Fatal("watch survived the outage but did not complete on the restored connection")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := countWatchSends(t, jm, watchID, instance+"/"+generation+"/terminal:A:communicate:completed"); got != 1 {
		t.Fatalf("completion deliveries after outage recovery=%d, want 1", got)
	}
}
