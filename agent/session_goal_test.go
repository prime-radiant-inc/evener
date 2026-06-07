package agent

import (
	"context"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

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
