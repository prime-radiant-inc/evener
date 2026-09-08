package agent

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/agent/schema"
)

// TestGoalMigrateV1_Streak5NudgesNotBlocks pins the spec §7 migration table
// (bound-preserving, one-way): a never-advanced v1 snapshot with streak 5
// (K=6 tier) seeds K−remaining synthetic "migrated"-fingerprint entries so
// exactly 1 more non-advancing turn reaches K — the nudge allowance: slice-3
// graduation nudges there (stage 1), never blocks outright. Slice 1 resolves
// the same bound through the interim breaker (which only knows block), so
// this test pins the TIMING (exactly 1 more turn to reach K, not 0, not 2);
// the nudge-vs-block split at K arrives with graduation.
func TestGoalMigrateV1_Streak5NudgesNotBlocks(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	restoreTime := created.Add(30 * time.Minute)
	v1 := &schema.GoalSnapshot{
		Objective:        "migrate me",
		Status:           "active",
		Iterations:       7,
		NoProgressStreak: 5,
		MadeProgressOnce: false,
		CreatedAt:        created,
		UpdatedAt:        created.Add(20 * time.Minute),
	}
	if v1.Budgets != nil {
		t.Fatal("precondition: v1 snapshots carry no budgets block")
	}

	persisted := goalRestoreToStore(v1, restoreTime)
	if len(persisted.LedgerSummary.Entries) != goal.NeverProgressedLimit-1 {
		t.Fatalf("seeded entries = %d, want K−remaining = %d (remaining 1 till block)", len(persisted.LedgerSummary.Entries), goal.NeverProgressedLimit-1)
	}
	for _, e := range persisted.LedgerSummary.Entries {
		if e.Fingerprint != goal.MigratedFingerprint {
			t.Fatalf("seeded fingerprint = %q, want the dedicated %q marker", e.Fingerprint, goal.MigratedFingerprint)
		}
	}
	if persisted.LedgerSummary.Repetition != goal.NeverProgressedLimit-1 {
		t.Fatalf("seeded repetition = %d, want %d", persisted.LedgerSummary.Repetition, goal.NeverProgressedLimit-1)
	}
	if persisted.LedgerSummary.Tier != goal.NeverProgressedLimit {
		t.Fatalf("seeded tier = %d, want K=%d (never-advanced regime)", persisted.LedgerSummary.Tier, goal.NeverProgressedLimit)
	}
	if persisted.LedgerSummary.Stage != goal.StageNone {
		t.Fatalf("seeded stage = %q, want none (1 turn of allowance remains; the next bound-hit graduates)", persisted.LedgerSummary.Stage)
	}

	// Budget backfill per the §7 rule.
	if persisted.Budgets.MaxContinuations != goal.DefaultMaxContinuations {
		t.Fatalf("MaxContinuations = %d, want default %d", persisted.Budgets.MaxContinuations, goal.DefaultMaxContinuations)
	}
	if persisted.Budgets.UsedContinuations != v1.Iterations {
		t.Fatalf("UsedContinuations = %d, want old Iterations %d", persisted.Budgets.UsedContinuations, v1.Iterations)
	}
	if want := created.Add(goal.DefaultGoalDeadline); !persisted.Budgets.Deadline.Equal(want) {
		t.Fatalf("Deadline = %v, want max(CreatedAt+4h, restore+1h) = %v", persisted.Budgets.Deadline, want)
	}
	if persisted.Budgets.ParkedTotal != 0 || persisted.Budgets.MaxParkedTotal != goal.DefaultMaxParkedTotal {
		t.Fatalf("parked totals = (%v, %v), want (0, default %v)", persisted.Budgets.ParkedTotal, persisted.Budgets.MaxParkedTotal, goal.DefaultMaxParkedTotal)
	}
	if persisted.AutoReparks != 0 || len(persisted.PendingWake) != 0 || persisted.DeadlineFinalDelivered || persisted.TerminalPending {
		t.Fatalf("migration must zero autoReparks/pendingWake/one-shot/latch, got %+v", persisted)
	}

	// Remaining-till-block is preserved exactly: the 1 remaining turn runs,
	// and the seeded run positions the ledger one identical turn short of K.
	// Slice 2 retires the interim verdict: the stall graduates (nudge first,
	// block after). Per the disclosed migration residual, a continuing
	// identical-but-distinct real turn resets the run to 1 (the seeded
	// "migrated" fingerprint never collides with a real action), granting at
	// most K−1 extra turns once — K−1 further identical turns then nudge,
	// and the next blocks. The seed table's shape (5 entries, tier 6, stage
	// none) is pinned above; here the ledger behavior after it.
	fresh := goal.NewStore()
	fresh.RestoreSnapshot(persisted)
	stall := goal.TurnOutcome{ActionFingerprint: "grep pattern=x", ObservationClass: "ok", ObservationHash: "same", StateDigest: "steady"}
	for i := range goal.NeverProgressedLimit - 1 {
		snap, active := fresh.RecordContinuation(stall, false, restoreTime.Add(time.Duration(i+1)*time.Minute))
		if !active || snap.Status != goal.StatusActive {
			t.Fatalf("residual turn %d = (%v, %v), want active (disclosed at-most-K−1 residual)", i+1, snap.Status, active)
		}
	}
	snap, active := fresh.RecordContinuation(stall, false, restoreTime.Add(time.Duration(goal.NeverProgressedLimit)*time.Minute))
	if !active || snap.Status != goal.StatusActive {
		t.Fatalf("K-th residual turn = (%v, %v), want the nudge (still active)", snap.Status, active)
	}
	full, _ := fresh.GoalSnapshot()
	if full.LedgerSummary.Stage != goal.StageNudged {
		t.Fatalf("stage = %q, want nudged after the residual K trip", full.LedgerSummary.Stage)
	}
	if snap, active := fresh.RecordContinuation(stall, false, restoreTime.Add(time.Duration(goal.NeverProgressedLimit+1)*time.Minute)); active || snap.Status != goal.StatusBlocked {
		t.Fatalf("post-nudge turn = (%v, %v), want blocked/no-progress", snap.Status, active)
	}
}

