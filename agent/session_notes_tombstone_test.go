package agent

import (
	"errors"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

// Atomic recorded results replace the former adoption/supersede tombstones,
// including the explicit-empty result. Exercise both equal resaves and rewrites.
func TestHumanNoteRecordedResultSurvivesNewerWrites(t *testing.T) {
	for _, original := range []string{"A", ""} {
		for _, rewrite := range []bool{false, true} {
			t.Run(original+map[bool]string{false: "/noop", true: "/rewrite"}[rewrite], func(t *testing.T) {
				s := newDurableHumanNoteSession(t)
				if _, err := s.SetHumanNote("seed", "seed"); err != nil {
					t.Fatal(err)
				}
				s.clientMutations.faults.AfterEffectSnapshotRename = func() error { return errors.New("lost response") }
				if _, err := s.SetHumanNote("original", original); err == nil {
					t.Fatal("missing fault")
				}
				reloadHumanNoteStore(t, s)
				if rewrite {
					if _, err := s.SetHumanNote("intervening", "X"); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := s.SetHumanNote("fresh", original); err != nil {
					t.Fatal(err)
				}
				if _, err := s.SetHumanNote("latest", "D"); err != nil {
					t.Fatal(err)
				}
				reloadHumanNoteStore(t, s)
				before := len(s.clientMutations.snapshot().SteeringOrder)
				// Clear prior notifications before asserting replay emits nothing.
				for len(s.Events()) > 0 {
					<-s.Events()
				}
				replay, err := s.SetHumanNote("original", original)
				if err != nil {
					t.Fatal(err)
				}
				if replay.Note != original || replay.Receipt.Disposition != appwire.MutationDispositionReplayed {
					t.Fatalf("replay = %+v", replay)
				}
				if note, _ := s.notesSnapshot(); note != "D" {
					t.Fatalf("retry reverted note to %q", note)
				}
				if len(s.clientMutations.snapshot().SteeringOrder) != before {
					t.Fatal("retry duplicated notification")
				}
				select {
				case ev := <-s.Events():
					if ev.Kind == events.EventNotesUpdated {
						t.Fatal("replay emitted update")
					}
				default:
				}
			})
		}
	}
}

func TestHumanNoteFailedRewritePreservesEarlierRecordedResult(t *testing.T) {
	s := newDurableHumanNoteSession(t)
	if _, err := s.SetHumanNote("first", "A"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetHumanNote("latest", "X"); err != nil {
		t.Fatal(err)
	}
	s.clientMutations.faults.BeforeEffectSnapshotRename = func() error { return errors.New("failed rewrite") }
	if _, err := s.SetHumanNote("rewrite", "A"); err == nil {
		t.Fatal("missing fault")
	}
	reloadHumanNoteStore(t, s)
	replay, err := s.SetHumanNote("first", "A")
	if err != nil {
		t.Fatal(err)
	}
	if replay.Note != "A" {
		t.Fatalf("recorded response = %+v", replay)
	}
	if note, _ := s.notesSnapshot(); note != "X" {
		t.Fatalf("failed rewrite/retry changed note to %q", note)
	}
	if len(s.clientMutations.snapshot().SteeringOrder) != 2 {
		t.Fatal("failed rewrite or replay queued notification")
	}
}
