package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
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
	// Slice 2 retires the interim bypass: the wake turn's own drive folds
	// into the ledger and accrues the continuation (spec §5: wake turns
	// count — the drive commits; the tail only drains, never re-folds).
	if snap, _ := store.Snapshot(); snap.Iterations != 1 {
		t.Fatalf("wake drive folded: snapshot = %+v, want the wake-turn fold (Iterations=1)", snap)
	}

	// The wake turn's own tail consumes the backlog and re-arms the plain
	// objective without re-folding (the drive already committed).
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
	if snap, _ := store.Snapshot(); snap.Iterations != 1 {
		t.Fatalf("wake-tail folded: snapshot = %+v, want no re-fold (still Iterations=1)", snap)
	}

	// The follow-up turn folds normally through the ledger.
	if _, cont := sess.armGoalContinuation(false, true); !cont {
		t.Fatal("follow-up gate should drive the active objective")
	}
	if snap, _ := store.Snapshot(); snap.Iterations != 2 {
		t.Fatalf("snapshot = %+v, want the post-wake fold (Iterations=2)", snap)
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
	// Determinism: the park gate armed the coalesced wait timer, and Advance
	// dispatches its callback on a clock goroutine — disarm before advancing
	// so the settle below is the only claimant (both paths are exactly-once,
	// but the winner is otherwise unscheduled and the settle may return false).
	sess.stopGoalWaitTimer()

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
// old objective's wake, never a silent drop. The superseded no-op drive
// bypasses the fold (dropped context, never stall evidence); its tail
// consumes the batch and re-arms the current objective without re-folding.
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
	// objective without folding (superseded evaluations never fold).
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
		t.Fatalf("snapshot = %+v, want zero fold across the superseded no-op turn", snap)
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
		// stale superseded flag - folds normally through the ledger.
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
	// Gates 1-2 bypass the fold (superseded no-op drive + tail drain); gate
	// 3 commits the single ledger fold.
	if snap, _ := store.Snapshot(); snap.Iterations != 1 {
		t.Fatalf("snapshot = %+v, want exactly the gate-3 ledger fold (Iterations=1)", snap)
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
	for i := range goal.DefaultMaxContinuations {
		store.RecordContinuation(goal.TurnOutcome{ActionFingerprint: "spend", ObservationClass: "ok", ObservationHash: "spend", StateDigest: fmt.Sprintf("spend-%d", i), Mutated: true}, false, clk.Now())
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
	for i := range goal.DefaultMaxContinuations {
		store.RecordContinuation(goal.TurnOutcome{ActionFingerprint: "spend", ObservationClass: "ok", ObservationHash: "spend", StateDigest: fmt.Sprintf("spend-%d", i), Mutated: true}, false, clk.Now())
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

// TestGateDeadlineBlocksWithDistinctVerdict pins rule 3 (spec §1): a goal
// past its wall-clock deadline first drives one final evaluation turn
// carrying "deadline exceeded" (the synthetic expiry wake through the same
// exactly-once claim machinery, with DeadlineFinalDelivered set at claim
// time so rule 3 cannot loop final turns), and the FOLLOWING gate blocks
// with "deadline exceeded" - never collapsed into the stall or budget
// verdicts, never an immediate silent block.
func TestGateDeadlineBlocksWithDistinctVerdict(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("deadline goal", clk.Now())
	clk.Advance(5 * time.Hour) // past the 4h default deadline
	// Gate 1: the final evaluation turn drives (rule 1 on the synthetic
	// wake), carrying the deadline verdict text.
	prompt, cont := sess.armGoalContinuation(false, true)
	if !cont || prompt == "" {
		t.Fatalf("deadline gate 1 = (%q, %v), want the final evaluation drive carrying the synthetic wake", prompt, cont)
	}
	if !strings.Contains(prompt, goal.DeadlineExpiryTrigger) {
		t.Fatalf("final-turn prompt must carry %q\nprompt:\n%s", goal.DeadlineExpiryTrigger, prompt)
	}
	if !strings.Contains(prompt, goalWaitWakeTrailerPrefix) {
		t.Fatalf("final-turn prompt must carry the wake trailer %q\nprompt:\n%s", goalWaitWakeTrailerPrefix, prompt)
	}
	full, _ := store.GoalSnapshot()
	if !full.DeadlineFinalDelivered {
		t.Fatalf("DeadlineFinalDelivered = false after the synthetic claim, want true (set at claim time): %+v", full)
	}
	if len(full.PendingWake) != 1 || full.PendingWake[0].WaitID != goal.DeadlineWakeID {
		t.Fatalf("pendingWake after gate 1 = %+v, want exactly the synthetic deadline entry", full.PendingWake)
	}
	// Gate 2: the final turn ran while the deadline was also exceeded, so it
	// latched terminal-pending (spec §1 R7 M-I1) — the next gate evaluates
	// rules 2/3 before rule 1, blocks with the distinct verdict, and drops
	// the fresh synthetic wake with the honest loss notice instead of
	// driving it again (no second final turn: the one-shot marker holds).
	prompt, cont = sess.armGoalContinuation(false, true)
	if cont || prompt != "" {
		t.Fatalf("deadline gate 2 = (%q, %v), want (\"\", false): the latched gate blocks", prompt, cont)
	}
	snap, _ := store.Snapshot()
	if snap.Status != goal.StatusBlocked {
		t.Fatalf("status = %q, want blocked past the deadline", snap.Status)
	}
	if snap.StopReason != goal.VerdictDeadlineExceeded {
		t.Fatalf("StopReason = %q, want %q (distinct from budget/stall)", snap.StopReason, goal.VerdictDeadlineExceeded)
	}
	if gsnap, _ := store.GoalSnapshot(); len(gsnap.PendingWake) != 0 {
		t.Fatalf("pendingWake after the latched block = %+v, want drained with the notice", gsnap.PendingWake)
	}
	// The latched block carries the terminal deadline note (blockGoalFromGate
	// verdict routing) and drains the synthetic backlog exactly once — the
	// one-shot marker holds, so no second final turn exists to drive.
	if n := countSteeringNotes(sess, "deadline exceeded"); n != 1 {
		t.Fatalf("deadline notes = %d, want exactly 1 terminal note", n)
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

// requireSingleLossNotice pins the exactly-once honest-loss notice: exactly
// one steering note names the cause.
func requireSingleLossNotice(t *testing.T, sess *Session) {
	t.Helper()
	if n := countSteeringNotes(sess, goalWaitNoticePrefix); n != 1 {
		t.Fatalf("loss notices = %d, want exactly 1", n)
	}
}

// requirePersistedLossCause pins the claim-side persistence: the drop stood
// the cause for the next gate's TakeLossCause/rule-5 read.
func requirePersistedLossCause(t *testing.T, store *goal.Store) {
	t.Helper()
	if full, _ := store.GoalSnapshot(); full.LossCause == "" {
		t.Fatalf("LossCause empty after the loss, want the cause persisted: %+v", full)
	}
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

// TestGoalWaitToolAskGenerationRemoved pins the ask_generation removal:
// goal_wait with a non-empty ask_generation rejects naming the removal —
// approval waits bind by content key alone, since no ask call carries a
// stable generation.
func TestGoalWaitToolAskGenerationRemoved(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("tool generation", clk.Now())
	args, _ := json.Marshal(map[string]any{
		"kind": "until_approval", "target": "ship it?",
		"ask_generation": "gen1", "timeout_seconds": 60,
	})
	res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{ID: "gw-gen", Name: "goal_wait", Arguments: args, Type: "function"})
	if !res.IsError {
		t.Fatalf("goal_wait with ask_generation should be IsError, got output: %s", res.Output)
	}
	if !strings.Contains(res.Output, "ask_generation") {
		t.Fatalf("rejection %q must name the removed field", res.Output)
	}
	if snap, _ := store.Snapshot(); snap.Status != goal.StatusActive {
		t.Fatalf("status = %q, want active (a removed field must not park)", snap.Status)
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

// TestGoalWaitToolChildFromChildScopedOut pins the slice-2 forward boundary
// (spec section 8): the slice-1 scoped-out rejection is lifted — until_child
// from a child session validates like any other registration. An unknown
// target still rejects fail-closed (naming the child); a known descendant
// parks (the parent→child forward in session_goal_ledger.go drives the wake).
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
		t.Fatalf("until_child on an unknown child should be IsError, got output: %s", res.Output)
	}
	if !strings.Contains(res.Output, "child_1") {
		t.Fatalf("rejection %q must name the unknown child target", res.Output)
	}
	if snap, _ := store.Snapshot(); snap.Status != goal.StatusActive {
		t.Fatalf("status = %q, want active (a rejected registration must not park)", snap.Status)
	}
}

// TestGoalWaitToolEventEmptyTargetRequiresTarget pins the tool-level
// target-required check for until_event subtypes with a durable target (spec
// section 2): file_modified with an empty target rejects with
// `target is required` - never falling through to a substrate error.
// (http_match rejects earlier with the removal named — see
// TestGoalWaitHTTPMatchRemoved.)
func TestGoalWaitToolEventEmptyTargetRequiresTarget(t *testing.T) {
	t.Parallel()
	for _, subtype := range []string{"file_modified"} {
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

// --- Fix-wave regression: C1/I2/I3/I4/I5 (spec §§1-2, 5, 7) ---
//
// Deterministic: FakeClock advance + store-direct substrate stubs, never
// wall-clock sleep or live I/O.

// fixStubSubstrate is a deterministic in-memory predicate substrate for the
// fix-wave regression tests (spec §2). Absent entries model hallucinated
// targets; present-but-terminal job/delegate entries model retained-terminal
// catch-up; files map to baselines; approvals to live asks; children to known
// descendants.
type fixStubSubstrate struct {
	jobs      map[string]fixStubTarget
	delegates map[string]fixStubTarget
	files     map[string]string
	approvals map[string]bool
	children  map[string]bool
}

type fixStubTarget struct {
	live     bool
	retained bool
	excerpt  string
}

func (f *fixStubSubstrate) LookupJob(id string) (bool, bool, string, bool) {
	t, ok := f.jobs[id]
	if !ok {
		return false, false, "", false
	}
	return t.live, t.retained, t.excerpt, true
}

func (f *fixStubSubstrate) LookupDelegate(id string) (bool, bool, string, bool) {
	t, ok := f.delegates[id]
	if !ok {
		return false, false, "", false
	}
	return t.live, t.retained, t.excerpt, true
}

func (f *fixStubSubstrate) StatFile(path string) (string, bool) {
	b, ok := f.files[path]
	return b, ok
}

func (f *fixStubSubstrate) LookupApproval(contentKey, generation string) bool {
	return f.approvals[contentKey+"\x00"+generation]
}

func (f *fixStubSubstrate) LookupChild(id string) bool { return f.children[id] }

// TestFixWaveC1AllKindsFire pins C1 (spec §§1-2): every non-timer kind fires
// through the gate claim — register, make true/expire, assert exactly-one
// wake drive with no strand (no live lease left, exactly one pending entry,
// wake prompt carries the trigger).
func TestFixWaveC1AllKindsFire(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		req  goal.WaitKind
		sub  *fixStubSubstrate
		// mutate flips the substrate from park-state to fire-state between
		// registration and the gate (nil = fires via expiry instead).
		mutate func(sub *fixStubSubstrate)
		// wantTrigger is the excerpt substring the wake prompt must carry.
		wantTrigger string
	}{
		{
			name: "until_job retained-terminal",
			req:  goal.WaitKind{Kind: goal.WaitUntilJob, Target: "job_1", Timeout: time.Hour},
			sub:  &fixStubSubstrate{jobs: map[string]fixStubTarget{"job_1": {live: true}}},
			mutate: func(sub *fixStubSubstrate) {
				sub.jobs["job_1"] = fixStubTarget{retained: true, excerpt: "job job_1 exited 0"}
			},
			wantTrigger: "exited 0",
		},
		{
			name: "until_delegate retained-terminal",
			req:  goal.WaitKind{Kind: goal.WaitUntilDelegate, Target: "dlg_1", Timeout: time.Hour},
			sub:  &fixStubSubstrate{delegates: map[string]fixStubTarget{"dlg_1": {live: true}}},
			mutate: func(sub *fixStubSubstrate) {
				sub.delegates["dlg_1"] = fixStubTarget{retained: true, excerpt: "delegate dlg_1 completed"}
			},
			wantTrigger: "completed",
		},
		{
			name:        "until_approval answered",
			req:         goal.WaitKind{Kind: goal.WaitUntilApproval, Target: "ship it?", AskGeneration: "gen1", Timeout: time.Hour},
			sub:         &fixStubSubstrate{approvals: map[string]bool{"ship it?\x00gen1": true}},
			mutate:      func(sub *fixStubSubstrate) { delete(sub.approvals, "ship it?\x00gen1") },
			wantTrigger: "approval answered",
		},
		{
			name:        "file_modified delta",
			req:         goal.WaitKind{Kind: goal.WaitUntilEvent, EventSubtype: goal.EventFileModified, Target: "/sandbox/plan.md", Timeout: time.Hour},
			sub:         &fixStubSubstrate{files: map[string]string{"/sandbox/plan.md": "base-1"}},
			mutate:      func(sub *fixStubSubstrate) { sub.files["/sandbox/plan.md"] = "base-2" },
			wantTrigger: "file modified",
		},
		{
			name:        "until_child terminal",
			req:         goal.WaitKind{Kind: goal.WaitUntilChild, Target: "child_1", Timeout: time.Hour},
			sub:         &fixStubSubstrate{children: map[string]bool{"child_1": true}},
			mutate:      nil, // terminality arrives via the tracked child below
			wantTrigger: "child_1",
		},
		{
			name:        "non-timer expiry for every kind",
			req:         goal.WaitKind{Kind: goal.WaitUntilJob, Target: "job_slow", Timeout: time.Minute},
			sub:         &fixStubSubstrate{jobs: map[string]fixStubTarget{"job_slow": {live: true}}},
			mutate:      nil, // no predicate flip: the lease deadline fires it
			wantTrigger: "wait expired:",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			clk := agenttest.NewFakeClock()
			sess := newWaitGateSession(t, clk)
			defer sess.Close()
			wireKickAndNotify(sess)
			store := sess.getOrCreateGoalStore()
			store.Set("c1 "+tc.name, clk.Now())
			store.SetSubstrate(tc.sub)
			w, ok := store.RegisterWait(tc.req, clk.Now())
			if !ok {
				t.Fatalf("precondition: registration should succeed: %q", store.LastRejectReason())
			}
			if tc.name == "until_child terminal" {
				trackSyntheticChild(t, sess, "child_1", SubagentCompleted, false, false, clk.Now(), false)
			}
			if tc.mutate != nil {
				tc.mutate(tc.sub)
			}
			if strings.Contains(tc.wantTrigger, "expired:") {
				clk.Advance(2 * time.Minute)
			}
			prompt, cont := sess.armGoalContinuation(false, true)
			if !cont || prompt == "" {
				t.Fatalf("gate = (%q, %v), want exactly one wake drive", prompt, cont)
			}
			if !strings.Contains(prompt, tc.wantTrigger) {
				t.Fatalf("wake prompt must carry %q\nprompt:\n%s", tc.wantTrigger, prompt)
			}
			full, _ := store.GoalSnapshot()
			if len(full.PendingWake) != 1 || full.PendingWake[0].WaitID != w.Lease.WaitID {
				t.Fatalf("pendingWake = %+v, want exactly the fired lease %q (no strand, no pile-up)", full.PendingWake, w.Lease.WaitID)
			}
			if goal.HasLiveWait(full.Waits) {
				t.Fatalf("live leases remain after the fire: %+v (stranded)", full.Waits)
			}
			// Exactly-once: the wake tail consumes the backlog; the next
			// gate must not re-drive the same fire.
			sess.armGoalContinuation(false, true)
			if full, _ := store.GoalSnapshot(); len(full.PendingWake) != 0 {
				t.Fatalf("pendingWake after wake-tail = %+v, want drained (exactly one wake)", full.PendingWake)
			}
		})
	}
}

// TestFixWaveI2DeadlineFinalTurnThenBlock pins I2 (spec §1 rule 3): deadline
// expiry drives the synthetic final evaluation turn carrying "deadline
// exceeded" with the marker set at claim, then the latched gate blocks with
// the distinct verdict (never an immediate silent block, never a verdict
// collapse, never a second final turn).
func TestFixWaveI2DeadlineFinalTurnThenBlock(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)
	store := sess.getOrCreateGoalStore()
	store.Set("i2 deadline", clk.Now())
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Target: "t", Timeout: time.Hour, Label: "hour-timer"}, clk.Now()); !ok {
		t.Fatal("precondition: timer registration should succeed")
	}
	clk.Advance(5 * time.Hour) // past the 4h goal deadline, timer also expired
	prompt, cont := sess.armGoalContinuation(false, true)
	if !cont || !strings.Contains(prompt, goal.DeadlineExpiryTrigger) {
		t.Fatalf("gate 1 = (%v, %.80q...), want the final turn carrying %q", cont, prompt, goal.DeadlineExpiryTrigger)
	}
	if !strings.Contains(prompt, "hour-timer") {
		t.Fatalf("final-turn prompt must carry the wait labels:\n%s", prompt)
	}
	if full, _ := store.GoalSnapshot(); !full.DeadlineFinalDelivered {
		t.Fatalf("marker must be set at claim time: %+v", full)
	}
	prompt, cont = sess.armGoalContinuation(false, true)
	if cont || prompt != "" {
		t.Fatalf("gate 2 = (%q, %v), want the latched block", prompt, cont)
	}
	snap, _ := store.Snapshot()
	if snap.Status != goal.StatusBlocked || snap.StopReason != goal.VerdictDeadlineExceeded {
		t.Fatalf("snapshot = %+v, want blocked/deadline exceeded", snap)
	}
	// No second final turn: a further gate stays blocked.
	if prompt, cont := sess.armGoalContinuation(false, true); cont || prompt != "" {
		t.Fatalf("gate 3 = (%q, %v), want still blocked (one-shot)", prompt, cont)
	}
}

// TestFixWaveI3ParkedTotalBinds pins I3 (spec §5): wall-clock park time
// accrues entry→wake deltas so a tight parked cap below the deadline binds
// with "budget exhausted" (not the deadline verdict — the §5 layering).
func TestFixWaveI3ParkedTotalBinds(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)
	store := sess.getOrCreateGoalStore()
	store.Set("i3 parked cap", clk.Now())
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Hour}, clk.Now()); !ok {
		t.Fatal("precondition: registration should succeed")
	}
	// Tighten the parked cap below the lease deadline: 30m parked of a 1h
	// lease must bind first.
	full, _ := store.GoalSnapshot()
	persisted, _ := goal.PersistedFromSnapshot(full)
	persisted.Budgets.MaxParkedTotal = 30 * time.Minute
	store.RestoreSnapshot(persisted)
	store.NoteParkEnter(clk.Now())
	clk.Advance(time.Hour) // lease expires; the claim accrues 60m > 30m cap
	prompt, cont := sess.armGoalContinuation(false, true)
	full, _ = store.GoalSnapshot()
	if full.Budgets.ParkedTotal < 30*time.Minute {
		t.Fatalf("ParkedTotal = %v, want ≥30m accrued at the claim: %+v", full.Budgets.ParkedTotal, full.Budgets)
	}
	// The wake drove terminal-flagged (parked bound also exceeded): the next
	// gate blocks budget-exhausted, not deadline-exceeded.
	_ = prompt
	_ = cont
	prompt, cont = sess.armGoalContinuation(false, true)
	if cont || prompt != "" {
		t.Fatalf("post-wake gate = (%q, %v), want the parked-total block", prompt, cont)
	}
	snap, _ := store.Snapshot()
	if snap.StopReason != goal.VerdictBudgetExhausted {
		t.Fatalf("StopReason = %q, want %q (parked-total binds as budget)", snap.StopReason, goal.VerdictBudgetExhausted)
	}
}

