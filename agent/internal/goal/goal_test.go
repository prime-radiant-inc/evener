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

// foldStallOutcome builds the identical non-advancing outcome the stall tests
// fold: same fingerprint + class, no digest delta.
func foldStallOutcome() TurnOutcome {
	return TurnOutcome{
		ActionFingerprint: "grep pattern=x",
		ObservationClass:  "ok",
		ObservationHash:   "same",
		StateDigest:       "steady",
	}
}

// TestRecordContinuationNoProgressGrace: leading identical non-advancing turns
// accrue toward the K=6 fresh tier (not blocked early); the 6th nudges
// (stage none → nudged, still active) and the 7th blocks with "no progress".
func TestRecordContinuationNoProgressGrace(t *testing.T) {
	s := NewStore()
	s.Set("obj", clock())
	for i := range RepetitionThresholdFresh - 1 {
		s.RecordContinuation(foldStallOutcome(), false, clock())
		snap, _ := s.Snapshot()
		if snap.Status != StatusActive {
			t.Fatalf("leading turn %d should stay active, got %v", i, snap.Status)
		}
	}
	// G1: the K-th identical turn must nudge (stay active, stage trips) and
	// increment Iterations.
	snap, active := s.RecordContinuation(foldStallOutcome(), false, clock())
	if !active {
		t.Fatal("K-th identical turn must nudge, not block")
	}
	if snap.Status != StatusActive {
		t.Fatalf("after nudge turn: want StatusActive, got %v", snap.Status)
	}
	gsnap, _ := s.GoalSnapshot()
	if gsnap.LedgerSummary.Stage != StageNudged {
		t.Fatalf("after nudge turn: want stage nudged, got %q", gsnap.LedgerSummary.Stage)
	}
	// G5: (K-1) leading turns + 1 nudge turn.
	wantIters := RepetitionThresholdFresh
	if snap.Iterations != wantIters {
		t.Fatalf("after nudge turn: want Iterations=%d, got %d", wantIters, snap.Iterations)
	}
	// G4: the next identical turn after the nudge blocks.
	snap, active = s.RecordContinuation(foldStallOutcome(), false, clock())
	if active || snap.Status != StatusBlocked || snap.StopReason != "no progress" {
		t.Fatalf("post-nudge turn: want blocked/no-progress, got %v/%q active=%v", snap.Status, snap.StopReason, active)
	}
}

