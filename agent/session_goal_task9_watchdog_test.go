package agent

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/goal"
)

// Task-9 slice-3 watchdog-arming tests (spec §6 Task-8 residual acceptance
// criteria): parked stretches piggyback the coalesced timer with
// threshold-crossing re-arm; active goals evaluate at turn tail; all three
// activity signals (ledger fold, wake delivery, owner-visible output) reset
// the stretch so arming never false-positives active-quiet on advancing
// goals. Deterministic: FakeClock only.

// TestGoalWatchdogTimerPiggybacksParkStretch pins the piggyback: arming the
// coalesced wait timer for a parked stretch also schedules the watchdog
// evaluation — a park past the quiet threshold produces its park-start
// notice via the timer alone, with no direct checkGoalWatchdog call.
func TestGoalWatchdogTimerPiggybacksParkStretch(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	sess.getOrCreateGoalStore().Set("long park", clk.Now())
	if _, ok := sess.getOrCreateGoalStore().RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: 2 * time.Hour}, clk.Now()); !ok {
		t.Fatal("precondition: registration should succeed")
	}
	drainGoalEvents(sess)

	// Park the goal through the gate so production state (holds, timer) is
	// real, then advance past the quiet threshold: the coalesced timer's
	// watchdog piggyback must evaluate and emit park-start on its own.
	if prompt, cont := sess.armGoalContinuation(false, true); cont || prompt != "" {
		t.Fatalf("park gate = (%q, %v), want a park", prompt, cont)
	}
	// Determinism: the park gate armed the coalesced wait timer, and Advance
	// dispatches its callback on a clock goroutine (Advance returns before
	// the callback runs). The callback evaluates the watchdog on the same
	// stretch: the losing evaluation observes stretchNotices=1 and
	// suppresses, so the first emission always carries park-start. Drain
	// after the assertion so a late second evaluation cannot leak events
	// into a later test's stream.
	clk.Advance(31 * time.Minute)
	ev := nextGoalEventOfKind(t, sess, events.EventGoalWatchdog)
	clk.Drain()
	d, ok := ev.Data.(events.GoalWatchdogData)
	if !ok || d.Kind != "park-start" {
		t.Fatalf("timer-piggyback watchdog = %+v, want park-start via the coalesced timer alone", ev.Data)
	}
}

// TestGoalWatchdogTurnTailEvaluatesActive pins the turn-tail arm: an active
// goal past the quiet threshold notifies on its next gate evaluation without
// any timer armed (active goals arm no timer — the tail is the only eval).
func TestGoalWatchdogTurnTailEvaluatesActive(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	sess.getOrCreateGoalStore().Set("quiet active work", clk.Now())
	drainGoalEvents(sess)
	if sess.goalWaitTimerArmed() {
		t.Fatal("precondition: an active goal must arm no coalesced timer")
	}

	// Seed the ledger with one quiet turn, then advance past the threshold:
	// the gate's fold of the identical outcome is repetition without
	// novelty or digest delta (non-advancing, below K — still a drive), so
	// the stretch survives to the tail eval, which must notify with no
	// direct checkGoalWatchdog call.
	quiet := ledgerGateOutcome("read tree", "ok", "steady-hash", "steady-digest", false)
	sess.getOrCreateGoalStore().RecordContinuation(quiet, false, clk.Now())
	clk.Advance(31 * time.Minute)
	if prompt, cont := sess.armGoalContinuationWithOutcome(false, true, quiet); !cont || prompt == "" {
		t.Fatal("precondition: quiet gate must drive")
	}
	ev := nextGoalEventOfKind(t, sess, events.EventGoalWatchdog)
	d, ok := ev.Data.(events.GoalWatchdogData)
	if !ok || d.Kind != "active-quiet" {
		t.Fatalf("turn-tail watchdog = %+v, want active-quiet from the gate tail", ev.Data)
	}
}