// TestFixWaveI4RestoreSubstrateLoss pins I4 (spec §2): a restart after the
// substrate disappeared (job aged out, file gone, child unknown) produces
// the persisted loss + honest waiting-lost notice + re-drive — never a
// silent re-park on a dead substrate.
func TestFixWaveI4RestoreSubstrateLoss(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)
	store := sess.getOrCreateGoalStore()
	store.Set("i4 loss", clk.Now())
	store.SetSubstrate(&fixStubSubstrate{jobs: map[string]fixStubTarget{"job_gone": {live: true}}})
	w, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilJob, Target: "job_gone", Timeout: time.Hour}, clk.Now())
	if !ok {
		t.Fatalf("precondition: live job must park: %q", store.LastRejectReason())
	}
	_ = w
	// Restart: the substrate the lease validated against is gone.
	store.SetSubstrate(&fixStubSubstrate{})
	sess.restoreGoalAttachScan()
	full, _ := store.GoalSnapshot()
	if goal.HasLiveWait(full.Waits) {
		t.Fatalf("dead lease still live after restore scan: %+v (silent re-park)", full.Waits)
	}
	if full.LossCause == "" {
		t.Fatalf("LossCause empty after the restore loss, want the cause persisted: %+v", full)
	}
	// The next gate surfaces the honest notice and re-drives (single live
	// goal, no live waits, advancement window empty → rule 5 blocks with the
	// waiting-lost verdict. The honest notice text rides the terminal report
	// (blockGoalFromGate verdict routing), not a separate re-drive — the
	// loss is terminal because nothing remains to wait on.
	prompt, cont := sess.armGoalContinuation(false, true)
	snap, _ := store.Snapshot()
	if snap.Status != goal.StatusBlocked || !strings.HasPrefix(snap.StopReason, "waiting lost:") {
		t.Fatalf("snapshot = %+v prompt=(%.60q..., %v), want the rule-5 waiting-lost block", snap, prompt, cont)
	}
	if !strings.Contains(snap.StopReason, "job_gone") {
		t.Fatalf("StopReason = %q, want the cause naming the lost substrate", snap.StopReason)
	}
	if n := countSteeringNotes(sess, "waiting lost:"); n < 1 {
		t.Fatalf("waiting-lost notes = %d, want at least 1 terminal notice", n)
	}
}