// TestNeverProgressedBlocks pins the ledger bound for a never-advancing goal:
// identical non-advancing turns nudge at K=6 then block on the next turn —
// never run forever (the /par Critical B1, now closed by the ledger).
func TestNeverProgressedBlocks(t *testing.T) {
	s := NewStore()
	s.Set("summarize the architecture", clock())
	for i := range RepetitionThresholdFresh - 1 {
		if _, active := s.RecordContinuation(foldStallOutcome(), false, clock()); !active {
			t.Fatalf("turn %d: never-advanced goal nudged/blocked too early", i)
		}
	}
	if _, active := s.RecordContinuation(foldStallOutcome(), false, clock()); !active {
		t.Fatal("K-th identical turn must nudge, not block")
	}
	if _, active := s.RecordContinuation(foldStallOutcome(), false, clock()); active {
		t.Fatal("post-nudge identical turn must block")
	}
	snap, _ := s.Snapshot()
	if snap.Status != StatusBlocked || snap.StopReason != "no progress" {
		t.Fatalf("want blocked/no-progress, got %v/%q", snap.Status, snap.StopReason)
	}
	// G5: Iterations counts every folded turn (K leading + nudge + block).
	if snap.Iterations != RepetitionThresholdFresh+1 {
		t.Fatalf("want Iterations=%d, got %d", RepetitionThresholdFresh+1, snap.Iterations)
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
// fields and Restore reconstitutes them faithfully, including the ledger
// window. The key behavioral invariant: a restored goal with a tightened
// (advanced) tier keeps it — the next identical turns stall at K=3, not K=6.
// G6: covers the previously untested PersistSnapshot/Restore path.
func TestPersistSnapshotRestoreRoundTrip(t *testing.T) {
	s := NewStore()
	s.Set("obj", clock())
	// Two advancing turns so tier=K=3 (the first fold has no previous digest
	// to delta against, so tightening needs the second mutated-with-delta
	// turn), repetition=1, Iterations=2.
	s.RecordContinuation(TurnOutcome{ActionFingerprint: "seed", ObservationClass: "ok", ObservationHash: "h1", StateDigest: "d-seed", Mutated: true}, false, clock())
	s.RecordContinuation(TurnOutcome{ActionFingerprint: "write file=x", ObservationClass: "ok", ObservationHash: "same", StateDigest: "steady", Mutated: true}, false, clock())

	persisted, ok := s.PersistSnapshot()
	if !ok {
		t.Fatal("PersistSnapshot: expected ok=true")
	}
	if persisted.Objective != "obj" || persisted.Status != StatusActive || persisted.StopReason != "" {
		t.Fatalf("PersistSnapshot: unexpected values: obj=%q status=%q stopReason=%q", persisted.Objective, persisted.Status, persisted.StopReason)
	}
	if persisted.Iterations != 2 || persisted.NoProgressStreak != 0 || !persisted.MadeProgressOnce {
		t.Fatalf("PersistSnapshot: unexpected counters: iters=%d streak=%d madeProgressOnce=%v", persisted.Iterations, persisted.NoProgressStreak, persisted.MadeProgressOnce)
	}
	if persisted.LedgerSummary.Tier != RepetitionThresholdAdvanced {
		t.Fatalf("PersistSnapshot: ledger tier = %d, want advanced K=%d", persisted.LedgerSummary.Tier, RepetitionThresholdAdvanced)
	}

	// Restore into a fresh store and verify the advanced tier survived: two
	// more identical non-advancing turns reach K=3 and nudge (not K=6).
	s2 := NewStore()
	s2.RestoreSnapshot(persisted)

	stalled := TurnOutcome{ActionFingerprint: "write file=x", ObservationClass: "ok", ObservationHash: "same", StateDigest: "steady"}
	// Restored run sits at rep=1 (the pre-restore turn advanced): two more
	// identical non-advancing turns reach K=3 and nudge (not K=6).
	for i := range RepetitionThresholdAdvanced - 1 {
		if _, active := s2.RecordContinuation(stalled, false, clock()); !active {
			t.Fatalf("restored goal: stalled turn %d blocked too early (want K=%d)", i, RepetitionThresholdAdvanced)
		}
	}
	snap, _ := s2.Snapshot()
	if snap.Status != StatusActive {
		t.Fatalf("restored goal: want active after the nudge, got %v", snap.Status)
	}
	if gsnap, _ := s2.GoalSnapshot(); gsnap.LedgerSummary.Stage != StageNudged {
		t.Fatalf("restored goal: want stage nudged, got %q", gsnap.LedgerSummary.Stage)
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
	snap, active := s.RecordContinuation(TurnOutcome{ActionFingerprint: "write file=x", ObservationClass: "ok", ObservationHash: "h1", StateDigest: "d1", Mutated: true}, false, clock())
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
	snap, active := s.RecordContinuation(foldStallOutcome(), false, clock())
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
	s.RecordContinuation(TurnOutcome{ActionFingerprint: "write file=x", ObservationClass: "ok", ObservationHash: "h1", StateDigest: "d1", Mutated: true}, false, clock())
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
	mkStalled := func(rep, tier int, stage GraduationStage) LedgerSummary {
		entries := make([]LedgerEntry, 0, rep)
		for range rep {
			entries = append(entries, LedgerEntry{Fingerprint: "grep pattern=x", Class: "ok", Hash: "same", Digest: "steady"})
		}
		return LedgerSummary{Entries: entries, Repetition: rep, Tier: tier, Stage: stage}
	}
	mkBackstopped := func(stage GraduationStage) LedgerSummary {
		entries := make([]LedgerEntry, 0, BackstopThreshold)
		for i := range BackstopThreshold {
			fp := "poll-a"
			if i%2 == 1 {
				fp = "poll-b"
			}
			entries = append(entries, LedgerEntry{Fingerprint: fp, Class: "ok", Hash: "h", Digest: "steady"})
		}
		return LedgerSummary{Entries: entries, Repetition: 1, Tier: RepetitionThresholdFresh, Stage: stage}
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
		{"stall-K first trip nudges", GoalSnapshot{Budgets: full, LedgerSummary: mkStalled(RepetitionThresholdFresh, RepetitionThresholdFresh, StageNone)}, nil, AdvancementMarkers{}, StepNudge, VerdictNoProgress},
		{"stall-K second trip blocks", GoalSnapshot{Budgets: full, LedgerSummary: mkStalled(RepetitionThresholdFresh, RepetitionThresholdFresh, StageNudged)}, nil, AdvancementMarkers{}, StepBlock, VerdictNoProgress},
		{"below-K drives", GoalSnapshot{Budgets: full, LedgerSummary: mkStalled(RepetitionThresholdFresh-1, RepetitionThresholdFresh, StageNone)}, nil, AdvancementMarkers{}, StepDrive, ""},
		{"backstop first trip nudges", GoalSnapshot{Budgets: full, LedgerSummary: mkBackstopped(StageNone)}, nil, AdvancementMarkers{}, StepNudge, VerdictNoProgress},
		{"backstop second trip blocks", GoalSnapshot{Budgets: full, LedgerSummary: mkBackstopped(StageNudged)}, nil, AdvancementMarkers{}, StepBlock, VerdictNoProgress},
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

// --- Slice-2 progress ledger (spec §4): pure tables. ---
//
// The ledger is a pure unit in this task: FoldLedger maps (prev summary,
// TurnOutcome, waits-subgoal evidence) to the next summary, and the stall
// predicates read the summary. Task 7 wires the fold into the gate; the
// interim v1 judge stays armed until then.

// ledgerOutcome builds a TurnOutcome test input.
func ledgerOutcome(fp, class, hash, digest string, mutated bool) TurnOutcome {
	return TurnOutcome{
		ActionFingerprint: fp,
		ObservationClass:  class,
		ObservationHash:   hash,
		StateDigest:       digest,
		Mutated:           mutated,
	}
}

// foldLedgerTurns folds n identical turns onto prev.
func foldLedgerTurns(prev LedgerSummary, n int, o TurnOutcome, waitAdvanced bool) LedgerSummary {
	s := prev
	for range n {
		s = FoldLedger(s, o, waitAdvanced)
	}
	return s
}

// TestLedgerIdenticalThreePostEvidenceStalls pins K=3 after first
// mutation-or-subgoal evidence: three identical no-delta turns stall.
func TestLedgerIdenticalThreePostEvidenceStalls(t *testing.T) {
	var s LedgerSummary
	s = FoldLedger(s, ledgerOutcome("read f", "ok", "h0", "d0", false), false)
	s = FoldLedger(s, ledgerOutcome("write f", "ok", "h1", "d1", true), false)
	if s.Tier != RepetitionThresholdAdvanced {
		t.Fatalf("mutated-with-delta turn must tighten tier to %d, got %+v", RepetitionThresholdAdvanced, s)
	}
	if s.Repetition != 1 {
		t.Fatalf("digest delta must reset repetition to 1, got %+v", s)
	}
	same := ledgerOutcome("write f", "ok", "h1", "d1", false)
	s = FoldLedger(s, same, false)
	if s.Repetition != 2 || LedgerStalled(s) {
		t.Fatalf("rep=2 at K=3 must not stall yet: %+v", s)
	}
	s = FoldLedger(s, same, false)
	if s.Repetition != 3 || !RepetitionStalled(s) || !LedgerStalled(s) {
		t.Fatalf("rep=3 at K=3 must stall: %+v", s)
	}
	s = FoldLedger(s, same, false)
	if !LedgerStalled(s) {
		t.Fatalf("rep=4 must stay stalled: %+v", s)
	}
	if BackstopStalled(s) {
		t.Fatalf("short run must stall via repetition, not the backstop: %+v", s)
	}
}

// TestLedgerFreshOpeningNotStalledBeforeSix pins K=6 while never-advanced:
// a read-heavy opening accrues without stalling until the 6th identical turn.
func TestLedgerFreshOpeningNotStalledBeforeSix(t *testing.T) {
	var s LedgerSummary
	same := ledgerOutcome("read f", "ok", "h", "d", false)
	s = foldLedgerTurns(s, 5, same, false)
	if s.Repetition != 5 {
		t.Fatalf("rep = %d, want 5", s.Repetition)
	}
	if LedgerStalled(s) || RepetitionStalled(s) || BackstopStalled(s) {
		t.Fatalf("5 identical pre-evidence turns must not stall: %+v", s)
	}
	if s.Tier != RepetitionThresholdFresh {
		t.Fatalf("fresh tier = %d, want %d", s.Tier, RepetitionThresholdFresh)
	}
	s = FoldLedger(s, same, false)
	if s.Repetition != 6 || !RepetitionStalled(s) || !LedgerStalled(s) {
		t.Fatalf("6th identical turn must stall at K=6: %+v", s)
	}
	if BackstopStalled(s) {
		t.Fatalf("K=6 stall must be repetition, not backstop: %+v", s)
	}
}

// TestLedgerPeriodTwoRotationReachesBackstop pins B=12: alternating
// fingerprints never trip repetition, but 12 consecutive non-advancing turns
// trip the total backstop. The opening pair is novel (advancing), so the
// backstop lands on the 14th turn, not the 12th.
func TestLedgerPeriodTwoRotationReachesBackstop(t *testing.T) {
	var s LedgerSummary
	a := ledgerOutcome("poll a", "external-unchanged", "ha", "d", false)
	b := ledgerOutcome("poll b", "external-unchanged", "hb", "d", false)
	for i := range 13 {
		o := a
		if i%2 == 1 {
			o = b
		}
		s = FoldLedger(s, o, false)
	}
	if s.Repetition != 1 || RepetitionStalled(s) {
		t.Fatalf("alternation must never trip repetition: %+v", s)
	}
	if BackstopStalled(s) || LedgerStalled(s) {
		t.Fatalf("13 alternating turns (11 trailing non-advancing) must not trip B=12: %+v", s)
	}
	s = FoldLedger(s, b, false)
	if !BackstopStalled(s) || !LedgerStalled(s) {
		t.Fatalf("14th alternating turn (12 trailing non-advancing) must trip B=12: %+v", s)
	}
	if RepetitionStalled(s) {
		t.Fatalf("backstop stall must not trip repetition: %+v", s)
	}
}

// TestLedgerCanonicalizeObservationHash pins timestamp/id redaction: outputs
// differing only in timestamps and request ids canonicalize equal, while
// genuinely different content stays distinct. Empty stays empty (the
// class-only fallback signal — never novel by default).
func TestLedgerCanonicalizeObservationHash(t *testing.T) {
	a := CanonicalizeObservationHash("fetched 3 rows at 2026-09-08T04:00:00Z req_id=abc123 trace_id=9f2c11aa-1234-5678-9abc-def012345678")
	b := CanonicalizeObservationHash("fetched 3 rows at 2026-09-08T04:05:00Z req_id=xyz789 trace_id=00000000-0000-0000-0000-000000000000")
	if a == "" || a != b {
		t.Fatalf("timestamp/id noise must canonicalize equal: %q vs %q", a, b)
	}
	c := CanonicalizeObservationHash("fetched 4 rows at 2026-09-08T04:00:00Z req_id=abc123 trace_id=9f2c11aa-1234-5678-9abc-def012345678")
	if c == a {
		t.Fatalf("genuinely different content must stay distinct: %q", c)
	}
	if got := CanonicalizeObservationHash(""); got != "" {
		t.Fatalf("empty hash must stay empty, got %q", got)
	}
}

// TestLedgerTimestampNoiseStillStalls pins the §9 evasion case at fold level:
// identical (action, class) with no digest delta stalls even when every raw
// observation hash carries fresh timestamp noise.
func TestLedgerTimestampNoiseStillStalls(t *testing.T) {
	var s LedgerSummary
	s = FoldLedger(s, ledgerOutcome("read f", "ok", "seed at 2026-09-08T03:00:00Z", "d0", false), false)
	s = FoldLedger(s, ledgerOutcome("write f", "ok", "seed at 2026-09-08T03:01:00Z", "d1", true), false)
	for i := range 3 {
		o := ledgerOutcome("write f", "ok", "poll failed at 2026-09-08T04:00:0"+string(rune('0'+i))+"Z", "d1", false)
		s = FoldLedger(s, o, false)
	}
	if !RepetitionStalled(s) || !LedgerStalled(s) {
		t.Fatalf("timestamp-noisy identical no-delta turns must still stall at K=3: %+v", s)
	}
}

// TestLedgerGenuineRetryWithDeltaStaysLive pins that a same-action retry loop
// with a state-digest delta every turn never accrues: repetition resets and
// every entry advances.
func TestLedgerGenuineRetryWithDeltaStaysLive(t *testing.T) {
	var s LedgerSummary
	s = FoldLedger(s, ledgerOutcome("test ./...", "error", "h0", "d0", true), false)
	for _, d := range []string{"d1", "d2", "d3"} {
		s = FoldLedger(s, ledgerOutcome("test ./...", "error", "h0", d, true), false)
	}
	if s.Repetition != 1 {
		t.Fatalf("every-delta retry must hold repetition at 1, got %+v", s)
	}
	if LedgerStalled(s) || RepetitionStalled(s) || BackstopStalled(s) {
		t.Fatalf("genuine retry with delta must stay live: %+v", s)
	}
	for i, e := range s.Entries {
		if i > 0 && !e.Advancement {
			t.Fatalf("delta turn %d must be marked advancing: %+v", i, e)
		}
	}
}

// TestLedgerJunkWriteAccrues pins that a mutating turn with no digest delta
// (junk write) still accrues repetition under the fresh tier — it is not
// mutation evidence and does not tighten to K=3.
func TestLedgerJunkWriteAccrues(t *testing.T) {
	var s LedgerSummary
	junk := ledgerOutcome("write f", "ok", "h", "d", true)
	s = foldLedgerTurns(s, 3, junk, false)
	if LedgerStalled(s) {
		t.Fatalf("3 junk writes must not stall under the fresh tier: %+v", s)
	}
	if s.Tier != RepetitionThresholdFresh {
		t.Fatalf("junk writes must not tighten the tier: %+v", s)
	}
	s = foldLedgerTurns(s, 3, junk, false)
	if s.Repetition != 6 || !LedgerStalled(s) {
		t.Fatalf("6 junk writes must stall at K=6: %+v", s)
	}
}

// TestLedgerMutatedDeltaResetsBothTiers pins that a mutating turn with a
// digest delta resets the repetition run and tightens the tier to K=3, so a
// subsequent identical pair stalls sooner than a fresh opening would.
func TestLedgerMutatedDeltaResetsBothTiers(t *testing.T) {
	var s LedgerSummary
	s = foldLedgerTurns(s, 2, ledgerOutcome("read f", "ok", "h0", "d0", false), false)
	if s.Repetition != 2 {
		t.Fatalf("precondition rep = %d, want 2", s.Repetition)
	}
	s = FoldLedger(s, ledgerOutcome("write f", "ok", "h1", "d1", true), false)
	if s.Tier != RepetitionThresholdAdvanced || s.Repetition != 1 {
		t.Fatalf("mutated-with-delta must reset rep to 1 and tighten tier: %+v", s)
	}
	if LedgerStalled(s) {
		t.Fatalf("evidence turn itself must not stall: %+v", s)
	}
	same := ledgerOutcome("write f", "ok", "h1", "d1", false)
	s = FoldLedger(s, same, false)
	if s.Repetition != 2 || LedgerStalled(s) {
		t.Fatalf("rep=2 at K=3 must not stall: %+v", s)
	}
	s = FoldLedger(s, same, false)
	if !LedgerStalled(s) {
		t.Fatalf("rep=3 at K=3 must stall: %+v", s)
	}
}

// TestLedgerWaitEvidenceAdvancesButExpectChecksAccrue pins waits-only
// advancement: a condition check with no wait flip, no delta, and no novelty
// accrues like any other non-advancing turn (goal_expect never feeds the
// ledger), while a waits-predicate flip advances and tightens the tier.
func TestLedgerWaitEvidenceAdvancesButExpectChecksAccrue(t *testing.T) {
	var s LedgerSummary
	s = foldLedgerTurns(s, 2, ledgerOutcome("read f", "ok", "h", "d", false), false)
	s = FoldLedger(s, ledgerOutcome("read f", "ok", "h", "d", false), false)
	if s.Repetition != 3 || s.Tier != RepetitionThresholdFresh {
		t.Fatalf("condition check without wait evidence must accrue under the fresh tier: %+v", s)
	}
	if LedgerStalled(s) {
		t.Fatalf("rep=3 under the fresh tier must not stall: %+v", s)
	}
	if last := s.Entries[len(s.Entries)-1]; last.Advancement {
		t.Fatalf("non-advancing check must be marked non-advancing: %+v", last)
	}

	var w LedgerSummary
	w = foldLedgerTurns(w, 2, ledgerOutcome("read f", "ok", "h", "d", false), false)
	w = FoldLedger(w, ledgerOutcome("eval wake", "ok", "h2", "d2", false), true)
	if w.Tier != RepetitionThresholdAdvanced || w.Repetition != 1 {
		t.Fatalf("waits-predicate flip must advance, reset rep, tighten tier: %+v", w)
	}
	if LedgerStalled(w) {
		t.Fatalf("wait-evidence turn must not stall: %+v", w)
	}
	if last := w.Entries[len(w.Entries)-1]; !last.Advancement {
		t.Fatalf("wait-evidence turn must be marked advancing: %+v", last)
	}
}

// TestLedgerMigratedResidualAtMostKMinusOne pins the disclosed migration
// residual: seeded "migrated" entries are distinct from all real
// fingerprints, so a post-migration different first turn resets the run to 1
// (granting at most K−1 extra turns once); K−1 further identical turns then
// stall. The B=12 backstop still bites longer evasions.
func TestLedgerMigratedResidualAtMostKMinusOne(t *testing.T) {
	created := time.Unix(1_700_000_000, 0).UTC()
	restore := created.Add(time.Hour)
	for _, tc := range []struct {
		name         string
		streak       int
		madeProgress bool
		wantTier     int
		wantSeeded   int
	}{
		{"progressed streak-2", 2, true, RepetitionThresholdAdvanced, 2},
		{"never-advanced streak-5", 5, false, RepetitionThresholdFresh, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := MigrateV1ToPersisted("obj", "active", "", 4, tc.streak, tc.madeProgress, created, created, restore)
			s := p.LedgerSummary
			if s.Tier != tc.wantTier || s.Repetition != tc.wantSeeded {
				t.Fatalf("precondition summary = %+v, want tier=%d rep=%d", s, tc.wantTier, tc.wantSeeded)
			}
			s = FoldLedger(s, ledgerOutcome("other tool", "ok", "h", "d", false), false)
			if s.Repetition != 1 || LedgerStalled(s) {
				t.Fatalf("different post-migration turn must reset rep to 1 without stalling: %+v", s)
			}
			same := ledgerOutcome("other tool", "ok", "h", "d", false)
			extra := tc.wantTier - 1
			for i := 1; i < extra; i++ {
				s = FoldLedger(s, same, false)
				if LedgerStalled(s) {
					t.Fatalf("identical turn %d of %d must not stall yet: %+v", i+1, extra, s)
				}
			}
			s = FoldLedger(s, same, false)
			if s.Repetition != tc.wantTier || !LedgerStalled(s) {
				t.Fatalf("K−1 further identical turns must stall at K=%d: %+v", tc.wantTier, s)
			}
		})
	}
	if got := CanonicalizeActionFingerprint(MigratedFingerprint); got == MigratedFingerprint {
		t.Fatalf("canonicalizer must keep real fingerprints distinct from %q", MigratedFingerprint)
	}
}

// TestLedgerWindowBoundedToTwelve pins the 12-entry shape: the window keeps
// the last max(N,B)=12 entries with fingerprint/class/hash/digest/advancement
// populated, dropping the oldest.
func TestLedgerWindowBoundedToTwelve(t *testing.T) {
	var s LedgerSummary
	for i := range 15 {
		fp := "tool-" + string(rune('a'+i))
		s = FoldLedger(s, ledgerOutcome(fp, "ok", "hash-"+string(rune('a'+i)), "digest-"+string(rune('a'+i)), false), false)
	}
	if len(s.Entries) != LedgerWindowSize {
		t.Fatalf("window = %d entries, want %d", len(s.Entries), LedgerWindowSize)
	}
	if got, want := s.Entries[0].Fingerprint, CanonicalizeActionFingerprint("tool-d"); got != want {
		t.Fatalf("oldest kept entry = %q, want %q (turns 1-3 dropped)", got, want)
	}
	for i, e := range s.Entries {
		if e.Fingerprint == "" || e.Class == "" || e.Hash == "" || e.Digest == "" {
			t.Fatalf("entry %d missing fields: %+v", i, e)
		}
		if !e.Advancement {
			t.Fatalf("every-delta entry %d must be marked advancing: %+v", i, e)
		}
	}
}

// TestLedgerFoldPreservesStage pins that folding sets the tier but leaves the
// graduation stage to its owner (Task 8): a restart after a nudge graduates,
// never re-nudges, so the fold must not clear it.
func TestLedgerFoldPreservesStage(t *testing.T) {
	prev := LedgerSummary{Stage: StageNudged}
	s := FoldLedger(prev, ledgerOutcome("read f", "ok", "h", "d", false), false)
	if s.Stage != StageNudged {
		t.Fatalf("fold must preserve stage, got %q", s.Stage)
	}
	if s.Tier != RepetitionThresholdFresh {
		t.Fatalf("fresh fold must set tier %d, got %+v", RepetitionThresholdFresh, s)
	}
}

// TestLedgerCanonicalizeActionFingerprint pins the normalization rules: tool
// case/whitespace collapse, path cleaning, volatile-token redaction, and the
// migrated-fingerprint guard — without merging refining greps (different args
// stay distinct).
func TestLedgerCanonicalizeActionFingerprint(t *testing.T) {
	a := CanonicalizeActionFingerprint("Grep  pattern=x   path=/a//b/")
	b := CanonicalizeActionFingerprint("grep pattern=x path=/a/b")
	if a == "" || a != b {
		t.Fatalf("normalization must converge: %q vs %q", a, b)
	}
	refining := CanonicalizeActionFingerprint("grep pattern=y path=/a/b")
	if refining == a {
		t.Fatalf("refining grep must stay distinct: %q", refining)
	}
	nonceA := CanonicalizeActionFingerprint("run job_id=3f9a2c1e-4b5d-6e7f-8a9b-0c1d2e3f4a5b")
	nonceB := CanonicalizeActionFingerprint("run job_id=00000000-0000-0000-0000-000000000000")
	if nonceA == "" || nonceA != nonceB {
		t.Fatalf("volatile ids must redact: %q vs %q", nonceA, nonceB)
	}
	if got := CanonicalizeActionFingerprint("  "); got != "" {
		t.Fatalf("blank fingerprint must stay blank, got %q", got)
	}
}

// TestLedgerClassOnlyFallback pins the unhashable-observation rule: with an
// empty hash the class carries novelty — a repeated class accrues, a new
// class advances — and emptiness never counts as novel by default.
func TestLedgerClassOnlyFallback(t *testing.T) {
	var s LedgerSummary
	s = foldLedgerTurns(s, 5, ledgerOutcome("read f", "ok", "", "d", false), false)
	if s.Repetition != 5 || LedgerStalled(s) {
		t.Fatalf("5 empty-hash identical turns must accrue without stalling: %+v", s)
	}
	s = FoldLedger(s, ledgerOutcome("read f", "ok", "", "d", false), false)
	if !LedgerStalled(s) {
		t.Fatalf("6th empty-hash identical turn must stall: %+v", s)
	}
	s = FoldLedger(s, ledgerOutcome("read f", "error", "", "d", false), false)
	if s.Repetition != 1 {
		t.Fatalf("class change must reset the run, got %+v", s)
	}
	if last := s.Entries[len(s.Entries)-1]; !last.Advancement {
		t.Fatalf("unseen class must advance via the class-only fallback: %+v", last)
	}
}

// TestLedgerLongRotationDisclosed pins the disclosed residual: a rotation with
// period > N=8 presents a novel hash every turn, so every turn advances and
// neither signal fires — the maxContinuations=200 outer bound (Task 7
// wiring), not the ledger, closes this loop.
func TestLedgerLongRotationDisclosed(t *testing.T) {
	var s LedgerSummary
	fps := []string{"poll a", "poll b"}
	for i := range BackstopThreshold {
		fp := fps[i%len(fps)]
		s = FoldLedger(s, ledgerOutcome(fp, "external-unchanged", "unique-hash-"+string(rune('a'+i)), "d", false), false)
	}
	if LedgerStalled(s) || RepetitionStalled(s) || BackstopStalled(s) {
		t.Fatalf("novelty-every-turn rotation must not stall the ledger: %+v", s)
	}
	if last := s.Entries[len(s.Entries)-1]; !last.Advancement {
		t.Fatalf("novel-hash turn must be marked advancing: %+v", last)
	}
}
