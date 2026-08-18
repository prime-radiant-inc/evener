package appprojector

import (
	"testing"

	"primeradiant.com/evener/agent/events"
)

// TestGoalContinuationAdoptsItsDaemonTurnID is the projector half of the fix
// for kata c2ty's remainder. A goal continuation the daemon has already named
// must open its turn under THAT id: the daemon's mutation preconditions
// compare expectedTurnId against the same value, so a projector-minted
// turn_<n> here makes every steer, queue and interrupt aimed at the turn come
// back Conflict("turn is not active").
func TestGoalContinuationAdoptsItsDaemonTurnID(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")

	out := projector.Project(events.SessionEvent{
		Kind:      events.EventGoalContinuation,
		SessionID: "th_1",
		Data:      events.GoalContinuationData{Text: "Continuing toward: ship it", StableTurnID: "turn_m9"},
	})

	if got := turnStartedID(t, out); got != "turn_m9" {
		t.Fatalf("goal continuation opened turn %q, want the daemon's own turn_m9", got)
	}
	if got := projector.ActiveTurnID(); got != "turn_m9" {
		t.Fatalf("ActiveTurnID = %q, want turn_m9", got)
	}
}

// TestGoalContinuationWithoutAnIDStillMints keeps the unnamed path working:
// a session no daemon serves names nothing, and its turns must still open.
func TestGoalContinuationWithoutAnIDStillMints(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")

	out := projector.Project(events.SessionEvent{
		Kind:      events.EventGoalContinuation,
		SessionID: "th_1",
		Data:      events.GoalContinuationData{Text: "Continuing toward: ship it"},
	})

	if got := turnStartedID(t, out); got != "turn_1" {
		t.Fatalf("unnamed goal continuation opened turn %q, want the projector's own turn_1", got)
	}
}

// TestGoalContinuationCompletesThePreviousTurnUnderItsOwnID pins the ordering
// the out-of-band alternative got wrong: adopting the new turn's name must not
// swallow the turn/completed for the one it replaces.
func TestGoalContinuationCompletesThePreviousTurnUnderItsOwnID(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")

	projector.Project(events.SessionEvent{
		Kind:      events.EventUserInput,
		SessionID: "th_1",
		Data:      events.UserInputData{Text: "hello", StableTurnID: "turn_m1"},
	})
	out := projector.Project(events.SessionEvent{
		Kind:      events.EventGoalContinuation,
		SessionID: "th_1",
		Data:      events.GoalContinuationData{Text: "Continuing", StableTurnID: "turn_m2"},
	})

	if completed := completedTurnID(t, out); completed != "turn_m1" {
		t.Fatalf("previous turn completed as %q, want turn_m1", completed)
	}
	if got := turnStartedID(t, out); got != "turn_m2" {
		t.Fatalf("continuation opened turn %q, want turn_m2", got)
	}
}

// TestSteeringInjectedNeverNamesATurn pins the trap an earlier draft fell
// into. SteeringInjectedData.StableTurnID names the STEERING MUTATION's own
// durable record, not the turn the steer lands in — clientMutationSteer
// reserves a fresh one per steer. Adopting it would name the turn after the
// steer, and every control aimed at that turn would be rejected. See kata
// 7vmd for what naming a notification turn actually needs.
func TestSteeringInjectedNeverNamesATurn(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")

	projector.Project(events.SessionEvent{
		Kind:      events.EventSteeringInjected,
		SessionID: "th_1",
		Data:      events.SteeringInjectedData{Text: "look at this", StableTurnID: "turn_m77"},
	})
	out := projector.Project(events.SessionEvent{
		Kind:      events.EventAssistantTextStart,
		SessionID: "th_1",
		Data:      events.AssistantTextStartData{Model: "gpt-5"},
	})

	if got := turnStartedID(t, out); got == "turn_m77" {
		t.Fatal("a steering mutation's own id was adopted as the turn id; a steer names itself, not the turn it lands in")
	}
}