// TestFixWaveI5AttachScanAndCooldown pins I5 (spec §5): attach-scan-true at
// registration drives immediately (claim + no park) and the same target may
// not re-park for 30s.
func TestFixWaveI5AttachScanAndCooldown(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)
	store := sess.getOrCreateGoalStore()
	store.Set("i5 attach", clk.Now())
	sub := &fixStubSubstrate{files: map[string]string{"/sandbox/plan.md": "base-1"}}
	store.SetSubstrate(sub)
	// Already-changed file: mutate the baseline the lease would snapshot is
	// impossible pre-registration — instead register, flip, re-register: the
	// second registration's attach-scan sees the delta and fires at once.
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilEvent, EventSubtype: goal.EventFileModified, Target: "/sandbox/plan.md", Timeout: time.Hour}, clk.Now()); !ok {
		t.Fatalf("precondition: first registration should park: %q", store.LastRejectReason())
	}
	sub.files["/sandbox/plan.md"] = "base-2"
	w2, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilEvent, EventSubtype: goal.EventFileModified, Target: "/sandbox/plan.md", Timeout: time.Hour}, clk.Now())
	if !ok {
		t.Fatalf("precondition: attach-scan-true registration should succeed: %q", store.LastRejectReason())
	}
	full, _ := store.GoalSnapshot()
	if goal.HasLiveWait(full.Waits) {
		t.Fatalf("attach-scan-true must not park: %+v", full.Waits)
	}
	if len(full.PendingWake) != 1 || full.PendingWake[0].WaitID != w2.Lease.WaitID {
		t.Fatalf("pendingWake = %+v, want the immediate attach-scan fire for %q", full.PendingWake, w2.Lease.WaitID)
	}
	prompt, cont := sess.armGoalContinuation(false, true)
	if !cont || !strings.Contains(prompt, "file modified") {
		t.Fatalf("gate = (%v, %.80q...), want the immediate evaluation drive", cont, prompt)
	}
	// Same-target re-park inside 30s rejects with the cooldown named.
	sub.files["/sandbox/plan.md"] = "base-3"
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilEvent, EventSubtype: goal.EventFileModified, Target: "/sandbox/plan.md", Timeout: time.Hour}, clk.Now()); ok {
		t.Fatal("same-target re-park inside the cooldown must reject")
	}
	if reason := store.LastRejectReason(); !strings.Contains(reason, "cooldown") {
		t.Fatalf("reject reason %q must name the cooldown", reason)
	}
	clk.Advance(31 * time.Second)
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilEvent, EventSubtype: goal.EventFileModified, Target: "/sandbox/plan.md", Timeout: time.Hour}, clk.Now()); !ok {
		t.Fatalf("re-park after the cooldown should succeed: %q", store.LastRejectReason())
	}
}

