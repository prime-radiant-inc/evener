package appprojector

import (
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

func TestEnvironmentPreservesReservedRunnableIdentity(t *testing.T) {
	projector := NewAppEventProjector("environment-reservation", "local:environment-reservation")
	reserved := projector.ReserveTurnID()
	environment := projector.Project(events.SessionEvent{Kind: events.EventEnvironment, Data: events.EnvironmentData{TurnID: "turn_environment", Text: "context"}})
	if got := notificationThreadItem(t, environment, appwire.NotifyItemCompleted).TurnID; got != "turn_environment" {
		t.Fatalf("environment turn = %q, want its durable identity", got)
	}
	if got := projector.ReservedTurnID(); got != reserved {
		t.Fatalf("runnable reservation after environment = %q, want %q", got, reserved)
	}
	user := projector.Project(events.SessionEvent{Kind: events.EventUserInput, Data: events.UserInputData{Text: "input"}})
	if got := notificationThreadItem(t, user, appwire.NotifyItemCompleted).TurnID; got != reserved {
		t.Fatalf("user turn = %q, want advertised runnable identity %q", got, reserved)
	}
	if got := projector.ReservedTurnID(); got != "" {
		t.Fatalf("user input left reservation %q", got)
	}
}

func TestEmptyEnvironmentDoesNotOpenTurnOrConsumeReservation(t *testing.T) {
	projector := NewAppEventProjector("empty-environment", "local:empty-environment")
	reserved := projector.ReserveTurnID()
	notifications := projector.Project(events.SessionEvent{Kind: events.EventEnvironment, Data: events.EnvironmentData{TurnID: "turn_empty", Text: " \n\t "}})
	if len(notifications) != 0 {
		t.Fatalf("empty environment emitted %d notifications, want none", len(notifications))
	}
	if got := projector.ReservedTurnID(); got != reserved {
		t.Fatalf("empty environment changed runnable reservation to %q, want %q", got, reserved)
	}
}

func TestEnvironmentWithoutIdentityDoesNotUseRunnableReservation(t *testing.T) {
	projector := NewAppEventProjector("environment-reservation", "local:environment-reservation")
	reserved := projector.ReserveTurnID()
	environment := projector.Project(events.SessionEvent{Kind: events.EventEnvironment, Data: events.EnvironmentData{Text: "context"}})
	environmentID := notificationThreadItem(t, environment, appwire.NotifyItemCompleted).TurnID
	if environmentID == "" || environmentID == reserved {
		t.Fatalf("environment identity = %q, must be distinct from runnable reservation %q", environmentID, reserved)
	}
	user := projector.Project(events.SessionEvent{Kind: events.EventUserInput, Data: events.UserInputData{Text: "input"}})
	if got := notificationThreadItem(t, user, appwire.NotifyItemCompleted).TurnID; got != reserved {
		t.Fatalf("user turn = %q, want advertised runnable identity %q", got, reserved)
	}
}
