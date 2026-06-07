package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/goal"
	"primeradiant.com/serf/llm"
)

// newGateSession builds a session with a fake "openai" adapter and starts
// draining its events. The returned stop func closes the session and returns
// every emitted event (call sess.Close() is performed by stop()).
func newGateSession(t *testing.T) (*Session, func() []events.SessionEvent) {
	t.Helper()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	stop := drainEvents(sess)
	return sess, func() []events.SessionEvent {
		sess.Close()
		return stop()
	}
}

func countGoalEnded(evs []events.SessionEvent) int {
	n := 0
	for _, ev := range evs {
		if ev.Kind == events.EventGoalEnded {
			n++
		}
	}
	return n
}

func lastGoalEnded(t *testing.T, evs []events.SessionEvent) events.GoalEndedData {
	t.Helper()
	var found bool
	var data events.GoalEndedData
	for _, ev := range evs {
		if ev.Kind != events.EventGoalEnded {
			continue
		}
		d, ok := ev.Data.(events.GoalEndedData)
		if !ok {
			t.Fatalf("EventGoalEnded carried %T, want GoalEndedData", ev.Data)
		}
		data = d
		found = true
	}
	if !found {
		t.Fatal("no EventGoalEnded emitted")
	}
	return data
}

// TestArmGoalContinuation: with an active goal, armGoalContinuation(true) returns
// a non-empty continuation prompt and true. After the goal is marked complete,
// armGoalContinuation returns ("", false) and the gate emits one
// EventGoalEnded{Status:"complete"}.
func TestArmGoalContinuation(t *testing.T) {
	sess, stop := newGateSession(t)

	store := sess.getOrCreateGoalStore()
	store.Set("ship the feature", time.Now())

	prompt, ok := sess.armGoalContinuation(true)
	if !ok {
		t.Fatal("armGoalContinuation(true) on an active goal should return ok=true")
	}
	if prompt == "" {
		t.Fatal("armGoalContinuation(true) should return a non-empty continuation prompt")
	}

	// Model declares the goal complete via update_goal -> terminal status.
	if !store.SetTerminal(goal.StatusComplete, "", time.Now()) {
		t.Fatal("SetTerminal(complete) should succeed on the active goal")
	}

	prompt, ok = sess.armGoalContinuation(false)
	if ok || prompt != "" {
		t.Fatalf("armGoalContinuation after complete = (%q, %v), want (\"\", false)", prompt, ok)
	}

	evs := stop()
	if got := countGoalEnded(evs); got != 1 {
		t.Fatalf("EventGoalEnded count = %d, want 1", got)
	}
	if d := lastGoalEnded(t, evs); d.Status != "complete" {
		t.Fatalf("EventGoalEnded.Status = %q, want %q", d.Status, "complete")
	}
}

// TestArmGoalContinuationNoProgressBlocks: after the goal's first progressed
// turn, NoProgressLimit consecutive no-progress continuations flip it to blocked
// with StopReason "no progress" and emit exactly one EventGoalEnded.
func TestArmGoalContinuationNoProgressBlocks(t *testing.T) {
	sess, stop := newGateSession(t)

	store := sess.getOrCreateGoalStore()
	store.Set("do the impossible", time.Now())

	// First progressed turn establishes the grace baseline (streak accrues only
	// after the first progressed turn).
	if _, ok := sess.armGoalContinuation(true); !ok {
		t.Fatal("first progressed continuation should keep the goal active")
	}

	// NoProgressLimit no-progress continuations: the last one blocks.
	var lastOK bool
	for i := 0; i < goal.NoProgressLimit; i++ {
		_, lastOK = sess.armGoalContinuation(false)
	}
	if lastOK {
		t.Fatalf("after %d no-progress continuations the goal should be blocked (ok=false)", goal.NoProgressLimit)
	}

	snap, ok := store.Snapshot()
	if !ok {
		t.Fatal("goal snapshot missing")
	}
	if snap.Status != goal.StatusBlocked {
		t.Fatalf("status = %q, want blocked", snap.Status)
	}
	if snap.StopReason != "no progress" {
		t.Fatalf("StopReason = %q, want %q", snap.StopReason, "no progress")
	}

	evs := stop()
	if got := countGoalEnded(evs); got != 1 {
		t.Fatalf("EventGoalEnded count = %d, want 1", got)
	}
	d := lastGoalEnded(t, evs)
	if d.Status != "blocked" || d.Reason != "no progress" {
		t.Fatalf("EventGoalEnded = {Status:%q Reason:%q}, want {blocked, no progress}", d.Status, d.Reason)
	}
}

