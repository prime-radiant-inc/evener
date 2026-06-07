package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/goal"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// newGoalMethodSession builds an idle session (no events drained) for testing the
// SetGoal/ClearGoal Session methods directly.
func newGoalMethodSession(t *testing.T) *Session {
	t.Helper()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return sess
}

// TestSetGoalRejectsEmpty: SetGoal rejects an empty or whitespace objective and
// does not store a goal.
func TestSetGoalRejectsEmpty(t *testing.T) {
	sess := newGoalMethodSession(t)
	defer sess.Close()

	for _, obj := range []string{"", "   ", "\t\n"} {
		started, err := sess.SetGoal(context.Background(), obj)
		if err == nil {
			t.Fatalf("SetGoal(%q) should reject empty objective", obj)
		}
		if started {
			t.Fatalf("SetGoal(%q) should not report started", obj)
		}
	}
	if _, ok := sess.getOrCreateGoalStore().Snapshot(); ok {
		t.Fatal("SetGoal with empty objective must not store a goal")
	}
}

// TestSetGoalIdleKicks: on an idle session with a kick callback wired, SetGoal
// stores the goal, kicks with the rendered first continuation prompt, and reports
// started=true.
func TestSetGoalIdleKicks(t *testing.T) {
	sess := newGoalMethodSession(t)
	defer sess.Close()

	var mu sync.Mutex
	var kicks []string
	sess.SetKickFunc(func(prompt string) {
		mu.Lock()
		kicks = append(kicks, prompt)
		mu.Unlock()
	})

	started, err := sess.SetGoal(context.Background(), "ship the feature")
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if !started {
		t.Fatal("SetGoal on an idle session with a kick wired should report started=true")
	}

	snap, ok := sess.getOrCreateGoalStore().Snapshot()
	if !ok {
		t.Fatal("SetGoal should store an active goal")
	}
	if snap.Objective != "ship the feature" || snap.Status != goal.StatusActive {
		t.Fatalf("stored goal = {Objective:%q Status:%q}, want {ship the feature, active}", snap.Objective, snap.Status)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(kicks) != 1 {
		t.Fatalf("kick count = %d, want exactly 1", len(kicks))
	}
	if kicks[0] != goal.Render("ship the feature") {
		t.Fatalf("kick prompt = %q, want the rendered continuation prompt", kicks[0])
	}
}

// TestSetGoalInTurnDefersToGate: while a turn is running (goalInTurn set), SetGoal
// stores the goal but does NOT kick — the running drain-loop gate will pick it up
// — and reports started=false.
func TestSetGoalInTurnDefersToGate(t *testing.T) {
	sess := newGoalMethodSession(t)
	defer sess.Close()

	var kicked bool
	sess.SetKickFunc(func(prompt string) { kicked = true })

	// Simulate an in-flight turn.
	sess.mu.Lock()
	sess.goalInTurn = true
	sess.mu.Unlock()

	started, err := sess.SetGoal(context.Background(), "ship the feature")
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if started {
		t.Fatal("SetGoal during a running turn should report started=false (the gate backs it)")
	}
	if kicked {
		t.Fatal("SetGoal during a running turn must NOT kick")
	}
	if _, ok := sess.getOrCreateGoalStore().Snapshot(); !ok {
		t.Fatal("SetGoal should still store the goal even when deferring to the gate")
	}
}

// TestSetGoalNoKickFuncDoesNotStart: with no kick callback wired, an idle SetGoal
// stores the goal but reports started=false (there is no way to start it
// immediately; a later turn's gate is the backstop).
func TestSetGoalNoKickFuncDoesNotStart(t *testing.T) {
	sess := newGoalMethodSession(t)
	defer sess.Close()

	started, err := sess.SetGoal(context.Background(), "ship the feature")
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if started {
		t.Fatal("SetGoal with no kick callback should report started=false")
	}
	if _, ok := sess.getOrCreateGoalStore().Snapshot(); !ok {
		t.Fatal("SetGoal should still store the goal")
	}
}

// TestClearGoalRemovesGoal: ClearGoal removes a previously set goal.
func TestClearGoalRemovesGoal(t *testing.T) {
	sess := newGoalMethodSession(t)
	defer sess.Close()

	sess.getOrCreateGoalStore().Set("some objective", time.Now())
	if _, ok := sess.getOrCreateGoalStore().Snapshot(); !ok {
		t.Fatal("precondition: goal should be set")
	}

	sess.ClearGoal()
	if _, ok := sess.getOrCreateGoalStore().Snapshot(); ok {
		t.Fatal("ClearGoal should remove the goal")
	}
}

// TestProcessInputDelegatesAsUserInput verifies that the unchanged
// ProcessInput(ctx, input, images) entry point still records a user-input turn
// (i.e. it delegates with kind=EntryUserInput). The model is scripted to finish
// the turn immediately via the result tool.
func TestProcessInputDelegatesAsUserInput(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("all done") },
		},
	})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if _, err := sess.ProcessInput(context.Background(), "do the thing", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()
	var sawUserInput bool
	for _, turn := range sess.history {
		if turn.Kind == schema.TurnUserInput {
			sawUserInput = true
			break
		}
	}
	if !sawUserInput {
		t.Fatalf("ProcessInput should append a TurnUserInput turn; history kinds=%s", turnKinds(sess.history))
	}
}

// TestAcceptContinuationIsSteeringNotUser verifies that acceptContinuationInput
// appends a TurnSteering turn (not TurnUserInput), does not bump s.turns, emits
// EventGoalContinuation, and does NOT emit EventUserInput.
func TestAcceptContinuationIsSteeringNotUser(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	stop := drainEvents(sess)

	sess.mu.Lock()
	beforeTurns := sess.turns
	sess.mu.Unlock()

	sess.acceptContinuationInput(context.Background(), "CONTINUE")

	sess.mu.Lock()
	afterTurns := sess.turns
	last := sess.history[len(sess.history)-1]
	sess.mu.Unlock()

	if afterTurns != beforeTurns {
		t.Fatalf("acceptContinuationInput must not increment s.turns: before=%d after=%d", beforeTurns, afterTurns)
	}
	if last.Kind != schema.TurnSteering {
		t.Fatalf("last history turn kind = %v, want TurnSteering", last.Kind)
	}

	sess.Close()
	evs := stop()

	var sawContinuation, sawUserInput bool
	for _, ev := range evs {
		switch ev.Kind {
		case events.EventGoalContinuation:
			sawContinuation = true
			if d, ok := ev.Data.(events.GoalContinuationData); ok {
				if d.Text != "CONTINUE" {
					t.Fatalf("EventGoalContinuation text = %q, want %q", d.Text, "CONTINUE")
				}
			} else {
				t.Fatalf("EventGoalContinuation carried %T, want GoalContinuationData", ev.Data)
			}
		case events.EventUserInput:
			sawUserInput = true
		}
	}
	if !sawContinuation {
		t.Fatal("acceptContinuationInput should emit EventGoalContinuation")
	}
	if sawUserInput {
		t.Fatal("acceptContinuationInput must NOT emit EventUserInput")
	}
}
