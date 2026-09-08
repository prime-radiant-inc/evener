package goal

import (
	"testing"
	"time"
)

// clock returns a fixed time for deterministic tests.
func clock() time.Time { return time.Unix(0, 0).UTC() }

func TestStoreSetGetClear(t *testing.T) {
	s := NewStore()
	if _, ok := s.Snapshot(); ok {
		t.Fatal("empty store should report no goal")
	}
	s.Set("make tests pass", clock())
	snap, ok := s.Snapshot()
	if !ok || snap.Status != StatusActive || snap.Objective != "make tests pass" {
		t.Fatalf("after Set: %+v ok=%v", snap, ok)
	}
	s.Clear()
	if _, ok := s.Snapshot(); ok {
		t.Fatal("after Clear should report no goal")
	}
}

// TestRecordContinuationNoProgressGrace: leading read-only turns accrue toward the
// larger NeverProgressedLimit (not blocked early); a progressed turn resets the
// streak and flips to the tighter NoProgressLimit, after which NoProgressLimit
// consecutive no-progress turns block.
func TestRecordContinuationNoProgressGrace(t *testing.T) {
	s := NewStore()
	s.Set("obj", clock())
	for i := range NeverProgressedLimit - 1 {
		s.RecordContinuation(false /*progressed*/, clock())
		snap, _ := s.Snapshot()
		if snap.Status != StatusActive {
			t.Fatalf("leading turn %d should stay active, got %v", i, snap.Status)
		}
	}
	// G1: progress turn must (a) remain active, (b) reset NoProgressStreak to 0,
	// and (c) increment Iterations.
	snap, active := s.RecordContinuation(true, clock()) // progress: reset + flip to NoProgressLimit
	if !active {
		t.Fatal("progress turn must remain active")
	}
	if snap.Status != StatusActive {
		t.Fatalf("after progress turn: want StatusActive, got %v", snap.Status)
	}
	if snap.NoProgressStreak != 0 {
		t.Fatalf("after progress turn: want NoProgressStreak=0, got %d", snap.NoProgressStreak)
	}
	// G5: NeverProgressedLimit-1 leading turns + 1 progress turn.
	wantIters := NeverProgressedLimit
	if snap.Iterations != wantIters {
		t.Fatalf("after progress turn: want Iterations=%d, got %d", wantIters, snap.Iterations)
	}
	// G4: pin the exact turn at which blocking occurs — must be precisely
	// NoProgressLimit consecutive no-progress turns after the reset.
	for i := range NoProgressLimit {
		snap, active := s.RecordContinuation(false, clock())
		wantActive := i < NoProgressLimit-1
		if active != wantActive {
			t.Fatalf("no-progress turn %d: want active=%v, got %v", i, wantActive, active)
		}
		if wantActive && snap.Status != StatusActive {
			t.Fatalf("no-progress turn %d: want StatusActive, got %v", i, snap.Status)
		}
		if !wantActive && (snap.Status != StatusBlocked || snap.StopReason != "no progress") {
			t.Fatalf("no-progress turn %d: want blocked/no-progress, got %v/%q", i, snap.Status, snap.StopReason)
		}
	}
}

// TestNeverProgressedBlocks pins the fix for the breaker hole: a goal that never
// makes a mutating tool call must still stop (at NeverProgressedLimit), not run
// forever (the /par Critical B1).
func TestNeverProgressedBlocks(t *testing.T) {
	s := NewStore()
	s.Set("summarize the architecture", clock())
	for i := range NeverProgressedLimit - 1 {
		if _, active := s.RecordContinuation(false, clock()); !active {
			t.Fatalf("turn %d: never-progressed goal blocked too early", i)
		}
	}
	if _, active := s.RecordContinuation(false, clock()); active {
		t.Fatal("never-progressed goal must block at NeverProgressedLimit")
	}
	snap, _ := s.Snapshot()
	if snap.Status != StatusBlocked || snap.StopReason != "no progress" {
		t.Fatalf("want blocked/no-progress, got %v/%q", snap.Status, snap.StopReason)
	}
	// G5: Iterations must equal NeverProgressedLimit after exactly that many turns.
	if snap.Iterations != NeverProgressedLimit {
		t.Fatalf("want Iterations=%d, got %d", NeverProgressedLimit, snap.Iterations)
	}
}

