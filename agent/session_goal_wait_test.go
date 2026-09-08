package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/agent/schema"
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