// TestArmGoalContinuationIterationCap: a goal that keeps making progress but
// never self-declares complete is stopped by the iteration cap. The gate sets
// status blocked with reason "hit iteration limit" and emits EventGoalEnded.
func TestArmGoalContinuationIterationCap(t *testing.T) {
	sess, stop := newGateSession(t)

	store := sess.getOrCreateGoalStore()
	store.Set("never-ending work", time.Now())

	var stopped bool
	var iters int
	// The cap is a backstop: drive enough progressed continuations that the
	// iteration cap is the only thing that can stop the loop.
	for i := 0; i < goal.DefaultMaxIterations+2; i++ {
		_, ok := sess.armGoalContinuation(true)
		iters++
		if !ok {
			stopped = true
			break
		}
	}
	if !stopped {
		t.Fatalf("iteration cap never tripped after %d continuations", iters)
	}

	snap, ok := store.Snapshot()
	if !ok {
		t.Fatal("goal snapshot missing")
	}
	if snap.Status != goal.StatusBlocked {
		t.Fatalf("status = %q, want blocked", snap.Status)
	}
	if snap.StopReason != "hit iteration limit" {
		t.Fatalf("StopReason = %q, want %q", snap.StopReason, "hit iteration limit")
	}

	evs := stop()
	if got := countGoalEnded(evs); got != 1 {
		t.Fatalf("EventGoalEnded count = %d, want 1", got)
	}
	d := lastGoalEnded(t, evs)
	if d.Status != "blocked" || d.Reason != "hit iteration limit" {
		t.Fatalf("EventGoalEnded = {Status:%q Reason:%q}, want {blocked, hit iteration limit}", d.Status, d.Reason)
	}
}

// TestTerminateGoalOnErrorClassification: a plain error and a DeadlineExceeded
// transition an active goal to blocked and emit EventGoalEnded; a context that
// queuedInputDrainContext classifies as a genuine user interrupt leaves the goal
// active and emits no EventGoalEnded.
func TestTerminateGoalOnErrorClassification(t *testing.T) {
	// userInterruptCtx constructs a context exactly as production does for a
	// genuine user /interrupt: a marked, cancelled turn context. Paired with a
	// bare context.Canceled error, queuedInputDrainContext reports ok=true.
	userInterruptCtx := func() context.Context {
		root := context.Background()
		turnCtx, cancel := context.WithCancel(root)
		marked := WithQueuedInputDrainOnInterrupt(turnCtx, root)
		cancel()
		return marked
	}

	tests := []struct {
		name        string
		ctx         context.Context
		err         error
		wantBlocked bool
	}{
		{"plain error", context.Background(), errors.New("boom"), true},
		{"deadline exceeded", context.Background(), context.DeadlineExceeded, true},
		{"user interrupt stays active", userInterruptCtx(), context.Canceled, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sess, stop := newGateSession(t)

			store := sess.getOrCreateGoalStore()
			store.Set("some objective", time.Now())

			sess.terminateGoalOnError(tc.ctx, tc.err)

			snap, ok := store.Snapshot()
			if !ok {
				t.Fatal("goal snapshot missing")
			}
			evs := stop()

			if tc.wantBlocked {
				if snap.Status != goal.StatusBlocked {
					t.Fatalf("status = %q, want blocked", snap.Status)
				}
				if snap.StopReason != tc.err.Error() {
					t.Fatalf("StopReason = %q, want %q", snap.StopReason, tc.err.Error())
				}
				if got := countGoalEnded(evs); got != 1 {
					t.Fatalf("EventGoalEnded count = %d, want 1", got)
				}
				if d := lastGoalEnded(t, evs); d.Status != "blocked" {
					t.Fatalf("EventGoalEnded.Status = %q, want blocked", d.Status)
				}
			} else {
				if snap.Status != goal.StatusActive {
					t.Fatalf("status = %q, want active (user interrupt must not block)", snap.Status)
				}
				if got := countGoalEnded(evs); got != 0 {
					t.Fatalf("EventGoalEnded count = %d, want 0 (no report on user interrupt)", got)
				}
			}
		})
	}
}

// TestRoundCapSelection verifies the per-goal-turn round-cap selection (C13):
// continuation turns clamp an unbounded or >GoalTurnMaxRounds config down to
// GoalTurnMaxRounds; a config already <= the cap is left untouched; user-input
// turns are never clamped.
func TestRoundCapSelection(t *testing.T) {
	tests := []struct {
		name string
		cfg  int
		kind EntryKind
		want int
	}{
		{"unbounded continuation clamps", -1, EntryContinuation, goal.GoalTurnMaxRounds},
		{"over-cap continuation clamps", 200, EntryContinuation, goal.GoalTurnMaxRounds},
		{"under-cap continuation untouched", 10, EntryContinuation, 10},
		{"user input never clamped", goal.GoalTurnMaxRounds, EntryUserInput, goal.GoalTurnMaxRounds},
		{"unbounded user input stays unbounded", -1, EntryUserInput, -1},
		{"over-cap user input stays", 200, EntryUserInput, 200},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := goalRoundCap(tc.cfg, tc.kind); got != tc.want {
				t.Fatalf("goalRoundCap(%d, %v) = %d, want %d", tc.cfg, tc.kind, got, tc.want)
			}
		})
	}
}