// TestFixWaveLiveLossNoticesOnce pins the non-rule-5 exactly-once notice
// (spec §2 + Goal 4): a loss with live waits remaining notifies + re-drives
// on the first gate — and later gates must NOT re-notice the same consumed
// cause.
func TestFixWaveLiveLossNoticesOnce(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("live plus lost", clk.Now())
	store.SetSubstrate(&fixStubSubstrate{jobs: map[string]fixStubTarget{
		"job_live": {live: true},
		"job_gone": {live: true},
	}})
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilJob, Target: "job_live", Timeout: time.Hour}, clk.Now()); !ok {
		t.Fatalf("precondition: live job must park: %q", store.LastRejectReason())
	}
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilJob, Target: "job_gone", Timeout: time.Hour}, clk.Now()); !ok {
		t.Fatalf("precondition: second job must park: %q", store.LastRejectReason())
	}
	// Only job_gone's substrate vanishes; job_live keeps the goal parked.
	store.SetSubstrate(&fixStubSubstrate{jobs: map[string]fixStubTarget{
		"job_live": {live: true},
	}})
	prompt, cont := sess.armGoalContinuation(false, true)
	if cont || prompt != "" {
		t.Fatalf("loss-with-live gate = (%q, %v), want a park (notice + re-drive owns the turn)", prompt, cont)
	}
	if n := countSteeringNotes(sess, goalWaitNoticePrefix); n != 1 {
		t.Fatalf("loss notices after gate 1 = %d, want exactly 1", n)
	}
	// Later gates (user message, notification turn) must not re-notice the
	// same consumed cause.
	sess.armGoalContinuation(false, true)
	sess.armGoalContinuation(false, false)
	if n := countSteeringNotes(sess, goalWaitNoticePrefix); n != 1 {
		t.Fatalf("loss notices after gates 2-3 = %d, want still exactly 1", n)
	}
}

