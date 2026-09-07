package appprojector

import (
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

func TestEnvironmentTurnPreservesUserTurnIdentity(t *testing.T) {
	for _, active := range []bool{false, true} {
		name := "reserved"
		if active {
			name = "active"
		}
		t.Run(name, func(t *testing.T) {
			projector := NewAppEventProjector("session", "local:session")
			projector.ReserveStableTurnID("turn_m1")
			user := events.New(events.UserInputData{StableTurnID: "turn_m1", Text: "input"})
			if active {
				projector.Project(user)
			}
			reservedBefore, activeBefore := projector.reservedTurnID, projector.activeTurnID
			out := projector.Project(events.New(events.EnvironmentData{StableTurnID: "turn_env_context", Text: "context"}))
			if projector.reservedTurnID != reservedBefore || projector.activeTurnID != activeBefore {
				t.Fatal("environment context changed user turn identity")
			}
			if len(out) != 1 || out[0].Method != appwire.NotifyTurnCompleted {
				t.Fatalf("environment notifications: %v", out)
			}
			params, ok := out[0].Params.(appwire.TurnCompletedParams)
			if !ok || params.ThreadID != "session" || params.Ref != "local:session" || params.TurnID != "turn_env_context" || params.Turn.ID != params.TurnID {
				t.Fatalf("environment routing: %+v", out[0].Params)
			}
			if params.Turn.Status != appwire.TurnStatusCompleted || params.Turn.ItemsView != "full" || len(params.Turn.Items) != 1 {
				t.Fatal("environment must be one complete standalone turn")
			}
			item := params.Turn.Items[0]
			if item.Type != "systemMessage" || item.EventKind != appwire.ThreadItemEventKindEnvironment || item.TurnID != params.TurnID || item.Text != "context" {
				t.Fatalf("environment item: %+v", item)
			}
			if !active {
				started := notificationTurn(t, projector.Project(user), appwire.NotifyTurnStarted)
				if started.ID != "turn_m1" {
					t.Fatalf("user opened with %q", started.ID)
				}
			}
		})
	}
}