// TestGoalMigrateV1_OverBoundSeedsAtBound pins the clamp: a progressed-regime
// (K=3) v1 snapshot with streak 5 already spent its allowance (remaining
// 3−5 < 0 → 0), so migration seeds the full K window, marks the stage
// already-nudged (never re-nudge after restart), and the next non-advancing
// turn enforces immediately.
func TestGoalMigrateV1_OverBoundSeedsAtBound(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	restoreTime := created.Add(30 * time.Minute)
	v1 := &schema.GoalSnapshot{
		Objective:        "over bound",
		Status:           "active",
		Iterations:       9,
		NoProgressStreak: 5,
		MadeProgressOnce: true,
		CreatedAt:        created,
		UpdatedAt:        created.Add(20 * time.Minute),
	}
	persisted := goalRestoreToStore(v1, restoreTime)
	if len(persisted.LedgerSummary.Entries) != goal.NoProgressLimit {
		t.Fatalf("seeded entries = %d, want full K=%d window (no allowance remains)", len(persisted.LedgerSummary.Entries), goal.NoProgressLimit)
	}
	if persisted.LedgerSummary.Stage != goal.StageNudged {
		t.Fatalf("seeded stage = %q, want nudged (at bound: never re-nudge, next breach enforces)", persisted.LedgerSummary.Stage)
	}
	fresh := goal.NewStore()
	fresh.RestoreSnapshot(persisted)
	// The seeded window already sits AT K with stage nudged (never re-nudge
	// after restart). Per the disclosed residual, a continuing real turn
	// resets the run once — but the stage stays nudged, so K−1 further
	// identical turns trip the bound and block immediately (never a second
	// nudge).
	stall := goal.TurnOutcome{ActionFingerprint: "grep pattern=x", ObservationClass: "ok", ObservationHash: "same", StateDigest: "steady"}
	for i := range goal.NoProgressLimit - 1 {
		snap, active := fresh.RecordContinuation(stall, false, restoreTime.Add(time.Duration(i+1)*time.Minute))
		if !active || snap.Status != goal.StatusActive {
			t.Fatalf("residual turn %d = (%v, %v), want active (stage already nudged, run rebuilding)", i+1, snap.Status, active)
		}
	}
	if snap, active := fresh.RecordContinuation(stall, false, restoreTime.Add(time.Duration(goal.NoProgressLimit)*time.Minute)); active || snap.Status != goal.StatusBlocked {
		t.Fatalf("over-bound migration must enforce at the rebuilt K with no second nudge, got (%v, %v)", snap.Status, active)
	}
}

// TestGoalMigrateV1_DeadlineFloorNeverInstantExpiry pins the documented
// deadline anchor: an old goal whose CreatedAt+4h already passed gets
// restore_time+1h — never an instant-expiry, never a fresh 4h laundering the
// old spend.
func TestGoalMigrateV1_DeadlineFloorNeverInstantExpiry(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	restoreTime := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	v1 := &schema.GoalSnapshot{
		Objective:  "old goal",
		Status:     "active",
		Iterations: 2,
		CreatedAt:  created,
		UpdatedAt:  created.Add(time.Hour),
	}
	persisted := goalRestoreToStore(v1, restoreTime)
	if want := restoreTime.Add(time.Hour); !persisted.Budgets.Deadline.Equal(want) {
		t.Fatalf("Deadline = %v, want restore_time+1h floor %v", persisted.Budgets.Deadline, want)
	}
	if !persisted.Budgets.Deadline.After(restoreTime) {
		t.Fatalf("Deadline = %v, must be after restore time (never instant-expiry)", persisted.Budgets.Deadline)
	}
	// Never-advanced v1 snapshot seeds the K=6 tier.
	if persisted.LedgerSummary.Tier != goal.NeverProgressedLimit {
		t.Fatalf("tier = %d, want K=6 for a never-advanced goal", persisted.LedgerSummary.Tier)
	}
	if len(persisted.LedgerSummary.Entries) != 0 {
		t.Fatalf("streak-0 migration must seed no entries, got %+v", persisted.LedgerSummary.Entries)
	}
}

// TestGoalMigrateV1_TerminalsBlockedDormantCompleteDropped pins the terminal
// half of the migration: blocked restores dormant (no waits/backlog), complete
// is dropped by the restore dispatch (asserted in TestGoalRestoreOnlyActive;
// here: the migrated image itself carries no live state to arm).
func TestGoalMigrateV1_TerminalsBlockedDormantCompleteDropped(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	v1 := &schema.GoalSnapshot{
		Objective:  "finished work",
		Status:     "blocked",
		StopReason: "no progress",
		Iterations: 5,
		CreatedAt:  now.Add(-time.Hour),
		UpdatedAt:  now,
	}
	persisted := goalRestoreToStore(v1, now)
	if len(persisted.Waits) != 0 || len(persisted.PendingWake) != 0 {
		t.Fatalf("migrated terminal must carry no waits/backlog, got %+v", persisted)
	}
	if persisted.Budgets.UsedContinuations != v1.Iterations {
		t.Fatalf("UsedContinuations = %d, want old Iterations %d", persisted.Budgets.UsedContinuations, v1.Iterations)
	}
}