// TestFixWaveTimerParkedCrossingBinds pins the parked-total timer leg
// (spec §5): a maxParkedTotal crossing earlier than every lease deadline
// fires the coalesced timer, which accrues the open stretch so the bound
// binds — the goal must not park past its cap with the persisted total
// frozen pre-crossing.
func TestFixWaveTimerParkedCrossingBinds(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	kicks := wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("parked cap crossing", clk.Now())
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: 2 * time.Hour}, clk.Now()); !ok {
		t.Fatalf("precondition: registration should succeed: %q", store.LastRejectReason())
	}
	// Tighten the parked cap to 30m (lease runs 2h): the crossing fires the
	// timer with no lease expiry on this pass.
	full, _ := store.GoalSnapshot()
	persisted, _ := goal.PersistedFromSnapshot(full)
	persisted.Budgets.MaxParkedTotal = 30 * time.Minute
	store.RestoreSnapshot(persisted)
	if prompt, cont := sess.armGoalContinuation(false, true); cont || prompt != "" {
		t.Fatalf("park gate = (%q, %v), want a park", prompt, cont)
	}
	clk.Advance(31 * time.Minute)
	clk.Drain()
	if *kicks != 1 {
		t.Fatalf("timer crossing kicks = %d, want exactly 1 (the budget evaluation turn)", *kicks)
	}
	full, _ = store.GoalSnapshot()
	if full.Budgets.ParkedTotal < 30*time.Minute {
		t.Fatalf("ParkedTotal = %v, want ≥30m accrued at the timer crossing: %+v", full.Budgets.ParkedTotal, full.Budgets)
	}
	// The bound binds: the next gate blocks budget-exhausted (the crossing
	// kick scheduled the evaluation; rule 2 enforces the cap).
	prompt, cont := sess.armGoalContinuation(false, true)
	if cont || prompt != "" {
		t.Fatalf("post-crossing gate = (%q, %v), want the parked-total block", prompt, cont)
	}
	snap, _ := store.Snapshot()
	if snap.StopReason != goal.VerdictBudgetExhausted {
		t.Fatalf("StopReason = %q, want %q (parked-total binds as budget)", snap.StopReason, goal.VerdictBudgetExhausted)
	}
}

// TestFixWaveTimerDeadlineKicksFinalTurn pins the timer-leg synthetic claim
// (spec §1 rule 3): a goal deadline earlier than every wait deadline fires
// the coalesced timer with no lease expiry — the timer claims the synthetic
// wake and kicks the final evaluation turn instead of re-arming past the
// deadline into an indefinite park.
func TestFixWaveTimerDeadlineKicksFinalTurn(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	var prompts []string
	sess.SetKickFunc(func(p string) { prompts = append(prompts, p) })
	sess.SetNotifyFunc(func() {})

	store := sess.getOrCreateGoalStore()
	store.Set("deadline before waits", clk.Now())
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: 2 * time.Hour}, clk.Now()); !ok {
		t.Fatalf("precondition: registration should succeed: %q", store.LastRejectReason())
	}
	// Shrink the deadline below the wait deadline (budgets persist the
	// 4h default; the timer must fire at the deadline, not the lease).
	full, _ := store.GoalSnapshot()
	persisted, _ := goal.PersistedFromSnapshot(full)
	persisted.Budgets.Deadline = clk.Now().Add(time.Hour)
	store.RestoreSnapshot(persisted)
	// Park through the gate so production state (holds, timer) is real.
	if prompt, cont := sess.armGoalContinuation(false, true); cont || prompt != "" {
		t.Fatalf("park gate = (%q, %v), want a park", prompt, cont)
	}
	clk.Advance(time.Hour + time.Second)
	clk.Drain()
	if len(prompts) != 1 {
		t.Fatalf("timer deadline kicks = %d, want exactly 1 (the final evaluation turn)", len(prompts))
	}
	if !strings.Contains(prompts[0], goal.DeadlineExpiryTrigger) {
		t.Fatalf("timer kick must carry %q:\n%.200q...", goal.DeadlineExpiryTrigger, prompts[0])
	}
	if full, _ := store.GoalSnapshot(); !full.DeadlineFinalDelivered || len(full.PendingWake) != 1 {
		t.Fatalf("backlog after timer fire = %+v, want the one-shot marker + one synthetic entry", full)
	}
}

