package agent

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestManagerWatchCompletionExecutionBoundary(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "question_pending"}[pending], func(t *testing.T) {
			root := newMessagingRoot(t, newMessagingAdapter(), func(c *SessionConfig) { c.Manager = true })
			jm := root.jobManager
			const ref, instance, generation = "local:worker", "worker-instance", "2@worker"
			w, err := jm.configureHubWatch(ref, instance, generation, "local:manager", nil, true)
			if err != nil {
				t.Fatal(err)
			}
			accepted := true
			done := func(turn, id string) appwire.SessionObservationMetadata {
				return appwire.SessionObservationMetadata{TurnID: turn, EntryID: id, EndTurn: true, EndReason: "done", TerminalAccepted: &accepted, CausalKnown: true}
			}
			old := done("A", "old")
			snapshot := func(active string, turns []appwire.Turn) {
				t.Helper()
				if err := jm.onHubObservation(w.WatchID, appwire.SessionWatchUpdate{Metadata: appwire.SessionObservationMetadata{Ref: ref, InstanceID: instance, GenerationID: generation}, Snapshot: &appwire.ThreadReadResponse{BootGeneration: generation, Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: ref, InstanceID: instance, ActiveTurnID: active, AskPending: pending}, Turns: turns}}}); err != nil {
					t.Fatal(err)
				}
			}
			snapshot("B", []appwire.Turn{{ID: "A", Items: []appwire.ThreadItem{{Observation: &old}}}, {ID: "B"}})
			if !managerWatchActive(jm, w.WatchID) {
				t.Fatal("historical A completed watch for B")
			}
			live := func(m appwire.SessionObservationMetadata) {
				t.Helper()
				m.Ref, m.InstanceID, m.GenerationID = ref, instance, generation
				if err := jm.onHubObservation(w.WatchID, appwire.SessionWatchUpdate{Event: "communicate", Metadata: m}); err != nil {
					t.Fatal(err)
				}
			}
			live(old)
			if !managerWatchActive(jm, w.WatchID) {
				t.Fatal("live historical A completed watch for B")
			}
			if err := jm.suspendHubWatch(w.WatchID); err != nil {
				t.Fatal(err)
			}
			newer := done("C", "newer")
			snapshot("C", []appwire.Turn{{ID: "C", Items: []appwire.ThreadItem{{Observation: &newer}}}})
			live(newer)
			if !managerWatchActive(jm, w.WatchID) {
				t.Fatal("resync rebound B watch to C")
			}
			winner := done("B", "winner")
			if pending {
				snapshot("C", []appwire.Turn{{ID: "B", Items: []appwire.ThreadItem{{Observation: &winner}}}, {ID: "C"}})
			} else {
				live(winner)
			}
			live(winner)
			if managerWatchActive(jm, w.WatchID) {
				t.Fatal("matching accepted B did not complete")
			}
			completed := 0
			for _, event := range loadJobStoreEvents(t, jm) {
				if event.WatchSend != nil && event.WatchSend.Key.WatchID == w.WatchID && event.WatchSend.TriggerIdentity == "worker-instance/2@worker/winner:communicate:completed" && !event.WatchSend.EndNotice {
					completed++
				}
			}
			if completed != 1 {
				t.Fatalf("completion deliveries=%d, want one", completed)
			}
		})
	}
}

func TestManagerWatchIdleLatestWork(t *testing.T) {
	for _, unfinished := range []bool{false, true} {
		t.Run(map[bool]string{false: "prior_boot_done", true: "later_unfinished"}[unfinished], func(t *testing.T) {
			root := newMessagingRoot(t, newMessagingAdapter(), func(c *SessionConfig) { c.Manager = true })
			jm := root.jobManager
			w, err := jm.configureHubWatch("local:worker", "instance", "2@worker", "local:manager", nil, true)
			if err != nil {
				t.Fatal(err)
			}
			accepted := true
			m := appwire.SessionObservationMetadata{GenerationID: "1@worker", TurnID: "A", EntryID: "old", EndTurn: true, EndReason: "done", TerminalAccepted: &accepted, CausalKnown: true}
			turns := []appwire.Turn{{ID: "A", Items: []appwire.ThreadItem{{Observation: &m}}}}
			latestExecution := "A"
			if unfinished {
				turns = append(turns, appwire.Turn{ID: "B"})
				latestExecution = "B"
			}
			if err := jm.onHubObservation(w.WatchID, appwire.SessionWatchUpdate{Metadata: appwire.SessionObservationMetadata{Ref: "local:worker", InstanceID: "instance", GenerationID: "2@worker"}, Snapshot: &appwire.ThreadReadResponse{BootGeneration: "2@worker", LatestExecutionTurnID: latestExecution, Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: "local:worker", InstanceID: "instance"}, Turns: turns}}}); err != nil {
				t.Fatal(err)
			}
			if managerWatchActive(jm, w.WatchID) != unfinished {
				t.Fatalf("active=%v want %v", managerWatchActive(jm, w.WatchID), unfinished)
			}
			if m.GenerationID != "1@worker" {
				t.Fatal("recorded provenance changed")
			}
		})
	}
}
