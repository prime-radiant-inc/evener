package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// Slice-1 gate tests (spec sections 1-3): park skips every fold; expiry
// re-drives exactly once; stale parks kick exactly once; double-claims
// collapse; retarget voids waits and disarms the timer; clear drops the wake;
// cancel keeps a claimed wake; the terminal latch blocks after a flagged
// drive. Deterministic: scripted provider via newGoalMethodSession/newSession
// + withSteps, FakeClock where time matters.

// TestGateParkSkipsFold pins the slice-1 gate park branch (spec sections 1,
// 3): a goal parked on a live wait skips every fold and arms no continuation
// — the non-progressed continuation gate returns ("", false) with
// Iterations/NoProgressStreak unfolded and no breaker steering note.
func TestGateParkSkipsFold(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("park on the timer", clk.Now())
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Hour}, clk.Now()); !ok {
		t.Fatal("precondition: until_time registration should succeed")
	}

	prompt, ok := sess.armGoalContinuation(false, true)
	if ok || prompt != "" {
		t.Fatalf("parked gate = (%q, %v), want (\"\", false): a waiting goal must not re-arm", prompt, ok)
	}
	snap, ok := store.Snapshot()
	if !ok {
		t.Fatal("precondition: goal should still be set after a parked gate")
	}
	if snap.Status != goal.StatusWaiting {
		t.Fatalf("goal status = %q, want waiting after a parked gate", snap.Status)
	}
	if snap.Iterations != 0 || snap.NoProgressStreak != 0 {
		t.Fatalf("snapshot = %+v, want Iterations=0 NoProgressStreak=0 (parked turns must not fold)", snap)
	}
	sess.mu.Lock()
	notes := 0
	for _, turn := range sess.history {
		if turn.Kind == schema.TurnSteering {
			notes++
		}
	}
	sess.mu.Unlock()
	if notes != 0 {
		t.Fatalf("steering notes after a parked gate = %d, want 0 (parking is not a stop)", notes)
	}
}

// newWaitGateSession builds a session on a FakeClock for wait-gate tests so
// expiry is deterministic (Advance, never wall-clock sleep).
func newWaitGateSession(t *testing.T, clk *agenttest.FakeClock) *Session {
	t.Helper()
	return newSession(t, withConfig(SessionConfig{clock: clk}))
}

// TestGateExpiryRedrivesOnce pins the expiry path (spec sections 1-3): an
// expired until_time lease claims into pendingWake before the decide, and the
// gate drives exactly one wake turn carrying the trigger — never an
// auto-block. The wake turn's own tail consumes the backlog (drain at fold),
// so the follow-up gate re-arms normally instead of re-driving.
func TestGateExpiryRedrivesOnce(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("timer goal", clk.Now())
	w, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Minute}, clk.Now())
	if !ok {
		t.Fatal("precondition: until_time registration should succeed")
	}

	clk.Advance(2 * time.Minute)
	prompt, cont := sess.armGoalContinuation(false, true)
	if !cont || prompt == "" {
		t.Fatalf("expired gate = (%q, %v), want a wake drive: expiry must re-drive exactly one evaluation turn", prompt, cont)
	}
	if !strings.Contains(prompt, w.Lease.WaitID) {
		t.Fatalf("wake prompt missing wait_id %q: the wake turn must carry its trigger\nprompt:\n%s", w.Lease.WaitID, prompt)
	}
	if !strings.Contains(prompt, goalWaitWakeTrailerPrefix) {
		t.Fatalf("wake prompt missing trailer %q\nprompt:\n%s", goalWaitWakeTrailerPrefix, prompt)
	}
	if snap, _ := store.Snapshot(); snap.Iterations != 0 || snap.NoProgressStreak != 0 {
		t.Fatalf("wake drive folded: snapshot = %+v, want zero fold (wait-attributable turns bypass RecordContinuation)", snap)
	}

	// The wake turn's own tail consumes the backlog and re-arms the plain
	// objective without folding stall signal.
	prompt, cont = sess.armGoalContinuation(false, true)
	if !cont || prompt == "" {
		t.Fatalf("wake-tail gate = (%q, %v), want the re-armed objective drive", prompt, cont)
	}
	if strings.Contains(prompt, goalWaitWakeTrailerPrefix) {
		t.Fatalf("wake-tail prompt must not carry the wake trailer (backlog consumed):\n%s", prompt)
	}
	if gsnap, _ := store.GoalSnapshot(); len(gsnap.PendingWake) != 0 {
		t.Fatalf("pendingWake after wake-tail = %+v, want drained in the same commit as the tail fold", gsnap.PendingWake)
	}
	if snap, _ := store.Snapshot(); snap.Iterations != 0 || snap.NoProgressStreak != 0 {
		t.Fatalf("wake-tail folded: snapshot = %+v, want zero fold", snap)
	}

	// The follow-up turn folds normally through the interim judge.
	if _, cont := sess.armGoalContinuation(false, true); !cont {
		t.Fatal("follow-up gate should drive the active objective")
	}
	if snap, _ := store.Snapshot(); snap.Iterations != 1 || snap.NoProgressStreak != 1 {
		t.Fatalf("snapshot = %+v, want the single post-wake fold (Iterations=1 streak=1)", snap)
	}
}

// TestGateStaleParkKicksOnce pins the settle re-check (spec section 3): a
// park whose deadline passed between the gate and the settle kicks exactly
// once via the same claim path - no silent strand, no double kick.
func TestGateStaleParkKicksOnce(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	kicks := wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("stale park", clk.Now())
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Minute}, clk.Now()); !ok {
		t.Fatal("precondition: registration should succeed")
	}
	if prompt, ok := sess.armGoalContinuation(false, true); ok || prompt != "" {
		t.Fatalf("live park gate = (%q, %v), want held", prompt, ok)
	}

	clk.Advance(2 * time.Minute)
	if sess.settleGoalOnIdle() != true {
		t.Fatal("settle over an expired park must kick exactly once")
	}
	if *kicks != 1 {
		t.Fatalf("kicks = %d, want 1", *kicks)
	}
	if sess.settleGoalOnIdle() {
		t.Fatal("second settle must not re-kick the same fire (exactly-once)")
	}
	if *kicks != 1 {
		t.Fatalf("kicks = %d, want still 1 after the second settle", *kicks)
	}
}