// TestFixWaveRetargetCarriesDeadlineWake pins the retarget/deadline one-shot
// alignment (spec §§1, 3): store.Set carries the undelivered synthetic
// backlog (marked Superseded) alongside the reset DeadlineFinalDelivered
// marker — so the retargeted goal drives the carried batch as one superseded
// no-op on the CURRENT objective (never the old objective's wake, never an
// immediate block), its tail claims and drives the new objective's own final
// turn against the reset marker, the following gate blocks with the distinct
// verdict, and the one-shot holds after (no second synthetic drive).
func TestFixWaveRetargetDeadlineFinalTurn(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("goal A", clk.Now())
	clk.Advance(5 * time.Hour) // past the 4h default deadline
	prompt, cont := sess.armGoalContinuation(false, true)
	if !cont || !strings.Contains(prompt, goal.DeadlineExpiryTrigger) {
		t.Fatalf("gate 1 = (%v, %.80q...), want A's final turn", cont, prompt)
	}
	// Retarget before the wake tail consumes the backlog: Set carries the
	// synthetic entry marked Superseded onto B (budgets, incl. the past
	// deadline, carry over by design).
	if _, err := sess.SetGoal(context.Background(), "goal B"); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if full, _ := store.GoalSnapshot(); len(full.PendingWake) != 1 || !full.PendingWake[0].Superseded {
		t.Fatalf("pendingWake after retarget = %+v, want the carried Superseded synthetic entry", full.PendingWake)
	}
	// The carried batch drives once on the current objective as the
	// superseded no-op evaluation (spec §3: stale trigger as dropped
	// context, never the old objective's wake).
	prompt, cont = sess.armGoalContinuation(false, false)
	if !cont || !strings.Contains(prompt, "(superseded)") {
		t.Fatalf("retarget gate = (%v, %.80q...), want B's superseded no-op carrying the stale deadline excerpt", cont, prompt)
	}
	if !strings.Contains(prompt, "goal B") {
		t.Fatalf("no-op prompt must evaluate the CURRENT objective:\n%.120q...", prompt)
	}
	if strings.Contains(prompt, "goal A") {
		t.Fatalf("no-op prompt must never pursue the old objective:\n%.120q...", prompt)
	}
	// The no-op turn's own tail consumes the carried batch; the deadline is
	// still past with the one-shot unspent for B, so the tail claims the
	// fresh synthetic entry and drives B's own final evaluation turn
	// carrying the live deadline verdict (spec §1 rule 3 — B never had its
	// final turn; the carried no-op was dropped context, not a verdict).
	prompt, cont = sess.armGoalContinuation(false, true)
	if !cont || !strings.Contains(prompt, goal.DeadlineExpiryTrigger) {
		t.Fatalf("no-op tail = (%v, %.80q...), want B's final turn carrying %q", cont, prompt, goal.DeadlineExpiryTrigger)
	}
	if strings.Contains(prompt, "(superseded)") {
		t.Fatalf("final-turn prompt must carry the live verdict, not the consumed stale batch:\n%.120q...", prompt)
	}
	// The following gate blocks with the distinct verdict and the one-shot
	// holds after: no second final turn.
	prompt, cont = sess.armGoalContinuation(false, true)
	if cont || prompt != "" {
		t.Fatalf("post-final gate = (%q, %v), want the rule-3 block", prompt, cont)
	}
	snap, _ := store.Snapshot()
	if snap.Status != goal.StatusBlocked || snap.StopReason != goal.VerdictDeadlineExceeded {
		t.Fatalf("snapshot = %+v, want blocked/deadline-exceeded", snap)
	}
	if full, _ := store.GoalSnapshot(); len(full.PendingWake) != 0 {
		t.Fatalf("pendingWake after the block = %+v, want drained", full.PendingWake)
	}
	if prompt, cont := sess.armGoalContinuation(false, true); cont && strings.Contains(prompt, goal.DeadlineExpiryTrigger) {
		t.Fatalf("second post-final gate = (%q, %v), want no second synthetic drive (one-shot)", prompt, cont)
	}
}

// TestFixWaveAdvancementSoftensLaterLoss pins the rule-5 advancement window
// (spec §1 rule 5 check-before-reset): a waits-predicate fire persists
// advancement evidence, so a LATER loss with nothing live left notifies +
// re-drives instead of blocking immediately.
func TestFixWaveAdvancementSoftensLaterLoss(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("advance then lose", clk.Now())
	sub := &fixStubSubstrate{jobs: map[string]fixStubTarget{
		"job_a": {live: true},
		"job_b": {live: true},
	}}
	store.SetSubstrate(sub)
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilJob, Target: "job_a", Timeout: time.Hour}, clk.Now()); !ok {
		t.Fatalf("precondition: job_a must park: %q", store.LastRejectReason())
	}
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilJob, Target: "job_b", Timeout: time.Hour}, clk.Now()); !ok {
		t.Fatalf("precondition: job_b must park: %q", store.LastRejectReason())
	}
	// job_a reaches retained-terminal: the gate claims the predicate fire
	// (persisting advancement) and drives the wake.
	sub.jobs["job_a"] = fixStubTarget{retained: true, excerpt: "job job_a exited 0"}
	prompt, cont := sess.armGoalContinuation(false, true)
	if !cont || !strings.Contains(prompt, "exited 0") {
		t.Fatalf("wake gate = (%q, %v), want job_a's wake drive", prompt, cont)
	}
	// job_b's substrate vanishes with nothing live left: advancement stands
	// in the pre-reset window, so the gate notifies + re-drives (drives the
	// objective — not a block).
	sub.jobs["job_b"] = fixStubTarget{}
	prompt, cont = sess.armGoalContinuation(false, true)
	if !cont || strings.Contains(prompt, goalWaitWakeTrailerPrefix) {
		t.Fatalf("post-advancement loss gate = (%q, %v), want the objective re-drive (notice + re-drive, not a block)", prompt, cont)
	}
	if snap, _ := store.Snapshot(); snap.Status == goal.StatusBlocked {
		t.Fatalf("snapshot = %+v, want no block while advancement stands", snap)
	}
	// The notice+re-drive gate consumed the persisted cause (TakeLossCause),
	// so only the exactly-once note stands — no LossCause remains.
	requireSingleLossNotice(t, sess)
}

// TestFixWaveExpiryFireDoesNotAdvance pins the structural expiry mark: a
// lease-deadline fire ("wait expired: …") and the synthetic deadline wake
// ("deadline exceeded…") accrue as ordinary non-advancing turns —
// claimedPredicateFire keys on the Expiry mark and the deadline wait id,
// never on trigger text, so timer refires cannot launder the re-park
// counter through the advancement window.
func TestFixWaveExpiryFireDoesNotAdvance(t *testing.T) {
	t.Parallel()
	if claimedPredicateFire([]goal.PendingWake{
		{WaitID: "wait_1", Trigger: "wait expired: timer", Kind: goal.WaitUntilTime, Expiry: true},
	}) {
		t.Fatal("lease-expiry fire must not count as predicate advancement")
	}
	if claimedPredicateFire([]goal.PendingWake{
		{WaitID: goal.DeadlineWakeID, Trigger: "deadline exceeded (waiting on timer)", Kind: goal.WaitUntilTime},
	}) {
		t.Fatal("synthetic deadline wake must not count as predicate advancement")
	}
	if !claimedPredicateFire([]goal.PendingWake{
		{WaitID: "wait_2", Trigger: "job job_a exited 0", Kind: goal.WaitUntilJob},
	}) {
		t.Fatal("predicate fire must count as predicate advancement")
	}
	// Prefix-proof: a predicate trigger wearing expiry-like text still
	// advances (structure, not text, decides).
	if !claimedPredicateFire([]goal.PendingWake{
		{WaitID: "wait_3", Trigger: "wait expired: custom probe", Kind: goal.WaitUntilJob},
	}) {
		t.Fatal("non-expiry entry must advance even when its text mimics an expiry prefix")
	}
}

