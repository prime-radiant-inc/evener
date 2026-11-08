package hubcore

import (
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

func assertFleetVolatileFactsAbsent(t *testing.T, entry LiveEntry) {
	t.Helper()
	if entry.ActiveTurnID != "" || entry.ActiveTurnStartedAt != 0 {
		t.Fatalf("retained row kept active turn %q at %d", entry.ActiveTurnID, entry.ActiveTurnStartedAt)
	}
	if entry.Activity != nil {
		t.Fatal("retained row kept activity sample")
	}
}
