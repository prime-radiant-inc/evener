package agent

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// A resync re-reads authoritative state while the notification drain continues
// (job-control.md). On a target whose observations keep flowing across the
// resync it is not an observation loss, so it must not deliver an
// interruption notice.
func TestManagerWatchResyncOnFlowingObservationDeliversNoInterruptionNotice(t *testing.T) {
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	jm := root.jobManager
	watch, err := jm.configureHubWatch("local:worker", "worker-instance", "1", "local:manager", []string{"communicate"}, true)
	if err != nil {
		t.Fatal(err)
	}
	meta := appwire.SessionObservationMetadata{Ref: "local:worker", InstanceID: "worker-instance", GenerationID: "1"}
	// A live recorded observation flows first, so the resync cycles below
	// arrive on a stream whose drain is demonstrably continuing.
	flowing := appwire.SessionWatchUpdate{Metadata: meta, Snapshot: &appwire.ThreadReadResponse{
		BootGeneration: "1",
		Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: meta.Ref, InstanceID: meta.InstanceID, ActiveTurnID: "A"}, Turns: []appwire.Turn{{
			ID: "A",
			Items: []appwire.ThreadItem{{ID: "flow-1", TranscriptKey: "flow-1", Observation: &appwire.SessionObservationMetadata{
				Ref: meta.Ref, InstanceID: meta.InstanceID, GenerationID: meta.GenerationID,
				EntryID: "flow-1", TurnID: "A", CausalKnown: true,
			}}},
		}}},
	}}
	if err := jm.onHubObservation(watch.WatchID, flowing); err != nil {
		t.Fatal(err)
	}
	resync := appwire.SessionWatchUpdate{Metadata: meta, Notification: appwire.NotificationMessage(appwire.NotifyEvenerThreadResync, appwire.ThreadResyncParams{Ref: meta.Ref}).Notification}
	// Two independent resync cycles, each followed by the authoritative
	// re-read the hub's runtime performs after a resync.
	for range 2 {
		if err := jm.onHubObservation(watch.WatchID, resync); err != nil {
			t.Fatal(err)
		}
		if err := jm.onHubObservation(watch.WatchID, flowing); err != nil {
			t.Fatal(err)
		}
	}
	notices := 0
	deliveredFlow := 0
	wantInterruption := meta.Ref + "#interruption"
	wantFlow := "worker-instance/1/flow-1:communicate"
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.WatchSend == nil || event.WatchSend.Key.WatchID != watch.WatchID {
			continue
		}
		switch event.WatchSend.TriggerIdentity {
		case wantInterruption:
			notices++
		case wantFlow:
			deliveredFlow++
		}
	}
	if notices != 0 {
		t.Fatalf("resync cycles on a flowing observation delivered %d stale interruption notices, want 0", notices)
	}
	if deliveredFlow == 0 {
		t.Fatal("flowing observation was never delivered")
	}
}

// A resync re-reads authoritative state; it must neither end nor suspend a
// completion-only watch nor deliver an interruption notice, and the watch
// must still complete on the recorded verdict the re-read replays.
func TestManagerWatchResyncKeepsCompletionWatchArmedWithoutNotice(t *testing.T) {
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	jm := root.jobManager
	watch, err := jm.configureHubWatch("local:worker", "worker-instance", "1", "local:manager", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	meta := appwire.SessionObservationMetadata{Ref: "local:worker", InstanceID: "worker-instance", GenerationID: "1"}
	resync := appwire.SessionWatchUpdate{Metadata: meta, Notification: appwire.NotificationMessage(appwire.NotifyEvenerThreadResync, appwire.ThreadResyncParams{Ref: meta.Ref}).Notification}
	for range 2 {
		if err := jm.onHubObservation(watch.WatchID, resync); err != nil {
			t.Fatal(err)
		}
	}
	jm.mu.Lock()
	_, cfg, ok := jm.watchConfigByIDLocked(watch.WatchID)
	suspended := ok && cfg.hubSuspended
	jm.mu.Unlock()
	if !ok {
		t.Fatal("resync ended durable completion watch")
	}
	if suspended {
		t.Fatal("resync suspended a watch whose observation never stopped")
	}
	if notices := countWatchSends(t, jm, watch.WatchID, meta.Ref+"#interruption"); notices != 0 {
		t.Fatalf("resync delivered %d interruption notices, want 0", notices)
	}
	accepted := true
	terminal := meta
	terminal.EntryID, terminal.TurnID, terminal.CausalKnown = "terminal:A", "A", true
	terminal.TerminalAccepted, terminal.EndTurn, terminal.EndReason = &accepted, true, "done"
	reRead := appwire.SessionWatchUpdate{Metadata: meta, Snapshot: &appwire.ThreadReadResponse{
		BootGeneration: "1",
		Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: meta.Ref, InstanceID: meta.InstanceID, ActiveTurnID: "A"}, Turns: []appwire.Turn{{
			ID:    "A",
			Items: []appwire.ThreadItem{{ID: "terminal:A", TranscriptKey: "terminal:A", Observation: &terminal}},
		}}},
	}}
	if err := jm.onHubObservation(watch.WatchID, reRead); err != nil {
		t.Fatal(err)
	}
	if managerHubWatchPresent(jm, watch.WatchID) {
		t.Fatal("recorded terminal verdict after resync did not complete the watch")
	}
	if got := countWatchSends(t, jm, watch.WatchID, "worker-instance/1/terminal:A:communicate:completed"); got != 1 {
		t.Fatalf("completion deliveries=%d want=1", got)
	}
}

// countWatchSends counts journaled watch-send events for watchID whose trigger
// identity equals want.
func countWatchSends(t *testing.T, jm *jobManager, watchID, want string) int {
	t.Helper()
	count := 0
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.WatchSend != nil && event.WatchSend.Key.WatchID == watchID && event.WatchSend.TriggerIdentity == want {
			count++
		}
	}
	return count
}
