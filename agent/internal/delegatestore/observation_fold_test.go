package delegatestore

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// TestFoldRecordsObservationForAPacketlessAttentionFinish is the medium that a
// completed_no_action settle writes no terminal packet, so after its runtime is
// reclaimed a status read had nothing to report and fell back to the frozen
// ceiling, claiming tools the child never registered.
//
// The observation must survive on the aggregate, tagged with the generation that
// produced it, and must not create a terminal delivery — a no-action settle
// reports nothing by design.
func TestFoldRecordsObservationForAPacketlessAttentionFinish(t *testing.T) {
	observed := []string{"read_file"}
	finished := finishedEvent("dlg_alpha", 1, OutcomeCompleted, DispositionCompletedNoAction, "", nil)
	finished.RunFinished.ObservedTools = &observed

	state, err := Fold(sequence(
		createdEvent("dlg_alpha", ""),
		startedEvent("dlg_alpha", 1, TriggerAttention),
		finished,
	))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	got := state["dlg_alpha"]
	if got == nil {
		t.Fatal("no aggregate")
	}
	if got.LatestObservation == nil || !slices.Equal(*got.LatestObservation, observed) {
		t.Errorf("LatestObservation = %v, want %v", got.LatestObservation, observed)
	}
	if got.LatestObservationGeneration != 1 {
		t.Errorf("LatestObservationGeneration = %d, want 1", got.LatestObservationGeneration)
	}
	if len(got.PendingDeliveries) != 0 {
		t.Errorf("a completed_no_action settle queued %d deliveries; it must report nothing", len(got.PendingDeliveries))
	}
	if got.LatestPacket != nil {
		t.Errorf("a completed_no_action settle recorded a terminal packet: %#v", got.LatestPacket)
	}
}

// TestFoldTagsAPacketObservationWithItsGeneration is the other direction: a
// packet-carrying settle records the observation it found in the packet, and the
// next generation's start does NOT inherit it as current.
func TestFoldTagsAPacketObservationWithItsGeneration(t *testing.T) {
	packet := reportedPacket("done")
	packet.Metadata = json.RawMessage(`{"observed_tools":["read_file"]}`)

	state, err := Fold(sequence(
		createdEvent("dlg_alpha", ""),
		startedEvent("dlg_alpha", 1, TriggerOwnerInput),
		finishedEvent("dlg_alpha", 1, OutcomeCompleted, DispositionReported, "dlg_alpha/delivery/1", &packet),
	))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	got := state["dlg_alpha"]
	if got.LatestObservation == nil || !slices.Equal(*got.LatestObservation, []string{"read_file"}) {
		t.Errorf("LatestObservation = %v, want [read_file]", got.LatestObservation)
	}
	if got.LatestObservationGeneration != 1 {
		t.Errorf("LatestObservationGeneration = %d, want 1", got.LatestObservationGeneration)
	}
}

// TestFoldDistinguishesAnEmptyObservationFromNoObservation keeps the three-way
// distinction the reporting paths depend on: nil is "no observation recorded",
// an empty list is "registered nothing".
func TestFoldDistinguishesAnEmptyObservationFromNoObservation(t *testing.T) {
	empty := reportedPacket("done")
	empty.Metadata = json.RawMessage(`{"observed_tools":[]}`)

	state, err := Fold(sequence(
		createdEvent("dlg_alpha", ""),
		startedEvent("dlg_alpha", 1, TriggerOwnerInput),
		finishedEvent("dlg_alpha", 1, OutcomeCompleted, DispositionReported, "dlg_alpha/delivery/1", &empty),
	))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	got := state["dlg_alpha"]
	if got.LatestObservation == nil {
		t.Error("an empty observation folded as absent; a child that registered nothing is an answer")
	}
	if len(*got.LatestObservation) != 0 {
		t.Errorf("LatestObservation = %v, want empty", got.LatestObservation)
	}

	// A packet with no observation field at all leaves the prior value alone and
	// records nothing new: the fold must not invent an answer.
	none := reportedPacket("done")
	state, err = Fold(sequence(
		createdEvent("dlg_beta", ""),
		startedEvent("dlg_beta", 1, TriggerOwnerInput),
		finishedEvent("dlg_beta", 1, OutcomeCompleted, DispositionReported, "dlg_beta/delivery/1", &none),
	))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if obs := state["dlg_beta"].LatestObservation; obs != nil {
		t.Errorf("a packet with no observation produced %v, want nil", obs)
	}
}

// TestPacketlessEmptyObservationSurvivesTheJournal is L1: an explicitly empty
// observation must not decay into "no observation" on its way through the
// journal. A packetless completed_no_action settle has no packet to carry the
// answer, so if omitempty drops the empty list the reopened journal reports the
// ceiling for a generation that registered zero tools — the same absent-versus-
// empty confusion as the packet metadata, one layer down.
//
// The test goes through the real encode and decode: the event is marshalled and
// unmarshalled before it is folded, because the loss this guards against happens
// in serialization, not in memory.
func TestPacketlessEmptyObservationSurvivesTheJournal(t *testing.T) {
	empty := []string{}
	finished := finishedEvent("dlg_alpha", 1, OutcomeCompleted, DispositionCompletedNoAction, "", nil)
	finished.RunFinished.ObservedTools = &empty

	raw, err := json.Marshal(finished)
	if err != nil {
		t.Fatalf("marshal the finish: %v", err)
	}
	var reopened Event
	if err := json.Unmarshal(raw, &reopened); err != nil {
		t.Fatalf("unmarshal the finish: %v", err)
	}
	if reopened.RunFinished == nil || reopened.RunFinished.ObservedTools == nil {
		t.Fatalf("an explicitly empty observation did not survive the journal round trip: %s", raw)
	}
	if len(*reopened.RunFinished.ObservedTools) != 0 {
		t.Errorf("reopened observation = %v, want empty", *reopened.RunFinished.ObservedTools)
	}

	state, err := Fold(sequence(
		createdEvent("dlg_alpha", ""),
		startedEvent("dlg_alpha", 1, TriggerAttention),
		reopened,
	))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	got := state["dlg_alpha"].LatestObservation
	if got == nil {
		t.Fatal("a packetless empty observation folded as absent, so a reclaimed runtime would report the ceiling")
	}
	// The slice itself must be non-nil, not merely zero-length: a pointer to a
	// nil slice decays to "absent" at the next copy, and a reader that asks
	// "did anything get observed" answers no.
	if *got == nil {
		t.Error("the observation folded to a nil slice; a generation that registered nothing must be present-and-empty, not absent")
	}
	if len(*got) != 0 {
		t.Errorf("LatestObservation = %v, want empty", *got)
	}

	// The other direction, so the fix cannot pass by making every observation
	// present: an event that recorded nothing must stay absent on the wire.
	absent := finishedEvent("dlg_beta", 1, OutcomeCompleted, DispositionCompletedNoAction, "", nil)
	rawAbsent, err := json.Marshal(absent)
	if err != nil {
		t.Fatalf("marshal the observation-free finish: %v", err)
	}
	if strings.Contains(string(rawAbsent), "observed_tools") {
		t.Errorf("an event with no observation wrote the key anyway: %s", rawAbsent)
	}
}
