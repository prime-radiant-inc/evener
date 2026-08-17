package appprojector

import (
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/appwire"
)

func turnStartedID(t *testing.T, out []AppNotification) string {
	t.Helper()
	var found string
	for _, n := range out {
		if n.Method != appwire.NotifyTurnStarted {
			continue
		}
		params, ok := n.Params.(appwire.TurnStartedParams)
		if !ok {
			t.Fatalf("turn/started params are %T, want appwire.TurnStartedParams", n.Params)
		}
		if found != "" {
			t.Fatalf("two turn/started in one projection: %q and %q", found, params.Turn.ID)
		}
		found = params.Turn.ID
	}
	if found == "" {
		t.Fatalf("no turn/started in projection %+v", out)
	}
	return found
}

// completedTurnID pulls the turn id out of the turn/completed notification in a
// projection. It fails the test on a malformed frame rather than skipping it,
// and on a second completion in one projection: silently returning the first
// valid one would let a projection that also emits a malformed or duplicate
// completion pass.
func completedTurnID(t *testing.T, out []AppNotification) string {
	t.Helper()
	var found string
	var seen int
	for _, n := range out {
		if n.Method != appwire.NotifyTurnCompleted {
			continue
		}
		params, ok := n.Params.(map[string]any)
		if !ok {
			t.Fatalf("turn/completed params are %T, want map[string]any", n.Params)
		}
		turn, ok := params["turn"].(appwire.Turn)
		if !ok {
			t.Fatalf("turn/completed carries %T, want appwire.Turn", params["turn"])
		}
		seen++
		if seen > 1 {
			t.Fatalf("two turn/completed notifications in one projection: %q and %q", found, turn.ID)
		}
		found = turn.ID
	}
	return found
}

func statusChangedType(out []AppNotification) string {
	for _, n := range out {
		if n.Method != appwire.NotifyThreadStatusChanged {
			continue
		}
		if params, ok := n.Params.(appwire.ThreadStatusChangedParams); ok {
			return params.Status.Type
		}
	}
	return ""
}

// TestTurnBoundaryOpensTheNamedTurn pins the identity half: the daemon's own
// turn id must become the projection's, because that is the id it publishes
// and the id its mutation preconditions compare against.
func TestTurnBoundaryOpensTheNamedTurn(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")

	out := projector.Project(events.SessionEvent{
		Kind:      events.EventTurnStarted,
		SessionID: "th_1",
		Data:      events.TurnStartedData{TurnID: "turn_m9"},
	})

	if got := turnStartedID(t, out); got != "turn_m9" {
		t.Fatalf("boundary opened turn %q, want the daemon's own turn_m9", got)
	}
	if got := projector.ActiveTurnID(); got != "turn_m9" {
		t.Fatalf("ActiveTurnID = %q, want turn_m9", got)
	}
}

// TestTurnBoundaryPublishesActive pins the visibility half. Status and
// capabilities ride one frame, and the composer renders Stop and Steer only
// when it believes the thread is active — so a turn named perfectly but never
// published active offers no controls at all.
func TestTurnBoundaryPublishesActive(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")

	out := projector.Project(events.SessionEvent{
		Kind:      events.EventTurnStarted,
		SessionID: "th_1",
		Data:      events.TurnStartedData{TurnID: "turn_m9"},
	})

	if got := statusChangedType(out); got != appwire.ThreadStatusActive {
		t.Fatalf("thread status published as %q, want %q", got, appwire.ThreadStatusActive)
	}
}

// TestTurnBoundaryWithoutAnIDStillOpensATurn keeps the two halves separable: a
// turn the daemon could not name — an interrupt already pending, or another
// mutation still holding the slot — is still a turn, and still must not have
// its items folded into the one before it.
func TestTurnBoundaryWithoutAnIDStillOpensATurn(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")

	out := projector.Project(events.SessionEvent{
		Kind:      events.EventTurnStarted,
		SessionID: "th_1",
		Data:      events.TurnStartedData{},
	})

	if got := turnStartedID(t, out); got != "turn_1" {
		t.Fatalf("unnamed boundary opened turn %q, want the projector's own turn_1", got)
	}
}