// TestGateDoubleClaimCollapses pins the exactly-once claim (spec section 2,
// the serialized double-claim unit test): ClaimFire twice collapses - the
// second returns already-fired - and exactly one kick goes out, with no
// same-ms timing involved.
func TestGateDoubleClaimCollapses(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	kicks := wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("double fire", clk.Now())
	w, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Minute}, clk.Now())
	if !ok {
		t.Fatal("precondition: registration should succeed")
	}

	now := clk.Now()
	if _, ok := store.ClaimFire(w.Lease.WaitID, "first fire", now); !ok {
		t.Fatal("first ClaimFire should consume the lease")
	}
	if _, ok := store.ClaimFire(w.Lease.WaitID, "second fire", now); ok {
		t.Fatal("second ClaimFire must collapse (already-fired): exactly one kick per fire")
	}

	// Both racing claimants drive through the gate: exactly one wake.
	prompt, cont := sess.armGoalContinuation(false, true)
	if !cont || prompt == "" {
		t.Fatalf("claimed gate = (%q, %v), want the single coalesced wake drive", prompt, cont)
	}
	prompt, cont = sess.armGoalContinuation(false, true)
	if !cont || strings.Contains(prompt, goalWaitWakeTrailerPrefix) {
		t.Fatalf("second gate = (%q, %v), want the consumed wake-tail re-arm, not a second wake", prompt, cont)
	}

	// And the timer callback for the same expiry collapses too: the delivered
	// set skips the already-consumed wait_id instead of re-claiming.
	sess.fireGoalWaitTimer(0)
	if *kicks != 0 {
		t.Fatalf("kicks = %d, want 0 (no kick wired through the gate path; timer re-fire must collapse)", *kicks)
	}
}

// TestGateCoalescedTimerKicksOnce pins the coalesced timer (spec section 2):
// one sclock timer serves every live wait; advancing past the earliest
// deadline kicks exactly once even with several waits live, and the prompt
// carries the coalesced triggers.
func TestGateCoalescedTimerKicksOnce(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	var prompts []string
	sess.SetKickFunc(func(p string) { prompts = append(prompts, p) })
	sess.SetNotifyFunc(func() {})

	store := sess.getOrCreateGoalStore()
	store.Set("coalesced", clk.Now())
	// Distinct targets: same-target re-registers replace (spec section 2), so
	// two until_time waits sharing the empty target would collapse to one.
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Target: "early-timer", Timeout: time.Minute, Label: "early"}, clk.Now()); !ok {
		t.Fatal("precondition: early registration should succeed")
	}
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Target: "late-timer", Timeout: 2 * time.Minute, Label: "late"}, clk.Now()); !ok {
		t.Fatal("precondition: late registration should succeed")
	}
	sess.armGoalWaitTimer()
	clk.BlockUntil(1)
	clk.Advance(70 * time.Second)
	clk.Drain()
	if len(prompts) != 1 {
		t.Fatalf("timer kicks = %d, want exactly 1 (coalesced, not one per wait)", len(prompts))
	}
	if !strings.Contains(prompts[0], "early") {
		t.Fatalf("coalesced prompt must carry the fired wait's trigger:\n%s", prompts[0])
	}
	if gsnap, _ := store.GoalSnapshot(); len(gsnap.Waits) != 1 {
		t.Fatalf("live waits after earliest expiry = %d, want 1 (the late lease stays parked)", len(gsnap.Waits))
	}
}

// TestGateRetargetVoidsWaitsAndDisarms pins retarget semantics (spec sections
// 1, 3): /goal <new> clears live waits and disarms their timers (no stale
// fire re-triggers after the retarget). Nothing claimed stands here, so no
// superseded batch survives and no no-op turn drives.
func TestGateRetargetVoidsWaitsAndDisarms(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	var prompts []string
	sess.SetKickFunc(func(p string) { prompts = append(prompts, p) })
	sess.SetNotifyFunc(func() {})

	store := sess.getOrCreateGoalStore()
	store.Set("old objective", clk.Now())
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Minute}, clk.Now()); !ok {
		t.Fatal("precondition: registration should succeed")
	}
	sess.armGoalWaitTimer()

	if _, err := sess.SetGoal(context.Background(), "new objective"); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if gsnap, _ := store.GoalSnapshot(); len(gsnap.Waits) != 0 {
		t.Fatalf("waits after retarget = %+v, want cleared", gsnap.Waits)
	}
	sess.mu.Lock()
	armed := sess.goalWaitTimer != nil
	sess.mu.Unlock()
	if armed {
		t.Fatal("wait timer must disarm on retarget (no post-retarget stale fire)")
	}

	// A fire after the retarget cannot reuse the old lease (same-target
	// re-register replaces): the old wait stays gone.
	clk.Advance(2 * time.Minute)
	clk.Drain()
	for _, p := range prompts {
		if strings.Contains(p, "old objective") {
			t.Fatalf("stale wake drove the old objective after retarget:\n%s", p)
		}
	}
	if n := countSteeringNotes(sess, goalWaitNoticePrefix); n != 0 {
		t.Fatalf("loss notices = %d, want 0 (nothing claimed post-retarget, so nothing lost)", n)
	}
	if prompt, ok := sess.armGoalContinuation(false, false); !ok || !strings.Contains(prompt, "new objective") {
		t.Fatalf("post-retarget gate = (%q, %v), want the current objective's drive", prompt, ok)
	}
}