func TestSetTerminal(t *testing.T) {
	s := NewStore()
	s.Set("obj", clock())
	if !s.SetTerminal(StatusComplete, "", clock()) {
		t.Fatal("SetTerminal on active should succeed")
	}
	snap, _ := s.Snapshot()
	if snap.Status != StatusComplete {
		t.Fatalf("want complete, got %v", snap.Status)
	}
	// Second call on a non-active goal is a no-op returning false.
	if s.SetTerminal(StatusBlocked, "x", clock()) {
		t.Fatal("SetTerminal on non-active should be no-op")
	}
	// G3: verify status and stop reason are unchanged after the no-op call.
	snap, _ = s.Snapshot()
	if snap.Status != StatusComplete {
		t.Fatalf("after no-op SetTerminal: want status=complete, got %v", snap.Status)
	}
	if snap.StopReason != "" {
		t.Fatalf("after no-op SetTerminal: want StopReason empty, got %q", snap.StopReason)
	}
}

// TestPersistSnapshotRestoreRoundTrip verifies that PersistSnapshot captures all
// fields and Restore reconstitutes them faithfully, including madeProgressOnce.
// The key behavioral invariant: a restored goal with prior progress must use
// NoProgressLimit (not the larger NeverProgressedLimit).
// G6: covers the previously untested PersistSnapshot/Restore path.
func TestPersistSnapshotRestoreRoundTrip(t *testing.T) {
	s := NewStore()
	s.Set("obj", clock())
	// One progress turn so madeProgressOnce=true, NoProgressStreak=0, Iterations=1.
	s.RecordContinuation(true, clock())

	persisted, ok := s.PersistSnapshot()
	if !ok {
		t.Fatal("PersistSnapshot: expected ok=true")
	}
	if persisted.Objective != "obj" || persisted.Status != StatusActive || persisted.StopReason != "" {
		t.Fatalf("PersistSnapshot: unexpected values: obj=%q status=%q stopReason=%q", persisted.Objective, persisted.Status, persisted.StopReason)
	}
	if persisted.Iterations != 1 || persisted.NoProgressStreak != 0 || !persisted.MadeProgressOnce {
		t.Fatalf("PersistSnapshot: unexpected counters: iters=%d streak=%d madeProgressOnce=%v", persisted.Iterations, persisted.NoProgressStreak, persisted.MadeProgressOnce)
	}

	// Restore into a fresh store and verify the limit regime is NoProgressLimit
	// (not NeverProgressedLimit), proving madeProgressOnce survived the round-trip.
	s2 := NewStore()
	s2.RestoreSnapshot(persisted)

	for i := range NoProgressLimit - 1 {
		if _, active := s2.RecordContinuation(false, clock()); !active {
			t.Fatalf("restored goal: no-progress turn %d blocked too early (want NoProgressLimit=%d)", i, NoProgressLimit)
		}
	}
	if _, active := s2.RecordContinuation(false, clock()); active {
		t.Fatal("restored goal: must block at NoProgressLimit after prior progress (not NeverProgressedLimit)")
	}
	snap, _ := s2.Snapshot()
	if snap.Status != StatusBlocked || snap.StopReason != "no progress" {
		t.Fatalf("restored goal: want blocked/no-progress, got %v/%q", snap.Status, snap.StopReason)
	}
	if snap.Objective != "obj" {
		t.Fatalf("restored goal: objective changed to %q", snap.Objective)
	}
}

