package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/goal"
)

// Round-15 watchdog tests (spec §6): the quiet-goal watchdog keys its state
// on the goal identity. store.Set stamps CreatedAt=now on every Set, so the
// key is (objective, CreatedAt) — a same-text retarget is a new goal and
// resets the stretch, the per-stretch notice count, and the per-goal 24h
// sentTimes ceiling. Deterministic: FakeClock + direct checkGoalWatchdog
// calls, mirroring session_goal_task8_test.go.

// TestFixWave15_WatchdogSameTextRetargetResetsStretch pins the round-15 LOW:
// a same-text SetGoal resets the watchdog stretch — a notice already sent on
// the old goal does not suppress the new goal's notice. Active goals carry
// no wait anchor, so no anchor-change branch can mask the bug here: before
// the fix the state keyed on objective text alone, the carried
// stretchNotices silenced the fresh goal, and this test times out waiting
// for its notice.
func TestFixWave15_WatchdogSameTextRetargetResetsStretch(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	sess.getOrCreateGoalStore().Set("same objective", clk.Now())
	drainGoalEvents(sess)

	clk.Advance(31 * time.Minute)
	sess.checkGoalWatchdog(clk.Now())
	ev := nextGoalEventOfKind(t, sess, events.EventGoalWatchdog)
	if d, ok := ev.Data.(events.GoalWatchdogData); !ok || d.Kind != "active-quiet" {
		t.Fatalf("first watchdog = %+v, want active-quiet", ev.Data)
	}

	// Same-text retarget: a new goal (fresh CreatedAt), still active.
	if _, err := sess.SetGoal(context.Background(), "same objective"); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	drainGoalEvents(sess)

	// The new goal's stretch anchors on its creation (backdated to Set time):
	// a further 31m of quiet crosses the threshold on the FRESH stretch.
	clk.Advance(31 * time.Minute)
	sess.checkGoalWatchdog(clk.Now())
	ev = nextGoalEventOfKind(t, sess, events.EventGoalWatchdog)
	if d, ok := ev.Data.(events.GoalWatchdogData); !ok || d.Kind != "active-quiet" {
		t.Fatalf("post-retarget watchdog = %+v, want a fresh active-quiet (same-text retarget resets the stretch)", ev.Data)
	}
}

// TestFixWave15_WatchdogSameTextRetargetResetsCeiling pins the ceiling half
// of the same LOW: the per-goal 24h sentTimes do not carry over a retarget —
// a new goal (even with identical text) gets a fresh per-goal ceiling. Four
// notices on the old goal must not silence the new one.
func TestFixWave15_WatchdogSameTextRetargetResetsCeiling(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	sess.getOrCreateGoalStore().Set("repeat objective", clk.Now())
	for i := range 4 {
		w, ok := sess.getOrCreateGoalStore().RegisterWait(
			goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: 2 * time.Hour},
			clk.Now(),
		)
		if !ok {
			t.Fatalf("stretch %d: registration should succeed", i+1)
		}
		clk.Advance(31 * time.Minute)
		sess.checkGoalWatchdog(clk.Now())
		nextGoalEventOfKind(t, sess, events.EventGoalWatchdog)
		if !sess.CancelGoalWait(w.Lease.WaitID) {
			t.Fatalf("stretch %d: cancel should end the stretch", i+1)
		}
		drainGoalEvents(sess)
	}
	sess.mu.Lock()
	sent := len(sess.goalWatchdog.sentTimes)
	sess.mu.Unlock()
	if sent != 4 {
		t.Fatalf("precondition: sentTimes = %d, want 4 (ceiling reached on the old goal)", sent)
	}

	// Same-text retarget: the ceiling resets with the goal.
	if _, err := sess.SetGoal(context.Background(), "repeat objective"); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if _, ok := sess.getOrCreateGoalStore().RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: 2 * time.Hour}, clk.Now()); !ok {
		t.Fatal("precondition: post-retarget registration should succeed")
	}
	drainGoalEvents(sess)

	clk.Advance(31 * time.Minute)
	sess.checkGoalWatchdog(clk.Now())
	ev := nextGoalEventOfKind(t, sess, events.EventGoalWatchdog)
	if d, ok := ev.Data.(events.GoalWatchdogData); !ok || d.Kind != "park-start" {
		t.Fatalf("post-retarget watchdog = %+v, want park-start (ceiling resets on new goal)", ev.Data)
	}
}

// TestFixWave15_WatchdogSameStretchKeepsState pins the non-reset side: within
// one goal (no retarget) the existing semantics hold — the per-stretch cap
// still suppresses a repeat poll and the ceiling still counts across
// stretches. A guard against over-resetting (e.g. keying on wall time or
// resetting every check).
func TestFixWave15_WatchdogSameStretchKeepsState(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	sess.getOrCreateGoalStore().Set("steady objective", clk.Now())
	if _, ok := sess.getOrCreateGoalStore().RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: 24 * time.Hour}, clk.Now()); !ok {
		t.Fatal("precondition: 24h until_time registration should succeed")
	}
	drainGoalEvents(sess)

	clk.Advance(31 * time.Minute)
	sess.checkGoalWatchdog(clk.Now())
	nextGoalEventOfKind(t, sess, events.EventGoalWatchdog)
	// Repeat poll in the same stretch: no second notice (per-stretch cap).
	sess.checkGoalWatchdog(clk.Now())
	select {
	case ev := <-sess.Events():
		if ev.Kind == events.EventGoalWatchdog {
			t.Fatalf("repeat-poll watchdog notice: %+v (want none, per-stretch cap)", ev.Data)
		}
	default:
	}
	// Half-deadline anchor (12h) still fires on the same stretch.
	clk.Advance(12*time.Hour - 31*time.Minute)
	sess.checkGoalWatchdog(clk.Now())
	ev := nextGoalEventOfKind(t, sess, events.EventGoalWatchdog)
	if d, ok := ev.Data.(events.GoalWatchdogData); !ok || d.Kind != "half-deadline" {
		t.Fatalf("second watchdog = %+v, want half-deadline on the undisturbed stretch", ev.Data)
	}
}