// TestGateRetargetBetweenClaimAndKickDrivesSupersededNoop pins the
// claim-vs-kick race (spec section 3 superseded): a claim that lands before a
// retarget survives marked Superseded and drives a single no-op evaluation on
// the CURRENT objective with the stale excerpt marked superseded - never the
// old objective's wake, never a silent drop. The no-op turn consumes the
// batch and re-arms the current objective without folding stall signal.
func TestGateRetargetBetweenClaimAndKickDrivesSupersededNoop(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	sess.SetKickFunc(func(string) {})
	sess.SetNotifyFunc(func() {})

	store := sess.getOrCreateGoalStore()
	store.Set("old objective", clk.Now())
	w, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Minute}, clk.Now())
	if !ok {
		t.Fatal("precondition: registration should succeed")
	}
	clk.Advance(2 * time.Minute)
	if _, ok := store.ClaimFire(w.Lease.WaitID, "wait expired: label", clk.Now()); !ok {
		t.Fatal("precondition: claim should consume the expired lease")
	}
	if _, err := sess.SetGoal(context.Background(), "new objective"); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	// The retarget carries the claim marked Superseded.
	if gsnap, _ := store.GoalSnapshot(); len(gsnap.PendingWake) != 1 || !gsnap.PendingWake[0].Superseded {
		t.Fatalf("pendingWake after retarget = %+v, want one Superseded entry", gsnap.PendingWake)
	}
	// The gate drives the no-op evaluation on the current objective.
	prompt, cont := sess.armGoalContinuation(false, false)
	if !cont {
		t.Fatal("gate after claim-then-retarget must drive the superseded no-op evaluation")
	}
	if !strings.Contains(prompt, "new objective") {
		t.Fatalf("no-op prompt must evaluate the CURRENT objective:\n%s", prompt)
	}
	if !strings.Contains(prompt, "(superseded)") || !strings.Contains(prompt, w.Lease.WaitID) {
		t.Fatalf("no-op prompt must mark the stale trigger superseded with its wait_id:\n%s", prompt)
	}
	if strings.Contains(prompt, "old objective") {
		t.Fatalf("no-op prompt must never pursue the old objective:\n%s", prompt)
	}
	// The no-op turn's own tail consumes the batch and re-arms the current
	// objective with zero stall fold.
	prompt, cont = sess.armGoalContinuation(false, true)
	if !cont || !strings.Contains(prompt, "new objective") {
		t.Fatalf("no-op tail = (%q, %v), want the current objective re-armed", prompt, cont)
	}
	if strings.Contains(prompt, "(superseded)") {
		t.Fatalf("re-armed prompt must not carry the consumed superseded batch:\n%s", prompt)
	}
	if gsnap, _ := store.GoalSnapshot(); len(gsnap.PendingWake) != 0 {
		t.Fatalf("pendingWake after no-op tail = %+v, want drained", gsnap.PendingWake)
	}
	if snap, _ := store.Snapshot(); snap.Iterations != 0 || snap.NoProgressStreak != 0 {
		t.Fatalf("snapshot = %+v, want zero fold across the no-op turn", snap)
	}
}

// TestGateSupersededThreeGateSequenceNoHang is the regression test for the
// goalUpdateMu self-deadlock: a continuation-tail superseded no-op (wakeTail
// path consuming via per-ID drain) left goalSupersededArmed set, and the next
// empty-backlog continuation gate re-locked the non-reentrant serializer and
// hung forever. The full old→register→claim→SetGoal→arm→arm→arm sequence must
// complete (TRIPWIRE-guarded) and drive correctly at each step.
func TestGateSupersededThreeGateSequenceNoHang(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	sess.SetKickFunc(func(string) {})
	sess.SetNotifyFunc(func() {})

	store := sess.getOrCreateGoalStore()
	store.Set("old objective", clk.Now())
	w, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Minute}, clk.Now())
	if !ok {
		t.Fatal("precondition: registration should succeed")
	}
	clk.Advance(2 * time.Minute)
	if _, ok := store.ClaimFire(w.Lease.WaitID, "wait expired: label", clk.Now()); !ok {
		t.Fatal("precondition: claim should consume the expired lease")
	}
	if _, err := sess.SetGoal(context.Background(), "new objective"); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Gate 1 (non-continuation tail): drives the superseded no-op.
		prompt, cont := sess.armGoalContinuation(false, false)
		if !cont || !strings.Contains(prompt, "(superseded)") {
			t.Errorf("gate 1 = (%q, %v), want the superseded no-op drive", prompt, cont)
			return
		}
		// Gate 2 (continuation tail = the no-op turn itself): consumes the
		// marked batch and re-arms the current objective with zero fold.
		prompt, cont = sess.armGoalContinuation(false, true)
		if !cont || !strings.Contains(prompt, "new objective") {
			t.Errorf("gate 2 = (%q, %v), want the re-armed current objective", prompt, cont)
			return
		}
		// Gate 3 (continuation tail, empty backlog): must NOT hang on a
		// stale superseded flag - folds normally through the interim judge.
		if _, cont = sess.armGoalContinuation(false, true); !cont {
			t.Errorf("gate 3 must drive the active objective (stale flag must be consumed)")
			return
		}
	}()
	// TRIPWIRE: scripted in-process gate calls, no I/O; only fires on a genuine hang (e.g. the serializer self-deadlock).
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		t.Fatal("three-gate superseded sequence hung: goalUpdateMu self-deadlock")
	}
	if snap, _ := store.Snapshot(); snap.Iterations != 1 || snap.NoProgressStreak != 1 {
		t.Fatalf("snapshot = %+v, want exactly the gate-3 interim fold (Iterations=1 streak=1)", snap)
	}
}

// TestGateSupersededMixedBatchKeepsFreshClaim pins the mixed-batch drain
// (spec section 3): a retarget-carried superseded batch plus a fresh claim
// registered before the no-op runs must drive the stale excerpt once AND
// still wake the fresh claim separately - the superseded tail drains ONLY
// superseded IDs, never the fresh backlog.
func TestGateSupersededMixedBatchKeepsFreshClaim(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	sess.SetKickFunc(func(string) {})
	sess.SetNotifyFunc(func() {})

	store := sess.getOrCreateGoalStore()
	store.Set("old objective", clk.Now())
	stale, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Target: "stale-timer", Timeout: time.Minute}, clk.Now())
	if !ok {
		t.Fatal("precondition: stale registration should succeed")
	}
	clk.Advance(2 * time.Minute)
	if _, ok := store.ClaimFire(stale.Lease.WaitID, "wait expired: stale", clk.Now()); !ok {
		t.Fatal("precondition: stale claim should consume the expired lease")
	}
	if _, err := sess.SetGoal(context.Background(), "new objective"); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	// Fresh wait on the new objective, backdated so it is already expired:
	// the next gate claims it alongside the carried superseded batch.
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Target: "fresh-timer", Timeout: time.Minute}, clk.Now().Add(-time.Minute-time.Second)); !ok {
		t.Fatal("precondition: fresh registration should succeed")
	}
	// Gate 1 drives the superseded no-op (stale excerpt only - fresh claims
	// are not Superseded and never render in the no-op frame).
	prompt, cont := sess.armGoalContinuation(false, false)
	if !cont {
		t.Fatal("gate 1 must drive the superseded no-op evaluation")
	}
	if !strings.Contains(prompt, stale.Lease.WaitID) || !strings.Contains(prompt, "(superseded)") {
		t.Fatalf("gate-1 prompt must carry the stale excerpt marked superseded:\n%s", prompt)
	}
	if strings.Contains(prompt, "fresh-timer") {
		t.Fatalf("gate-1 no-op must not carry the fresh claim:\n%s", prompt)
	}
	// Gate 2 (the no-op turn's tail): consumes ONLY the superseded IDs; the
	// fresh claim survives and drives its own wake turn now.
	prompt, cont = sess.armGoalContinuation(false, true)
	if !cont {
		t.Fatal("gate 2 must drive the fresh claim's wake turn")
	}
	if strings.Contains(prompt, "(superseded)") {
		t.Fatalf("gate-2 wake must not carry the consumed stale batch:\n%s", prompt)
	}
	if !strings.Contains(prompt, goalWaitWakeTrailerPrefix) || !strings.Contains(prompt, "fresh-timer") {
		t.Fatalf("gate-2 prompt must be the fresh claim's wake with its trigger:\n%s", prompt)
	}
}

