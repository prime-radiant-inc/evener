package hubcore

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

func TestFleetProbeStateLifecycle(t *testing.T) {
	entry := rendezvous.Entry{
		PID:       4321,
		SessionID: "session-1",
		StartedAt: time.Now(),
	}
	fresh := liveEntryFromProbe(entry, ProbeResult{
		SessionID:           entry.SessionID,
		Status:              "active",
		ActiveTurnID:        "turn-running",
		ActiveTurnStartedAt: 1_700_000_000_000,
		Activity:            &appwire.ThreadActivity{},
		Failure:             &appwire.ThreadFailure{},
		OK:                  true,
	})
	if fresh.ProbeState != "fresh" {
		t.Fatalf("fresh probe state = %q, want fresh", fresh.ProbeState)
	}

	retained := classifyMissedProbe(fresh, true, entry, ProcessOwnsEntry, nil)
	if retained.kind != missReused {
		t.Fatalf("retained miss kind = %v, want %v", retained.kind, missReused)
	}
	if retained.entry.ProbeState != "retained" {
		t.Fatalf("retained probe state = %q, want retained", retained.entry.ProbeState)
	}
	assertFleetVolatileFactsAbsent(t, retained.entry)
	if retained.entry.Failure == nil {
		t.Fatal("retained probe dropped durable failure")
	}
	recovered := liveEntryFromProbe(entry, ProbeResult{
		SessionID: entry.SessionID,
		Status:    "idle",
		OK:        true,
	})
	if recovered.ProbeState != appwire.ThreadProbeStateFresh {
		t.Fatalf("recovered probe state = %q, want fresh", recovered.ProbeState)
	}
	assertFleetVolatileFactsAbsent(t, recovered)

	crashed := classifyMissedProbe(fresh, true, entry, ProcessNotOwner, nil)
	if crashed.kind != missCrashed {
		t.Fatalf("crashed miss kind = %v, want %v", crashed.kind, missCrashed)
	}
	if crashed.entry.ProbeState != "retained" {
		t.Fatalf("crashed probe state = %q, want retained", crashed.entry.ProbeState)
	}
	assertFleetVolatileFactsAbsent(t, crashed.entry)
	if crashed.entry.Failure == nil {
		t.Fatal("crashed probe dropped durable failure")
	}
}

func TestFleetDiscoveryFailureRetainsRowsWithoutVolatileFacts(t *testing.T) {
	dir := t.TempDir()
	entry := rendezvous.Entry{PID: 4321, SessionID: "session-1", StartedAt: time.Now()}
	writeRendezvous(t, dir, entry)
	prober := &runningSubagentProber{result: ProbeResult{
		SessionID:           entry.SessionID,
		Status:              appwire.ThreadStatusActive,
		ActiveTurnID:        "turn-running",
		ActiveTurnStartedAt: 1_700_000_000_000,
		Activity:            &appwire.ThreadActivity{},
		Failure:             &appwire.ThreadFailure{},
		OK:                  true,
	}}
	roster := NewRoster(dir, prober)
	roster.Refresh()
	if got := roster.List(); len(got) != 1 || got[0].ProbeState != appwire.ThreadProbeStateFresh {
		t.Fatalf("initial roster = %+v, want one fresh row", got)
	}

	badClaim := filepath.Join(dir, "9999.json")
	if err := os.WriteFile(badClaim, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	roster.Refresh()
	if roster.OwnershipError() == nil {
		t.Fatal("malformed claim did not make discovery incomplete")
	}
	retained := roster.List()
	if len(retained) != 1 || retained[0].ProbeState != appwire.ThreadProbeStateRetained {
		t.Fatalf("roster after incomplete discovery = %+v, want one retained row", retained)
	}
	assertFleetVolatileFactsAbsent(t, retained[0])
	if retained[0].Failure == nil {
		t.Fatal("incomplete discovery dropped durable failure")
	}
	if _, ok := roster.Find(entry.SessionID); !ok {
		t.Fatal("incomplete discovery dropped the retained row's route")
	}

	if err := os.Remove(badClaim); err != nil {
		t.Fatal(err)
	}
	roster.Refresh()
	recovered := roster.List()
	if roster.OwnershipError() != nil || len(recovered) != 1 || recovered[0].ProbeState != appwire.ThreadProbeStateFresh {
		t.Fatalf("roster after recovery = %+v, error = %v, want one fresh row", recovered, roster.OwnershipError())
	}
	if recovered[0].ActiveTurnID != "turn-running" || recovered[0].Activity == nil {
		t.Fatalf("recovered row did not restore fresh volatile facts: %+v", recovered[0])
	}
}

func assertFleetVolatileFactsAbsent(t *testing.T, entry LiveEntry) {
	t.Helper()
	if entry.ActiveTurnID != "" || entry.ActiveTurnStartedAt != 0 {
		t.Fatalf("retained row kept active turn %q at %d", entry.ActiveTurnID, entry.ActiveTurnStartedAt)
	}
	if entry.Activity != nil {
		t.Fatal("retained row kept activity sample")
	}
}