func TestBudgetsDefaults(t *testing.T) {
	now := clock()
	s := NewStore()
	s.Set("ship it", now)
	gsnap, ok := s.GoalSnapshot()
	if !ok {
		t.Fatal("GoalSnapshot: expected ok=true")
	}
	b := gsnap.Budgets
	if b.MaxContinuations != DefaultMaxContinuations || DefaultMaxContinuations != 200 {
		t.Fatalf("MaxContinuations = %d, want default 200", b.MaxContinuations)
	}
	if b.UsedContinuations != 0 || b.ParkedTotal != 0 {
		t.Fatalf("fresh budgets must be unused: %+v", b)
	}
	if !b.Deadline.Equal(now.Add(DefaultGoalDeadline)) || DefaultGoalDeadline != 4*time.Hour {
		t.Fatalf("Deadline = %v, want now+4h", b.Deadline)
	}
	if b.MaxParkedTotal != DefaultMaxParkedTotal || DefaultMaxParkedTotal != 24*time.Hour {
		t.Fatalf("MaxParkedTotal = %v, want 24h", b.MaxParkedTotal)
	}
	if MaxContinuationsCap != 1000 || GoalDeadlineCap != 24*time.Hour || MaxParkedTotalCap != 24*time.Hour {
		t.Fatalf("caps = %d/%v/%v, want 1000/24h/24h", MaxContinuationsCap, GoalDeadlineCap, MaxParkedTotalCap)
	}
}

func TestBudgetsAccrueInFold(t *testing.T) {
	s := NewStore()
	s.Set("obj", clock())
	snap, active := s.RecordContinuation(true, clock())
	if !active {
		t.Fatal("progress turn must remain active")
	}
	gsnap, ok := s.GoalSnapshot()
	if !ok {
		t.Fatal("GoalSnapshot: expected ok=true")
	}
	if gsnap.Budgets.UsedContinuations != 1 || snap.Iterations != 1 {
		t.Fatalf("fold must accrue budgets with iterations: budgets=%+v iterations=%d", gsnap.Budgets, snap.Iterations)
	}
}

func TestRecordContinuationSkipsFoldWhileWaiting(t *testing.T) {
	s := NewStore()
	s.Set("obj", clock())
	if _, ok := s.RegisterWait(WaitKind{Kind: WaitUntilTime, Timeout: time.Minute}, clock()); !ok {
		t.Fatal("register should succeed")
	}
	snap, active := s.RecordContinuation(false, clock())
	if active {
		t.Fatal("RecordContinuation on a waiting goal must not drive")
	}
	if snap.Status != StatusWaiting || snap.Iterations != 0 {
		t.Fatalf("waiting goal must skip every fold: %+v", snap)
	}
	gsnap, _ := s.GoalSnapshot()
	if gsnap.Budgets.UsedContinuations != 0 {
		t.Fatalf("parked goal burns zero budget: %+v", gsnap.Budgets)
	}
}

func TestSetTerminalFromWaitingClearsWaits(t *testing.T) {
	s := NewStore()
	s.Set("obj", clock())
	if _, ok := s.RegisterWait(WaitKind{Kind: WaitUntilTime, Timeout: time.Minute}, clock()); !ok {
		t.Fatal("register should succeed")
	}
	if !s.SetTerminal(StatusBlocked, "done waiting", clock()) {
		t.Fatal("SetTerminal from waiting should succeed")
	}
	gsnap, _ := s.GoalSnapshot()
	if gsnap.Status != StatusBlocked || gsnap.StopReason != "done waiting" {
		t.Fatalf("terminal = %v/%q", gsnap.Status, gsnap.StopReason)
	}
	if len(gsnap.Waits) != 0 {
		t.Fatalf("terminal transition must clear waits: %+v", gsnap.Waits)
	}
}

func TestRetargetKeepsBudgetsClearsWaits(t *testing.T) {
	s := NewStore()
	s.Set("old", clock())
	s.RecordContinuation(true, clock())
	if _, ok := s.RegisterWait(WaitKind{Kind: WaitUntilTime, Timeout: time.Minute}, clock()); !ok {
		t.Fatal("register should succeed")
	}
	s.Set("new", clock())
	gsnap, ok := s.GoalSnapshot()
	if !ok || gsnap.Status != StatusActive || gsnap.Objective != "new" {
		t.Fatalf("retarget = %+v ok=%v", gsnap, ok)
	}
	if len(gsnap.Waits) != 0 {
		t.Fatalf("retarget must clear waits: %+v", gsnap.Waits)
	}
	if gsnap.Budgets.UsedContinuations != 1 || gsnap.Budgets.MaxContinuations != DefaultMaxContinuations {
		t.Fatalf("retarget must keep budgets: %+v", gsnap.Budgets)
	}
}