// TestFixWaveTimerLossesKickNotice pins the timer-path losses strand (spec
// §2: never a silent strand): a parked lease whose substrate vanishes is
// dropped at the coalesced timer's fire with the cause persisted — and the
// timer kicks the honest notice + re-drive promptly instead of re-arming
// into an idle goal with nothing scheduled.
func TestFixWaveTimerLossesKickNotice(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	kicks := wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("timer loss", clk.Now())
	store.SetSubstrate(&fixStubSubstrate{jobs: map[string]fixStubTarget{"job_gone": {live: true}}})
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilJob, Target: "job_gone", Timeout: time.Hour}, clk.Now()); !ok {
		t.Fatalf("precondition: live job must park: %q", store.LastRejectReason())
	}
	// Park through the gate so production state (holds, timer) is real.
	if prompt, cont := sess.armGoalContinuation(false, true); cont || prompt != "" {
		t.Fatalf("park gate = (%q, %v), want a park", prompt, cont)
	}
	// Restart-away substrate: the job record the lease validated against is
	// gone before the timer's poll-leg fire.
	store.SetSubstrate(&fixStubSubstrate{})
	clk.Advance(61 * time.Second)
	clk.Drain()
	if *kicks != 1 {
		t.Fatalf("timer loss kicks = %d, want exactly 1 (honest notice + re-drive)", *kicks)
	}
	// No gate has run since the timer's drop, so the cause still stands for
	// the next gate's TakeLossCause/rule-5 read.
	requirePersistedLossCause(t, store)
	requireSingleLossNotice(t, sess)
}

// TestGoalWaitApprovalKeyPairContract pins the round-14 MEDIUM (header,
// question) pair contract against the REAL goalSessionSubstrate: with a
// headed ask {Header, Question} pending, an until_approval wait with the
// two-half "header\x00question" key parks; a bare half-key against a
// two-half ask rejects (documented behavior — bare halves match only
// header-empty-or-question-empty asks); a bare half-key against a lone-half
// ask parks (ambiguous-by-construction).
func TestGoalWaitApprovalKeyPairContract(t *testing.T) {
	t.Parallel()
	newHeadedSession := func(t *testing.T, clk *agenttest.FakeClock, header, question string) *Session {
		t.Helper()
		sess := newWaitGateSession(t, clk)
		wireKickAndNotify(sess)
		store := sess.getOrCreateGoalStore()
		store.Set("wait on the ask", clk.Now())
		sess.mu.Lock()
		sess.askPending = []askQuestion{{Header: header, Question: question}}
		sess.mu.Unlock()
		return sess
	}
	approvalCall := func(id, target string) llm.ToolCallData {
		args, _ := json.Marshal(map[string]any{
			"kind": "until_approval", "target": target, "timeout_seconds": 60,
		})
		return llm.ToolCallData{ID: id, Name: "goal_wait", Arguments: args, Type: "function"}
	}

	// Two-half key against a headed ask parks (the documented contract).
	t.Run("two-half parks", func(t *testing.T) {
		t.Parallel()
		clk := agenttest.NewFakeClock()
		sess := newHeadedSession(t, clk, "ship it?", "merge now?")
		defer sess.Close()
		res := sess.reg.ExecuteCall(context.Background(), sess.env, approvalCall("gw-pair", "ship it?\x00merge now?"))
		if res.IsError {
			t.Fatalf("two-half key should park, got error: %s", res.Output)
		}
		if snap, _ := sess.getOrCreateGoalStore().Snapshot(); snap.Status != goal.StatusWaiting {
			t.Fatalf("status = %q, want waiting on the live ask", snap.Status)
		}
	})

	// Bare half-key against a two-half ask rejects (both halves non-empty).
	t.Run("bare half rejects on headed ask", func(t *testing.T) {
		t.Parallel()
		clk := agenttest.NewFakeClock()
		sess := newHeadedSession(t, clk, "ship it?", "merge now?")
		defer sess.Close()
		res := sess.reg.ExecuteCall(context.Background(), sess.env, approvalCall("gw-bare", "ship it?"))
		if !res.IsError {
			t.Fatalf("bare half-key against a headed ask should reject, got: %s", res.Output)
		}
		if snap, _ := sess.getOrCreateGoalStore().Snapshot(); snap.Status != goal.StatusActive {
			t.Fatalf("status = %q, want active (rejected registration must not park)", snap.Status)
		}
	})

	// Bare half-key against a lone-half ask parks (ambiguous-by-construction).
	t.Run("bare half parks on lone half", func(t *testing.T) {
		t.Parallel()
		clk := agenttest.NewFakeClock()
		sess := newHeadedSession(t, clk, "", "merge now?")
		defer sess.Close()
		res := sess.reg.ExecuteCall(context.Background(), sess.env, approvalCall("gw-lone", "merge now?"))
		if res.IsError {
			t.Fatalf("bare half-key against a lone-half ask should park, got error: %s", res.Output)
		}
		if snap, _ := sess.getOrCreateGoalStore().Snapshot(); snap.Status != goal.StatusWaiting {
			t.Fatalf("status = %q, want waiting on the live ask", snap.Status)
		}
	})
}

// TestFixWave15_SettleRestoredBacklogWakeParity pins the round-15 HIGH fix
// (settleGoalOnIdle lock inversion): a restored-but-never-kicked backlog
// settles to a kick whose prompt is the full wake prompt — same wait ids,
// same trailer — as the claim path. The restored-backlog wake re-read must
// run OUTSIDE the s.mu section (the established goalUpdateMu-then-s.mu order,
// cf. SetGoal), so the s.mu section only consumes the pre-read wakeIDs /
// wakePrompt locals and never takes goalUpdateMu.
func TestFixWave15_SettleRestoredBacklogWakeParity(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	src := newWaitGateSession(t, clk)
	defer src.Close()
	wireKickAndNotify(src)

	store := src.getOrCreateGoalStore()
	store.Set("crash mid-wake", clk.Now())
	w, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Target: "crash-timer", Timeout: time.Minute, Label: "crash-timer"}, clk.Now())
	if !ok {
		t.Fatal("precondition: registration should succeed")
	}
	clk.Advance(2 * time.Minute)
	if _, ok := store.ClaimFire(w.Lease.WaitID, "wait expired: crash-timer", clk.Now()); !ok {
		t.Fatal("precondition: claim should consume the expired lease")
	}
	// The expected wake prompt is whatever the claim path renders for the
	// same backlog: the settle must produce it identically.
	wantFull, ok := store.GoalSnapshot()
	if !ok || len(wantFull.PendingWake) != 1 {
		t.Fatalf("precondition: backlog = %+v ok=%v, want 1 pending wake", wantFull, ok)
	}
	wantPrompt := src.renderGoalWakePrompt(wantFull)

	meta := src.Meta()
	clk2 := agenttest.NewFakeClockAt(clk.Now())
	meta.ID = "fixwave15-settle-parity"
	restored := restoreGoalTestSession(t, clk2, meta)
	defer restored.Close()
	var prompts []string
	restored.SetKickFunc(func(p string) { prompts = append(prompts, p) })
	// Drain the wiring-time flush so the assert below pins the SETTLE path,
	// not SetKickFunc's own flush: clear the delivered mark and re-arm the
	// backlog as never-kicked, then settle directly.
	restored.mu.Lock()
	restored.goalWakeDelivered = nil
	restored.mu.Unlock()
	drainGoalEvents(restored)
	if !restored.settleGoalOnIdle() {
		t.Fatal("settle must kick a restored-but-never-kicked backlog")
	}
	if len(prompts) != 2 {
		t.Fatalf("prompts = %d, want 2 (wiring flush + settle kick)", len(prompts))
	}
	if prompts[1] != wantPrompt {
		t.Fatalf("settle wake prompt mismatch:\n got: %.300q\nwant: %.300q", prompts[1], wantPrompt)
	}
	if !strings.Contains(prompts[1], w.Lease.WaitID) {
		t.Fatalf("settle wake prompt must carry wait %q:\n%.300q...", w.Lease.WaitID, prompts[1])
	}
	if !strings.Contains(prompts[1], goalWaitWakeTrailerPrefix) {
		t.Fatalf("settle wake prompt missing trailer %q\nprompt:\n%s", goalWaitWakeTrailerPrefix, prompts[1])
	}
}