// TestGateClearDropsWake pins the clear half of superseded/drop (spec section
// 3): with no goal current, a claimed wake is dropped - not driven, consuming
// no budget and producing no notice.
func TestGateClearDropsWake(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	var prompts []string
	sess.SetKickFunc(func(p string) { prompts = append(prompts, p) })
	sess.SetNotifyFunc(func() {})

	store := sess.getOrCreateGoalStore()
	store.Set("doomed goal", clk.Now())
	w, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Minute}, clk.Now())
	if !ok {
		t.Fatal("precondition: registration should succeed")
	}
	clk.Advance(2 * time.Minute)
	if _, ok := store.ClaimFire(w.Lease.WaitID, "wait expired: label", clk.Now()); !ok {
		t.Fatal("precondition: claim should consume the expired lease")
	}
	sess.ClearGoal()
	sess.fireGoalWaitTimer(0)
	clk.Drain()
	if len(prompts) != 0 {
		t.Fatalf("kicks after clear = %d, want 0 (no goal, no notice, wake dropped)", len(prompts))
	}
	if n := countSteeringNotes(sess, goalWaitNoticePrefix); n != 0 {
		t.Fatalf("loss notices after clear = %d, want 0 (clear wins the race silently)", n)
	}
	if prompt, ok := sess.armGoalContinuation(false, true); ok || prompt != "" {
		t.Fatalf("post-clear gate = (%q, %v), want (\"\", false)", prompt, ok)
	}
}

// TestGateCancelKeepsClaimedWake pins cancel-vs-pendingWake (spec section 7):
// cancel removes the live lease only - an already-claimed pendingWake entry
// still drives once with the cancellation noted; only the post-clear "no
// goal current" condition drops a wake.
func TestGateCancelKeepsClaimedWake(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("cancel race", clk.Now())
	w, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Minute}, clk.Now())
	if !ok {
		t.Fatal("precondition: registration should succeed")
	}
	clk.Advance(2 * time.Minute)
	if _, ok := store.ClaimFire(w.Lease.WaitID, "wait expired: "+w.Lease.Label, clk.Now()); !ok {
		t.Fatal("precondition: claim should consume the expired lease")
	}
	if sess.CancelGoalWait(w.Lease.WaitID) {
		t.Fatal("CancelGoalWait on a claimed (no longer live) lease must report false - cancel removes live leases only")
	}
	prompt, cont := sess.armGoalContinuation(false, true)
	if !cont || !strings.Contains(prompt, w.Lease.WaitID) {
		t.Fatalf("gate after cancel = (%q, %v), want the claimed wake to still drive once", prompt, cont)
	}
}

// TestGateCancelLiveLeaseDisarms pins the live-cancel half: cancelling the
// last live lease returns the goal to active and disarms the timer, so the
// goal re-arms normally with no wake.
func TestGateCancelLiveLeaseDisarms(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("live cancel", clk.Now())
	w, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Minute}, clk.Now())
	if !ok {
		t.Fatal("precondition: registration should succeed")
	}
	sess.armGoalWaitTimer()
	if !sess.CancelGoalWait(w.Lease.WaitID) {
		t.Fatal("CancelGoalWait on the live lease should succeed")
	}
	sess.mu.Lock()
	armed := sess.goalWaitTimer != nil
	sess.mu.Unlock()
	if armed {
		t.Fatal("timer must disarm when the last live lease is cancelled")
	}
	prompt, cont := sess.armGoalContinuation(false, true)
	if !cont || strings.Contains(prompt, goalWaitWakeTrailerPrefix) {
		t.Fatalf("post-cancel gate = (%q, %v), want a plain active drive with no wake trailer", prompt, cont)
	}
	if snap, _ := store.Snapshot(); snap.Status != goal.StatusActive {
		t.Fatalf("status = %q, want active after cancelling the last live lease", snap.Status)
	}
}

// TestGateTerminalLatchBlocksAfterFlaggedDrive pins the terminalPending latch
// (spec section 1 R7 M-I1): a wake that fires while a budget is also exceeded
// still drives (terminal-flagged), and the NEXT gate enforces the bound
// before rule 1 - dropping fresh wakes with the honest loss notice instead
// of starving on refire.
func TestGateTerminalLatchBlocksAfterFlaggedDrive(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("latch goal", clk.Now())
	// Spend the continuation budget while ACTIVE (parked gates never fold):
	// fold the interim judge to the cap first, then park the wait.
	for i := 0; i < goal.DefaultMaxContinuations; i++ {
		store.RecordContinuation(true, clk.Now())
	}
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Minute}, clk.Now()); !ok {
		t.Fatal("precondition: registration should succeed")
	}
	clk.Advance(2 * time.Minute)

	// Flagged drive: the wake still runs even though the budget is spent.
	prompt, cont := sess.armGoalContinuation(false, true)
	if !cont || !strings.Contains(prompt, goalWaitWakeTrailerPrefix) {
		t.Fatalf("flagged gate = (%q, %v), want the terminal-flagged wake drive (rule 1 first)", prompt, cont)
	}
	sess.mu.Lock()
	latched := sess.goalTerminalPending
	sess.mu.Unlock()
	if !latched {
		t.Fatal("terminalPending must latch on a flagged drive (budget also exceeded)")
	}

	// Next gate: bounds enforce before rule 1 - the goal blocks with the
	// budget verdict. Here the wake-tail consumed the flagged batch (no fresh
	// wakes stand), so the block carries no additional loss notice - the
	// drop-with-notice half is pinned by a store-level drain check below.
	prompt, cont = sess.armGoalContinuation(false, true)
	if cont || prompt != "" {
		t.Fatalf("latched gate = (%q, %v), want (\"\", false): bound blocks before rule 1", prompt, cont)
	}
	snap, _ := store.Snapshot()
	if snap.Status != goal.StatusBlocked {
		t.Fatalf("status = %q, want blocked after the latched bound fires", snap.Status)
	}
	if snap.StopReason != goal.VerdictBudgetExhausted {
		t.Fatalf("StopReason = %q, want %q", snap.StopReason, goal.VerdictBudgetExhausted)
	}
	if gsnap, _ := store.GoalSnapshot(); len(gsnap.PendingWake) != 0 {
		t.Fatalf("pendingWake after latched block = %+v, want consumed (nothing fresh stood to drop)", gsnap.PendingWake)
	}
}