func TestDecideGoalStepSlice1(t *testing.T) {
	now := clock()
	full := Budgets{MaxContinuations: 200, Deadline: now.Add(4 * time.Hour), MaxParkedTotal: 24 * time.Hour}
	mkWait := func() Wait {
		return Wait{Lease: Lease{WaitID: "wait_1", Kind: WaitUntilTime, Deadline: now.Add(time.Minute), RegisteredAt: now, IdempotencyKey: "k"}}
	}
	cases := []struct {
		name        string
		snap        GoalSnapshot
		pending     []PendingWake
		markers     AdvancementMarkers
		wantStep    GoalStep
		wantVerdict string
	}{
		{"undelivered pending wake drives", GoalSnapshot{Budgets: full}, []PendingWake{{WaitID: "wait_1", Trigger: "t", FiredAt: now}}, AdvancementMarkers{}, StepDrive, ""},
		{"snapshot backlog drives", GoalSnapshot{Budgets: full, PendingWake: []PendingWake{{WaitID: "wait_1"}}}, nil, AdvancementMarkers{}, StepDrive, ""},
		{"continuations spent blocks", GoalSnapshot{Budgets: Budgets{MaxContinuations: 200, UsedContinuations: 200, Deadline: now.Add(time.Hour), MaxParkedTotal: 24 * time.Hour}}, nil, AdvancementMarkers{}, StepBlock, VerdictBudgetExhausted},
		{"parked total spent blocks", GoalSnapshot{Budgets: Budgets{MaxContinuations: 200, Deadline: now.Add(time.Hour), ParkedTotal: 24 * time.Hour, MaxParkedTotal: 24 * time.Hour}}, nil, AdvancementMarkers{}, StepBlock, VerdictBudgetExhausted},
		{"deadline passed blocks", GoalSnapshot{Budgets: Budgets{MaxContinuations: 200, Deadline: now.Add(-time.Second), MaxParkedTotal: 24 * time.Hour}}, nil, AdvancementMarkers{}, StepBlock, VerdictDeadlineExceeded},
		{"live wait parks", GoalSnapshot{Budgets: full, Waits: []Wait{mkWait()}}, nil, AdvancementMarkers{}, StepPark, ""},
		{"lost wait without advancement blocks", GoalSnapshot{Budgets: full}, nil, AdvancementMarkers{LossThisTurn: true, LossCause: "disk gone"}, StepBlock, "waiting lost: disk gone"},
		{"lost wait with live waits parks", GoalSnapshot{Budgets: full, Waits: []Wait{mkWait()}}, nil, AdvancementMarkers{LossThisTurn: true, LossCause: "disk gone"}, StepPark, ""},
		{"lost wait with advancement drives", GoalSnapshot{Budgets: full}, nil, AdvancementMarkers{LossThisTurn: true, LossCause: "disk gone", AdvancedSinceLoss: true}, StepDrive, ""},
		{"unset budgets never block", GoalSnapshot{}, nil, AdvancementMarkers{}, StepDrive, ""},
		{"active and idle drives", GoalSnapshot{Budgets: full}, nil, AdvancementMarkers{}, StepDrive, ""},
	}
	for _, tc := range cases {
		step, verdict := DecideGoalStep(tc.snap, TurnOutcome{}, nil, tc.pending, tc.markers, now)
		if step != tc.wantStep || verdict != tc.wantVerdict {
			t.Errorf("%s: got (%q,%q), want (%q,%q)", tc.name, step, verdict, tc.wantStep, tc.wantVerdict)
		}
	}
	if VerdictNoProgress != "no progress" || VerdictBudgetExhausted != "budget exhausted" || VerdictDeadlineExceeded != "deadline exceeded" {
		t.Fatal("verdict strings must match spec §1 verbatim")
	}
}
