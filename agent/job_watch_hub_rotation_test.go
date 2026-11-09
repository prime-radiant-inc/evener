package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/appwire"
)

// A same-target history rotation reaches the watcher as a fresh snapshot with
// a new epoch and incarnation (the hub re-anchors its subscription and sends
// the rebuilt cut). The consumer must re-anchor its held cut on it and keep
// the watch alive: the rebuilt window replays under the watch's own delivery
// dedup, and a terminal verdict recorded in the rebuilt history still
// completes the watch (job-control.md: index contention or rebuilding recovers
// without re-registration).
func TestManagerWatchConsumesRotatedHistorySnapshot(t *testing.T) {
	const ref, instance, generation = "local:worker", "worker-instance", "7@worker"
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	observed := appwire.SessionObservationMetadata{Ref: ref, InstanceID: instance, GenerationID: generation, EntryID: "turn-a-item", TurnID: "A", CausalKnown: true}
	initial := appwire.SessionWatchUpdate{
		Snapshot: &appwire.ThreadReadResponse{
			BootGeneration: generation,
			Epoch:          1,
			Snapshot:       &appwire.SnapshotIdentity{Incarnation: "history-a", Length: 1},
			Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: ref, InstanceID: instance, ActiveTurnID: "A"}, Turns: []appwire.Turn{{
				ID:    "A",
				Items: []appwire.ThreadItem{{ID: "turn-a-item", TranscriptKey: "turn-a-item", Observation: &observed}},
			}}},
		},
		Metadata: appwire.SessionObservationMetadata{Ref: ref, InstanceID: instance, GenerationID: generation},
	}
	stream := make(chan appwire.SessionWatchUpdate, 4)
	stream <- initial
	base := &managerRecoverClient{
		view:    appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true},
		streams: make(chan chan appwire.SessionWatchUpdate, 1),
		watches: make(chan struct{}, 1),
		ended:   make(chan struct{}, 1),
	}
	base.streams <- stream
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

	// The history rotates: same target, new epoch and incarnation, and the
	// rebuilt window now records the turn's terminal verdict.
	accepted := true
	terminal := observed
	terminal.EntryID, terminal.TerminalAccepted, terminal.EndTurn, terminal.EndReason = "terminal:A", &accepted, true, "done"
	rotated := appwire.SessionWatchUpdate{
		Snapshot: &appwire.ThreadReadResponse{
			BootGeneration: generation,
			Epoch:          2,
			Snapshot:       &appwire.SnapshotIdentity{Incarnation: "history-b", Length: 2},
			Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: ref, InstanceID: instance, ActiveTurnID: "A"}, Turns: []appwire.Turn{{
				ID: "A",
				Items: []appwire.ThreadItem{
					{ID: "turn-a-item", TranscriptKey: "turn-a-item", Observation: &observed},
					{ID: "terminal:A", TranscriptKey: "terminal:A", Observation: &terminal},
				},
			}}},
		},
		Metadata: appwire.SessionObservationMetadata{Ref: ref, InstanceID: instance, GenerationID: generation},
	}
	stream <- rotated

	awaitWithin(t, hubWatchTestHangGuard, "watch completion on the rotated history", func() { <-base.ended })
	if got := countWatchSends(t, jm, watchID, instance+"/"+generation+"/terminal:A:communicate:completed"); got != 1 {
		t.Fatalf("completion deliveries on the rotated history=%d, want 1 (the rebuilt window recorded the terminal verdict)", got)
	}
}

// Within one history — same epoch and incarnation — a snapshot shorter than
// the held cut is truncation: the observation ends immediately with a
// durable end notice and re-registration guidance, never an inferred verdict
// (job-control.md).
func TestManagerWatchTruncatedHistoryEndsObservation(t *testing.T) {
	const ref, instance, generation = "local:worker", "worker-instance", "7@worker"
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	held := appwire.SessionWatchUpdate{
		Snapshot: &appwire.ThreadReadResponse{
			BootGeneration: generation,
			Epoch:          1,
			Snapshot:       &appwire.SnapshotIdentity{Incarnation: "history-a", Length: 2},
			Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: ref, InstanceID: instance, ActiveTurnID: "A"}, Turns: []appwire.Turn{{
				ID: "A",
				Items: []appwire.ThreadItem{
					{ID: "turn-a-1", TranscriptKey: "turn-a-1", Observation: &appwire.SessionObservationMetadata{Ref: ref, InstanceID: instance, GenerationID: generation, EntryID: "turn-a-1", TurnID: "A", CausalKnown: true}},
					{ID: "turn-a-2", TranscriptKey: "turn-a-2", Observation: &appwire.SessionObservationMetadata{Ref: ref, InstanceID: instance, GenerationID: generation, EntryID: "turn-a-2", TurnID: "A", CausalKnown: true}},
				},
			}}},
		},
		Metadata: appwire.SessionObservationMetadata{Ref: ref, InstanceID: instance, GenerationID: generation},
	}
	truncated := held
	truncated.Snapshot = &appwire.ThreadReadResponse{
		BootGeneration: generation,
		Epoch:          1,
		Snapshot:       &appwire.SnapshotIdentity{Incarnation: "history-a", Length: 1},
		Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: ref, InstanceID: instance, ActiveTurnID: "A"}, Turns: []appwire.Turn{{
			ID:    "A",
			Items: []appwire.ThreadItem{{ID: "turn-a-1", TranscriptKey: "turn-a-1", Observation: &appwire.SessionObservationMetadata{Ref: ref, InstanceID: instance, GenerationID: generation, EntryID: "turn-a-1", TurnID: "A", CausalKnown: true}}},
		}}},
	}
	stream := make(chan appwire.SessionWatchUpdate, 4)
	stream <- held
	base := &managerRecoverClient{
		view:    appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true},
		streams: make(chan chan appwire.SessionWatchUpdate, 1),
		watches: make(chan struct{}, 1),
		ended:   make(chan struct{}, 1),
	}
	base.streams <- stream
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

	stream <- truncated
	awaitWithin(t, hubWatchTestHangGuard, "durable end on truncated history", func() { <-base.ended })
	if managerHubWatchPresent(jm, watchID) {
		t.Fatal("truncated history left the watch armed")
	}
	if got := countWatchSends(t, jm, watchID, ref+"#ended"); got != 1 {
		t.Fatalf("durable end notices=%d, want 1", got)
	}
}
