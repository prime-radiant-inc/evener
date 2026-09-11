package agent

import (
	"errors"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

func TestURLRemoveReservationCrashUnknownTarget(t *testing.T) {
	s := newDurableHumanNoteSession(t)
	fault := errors.New("reservation interrupted")
	s.clientMutations.faults.AfterReservation = func() error { return fault }
	removed, err := s.RemoveSessionURL("remove-unknown", "never-existed")
	var wire appwire.WireError
	if removed || (!errors.Is(err, fault) && !(errors.As(err, &wire) && wire.Code == appwire.CodeInvalidParams)) {
		t.Fatalf("initial unknown removal = %v, %v; want rejection or injected interruption", removed, err)
	}
	reloadHumanNoteStore(t, s)
	for range 2 {
		removed, err = s.RemoveSessionURL("remove-unknown", "never-existed")
		if removed || !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
			t.Fatalf("retried unknown removal = %v, %v; want false and InvalidParams", removed, err)
		}
		reloadHumanNoteStore(t, s)
	}
}

func TestURLRemoveExistingTargetRecovery(t *testing.T) {
	for _, boundary := range []string{"none", "reservation", "receipt"} {
		t.Run(boundary, func(t *testing.T) {
			s := newDurableHumanNoteSession(t)
			entry, err := s.addSessionURL("https://example.test/target", "target")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.persistNotesMeta(); err != nil {
				t.Fatal(err)
			}
			fault := errors.New("removal interrupted")
			switch boundary {
			case "reservation":
				s.clientMutations.faults.AfterReservation = func() error { return fault }
			case "receipt":
				s.clientMutations.faults.BeforeEffectSnapshotRename = func() error { return fault }
			}
			removed, err := s.RemoveSessionURL("remove-existing", entry.ID)
			if boundary == "none" {
				if !removed || err != nil {
					t.Fatalf("initial removal = %v, %v", removed, err)
				}
			} else if removed != (boundary == "receipt") || err == nil {
				t.Fatalf("interrupted removal = %v, %v; want removed=%v and an error", removed, err, boundary == "receipt")
			}
			wantURLs := 0
			if boundary == "reservation" {
				wantURLs = 1
			}
			if got := len(s.snapshotSessionURLsLocked()); got != wantURLs {
				t.Fatalf("URLs at %s = %d, want %d", boundary, got, wantURLs)
			}
			disk, err := schema.LoadSessionMeta(s.stateDir, s.ID())
			if err != nil {
				t.Fatal(err)
			}
			if len(disk.SessionURLs) != wantURLs || (wantURLs == 1 && disk.SessionURLs[0].ID != entry.ID) {
				t.Fatalf("durable URLs at %s = %+v, want %d entries", boundary, disk.SessionURLs, wantURLs)
			}
			s.notesUpdateMu.Lock()
			s.restoreSessionURLsLocked(disk.SessionURLs)
			s.notesUpdateMu.Unlock()
			reloadHumanNoteStore(t, s)
			for range 2 {
				if removed, err := s.RemoveSessionURL("remove-existing", entry.ID); !removed || err != nil {
					t.Fatalf("recovered removal = %v, %v; want success", removed, err)
				}
				if urls := s.snapshotSessionURLsLocked(); len(urls) != 0 {
					t.Fatalf("removed entry returned: %+v", urls)
				}
				reloadHumanNoteStore(t, s)
			}
		})
	}
}
