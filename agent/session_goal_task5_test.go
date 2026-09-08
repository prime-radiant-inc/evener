package agent

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/goal"
)

// Slice-1 projection + interim judge tests (Task 5, spec §§7 wire, 9 slice-1
// scope + interim rule). TDD red phase: these fail until GoalStateData carries
// the wait list, EventGoalWaiting/EventGoalResumed exist and are emitted, the
// autonomy treatment covers timer-parked goals, and the interim judge keeps the
// v1 3/6 breaker armed for non-parked loops while wait-attributable turns
// bypass RecordContinuation.

// drainGoalEvents discards buffered session events so the next read observes
// only events emitted after this point.
func drainGoalEvents(sess *Session) {
	for {
		select {
		case _, ok := <-sess.Events():
			if !ok {
				return
			}
		default:
			return
		}
	}
}

// nextGoalEventOfKind blocks (bounded) for the next event of the named kind,
// skipping unrelated events.
func nextGoalEventOfKind(t *testing.T, sess *Session, kind events.EventKind) events.SessionEvent {
	t.Helper()
	// TRIPWIRE: mutations emit synchronously in-process; the bound only fires
	// if the emission path wedges (cf. nextGoalUpdated's 1s tripwire).
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatalf("session event stream closed before %s", kind)
			}
			if ev.Kind != kind {
				continue
			}
			return ev
		case <-timer.C:
			t.Fatalf("timed out waiting for %s", kind)
			return events.SessionEvent{}
		}
	}
}

// TestGoalStateDataCarriesWaitList pins the §7 wire gap (M2): the GoalUpdated
// payload must carry the live wait list (labels + deadlines only — never full
// predicate payloads) so the wire projection can summarize waiting state.
func TestGoalStateDataCarriesWaitList(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("wire the waits", clk.Now())
	drainGoalEvents(sess)
	deadline := clk.Now().Add(time.Hour)
	w, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Hour, Label: "timer-one"}, clk.Now())
	if !ok {
		t.Fatal("precondition: until_time registration should succeed")
	}
	// The store-level RegisterWait used here emits nothing; build the payload
	// from the full-shape read exactly as the emission path does.
	full, ok := store.GoalSnapshot()
	if !ok {
		t.Fatal("precondition: goal should still be set")
	}
	data := goalStateDataFromFull(full)
	if len(data.WaitingOn) != 1 {
		t.Fatalf("WaitingOn = %+v, want exactly the one live wait", data.WaitingOn)
	}
	got := data.WaitingOn[0]
	if got.WaitID != w.Lease.WaitID || got.Label != "timer-one" {
		t.Fatalf("WaitingOn[0] = %+v, want wait_id %q label %q", got, w.Lease.WaitID, "timer-one")
	}
	if got.DeadlineUnixMilli != deadline.UnixMilli() {
		t.Fatalf("WaitingOn[0].DeadlineUnixMilli = %d, want %d", got.DeadlineUnixMilli, deadline.UnixMilli())
	}
	if data.NearestDeadlineUnixMilli != deadline.UnixMilli() || data.NearestLabel != "timer-one" {
		t.Fatalf("nearest = (%q, %d), want (timer-one, %d)", data.NearestLabel, data.NearestDeadlineUnixMilli, deadline.UnixMilli())
	}
	if data.UsedContinuations != 0 || data.MaxContinuations != goal.DefaultMaxContinuations {
		t.Fatalf("progress = %d/%d, want 0/%d", data.UsedContinuations, data.MaxContinuations, goal.DefaultMaxContinuations)
	}
}

// TestGoalWaitNearestDeadlineTieBreaksOnWaitID pins the §6 chip rule: nearest =
// earliest deadline, tie → smallest wait_id.
func TestGoalWaitNearestDeadlineTieBreaksOnWaitID(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("tie deadlines", clk.Now())
	now := clk.Now()
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Hour, Label: "first"}, now); !ok {
		t.Fatal("precondition: first registration should succeed")
	}
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Hour, Label: "second", Target: "other"}, now); !ok {
		t.Fatal("precondition: second registration should succeed")
	}
	// Both deadlines are now+1h (same FakeClock instant). The payload must
	// resolve the nearest to the smaller wait_id.
	full, ok := store.GoalSnapshot()
	if !ok {
		t.Fatal("precondition: goal should still be set")
	}
	data := goalStateDataFromFull(full)
	if len(data.WaitingOn) != 2 {
		t.Fatalf("WaitingOn = %+v, want both waits", data.WaitingOn)
	}
	if data.NearestLabel != "first" {
		t.Fatalf("NearestLabel = %q, want first (wait_1 wins the tie)", data.NearestLabel)
	}
	// Earliest-deadline ordering beats registration order: an earlier
	// deadline registered second still wins.
	store2 := goal.NewStore()
	store2.Set("earliest wins", now)
	if _, ok := store2.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: 2 * time.Hour, Label: "later"}, now); !ok {
		t.Fatal("precondition: later registration should succeed")
	}
	// A different target avoids the same-target replace rule.
	if _, ok := store2.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Hour, Label: "earlier", Target: "other"}, now); !ok {
		t.Fatal("precondition: earlier registration should succeed")
	}
	full2, _ := store2.GoalSnapshot()
	data2 := goalStateDataFromFull(full2)
	if data2.NearestLabel != "earlier" {
		t.Fatalf("NearestLabel = %q, want earlier (earliest deadline wins)", data2.NearestLabel)
	}
}