// TestGateLatchedDropNoticesFreshWake pins the drop half of the latch (spec
// section 1 R7 M-I1): when a FRESH wake stands while latched and the bound is
// breached, the wake drops with the honest loss notice instead of driving.
func TestGateLatchedDropNoticesFreshWake(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("latch drop", clk.Now())
	for i := 0; i < goal.DefaultMaxContinuations; i++ {
		store.RecordContinuation(true, clk.Now())
	}
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Target: "first", Timeout: time.Minute}, clk.Now()); !ok {
		t.Fatal("precondition: registration should succeed")
	}
	clk.Advance(2 * time.Minute)
	// Flagged drive latches (budget also exceeded) - but do NOT run the wake
	// tail: register a second expired wait so a fresh wake stands at the next
	// gate while latched.
	if _, cont := sess.armGoalContinuation(false, true); !cont {
		t.Fatal("precondition: flagged wake drive should run")
	}
	sess.mu.Lock()
	latched := sess.goalTerminalPending
	sess.mu.Unlock()
	if !latched {
		t.Fatal("precondition: terminalPending must latch on the flagged drive")
	}
	// Register the second wait backdated so it is ALREADY expired at the next
	// gate: registering at (now - timeout - 1s) puts its deadline a second in
	// the past, so the gate's claim-before-decide converts it to a fresh wake
	// while latched - exercising the drop-with-notice half.
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Target: "second", Timeout: time.Minute}, clk.Now().Add(-time.Minute-time.Second)); !ok {
		t.Fatal("precondition: second registration should succeed")
	}
	prompt, cont := sess.armGoalContinuation(false, true)
	if cont || prompt != "" {
		t.Fatalf("latched gate with fresh wake = (%q, %v), want (\"\", false): bound blocks before rule 1", prompt, cont)
	}
	snap, _ := store.Snapshot()
	if snap.Status != goal.StatusBlocked || snap.StopReason != goal.VerdictBudgetExhausted {
		t.Fatalf("snapshot = %+v, want blocked/budget-exhausted", snap)
	}
	if n := countSteeringNotes(sess, goalWaitNoticePrefix); n != 1 {
		t.Fatalf("loss notices = %d, want exactly 1 for the dropped fresh wakes", n)
	}
	if gsnap, _ := store.GoalSnapshot(); len(gsnap.PendingWake) != 0 {
		t.Fatalf("pendingWake after latched drop = %+v, want drained with the notice", gsnap.PendingWake)
	}
}

// TestGateDeadlineBlocksWithDistinctVerdict pins rule 3 (spec section 1): a
// goal past its wall-clock deadline blocks with "deadline exceeded" - never
// collapsed into the stall or budget verdicts.
func TestGateDeadlineBlocksWithDistinctVerdict(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("deadline goal", clk.Now())
	clk.Advance(5 * time.Hour) // past the 4h default deadline
	prompt, cont := sess.armGoalContinuation(false, true)
	if cont || prompt != "" {
		t.Fatalf("deadline gate = (%q, %v), want (\"\", false)", prompt, cont)
	}
	snap, _ := store.Snapshot()
	if snap.Status != goal.StatusBlocked {
		t.Fatalf("status = %q, want blocked past the deadline", snap.Status)
	}
	if snap.StopReason != goal.VerdictDeadlineExceeded {
		t.Fatalf("StopReason = %q, want %q (distinct from budget/stall)", snap.StopReason, goal.VerdictDeadlineExceeded)
	}
}

func countSteeringNotes(sess *Session, substr string) int {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	n := 0
	for _, turn := range sess.history {
		if turn.Kind == schema.TurnSteering && strings.Contains(turn.Message.Text(), substr) {
			n++
		}
	}
	return n
}

// Slice-1 tool tests (spec sections 2, 7): goal_wait registers through the
// model-facing tool (hallucinated targets reject with the reason named; a
// valid until_time parks and arms the timer); goal_cancel_wait removes the
// live lease and disarms the timer; a claimed pendingWake still drives once.
func goalWaitToolCall(id, kind, target string, timeoutSeconds int64, label, matcher string) llm.ToolCallData {
	args, _ := json.Marshal(map[string]any{
		"kind":            kind,
		"target":          target,
		"timeout_seconds": timeoutSeconds,
		"label":           label,
		"matcher":         matcher,
	})
	return llm.ToolCallData{ID: id, Name: "goal_wait", Arguments: args, Type: "function"}
}

// TestGoalWaitToolHallucinatedTargetNamesReason pins the fail-closed
// registration path (spec section 2): a goal_wait naming a job with no record
// at all is rejected with an error naming the hallucinated target - never
// parked.
func TestGoalWaitToolHallucinatedTargetNamesReason(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("tool reject", clk.Now())
	res := sess.reg.ExecuteCall(context.Background(), sess.env, goalWaitToolCall("gw1", "until_job", "job_999", 60, "", ""))
	if !res.IsError {
		t.Fatalf("goal_wait on hallucinated job_999 should be IsError, got output: %s", res.Output)
	}
	if !strings.Contains(res.Output, "job_999") {
		t.Fatalf("goal_wait rejection %q must name the hallucinated target job_999", res.Output)
	}
	if snap, _ := store.Snapshot(); snap.Status != goal.StatusActive {
		t.Fatalf("status = %q, want active (a rejected registration must not park)", snap.Status)
	}
	if gsnap, _ := store.GoalSnapshot(); len(gsnap.Waits) != 0 {
		t.Fatalf("waits after reject = %+v, want none", gsnap.Waits)
	}
}

