package agent

import (
	"errors"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

// A lost response after the atomic rename no longer leaves a metadata intent
// to adopt: the original mutation already owns the note and its notification.
func TestHumanNoteLostResponseSameValueSaveDoesNotAdopt(t *testing.T) {
	s := newDurableHumanNoteSession(t)
	s.clientMutations.faults.AfterEffectSnapshotRename = func() error { return errors.New("lost response") }
	if _, err := s.SetHumanNote("first", "sentinel"); err == nil {
		t.Fatal("missing fault")
	}
	reloadHumanNoteStore(t, s)
	response, err := s.SetHumanNote("second", "sentinel")
	if err != nil {
		t.Fatal(err)
	}
	if response.Note != "sentinel" || response.Receipt.ProjectionState != appwire.MutationProjectionRemoved {
		t.Fatalf("noop = %+v", response)
	}
	reloadHumanNoteStore(t, s)
	if _, err := s.SetHumanNote("third", "sentinel"); err != nil {
		t.Fatal(err)
	}
	snapshot := s.clientMutations.snapshot()
	if len(snapshot.PendingExecutions) != 1 || len(snapshot.SteeringOrder) != 1 || snapshot.SteeringOrder[0] != "first" {
		t.Fatalf("duplicate notification: %+v", snapshot.SteeringOrder)
	}
	if snapshot.Journal["first"].SteeringKind != events.SteeringKindHumanNote {
		t.Fatal("lost typed notification")
	}
}

func TestHumanNoteNoOpFailureDoesNotSpendNotification(t *testing.T) {
	s := newDurableHumanNoteSession(t)
	if _, err := s.SetHumanNote("first", "sentinel"); err != nil {
		t.Fatal(err)
	}
	s.clientMutations.faults.BeforeEffectSnapshotRename = func() error { return errors.New("noop rename failure") }
	if _, err := s.SetHumanNote("second", "sentinel"); err == nil {
		t.Fatal("missing fault")
	}
	reloadHumanNoteStore(t, s)
	if _, err := s.SetHumanNote("second", "sentinel"); err != nil {
		t.Fatal(err)
	}
	if got := s.clientMutations.snapshot().SteeringOrder; len(got) != 1 || got[0] != "first" {
		t.Fatalf("notification ownership changed: %v", got)
	}
}

func TestHumanNoteNoOpUnderFencePreservesAcceptedNotification(t *testing.T) {
	s := newDurableHumanNoteSession(t)
	if _, err := s.SetHumanNote("first", "sentinel"); err != nil {
		t.Fatal(err)
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.InterruptFence = &clientMutationInterruptFence{ClientMutationID: "stop"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	response, err := s.SetHumanNote("second", "sentinel")
	if err != nil {
		t.Fatal(err)
	}
	if response.Receipt.ProjectionState != appwire.MutationProjectionRemoved {
		t.Fatalf("noop = %+v", response)
	}
	if _, err := s.SetHumanNote("third", "different"); !isHumanNoteConflict(err) {
		t.Fatalf("changed save = %v", err)
	}
	if got := s.clientMutations.snapshot().SteeringOrder; len(got) != 1 || got[0] != "first" {
		t.Fatalf("fence changed accepted work: %v", got)
	}
}
