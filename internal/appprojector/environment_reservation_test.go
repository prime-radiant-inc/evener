package appprojector

import (
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
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

// An environment block opens a turn on its own durable identity and then hands
// back the reservation it borrowed from the input that was waiting for it. The
// reservation's PROVENANCE has to come back with it: a stable id the counter
// mistakes for one it minted spends no number, so every turn the projector
// names after it lands one short of where a reload numbers the same
// conversation.
func TestAppEventProjectorAnnouncementsKeepAPendingStableReservationStable(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	// The client's durable input identity is advertised before the environment
	// block arrives, which is the window this is about.
	projector.ReserveStableTurnID("turn_stable_input")
	var live []string
	project := func(event events.SessionEvent) {
		out := projector.Project(event)
		live = append(live, notificationThreadItem(t, out, appwire.NotifyItemCompleted).TurnID)
	}
	project(events.SessionEvent{Kind: events.EventEnvironment, SessionID: "th_1", Data: events.EnvironmentData{Text: "env block", TurnID: "turn_env_stable"}})
	// No id on the event: the reservation is what names this turn, which is
	// the whole point of reserving it.
	project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "first"}})
	project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "second"}})

	environment := schema.NewTurn(schema.TurnEnvironment, llm.System("env block"))
	environment.StableTurnID = "turn_env_stable"
	firstInput := schema.NewTurn(schema.TurnUserInput, llm.User("first"))
	firstInput.StableTurnID = "turn_stable_input"
	entries := []transcript.Entry{
		{Turn: environment},
		{Turn: firstInput},
		{Turn: schema.NewTurn(schema.TurnUserInput, llm.User("second"))},
	}
	turns, err := apptranscript.ItemTurnsFromEntries(transcript.Header{SessionID: "th_1"}, entries,
		func(turn schema.Turn, turnID string, turnIndex int) []appwire.ThreadItem {
			return apptranscript.ProjectTurn(turnID, turnIndex, turn, map[string]string{}, nil, apptranscript.ToolResultOutputImages)
		})
	if err != nil {
		t.Fatalf("cold projection: %v", err)
	}
	var cold []string
	for _, turn := range turns {
		cold = append(cold, turn.ID)
	}
	if len(live) != len(cold) {
		t.Fatalf("live turns %v, reload turns %v", live, cold)
	}
	for i := range live {
		if live[i] != cold[i] {
			t.Fatalf("live and reload disagree after an environment block crossed a pending stable reservation:\nlive: %v\ncold: %v", live, cold)
		}
	}
}