// TestGoalWaitToolValidUntilTimeParks pins the model-declared registration
// path (spec section 2): a valid until_time goal_wait parks the goal and arms
// the coalesced timer, carrying the lease's wait_id.
func TestGoalWaitToolValidUntilTimeParks(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("tool park", clk.Now())
	res := sess.reg.ExecuteCall(context.Background(), sess.env, goalWaitToolCall("gw1", "until_time", "", 60, "short-timer", ""))
	if res.IsError {
		t.Fatalf("valid goal_wait should succeed, got error: %s", res.Output)
	}
	gsnap, _ := store.GoalSnapshot()
	if len(gsnap.Waits) != 1 {
		t.Fatalf("waits after goal_wait = %+v, want one live lease", gsnap.Waits)
	}
	waitID := gsnap.Waits[0].Lease.WaitID
	if !strings.Contains(res.Output, waitID) {
		t.Fatalf("goal_wait output %q must carry the lease wait_id %q", res.Output, waitID)
	}
	if snap, _ := store.Snapshot(); snap.Status != goal.StatusWaiting {
		t.Fatalf("status = %q, want waiting after a valid goal_wait", snap.Status)
	}
	if prompt, ok := sess.armGoalContinuation(false, true); ok || prompt != "" {
		t.Fatalf("parked gate = (%q, %v), want held after the tool-registered wait", prompt, ok)
	}
}

// TestGoalCancelWaitToolDisarmsLiveLease pins the live-cancel half (spec
// section 7): goal_cancel_wait removes the live lease, returns the goal to
// active, and disarms the timer, so the goal re-arms normally with no wake.
func TestGoalCancelWaitToolDisarmsLiveLease(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("tool cancel", clk.Now())
	res := sess.reg.ExecuteCall(context.Background(), sess.env, goalWaitToolCall("gw1", "until_time", "", 60, "", ""))
	if res.IsError {
		t.Fatalf("precondition goal_wait: %s", res.Output)
	}
	gsnap, _ := store.GoalSnapshot()
	waitID := gsnap.Waits[0].Lease.WaitID
	sess.armGoalWaitTimer()

	args, _ := json.Marshal(map[string]any{"wait_id": waitID})
	cres := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{ID: "gc1", Name: "goal_cancel_wait", Arguments: args, Type: "function"})
	if cres.IsError {
		t.Fatalf("goal_cancel_wait on the live lease should succeed, got error: %s", cres.Output)
	}
	sess.mu.Lock()
	armed := sess.goalWaitTimer != nil
	sess.mu.Unlock()
	if armed {
		t.Fatal("timer must disarm when the tool cancels the last live lease")
	}
	if snap, _ := store.Snapshot(); snap.Status != goal.StatusActive {
		t.Fatalf("status = %q, want active after cancelling the last live lease", snap.Status)
	}
	if prompt, cont := sess.armGoalContinuation(false, true); !cont || strings.Contains(prompt, goalWaitWakeTrailerPrefix) {
		t.Fatalf("post-cancel gate = (%q, %v), want a plain active drive with no wake trailer", prompt, cont)
	}
}

// TestGoalCancelWaitToolKeepsClaimedWake pins cancel-vs-pendingWake through
// the tool (spec section 7): cancelling a claimed (no longer live) lease
// reports the miss, and the already-claimed wake still drives once with the
// cancellation noted - cancel never silently swallows a consumed fire.
func TestGoalCancelWaitToolKeepsClaimedWake(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("tool cancel race", clk.Now())
	w, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Minute}, clk.Now())
	if !ok {
		t.Fatal("precondition: registration should succeed")
	}
	clk.Advance(2 * time.Minute)
	if _, ok := store.ClaimFire(w.Lease.WaitID, "wait expired: "+w.Lease.Label, clk.Now()); !ok {
		t.Fatal("precondition: claim should consume the expired lease")
	}
	args, _ := json.Marshal(map[string]any{"wait_id": w.Lease.WaitID})
	cres := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{ID: "gc1", Name: "goal_cancel_wait", Arguments: args, Type: "function"})
	if cres.IsError {
		t.Fatalf("cancel of a claimed lease must not be a tool error, got: %s", cres.Output)
	}
	prompt, cont := sess.armGoalContinuation(false, true)
	if !cont || !strings.Contains(prompt, w.Lease.WaitID) {
		t.Fatalf("gate after tool cancel = (%q, %v), want the claimed wake to still drive once", prompt, cont)
	}
	if !strings.Contains(prompt, goal.CancelledWakeNote) {
		t.Fatalf("driven wake prompt must carry the cancellation note %q:\n%s", goal.CancelledWakeNote, prompt)
	}
}

// TestGoalWaitToolSizeCapsRejectNamesCheck pins the spec section 2 size caps
// through the tool: an over-cap matcher, target, and label each reject with
// the failed check named - never parked.
func TestGoalWaitToolSizeCapsRejectNamesCheck(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		kind    string
		target  string
		label   string
		matcher string
		want    string
	}{
		{"matcher cap", "until_time", "", "", strings.Repeat("m", goal.MaxMatcherBytes+1), "matcher"},
		{"label cap", "until_time", "", strings.Repeat("l", goal.MaxLabelRunes+1), "", "label"},
		{"label charset", "until_time", "", "bad\x07label", "", "label"},
		{"target cap", "until_job", strings.Repeat("j", goal.MaxURLBytes+1), "", "", "target"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			clk := agenttest.NewFakeClock()
			sess := newWaitGateSession(t, clk)
			defer sess.Close()
			wireKickAndNotify(sess)

			store := sess.getOrCreateGoalStore()
			store.Set("tool caps", clk.Now())
			res := sess.reg.ExecuteCall(context.Background(), sess.env, goalWaitToolCall("gw1", tc.kind, tc.target, 60, tc.label, tc.matcher))
			if !res.IsError {
				t.Fatalf("over-cap %s should be IsError, got output: %s", tc.name, res.Output)
			}
			if !strings.Contains(res.Output, tc.want) {
				t.Fatalf("rejection %q must name the failed check %q", res.Output, tc.want)
			}
			if snap, _ := store.Snapshot(); snap.Status != goal.StatusActive {
				t.Fatalf("status = %q, want active (a capped registration must not park)", snap.Status)
			}
		})
	}
}

