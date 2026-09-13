package agent

import (
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
)

// A definite refusal records no write; a fresh request after the fence lifts
// can accept the note. Reusing the rejected ID remains a definite rejection.
func TestSetHumanNoteFreshIDAfterRefusal(t *testing.T) {
	s := newDurableHumanNoteSession(t)
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.InterruptFence = &clientMutationInterruptFence{ClientMutationID: "stop"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetHumanNote("first", "sentinel"); !isHumanNoteConflict(err) {
		t.Fatalf("refusal = %v", err)
	}
	if note, _ := s.notesSnapshot(); note != "" {
		t.Fatalf("refusal stored %q", note)
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error { snapshot.InterruptFence = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetHumanNote("first", "sentinel"); !isHumanNoteConflict(err) {
		t.Fatalf("rejected replay = %v", err)
	}
	if _, err := s.SetHumanNote("second", "sentinel"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetHumanNote("third", "sentinel"); err != nil {
		t.Fatal(err)
	}
	if got := s.clientMutations.snapshot().SteeringOrder; len(got) != 1 || got[0] != "second" {
		t.Fatalf("notification = %v", got)
	}
}

func TestSetHumanNoteFreshIDAfterUncertainCommit(t *testing.T) {
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
	if response.Receipt.ProjectionState != appwire.MutationProjectionRemoved {
		t.Fatalf("noop = %+v", response)
	}
	if got := s.clientMutations.snapshot().SteeringOrder; len(got) != 1 || got[0] != "first" {
		t.Fatalf("notification = %v", got)
	}
}

func TestSetHumanNoteFreshIDDifferentValueKeepsNewer(t *testing.T) {
	s := newDurableHumanNoteSession(t)
	if _, err := s.SetHumanNote("first", "A"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetHumanNote("second", "B"); err != nil {
		t.Fatal(err)
	}
	response, err := s.SetHumanNote("third", "A")
	if err != nil {
		t.Fatal(err)
	}
	if response.Note != "A" {
		t.Fatalf("new write = %+v", response)
	}
	if note, _ := s.notesSnapshot(); note != "A" {
		t.Fatalf("new write did not win: %q", note)
	}
	if len(s.clientMutations.snapshot().SteeringOrder) != 3 {
		t.Fatal("distinct transition lost notification")
	}
}
