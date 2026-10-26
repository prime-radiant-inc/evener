package hub

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestForceStopDescendantsScanWithoutAJournalFencesNothing(t *testing.T) {
	locks := hubcore.NewResumeLocks()
	for name, stateDir := range map[string]string{"no state dir": "", "no delegate journal": t.TempDir()} {
		t.Run(name, func(t *testing.T) {
			d := &forceStopDescendants{locks: locks, stateDir: stateDir, sessionID: "01ABCDEFGHJKMNPQRSTVWXYZ01", fenced: map[string]bool{}}
			if err := d.scan(context.Background()); err != nil {
				t.Fatalf("scan: %v", err)
			}
			if len(d.fences) != 0 {
				t.Fatalf("fences = %d, want none", len(d.fences))
			}
			d.reject()
			d.finish()
		})
	}
}

func TestForceStopDescendantsScanRefusesAnUnreadableJournalAsUnavailable(t *testing.T) {
	d := &forceStopDescendants{locks: hubcore.NewResumeLocks(), stateDir: t.TempDir(), sessionID: "not a session id", fenced: map[string]bool{}}
	err := d.scan(context.Background())
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("scan error = %v, want an unavailable wire error", err)
	}
	if len(d.fences) != 0 {
		t.Fatalf("a failed scan installed %d fences", len(d.fences))
	}
}

func TestDrainEntryResumeWithNoActiveResumeIsANoOp(t *testing.T) {
	cfg := hubcore.WebConfig{ResumeLocks: hubcore.NewResumeLocks()}
	canceled, aliases, release, err := drainEntryResume(context.Background(), cfg, appwire.Ref{SourceID: "local", ThreadID: "01ABCDEFGHJKMNPQRSTVWXYZ01"})
	if err != nil || canceled || aliases != nil {
		t.Fatalf("drain = canceled %v aliases %v err %v, want none", canceled, aliases, err)
	}
	release()
}
