package appprojector

import (
	"slices"
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

// An environment block opens a turn on its own durable identity and must leave
// the reservation it found untouched, provenance included: a stable id the
// counter mistakes for one it minted spends no number, so every turn the
// projector names after it lands one short of where a reload numbers the same
// conversation. Reload numbers a persisted turn by its entry index unless it
// carries a stable id, so the environment entry spends a number there too, and
// the live projector has to spend one for it in every ordering.
func TestAppEventProjectorAnnouncementsKeepAPendingStableReservationStable(t *testing.T) {
	environment := func(turnID string) events.SessionEvent {
		return events.SessionEvent{Kind: events.EventEnvironment, SessionID: "th_1", Data: events.EnvironmentData{Text: "env block", TurnID: turnID}}
	}
	input := func(text string) events.SessionEvent {
		// No id on the event: a pending reservation is what names this turn.
		return events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: text}}
	}
	environmentEntry := func(stableID string) schema.Turn {
		turn := schema.NewTurn(schema.TurnEnvironment, llm.System("env block"))
		turn.StableTurnID = stableID
		return turn
	}
	inputEntry := func(text, stableID string) schema.Turn {
		turn := schema.NewTurn(schema.TurnUserInput, llm.User(text))
		turn.StableTurnID = stableID
		return turn
	}
	// A step either reserves the client's durable input identity, releases
	// the pending reservation, or projects one event.
	type step struct {
		reserveStable string
		release       bool
		event         *events.SessionEvent
	}
	project := func(event events.SessionEvent) step { return step{event: &event} }
	for _, tc := range []struct {
		name    string
		steps   []step
		entries []schema.Turn
	}{
		{
			name:    "environment with its own id crosses a pending stable reservation",
			steps:   []step{{reserveStable: "turn_stable_input"}, project(environment("turn_env_stable")), project(input("first")), project(input("second"))},
			entries: []schema.Turn{environmentEntry("turn_env_stable"), inputEntry("first", "turn_stable_input"), inputEntry("second", "")},
		},
		{
			name:    "environment without an id crosses a pending stable reservation",
			steps:   []step{{reserveStable: "turn_stable_input"}, project(environment("")), project(input("first")), project(input("second"))},
			entries: []schema.Turn{environmentEntry(""), inputEntry("first", "turn_stable_input"), inputEntry("second", "")},
		},
		{
			name:    "the reservation the environment crossed is released unused",
			steps:   []step{{reserveStable: "turn_stable_input"}, project(environment("turn_env_stable")), {release: true}, project(input("second"))},
			entries: []schema.Turn{environmentEntry("turn_env_stable"), inputEntry("second", "")},
		},
		{
			name:    "environment after real work",
			steps:   []step{project(input("zero")), {reserveStable: "turn_stable_input"}, project(environment("turn_env_stable")), project(input("first")), project(input("second"))},
			entries: []schema.Turn{inputEntry("zero", ""), environmentEntry("turn_env_stable"), inputEntry("first", "turn_stable_input"), inputEntry("second", "")},
		},
		{
			name:    "environment with no reservation pending",
			steps:   []step{project(environment("turn_env_stable")), project(input("second"))},
			entries: []schema.Turn{environmentEntry("turn_env_stable"), inputEntry("second", "")},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projector := NewAppEventProjector("th_1", "local:th_1")
			var live []string
			for _, st := range tc.steps {
				switch {
				case st.reserveStable != "":
					projector.ReserveStableTurnID(st.reserveStable)
				case st.release:
					projector.ReleaseReservedTurnID(projector.ReservedTurnID())
				default:
					reserved, stable := projector.reservedTurnID, projector.reservedTurnIDIsStable
					live = append(live, notificationItemTurnID(t, projector.Project(*st.event), appwire.NotifyItemCompleted))
					if st.event.Kind == events.EventEnvironment && (projector.reservedTurnID != reserved || projector.reservedTurnIDIsStable != stable) {
						t.Fatalf("environment block changed the reservation from (%q, stable=%v) to (%q, stable=%v)", reserved, stable, projector.reservedTurnID, projector.reservedTurnIDIsStable)
					}
				}
			}

			entries := make([]transcript.Entry, len(tc.entries))
			for i, turn := range tc.entries {
				entries[i] = transcript.Entry{Turn: turn}
			}
			turns, err := apptranscript.ItemTurnsFromEntries(transcript.Header{SessionID: "th_1"}, entries,
				func(turn schema.Turn, turnID string, turnIndex int) []appwire.ThreadItem {
					return apptranscript.ProjectTurn(turnID, turnIndex, turn, apptranscript.NewToolCallRegistry(), nil, apptranscript.ToolResultOutputImages)
				})
			if err != nil {
				t.Fatalf("cold projection: %v", err)
			}
			cold := make([]string, len(turns))
			for i, turn := range turns {
				cold[i] = turn.ID
			}
			if !slices.Equal(live, cold) {
				t.Fatalf("live and reload number the conversation differently:\nlive: %v\ncold: %v", live, cold)
			}
		})
	}
}