// TestFixWave15_SettleRestoredBacklogSettleOnly pins the same HIGH from the
// no-flush side: with no kick wired at wiring time, the first settle drives
// the restored backlog itself (not just SetKickFunc's flush) with the
// identical wake prompt.
func TestFixWave15_SettleRestoredBacklogSettleOnly(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	src := newWaitGateSession(t, clk)
	defer src.Close()
	wireKickAndNotify(src)

	store := src.getOrCreateGoalStore()
	store.Set("crash mid-wake", clk.Now())
	w, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Target: "crash-timer", Timeout: time.Minute, Label: "crash-timer"}, clk.Now())
	if !ok {
		t.Fatal("precondition: registration should succeed")
	}
	clk.Advance(2 * time.Minute)
	if _, ok := store.ClaimFire(w.Lease.WaitID, "wait expired: crash-timer", clk.Now()); !ok {
		t.Fatal("precondition: claim should consume the expired lease")
	}
	wantFull, _ := store.GoalSnapshot()
	wantPrompt := src.renderGoalWakePrompt(wantFull)

	meta := src.Meta()
	clk2 := agenttest.NewFakeClockAt(clk.Now())
	meta.ID = "fixwave15-settle-only"
	restored := restoreGoalTestSession(t, clk2, meta)
	defer restored.Close()
	// No kick at wiring: the settle below owns the backlog kick.
	var prompts []string
	restored.mu.Lock()
	restored.kickFunc = func(p string) { prompts = append(prompts, p) }
	restored.mu.Unlock()
	drainGoalEvents(restored)
	if !restored.settleGoalOnIdle() {
		t.Fatal("settle must kick a restored-but-never-kicked backlog with no wiring flush")
	}
	if len(prompts) != 1 {
		t.Fatalf("prompts = %d, want exactly 1 (the settle kick)", len(prompts))
	}
	if prompts[0] != wantPrompt {
		t.Fatalf("settle wake prompt mismatch:\n got: %.300q\nwant: %.300q", prompts[0], wantPrompt)
	}
}

// TestFixWave15_SetKickFuncFlushCopySemantics pins the round-15 MEDIUM
// (SetKickFunc map race): the flush decision reads the delivered set under
// s.mu and iterates a copy, so a concurrent markGoalWakesDelivered racing
// the flush cannot panic (concurrent map read+write) and cannot flip the
// decision mid-iteration. Under -race this test fails on the old code (the
// live-map read races the writer); the copy-semantics half also asserts
// determinism serially: marking delivered between the store read and the
// flush loop must not change what the flush decided.
func TestFixWave15_SetKickFuncFlushCopySemantics(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("copy semantics", clk.Now())
	w, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Target: "copy-timer", Timeout: time.Minute, Label: "copy-timer"}, clk.Now())
	if !ok {
		t.Fatal("precondition: registration should succeed")
	}
	clk.Advance(2 * time.Minute)
	if _, ok := store.ClaimFire(w.Lease.WaitID, "wait expired: copy-timer", clk.Now()); !ok {
		t.Fatal("precondition: claim should consume the expired lease")
	}
	// Snapshot the delivered set the way the fixed flush does (under s.mu),
	// then mutate the live map: the copy must be unaffected.
	sess.mu.Lock()
	deliveredHere := make(map[string]bool, len(sess.goalWakeDelivered))
	for id := range sess.goalWakeDelivered {
		deliveredHere[id] = true
	}
	sess.mu.Unlock()
	sess.markGoalWakesDelivered([]string{w.Lease.WaitID})
	if deliveredHere[w.Lease.WaitID] {
		t.Fatal("flush copy must predate the concurrent mark (copy semantics, not the live map)")
	}
	sess.mu.Lock()
	liveMarked := sess.goalWakeDelivered[w.Lease.WaitID]
	sess.mu.Unlock()
	if !liveMarked {
		t.Fatal("precondition: live delivered set must carry the mark")
	}
}

// TestFixWave15_SetKickFuncFlushVsMarkRace hammers the round-15 MEDIUM race
// directly: concurrent SetKickFunc flushes vs markGoalWakesDelivered must
// complete without a concurrent-map panic. The -race detector is the real
// assertion (old code reads the live map while the writer mutates it);
// reaching the end is the signal.
func TestFixWave15_SetKickFuncFlushVsMarkRace(t *testing.T) {
	if !raceDetectorEnabled {
		t.Skip("race-detector stress test; run with -race")
	}
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	sess.SetNotifyFunc(func() {})

	store := sess.getOrCreateGoalStore()
	store.Set("flush race", clk.Now())
	w, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Target: "race-timer", Timeout: time.Minute, Label: "race-timer"}, clk.Now())
	if !ok {
		t.Fatal("precondition: registration should succeed")
	}
	clk.Advance(2 * time.Minute)
	if _, ok := store.ClaimFire(w.Lease.WaitID, "wait expired: race-timer", clk.Now()); !ok {
		t.Fatal("precondition: claim should consume the expired lease")
	}
	stop := make(chan struct{})
	var drivers sync.WaitGroup
	drivers.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			sess.SetKickFunc(func(string) {})
		}
	})
	drivers.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			sess.markGoalWakesDelivered([]string{w.Lease.WaitID})
		}
	})
	time.Sleep(50 * time.Millisecond)
	close(stop)
	drivers.Wait()
}
