package agent

import (
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestSetHumanNoteJournalFaultRetryDeliversWithoutRewrite(t *testing.T) {
	s := newDurableHumanNoteSession(t)
	s.clientMutations.faults.AfterEffectSnapshotRename = func() error { return errors.New("lost response") }
	if _, err := s.SetHumanNote("save", " note\nvalue "); err == nil {
		t.Fatal("missing fault")
	}
	if note, _ := s.notesSnapshot(); note != "note value" {
		t.Fatalf("committed note = %q", note)
	}
	reloadHumanNoteStore(t, s)
	replay, err := s.SetHumanNote("save", " note\nvalue ")
	if err != nil {
		t.Fatal(err)
	}
	if replay.Note != "note value" || replay.Receipt.Disposition != appwire.MutationDispositionReplayed {
		t.Fatalf("replay = %+v", replay)
	}
	if len(s.clientMutations.snapshot().SteeringOrder) != 1 {
		t.Fatal("retry duplicated notification")
	}
}

func TestSetHumanNoteJournalFaultInterveningSaveKeepsNewer(t *testing.T) {
	s := newDurableHumanNoteSession(t)
	s.clientMutations.faults.AfterEffectSnapshotRename = func() error { return errors.New("lost response") }
	if _, err := s.SetHumanNote("save", "note A"); err == nil {
		t.Fatal("missing fault")
	}
	reloadHumanNoteStore(t, s)
	if _, err := s.SetHumanNote("newer", "note B"); err != nil {
		t.Fatal(err)
	}
	replay, err := s.SetHumanNote("save", "note A")
	if err != nil {
		t.Fatal(err)
	}
	if replay.Note != "note A" {
		t.Fatalf("recorded response = %+v", replay)
	}
	if note, _ := s.notesSnapshot(); note != "note B" {
		t.Fatalf("retry reverted note to %q", note)
	}
	if len(s.clientMutations.snapshot().SteeringOrder) != 2 {
		t.Fatal("retry duplicated notification")
	}
}