// TestGoalWatchdogLedgerFoldResetsStretch pins activity signal 1 (ledger
// fold): an advancing fold resets the quiet stretch, so a goal that keeps
// advancing never notifies — arming never false-positives active-quiet on
// advancing goals.
func TestGoalWatchdogLedgerFoldResetsStretch(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	sess.getOrCreateGoalStore().Set("advancing work", clk.Now())
	drainGoalEvents(sess)

	// Advance the clock in sub-threshold steps, folding an advancing turn
	// each step: every fold resets the stretch, so 60m of advancing work
	// must cost zero notices.
	for i := range 6 {
		clk.Advance(10 * time.Minute)
		advance := ledgerGateOutcome("write report", "ok", "hash", "digest", true)
		advance.ObservationHash = "hash-" + string(rune('a'+i))
		advance.StateDigest = "digest-" + string(rune('a'+i))
		if prompt, cont := sess.armGoalContinuationWithOutcome(false, true, advance); !cont || prompt == "" {
			t.Fatalf("advancing gate %d must drive", i+1)
		}
		// The gate tail evaluates the watchdog inline; drain any notice.
		select {
		case ev := <-sess.Events():
			if ev.Kind == events.EventGoalWatchdog {
				t.Fatalf("advancing goal notified: %+v (fold activity must reset the stretch)", ev.Data)
			}
		default:
		}
	}
}

// TestGoalWatchdogWakeDeliveryResetsStretch pins activity signal 2 (wake
// delivery): kicking a wake turn resets the stretch even when the wake's own
// fold carries no advancement.
func TestGoalWatchdogWakeDeliveryResetsStretch(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	kicks := wireKickAndNotify(sess)

	sess.getOrCreateGoalStore().Set("wait then idle", clk.Now())
	if _, ok := sess.getOrCreateGoalStore().RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Hour}, clk.Now()); !ok {
		t.Fatal("precondition: registration should succeed")
	}
	drainGoalEvents(sess)

	clk.Advance(31 * time.Minute)
	sess.checkGoalWatchdog(clk.Now())
	ev := nextGoalEventOfKind(t, sess, events.EventGoalWatchdog)
	if d, ok := ev.Data.(events.GoalWatchdogData); !ok || d.Kind != "park-start" {
		t.Fatalf("first watchdog = %+v, want park-start", ev.Data)
	}
	// Expire the wait and drive the wake through the gate (production path:
	// claim + wake drive + delivered mark). The delivery resets the stretch,
	// so a fresh 20m quiet period (under threshold) notifies nothing.
	clk.Advance(31 * time.Minute)
	if prompt, cont := sess.armGoalContinuation(false, true); !cont || prompt == "" {
		t.Fatal("precondition: expired gate must drive the wake turn")
	}
	if *kicks != 0 {
		t.Fatalf("kicks = %d, want the gate drive (no timer kick in this path)", *kicks)
	}
	drainGoalEvents(sess)
	clk.Advance(20 * time.Minute)
	sess.checkGoalWatchdog(clk.Now())
	select {
	case ev := <-sess.Events():
		if ev.Kind == events.EventGoalWatchdog {
			t.Fatalf("post-wake quiet notified: %+v (delivery must reset the stretch)", ev.Data)
		}
	default:
	}
}

// TestGoalWatchdogOwnerOutputResetsStretch pins activity signal 3
// (owner-visible output): recording owner-visible output resets the stretch,
// so an active goal that keeps reporting never notifies.
func TestGoalWatchdogOwnerOutputResetsStretch(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	sess.getOrCreateGoalStore().Set("chatty work", clk.Now())
	drainGoalEvents(sess)

	clk.Advance(20 * time.Minute)
	sess.noteGoalWatchdogOwnerOutput(clk.Now())
	clk.Advance(20 * time.Minute)
	sess.checkGoalWatchdog(clk.Now())
	select {
	case ev := <-sess.Events():
		if ev.Kind == events.EventGoalWatchdog {
			t.Fatalf("reporting goal notified: %+v (owner output must reset the stretch)", ev.Data)
		}
	default:
	}
}