// TestGoalWaitToolNoGoalNamesState pins the no-goal rejection (spec section
// 7: validation errors name the failed check): goal_wait with no goal set
// errors naming the missing goal instead of parking nothing.
func TestGoalWaitToolNoGoalNamesState(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	sess.getOrCreateGoalStore().Clear()
	res := sess.reg.ExecuteCall(context.Background(), sess.env, goalWaitToolCall("gw1", "until_time", "", 60, "", ""))
	if !res.IsError {
		t.Fatalf("goal_wait with no goal should be IsError, got output: %s", res.Output)
	}
	if !strings.Contains(res.Output, "no active goal") {
		t.Fatalf("rejection %q must name the missing goal", res.Output)
	}
}

// TestGoalWaitToolUnknownKindNamesKind pins the unknown-kind rejection:
// goal_wait with a kind outside the six registry kinds errors naming the
// kind - never parked.
func TestGoalWaitToolUnknownKindNamesKind(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("tool kind", clk.Now())
	res := sess.reg.ExecuteCall(context.Background(), sess.env, goalWaitToolCall("gw1", "until_never", "", 60, "", ""))
	if !res.IsError {
		t.Fatalf("goal_wait with unknown kind should be IsError, got output: %s", res.Output)
	}
	if !strings.Contains(res.Output, "until_never") {
		t.Fatalf("rejection %q must name the unknown kind", res.Output)
	}
	if snap, _ := store.Snapshot(); snap.Status != goal.StatusActive {
		t.Fatalf("status = %q, want active (an unknown kind must not park)", snap.Status)
	}
}

// TestGoalWaitToolRetainedTerminalCatchUp pins the terminal catch-up route
// through the tool (spec section 2): a goal_wait on a retained-terminal job
// fires immediately with the terminal outcome as the trigger - no live lease,
// no park.
func TestGoalWaitToolRetainedTerminalCatchUp(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("tool catch-up", clk.Now())
	store.SetSubstrate(&goalWaitToolSubstrate{jobs: map[string]goalWaitToolTarget{"job_7": {retained: true, excerpt: "job job_7 exited 0"}}})
	res := sess.reg.ExecuteCall(context.Background(), sess.env, goalWaitToolCall("gw1", "until_job", "job_7", 60, "", ""))
	if res.IsError {
		t.Fatalf("retained-terminal goal_wait should succeed, got error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "fired immediately") || !strings.Contains(res.Output, "exited 0") {
		t.Fatalf("catch-up output %q must carry the fire and the terminal excerpt", res.Output)
	}
	gsnap, _ := store.GoalSnapshot()
	if len(gsnap.Waits) != 0 {
		t.Fatalf("catch-up leaves no live lease: %+v", gsnap.Waits)
	}
	if len(gsnap.PendingWake) != 1 {
		t.Fatalf("catch-up must queue exactly one pending wake: %+v", gsnap.PendingWake)
	}
	if snap, _ := store.Snapshot(); snap.Status != goal.StatusActive {
		t.Fatalf("status = %q, want active (catch-up never parks)", snap.Status)
	}
}

// TestGoalWaitToolLiveJobParks pins the live-target route through the tool
// (spec section 2): a goal_wait on a running job parks on a live lease.
func TestGoalWaitToolLiveJobParks(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("tool live job", clk.Now())
	store.SetSubstrate(&goalWaitToolSubstrate{jobs: map[string]goalWaitToolTarget{"job_1": {live: true}}})
	res := sess.reg.ExecuteCall(context.Background(), sess.env, goalWaitToolCall("gw1", "until_job", "job_1", 60, "", ""))
	if res.IsError {
		t.Fatalf("live-job goal_wait should succeed, got error: %s", res.Output)
	}
	if snap, _ := store.Snapshot(); snap.Status != goal.StatusWaiting {
		t.Fatalf("status = %q, want waiting on a live job", snap.Status)
	}
}

// TestGoalCancelWaitToolUnknownIDReportsMiss pins the cancel-miss path (spec
// section 7): goal_cancel_wait naming no live lease is not a tool error - it
// reports the miss - and leaves the goal undisturbed.
func TestGoalCancelWaitToolUnknownIDReportsMiss(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("tool miss", clk.Now())
	args, _ := json.Marshal(map[string]any{"wait_id": "wait_404"})
	res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{ID: "gc1", Name: "goal_cancel_wait", Arguments: args, Type: "function"})
	if res.IsError {
		t.Fatalf("cancel of an unknown id must not be a tool error, got: %s", res.Output)
	}
	if !strings.Contains(res.Output, "wait_404") {
		t.Fatalf("miss output %q must name the unknown wait_id", res.Output)
	}
	if snap, _ := store.Snapshot(); snap.Status != goal.StatusActive {
		t.Fatalf("status = %q, want active (a missed cancel disturbs nothing)", snap.Status)
	}
}

// goalWaitToolTarget is one deterministic substrate entry for tool-level
// registration tests (live = wake-capable; retained = terminal inside the
// record-retention window with the terminal outcome excerpt).
type goalWaitToolTarget struct {
	live     bool
	retained bool
	excerpt  string
}

// goalWaitToolSubstrate is a deterministic in-memory predicate substrate for
// tool-level registration tests. Absent entries model hallucinated targets.
type goalWaitToolSubstrate struct {
	jobs map[string]goalWaitToolTarget
}

func (f *goalWaitToolSubstrate) LookupJob(id string) (bool, bool, string, bool) {
	t, ok := f.jobs[id]
	if !ok {
		return false, false, "", false
	}
	return t.live, t.retained, t.excerpt, true
}

func (f *goalWaitToolSubstrate) LookupDelegate(id string) (bool, bool, string, bool) {
	return false, false, "", false
}

func (f *goalWaitToolSubstrate) StatFile(path string) (string, bool) { return "", false }

func (f *goalWaitToolSubstrate) LookupApproval(contentKey, generation string) bool { return false }

func (f *goalWaitToolSubstrate) LookupChild(id string) bool { return false }

func (f *goalWaitToolSubstrate) CheckURL(rawURL string, timeout time.Duration) bool { return false }

