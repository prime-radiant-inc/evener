package goal

import (
	"testing"
	"time"
)

// TestRestoreSnapshotKeepsWaitIDsUnique pins the ABA guard: restoring waits
// (and a pendingWake backlog) seeds the wait-id counter past every restored
// id, so fresh registrations never reuse one.
func TestRestoreSnapshotKeepsWaitIDsUnique(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	s := NewStore()
	s.Set("obj", now)
	w1, ok := s.RegisterWait(WaitKind{Kind: WaitUntilTime, Timeout: time.Minute}, now)
	if !ok {
		t.Fatal("precondition: registration should succeed")
	}
	if _, ok := s.ClaimFire(w1.Lease.WaitID, "fire", now); !ok {
		t.Fatal("precondition: claim should consume the lease")
	}
	persisted, ok := s.PersistSnapshot()
	if !ok {
		t.Fatal("precondition: persist should succeed")
	}

	restored := NewStore()
	restored.RestoreSnapshot(persisted)
	w2, ok := restored.RegisterWait(WaitKind{Kind: WaitUntilTime, Target: "fresh", Timeout: time.Minute}, now)
	if !ok {
		t.Fatal("fresh registration after restore should succeed")
	}
	if w2.Lease.WaitID == w1.Lease.WaitID {
		t.Fatalf("fresh wait_id %q reuses restored id (ABA)", w2.Lease.WaitID)
	}
}

// TestRestoreSnapshotWaitingWithoutWaitsResolvesActive pins the no-strand
// rule: a restored "waiting" status with no waits and no backlog resolves to
// active (never parked on nothing).
func TestRestoreSnapshotWaitingWithoutWaitsResolvesActive(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	restored := NewStore()
	restored.RestoreSnapshot(PersistedGoal{Objective: "obj", Status: StatusWaiting, CreatedAt: now, UpdatedAt: now})
	snap, ok := restored.Snapshot()
	if !ok || snap.Status != StatusActive {
		t.Fatalf("restored waiting-without-waits = %+v ok=%v, want active", snap, ok)
	}
}

// TestMigrateV1SeedMath pins the bound-preserving seed table directly:
// remaining = K − streak over the tier's K, seeded K−remaining entries.
func TestMigrateV1SeedMath(t *testing.T) {
	created := time.Unix(1_700_000_000, 0).UTC()
	restoreTime := created.Add(time.Hour)
	for _, tc := range []struct {
		name         string
		streak       int
		madeProgress bool
		wantSeeded   int
		wantTier     int
		wantStage    GraduationStage
		wantRep      int
	}{
		{"streak-5 never-advanced", 5, false, 5, NeverProgressedLimit, StageNone, 5},
		{"streak-0 never-advanced", 0, false, 0, NeverProgressedLimit, StageNone, 0},
		{"streak-2 progressed", 2, true, 2, NoProgressLimit, StageNone, 2},
		{"streak-3 progressed at bound", 3, true, 3, NoProgressLimit, StageNudged, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := MigrateV1ToPersisted("obj", "active", "", 4, tc.streak, tc.madeProgress, created, created, restoreTime)
			if len(p.LedgerSummary.Entries) != tc.wantSeeded {
				t.Fatalf("seeded = %d, want %d", len(p.LedgerSummary.Entries), tc.wantSeeded)
			}
			if p.LedgerSummary.Tier != tc.wantTier || p.LedgerSummary.Stage != tc.wantStage || p.LedgerSummary.Repetition != tc.wantRep {
				t.Fatalf("summary = %+v, want tier=%d stage=%q rep=%d", p.LedgerSummary, tc.wantTier, tc.wantStage, tc.wantRep)
			}
			if p.Budgets.UsedContinuations != 4 || p.Budgets.MaxContinuations != DefaultMaxContinuations {
				t.Fatalf("budgets = %+v, want used=old-iterations max=default", p.Budgets)
			}
		})
	}
}