// TestGoalWaitingEventEmittedOnPark pins §7: parking on a wait emits
// EventGoalWaiting (the announcement channel for the park), carrying the wait
// count and nearest label.
func TestGoalWaitingEventEmittedOnPark(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("announce the park", clk.Now())
	drainGoalEvents(sess)
	// Park through the session path (not the bare store): it owns the
	// GOAL_UPDATED + GOAL_WAITING emission pair.
	if _, ok := sess.registerGoalWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Hour, Label: "timer-one"}, clk.Now()); !ok {
		t.Fatal("precondition: registration should succeed")
	}
	ev := nextGoalEventOfKind(t, sess, events.EventGoalWaiting)
	d, ok := ev.Data.(events.GoalWaitingData)
	if !ok {
		t.Fatalf("GOAL_WAITING payload = %T, want events.GoalWaitingData", ev.Data)
	}
	if d.Count != 1 || d.NearestLabel != "timer-one" {
		t.Fatalf("GoalWaitingData = %+v, want count 1 nearest timer-one", d)
	}
}

// TestGoalResumedEventEmittedOnWake pins §7: the wake turn's delivery emits
// EventGoalResumed carrying the fired wait_ids.
func TestGoalResumedEventEmittedOnWake(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("announce the wake", clk.Now())
	w, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Minute, Label: "timer-one"}, clk.Now())
	if !ok {
		t.Fatal("precondition: registration should succeed")
	}
	drainGoalEvents(sess)
	clk.Advance(2 * time.Minute)
	prompt, cont := sess.armGoalContinuation(false, true)
	if !cont || prompt == "" {
		t.Fatal("precondition: expired gate must drive the wake turn")
	}
	ev := nextGoalEventOfKind(t, sess, events.EventGoalResumed)
	d, ok := ev.Data.(events.GoalResumedData)
	if !ok {
		t.Fatalf("GOAL_RESUMED payload = %T, want events.GoalResumedData", ev.Data)
	}
	if len(d.WaitIDs) != 1 || d.WaitIDs[0] != w.Lease.WaitID {
		t.Fatalf("GoalResumedData = %+v, want the fired wait_id", d)
	}
}

// TestTimerParkedGoalIsAutonomyInFlight pins §7: a goal parked on a timer wait
// (nothing for the user to answer) reads as autonomy-in-flight — never "needs
// you" with nothing to answer.
//
// The pin is on autonomyInFlight (the settle/restore driver): a parked goal
// owns a live wait whose coalesced timer will kick the wake turn without user
// input. Like the live-child precedent
// (TestWireState_LiveChildDoesNotMakeIdleParentActive), autonomy does not
// itself upgrade WireState: the needs-you tier keys on awaiting/warning, so a
// parked goal resting idle never lands there (never "needs you" with nothing
// to answer) while the wake path still re-arms through the timer.
func TestTimerParkedGoalIsAutonomyInFlight(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()

	store := sess.getOrCreateGoalStore()
	store.Set("parked but moving", clk.Now())
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Hour}, clk.Now()); !ok {
		t.Fatal("precondition: registration should succeed")
	}
	// The store-level registration above does not arm the coalesced timer
	// (that is the session path's job); arm it as the gate would on park.
	sess.armGoalWaitTimer()
	if !sess.autonomyInFlight() {
		t.Fatal("a timer-parked goal must read as autonomy-in-flight")
	}
	// Disarming (cancel the last live lease, as the cancel path does) clears
	// the treatment: with no live waits nothing will move the session.
	full, _ := store.GoalSnapshot()
	for _, w := range full.Waits {
		if !sess.CancelGoalWait(w.Lease.WaitID) {
			t.Fatalf("precondition: cancel of %s should succeed", w.Lease.WaitID)
		}
	}
	if sess.autonomyInFlight() {
		t.Fatal("a goal with no live waits must not read as autonomy-in-flight")
	}
}