// TestGoalWaitToolChildFromChildScopedOut pins the slice-1 honest boundary
// (spec section 8): a child session registering until_child is rejected with
// the scoped-out reason named - never parked on an unwired forward path.
func TestGoalWaitToolChildFromChildScopedOut(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newSession(t, withConfig(SessionConfig{clock: clk, spawn: spawnConfig{parentSessionID: "parent-root"}}))
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("child wait", clk.Now())
	res := sess.reg.ExecuteCall(context.Background(), sess.env, goalWaitToolCall("gw1", "until_child", "child_1", 60, "", ""))
	if !res.IsError {
		t.Fatalf("until_child from a child session should be IsError, got output: %s", res.Output)
	}
	if !strings.Contains(res.Output, "child waits scoped out in this slice") {
		t.Fatalf("rejection %q must carry the scoped-out boundary reason", res.Output)
	}
	if snap, _ := store.Snapshot(); snap.Status != goal.StatusActive {
		t.Fatalf("status = %q, want active (a scoped-out registration must not park)", snap.Status)
	}
}

// TestGoalWaitToolEventEmptyTargetRequiresTarget pins the tool-level
// target-required check for until_event subtypes with a durable target (spec
// section 2): file_modified / http_match with an empty target reject with
// `target is required` - never falling through to a substrate error.
func TestGoalWaitToolEventEmptyTargetRequiresTarget(t *testing.T) {
	t.Parallel()
	for _, subtype := range []string{"file_modified", "http_match"} {
		t.Run(subtype, func(t *testing.T) {
			t.Parallel()
			clk := agenttest.NewFakeClock()
			sess := newWaitGateSession(t, clk)
			defer sess.Close()
			wireKickAndNotify(sess)

			store := sess.getOrCreateGoalStore()
			store.Set("tool event target", clk.Now())
			args, _ := json.Marshal(map[string]any{"kind": "until_event", "event_subtype": subtype, "timeout_seconds": 60})
			res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{ID: "gw1", Name: "goal_wait", Arguments: args, Type: "function"})
			if !res.IsError {
				t.Fatalf("until_event %s with empty target should be IsError, got output: %s", subtype, res.Output)
			}
			if !strings.Contains(res.Output, "target is required") {
				t.Fatalf("rejection %q must name the required target", res.Output)
			}
			if snap, _ := store.Snapshot(); snap.Status != goal.StatusActive {
				t.Fatalf("status = %q, want active (a target-less registration must not park)", snap.Status)
			}
		})
	}
}

// TestGoalWaitToolTimeoutRangeRejects pins the tool-level timeout range
// (spec section 2: required deadline, cap 24h): a zero/non-positive or
// over-cap timeout_seconds rejects with the range named - never parked.
func TestGoalWaitToolTimeoutRangeRejects(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		timeout int64
	}{
		{"zero", 0},
		{"negative", -5},
		{"over cap", 86401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			clk := agenttest.NewFakeClock()
			sess := newWaitGateSession(t, clk)
			defer sess.Close()
			wireKickAndNotify(sess)

			store := sess.getOrCreateGoalStore()
			store.Set("tool timeout", clk.Now())
			res := sess.reg.ExecuteCall(context.Background(), sess.env, goalWaitToolCall("gw1", "until_time", "", tc.timeout, "", ""))
			if !res.IsError {
				t.Fatalf("timeout %d should be IsError, got output: %s", tc.timeout, res.Output)
			}
			if !strings.Contains(res.Output, "timeout_seconds") {
				t.Fatalf("rejection %q must name timeout_seconds", res.Output)
			}
			if snap, _ := store.Snapshot(); snap.Status != goal.StatusActive {
				t.Fatalf("status = %q, want active (a range-violating registration must not park)", snap.Status)
			}
		})
	}
}

// TestGoalCancelWaitToolLiveCancelEmitsGoalUpdated pins the emission parity
// (spec section 7): a successful goal_cancel_wait publishes GOAL_UPDATED
// carrying the store state - cancelling the last live lease flips
// waiting->active visibly.
func TestGoalCancelWaitToolLiveCancelEmitsGoalUpdated(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("tool cancel emit", clk.Now())
	res := sess.reg.ExecuteCall(context.Background(), sess.env, goalWaitToolCall("gw1", "until_time", "", 60, "", ""))
	if res.IsError {
		t.Fatalf("precondition goal_wait: %s", res.Output)
	}
	assertGoalUpdatedMatchesStore(t, sess, nextGoalUpdated(t, sess))
	assertNoGoalUpdated(t, sess)
	gsnap, _ := store.GoalSnapshot()
	args, _ := json.Marshal(map[string]any{"wait_id": gsnap.Waits[0].Lease.WaitID})
	cres := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{ID: "gc1", Name: "goal_cancel_wait", Arguments: args, Type: "function"})
	if cres.IsError {
		t.Fatalf("goal_cancel_wait should succeed, got error: %s", cres.Output)
	}
	assertGoalUpdatedMatchesStore(t, sess, nextGoalUpdated(t, sess))
	assertNoGoalUpdated(t, sess)
}

// TestGoalWaitToolsRegisteredRegistryOnlyNonReadOnly pins the registration
// shape (mirroring TestManageWorktreeToolRegisteredRegistryOnlyNonReadOnly):
// goal_wait and goal_cancel_wait are registered directly on the registry (not
// part of the provider profile's own tool definitions, like
// update_goal/task_list), they are non-read-only (they mutate the wait
// registry), and they are advertised to the model via ToolDefinitions().
func TestGoalWaitToolsRegisteredRegistryOnlyNonReadOnly(t *testing.T) {
	t.Parallel()
	s := newSession(t)

	for _, name := range []string{"goal_wait", "goal_cancel_wait"} {
		rt := s.reg.Get(name)
		if rt == nil {
			t.Fatalf("registry is missing %s", name)
		}
		if rt.ReadOnly {
			t.Errorf("%s.ReadOnly = true, want false (wait registration/cancel mutates the registry)", name)
		}
		for _, td := range s.profile.ToolDefinitions() {
			if td.Name == name {
				t.Errorf("%s must not be part of the provider profile's tool definitions (registry-only, like update_goal)", name)
			}
		}
		found := false
		for _, td := range s.ToolDefinitions() {
			if td.Name == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s not advertised in ToolDefinitions()", name)
		}
	}
}