// TestGoalWatchdogKindChangeKeepsActivityReset pins the anchored
// state-machine branch (fix-9 M5): after an explicit activity reset, a
// park→active kind change keeps the post-activity start instead of
// backdating to goal creation. The scenario: a park stretch notifies, the
// wait is cancelled (goal back to active), activity resets the stretch, and
// 20m later (under threshold from the reset, over threshold from creation)
// nothing notifies.
func TestGoalWatchdogKindChangeKeepsActivityReset(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	sess.getOrCreateGoalStore().Set("park then work", clk.Now())
	w, ok := sess.getOrCreateGoalStore().RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: 2 * time.Hour}, clk.Now())
	if !ok {
		t.Fatal("precondition: registration should succeed")
	}
	drainGoalEvents(sess)

	clk.Advance(31 * time.Minute)
	sess.checkGoalWatchdog(clk.Now())
	ev := nextGoalEventOfKind(t, sess, events.EventGoalWatchdog)
	if d, ok := ev.Data.(events.GoalWatchdogData); !ok || d.Kind != "park-start" {
		t.Fatalf("first watchdog = %+v, want park-start", ev.Data)
	}
	// End the park (cancel → active) and reset via activity: the kind flips
	// park→active with an anchored start.
	if !sess.CancelGoalWait(w.Lease.WaitID) {
		t.Fatal("precondition: cancel should end the stretch")
	}
	drainGoalEvents(sess)
	sess.noteGoalWatchdogActivity(clk.Now())
	// 20m after the reset (51m after creation): quiet-from-reset is under
	// threshold, quiet-from-creation is over. Silence proves the reset
	// survived the kind change.
	clk.Advance(20 * time.Minute)
	sess.checkGoalWatchdog(clk.Now())
	select {
	case ev := <-sess.Events():
		if ev.Kind == events.EventGoalWatchdog {
			t.Fatalf("post-reset kind change notified: %+v (reset must survive)", ev.Data)
		}
	default:
	}
}

// TestGoalWatchdogReregistrationStartsFreshStretch pins the re-registration
// branch (fix-9 M5): a new park anchor after the old stretch ended starts a
// fresh stretch at the new registration — the old stretch's notices do not
// leak (per-stretch count resets; only the per-24h ceiling carries over).
func TestGoalWatchdogReregistrationStartsFreshStretch(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	sess.getOrCreateGoalStore().Set("wait twice", clk.Now())
	w, ok := sess.getOrCreateGoalStore().RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: 2 * time.Hour}, clk.Now())
	if !ok {
		t.Fatal("precondition: registration should succeed")
	}
	drainGoalEvents(sess)

	clk.Advance(31 * time.Minute)
	sess.checkGoalWatchdog(clk.Now())
	ev := nextGoalEventOfKind(t, sess, events.EventGoalWatchdog)
	if d, ok := ev.Data.(events.GoalWatchdogData); !ok || d.Kind != "park-start" {
		t.Fatalf("first watchdog = %+v, want park-start", ev.Data)
	}
	// End the stretch and re-register on a new deadline: the new stretch
	// anchors at the new registration, so 20m later (under threshold)
	// nothing notifies even though 51m elapsed since goal creation.
	if !sess.CancelGoalWait(w.Lease.WaitID) {
		t.Fatal("precondition: cancel should end the stretch")
	}
	drainGoalEvents(sess)
	if _, ok := sess.getOrCreateGoalStore().RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: 2 * time.Hour}, clk.Now()); !ok {
		t.Fatal("precondition: re-registration should succeed")
	}
	drainGoalEvents(sess)
	clk.Advance(20 * time.Minute)
	sess.checkGoalWatchdog(clk.Now())
	select {
	case ev := <-sess.Events():
		if ev.Kind == events.EventGoalWatchdog {
			t.Fatalf("fresh stretch notified early: %+v (must anchor at re-registration)", ev.Data)
		}
	default:
	}
	// And the fresh stretch still notifies once it genuinely crosses: 31m
	// after the re-registration the park-start fires (per-stretch count
	// reset — the old stretch's notice did not leak).
	clk.Advance(11 * time.Minute)
	sess.checkGoalWatchdog(clk.Now())
	ev = nextGoalEventOfKind(t, sess, events.EventGoalWatchdog)
	if d, ok := ev.Data.(events.GoalWatchdogData); !ok || d.Kind != "park-start" {
		t.Fatalf("fresh stretch watchdog = %+v, want park-start after crossing", ev.Data)
	}
}