// TestTurnBoundaryWithoutAnIDDoesNotPublishActive is the other half of the
// same rule, and the more important one: status and capabilities ride a single
// frame, so announcing active hands the composer steer:true/interrupt:true and
// renders Stop and Steer. For a turn the daemon could not name, every control
// they offer is compared against a durable id it does not hold and is rejected
// with nothing shown (kata 2f41). Absent buttons are today's behaviour there;
// lying buttons would be a regression.
func TestTurnBoundaryWithoutAnIDDoesNotPublishActive(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")

	out := projector.Project(events.SessionEvent{
		Kind:      events.EventTurnStarted,
		SessionID: "th_1",
		Data:      events.TurnStartedData{},
	})

	if got := statusChangedType(out); got != "" {
		t.Fatalf("an unnameable turn published status %q; it must publish none", got)
	}
}

// TestTurnBoundaryCompletesThePreviousTurn pins that the boundary closes what
// it replaces. EventTurnEnded only stashes timing, so without this the
// previous turn stays open and collects the next turn's items.
func TestTurnBoundaryCompletesThePreviousTurn(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")

	projector.Project(events.SessionEvent{
		Kind:      events.EventUserInput,
		SessionID: "th_1",
		Data:      events.UserInputData{Text: "hello", StableTurnID: "turn_m1"},
	})
	out := projector.Project(events.SessionEvent{
		Kind:      events.EventTurnStarted,
		SessionID: "th_1",
		Data:      events.TurnStartedData{TurnID: "turn_m2"},
	})

	if got := completedTurnID(t, out); got != "turn_m1" {
		t.Fatalf("previous turn completed as %q, want turn_m1", got)
	}
	if got := turnStartedID(t, out); got != "turn_m2" {
		t.Fatalf("boundary opened turn %q, want turn_m2", got)
	}
}

// TestNotificationTurnDoesNotJoinThePreviousTurn is the interleave case: a
// notification turn running inside the drain loop, with no EventSessionEnd
// between it and the turn before. Its content must carry its own turn id.
//
// Asserted on the assistant item, not the steering notification —
// serf/steering/injected's params carry no turn id at all.
func TestNotificationTurnDoesNotJoinThePreviousTurn(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")

	projector.Project(events.SessionEvent{
		Kind:      events.EventUserInput,
		SessionID: "th_1",
		Data:      events.UserInputData{Text: "hello", StableTurnID: "turn_m1"},
	})
	projector.Project(events.SessionEvent{
		Kind:      events.EventAssistantTextStart,
		SessionID: "th_1",
		Data:      events.AssistantTextStartData{Model: "gpt-5"},
	})
	projector.Project(events.SessionEvent{
		Kind:      events.EventTurnStarted,
		SessionID: "th_1",
		Data:      events.TurnStartedData{TurnID: "turn_m2"},
	})
	projector.Project(events.SessionEvent{
		Kind:      events.EventSteeringInjected,
		SessionID: "th_1",
		Data:      events.SteeringInjectedData{Text: "job done", Kind: events.SteeringKindNotification},
	})
	projector.Project(events.SessionEvent{
		Kind:      events.EventAssistantTextStart,
		SessionID: "th_1",
		Data:      events.AssistantTextStartData{Model: "gpt-5"},
	})
	// The DELTA is what materializes the item and stamps a turn id on it.
	// EventAssistantTextStart only calls ensureTurn, which returns nothing at
	// all once a turn is open — asserting over its output proves nothing.
	out := projector.Project(events.SessionEvent{
		Kind:      events.EventAssistantTextDelta,
		SessionID: "th_1",
		Data:      events.AssistantTextDeltaData{Delta: "working"},
	})

	if got := projector.ActiveTurnID(); got != "turn_m2" {
		t.Fatalf("after the notification boundary ActiveTurnID = %q, want turn_m2", got)
	}
	var stamped int
	for _, n := range out {
		params, ok := n.Params.(appwire.ItemLifecycleParams)
		if !ok {
			continue
		}
		stamped++
		if params.TurnID != "turn_m2" {
			t.Fatalf("the notification turn's item carries turnId %q, want turn_m2 — it joined the previous turn", params.TurnID)
		}
	}
	if stamped == 0 {
		t.Fatalf("no item carried a turn id in %+v; the assertion above never ran", out)
	}
}
