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

func completedTurnID(out []AppNotification) string {
	for _, n := range out {
		if n.Method != appwire.NotifyTurnCompleted {
			continue
		}
		params, ok := n.Params.(map[string]any)
		if !ok {
			continue
		}
		if turn, ok := params["turn"].(appwire.Turn); ok {
			return turn.ID
		}
	}
	return ""
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

	if got := completedTurnID(out); got != "turn_m1" {
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
	out := projector.Project(events.SessionEvent{
		Kind:      events.EventAssistantTextStart,
		SessionID: "th_1",
		Data:      events.AssistantTextStartData{Model: "gpt-5"},
	})

	if got := projector.ActiveTurnID(); got != "turn_m2" {
		t.Fatalf("after the notification boundary ActiveTurnID = %q, want turn_m2", got)
	}
	for _, n := range out {
		params, ok := n.Params.(appwire.ItemLifecycleParams)
		if !ok {
			continue
		}
		if params.TurnID != "turn_m2" {
			t.Fatalf("the notification turn's item carries turnId %q, want turn_m2 — it joined the previous turn", params.TurnID)
		}
	}
}