// TestInterimJudgeStaysArmedForNonParkedLoops pins §9: the v1 3/6 mutation
// breaker stays armed for non-parked loops in slice 1.
func TestInterimJudgeStaysArmedForNonParkedLoops(t *testing.T) {
	t.Parallel()
	sess := newGoalMethodSession(t)
	defer sess.Close()

	sess.getOrCreateGoalStore().Set("stall out", time.Now())
	if _, ok := sess.armGoalContinuation(true, true); !ok {
		t.Fatal("first progressed continuation should keep the goal active")
	}
	for range goal.NoProgressLimit {
		sess.armGoalContinuation(false, true)
	}
	snap, _ := sess.getOrCreateGoalStore().Snapshot()
	if snap.Status != goal.StatusBlocked || snap.StopReason != goal.VerdictNoProgress {
		t.Fatalf("snapshot = %+v, want the interim v1 breaker block (no progress)", snap)
	}
}

// TestInterimWakeTurnsBypassRecordContinuation pins §9: slice-1
// wake/evaluation/expiry turns bypass RecordContinuation exactly like parks —
// wait-attributable, never stall evidence.
//
// The three wait-attributable shapes: the wake drive (backlog standing), the
// wake tail (delivered batch consumed), and the superseded no-op evaluation
// (retarget-after-claim). The expiry-evaluation turn IS the wake turn in
// slice 1 (timer expiry claims into pendingWake and the gate drives one
// combined wake) — pinned here through the timer-expiry path, not a bare
// active drive.
func TestInterimWakeTurnsBypassRecordContinuation(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("wake bypasses fold", clk.Now())
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Minute}, clk.Now()); !ok {
		t.Fatal("precondition: registration should succeed")
	}
	clk.Advance(2 * time.Minute)
	if _, cont := sess.armGoalContinuation(false, true); !cont {
		t.Fatal("precondition: expired gate must drive the wake turn")
	}
	// Wake (expiry-evaluation) drive + wake tail must both bypass the fold:
	// Iterations and streak stay zero through them.
	if snap, _ := store.Snapshot(); snap.Iterations != 0 || snap.NoProgressStreak != 0 {
		t.Fatalf("snapshot after wake drive = %+v, want zero fold", snap)
	}
	if _, cont := sess.armGoalContinuation(false, true); !cont {
		t.Fatal("precondition: wake tail must re-arm")
	}
	if snap, _ := store.Snapshot(); snap.Iterations != 0 || snap.NoProgressStreak != 0 {
		t.Fatalf("snapshot after wake tail = %+v, want zero fold", snap)
	}
	// Superseded no-op evaluation bypasses too: claim, then retarget, then
	// drive the marked no-op and fold its tail — still zero fold.
	//
	// Drive the sequence the production path uses (cf.
	// TestGateRetargetBetweenClaimAndKickDrivesSupersededNoop): claim, then
	// retarget (carries the claim marked Superseded), then drive the marked
	// no-op from a non-continuation tail, then fold the no-op turn's own
	// continuation tail. The no-op turn's kick marks the batch delivered, so
	// its tail consumes via the wake-tail per-ID drain and bypasses the fold.
	store2 := sess.getOrCreateGoalStore()
	store2.Set("old objective", clk.Now())
	w2, ok := store2.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Minute}, clk.Now())
	if !ok {
		t.Fatal("precondition: superseded registration should succeed")
	}
	clk.Advance(2 * time.Minute)
	if _, ok := store2.ClaimFire(w2.Lease.WaitID, "wait expired: label", clk.Now()); !ok {
		t.Fatal("precondition: claim should consume the expired lease")
	}
	store2.Set("retargeted objective", clk.Now())
	before := func() (int, int) {
		snap, _ := store2.Snapshot()
		return snap.Iterations, snap.NoProgressStreak
	}
	prompt, cont := sess.armGoalContinuation(false, false)
	if !cont || prompt == "" {
		t.Fatal("precondition: superseded gate must drive the no-op evaluation")
	}
	if it, streak := before(); it != 0 || streak != 0 {
		t.Fatalf("snapshot after superseded drive = iterations=%d streak=%d, want zero fold", it, streak)
	}
	if _, cont = sess.armGoalContinuation(false, true); !cont {
		t.Fatal("precondition: superseded tail must re-arm")
	}
	if it, streak := before(); it != 0 || streak != 0 {
		t.Fatalf("snapshot after superseded tail = iterations=%d streak=%d, want zero fold", it, streak)
	}
}
