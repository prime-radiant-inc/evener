package agent

import (
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/goal"
)

// TestGoalTerminalReportEmittedOnce pins the fix for the re-emit defect: a
// terminated goal lingers in the store (only /goal clear removes it) and the gate
// runs at every turn tail, so the gate must emit EventGoalEnded exactly once, not
// on every subsequent turn. Simulates update_goal("complete") followed by three
// further turn tails.
func TestGoalTerminalReportEmittedOnce(t *testing.T) {
	t.Parallel()
	sess, stop := newGateSession(t)
	store := sess.getOrCreateGoalStore()
	store.Set("obj", time.Now())
	store.SetTerminal(goal.StatusComplete, "", time.Now()) // as update_goal("complete") would

	for i := 0; i < 3; i++ {
		if _, ok := sess.armGoalContinuation(false, true); ok {
			t.Fatalf("turn %d: a terminal goal must not continue", i)
		}
	}
	evs := stop()
	if n := countGoalEnded(evs); n != 1 {
		t.Fatalf("EventGoalEnded emitted %d times across 3 post-completion turns, want exactly 1", n)
	}
}

// TestSettleGoalOnIdleKicksWindowGoal pins the fix for the idle-kick stall: a goal
// set in the turn-tail window (goalInTurn still true, after the gate's store read)
// must be kicked at the idle transition rather than stranded active-but-idle until
// the next user message (spec §7).
func TestSettleGoalOnIdleKicksWindowGoal(t *testing.T) {
	t.Parallel()
	sess, stop := newGateSession(t)
	defer stop()

	var kicked []string
	sess.SetKickFunc(func(prompt string) { kicked = append(kicked, prompt) })

	// Window: a turn is "in progress" and a goal was just set under it.
	sess.mu.Lock()
	sess.goalInTurn = true
	sess.mu.Unlock()
	sess.getOrCreateGoalStore().Set("window goal", time.Now())

	sess.settleGoalOnIdle()

	sess.mu.Lock()
	inTurn := sess.goalInTurn
	sess.mu.Unlock()
	if inTurn {
		t.Fatal("settleGoalOnIdle must clear goalInTurn")
	}
	if len(kicked) != 1 {
		t.Fatalf("window goal kicked %d times, want exactly 1", len(kicked))
	}
}

// TestSettleGoalOnIdleNoKickWhenTerminal confirms the idle settle does not kick a
// goal that already finished (only a fresh active goal set in the window is kicked).
func TestSettleGoalOnIdleNoKickWhenTerminal(t *testing.T) {
	t.Parallel()
	sess, stop := newGateSession(t)
	defer stop()

	kicked := 0
	sess.SetKickFunc(func(string) { kicked++ })
	store := sess.getOrCreateGoalStore()
	store.Set("g", time.Now())
	store.SetTerminal(goal.StatusComplete, "", time.Now())

	sess.settleGoalOnIdle()
	if kicked != 0 {
		t.Fatalf("a terminal goal must not be kicked at idle, got %d kicks", kicked)
	}
}
