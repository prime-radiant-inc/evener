package server

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

func TestManagerInputNeverFallsBackToHumanCallback(t *testing.T) {
	s := NewServer(ServerConfig{})
	defer s.Close()
	s.SetAppIdentity("local", "worker")
	s.SetStatus(StatusInfo{SessionID: "worker", State: "idle"})
	humanCalls, managerCalls := 0, 0
	s.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
		Start: func(appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			humanCalls++
			return appwire.TurnStartResponse{}, nil
		},
		StartAs: func(p appwire.TurnStartParams, a schema.SessionActor) (appwire.TurnStartResponse, error) {
			managerCalls++
			if a.Kind != "manager" || a.ManagerRef != "local:manager" {
				t.Fatalf("actor=%+v", a)
			}
			return appwire.TurnStartResponse{}, nil
		},
	})
	params := appwire.TurnStartParams{ThreadID: "worker", ExpectedInstanceID: "worker", ClientMutationID: "one", Input: []appwire.InputItem{{Type: "text", Text: "answer"}}}
	if _, err := s.handleAppTurnStart(withSessionActor(context.Background(), schema.SessionActor{Kind: "manager", ManagerRef: "local:manager"}), params); err != nil {
		t.Fatal(err)
	}
	if managerCalls != 1 || humanCalls != 0 {
		t.Fatalf("manager=%d human=%d", managerCalls, humanCalls)
	}
	s.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{Start: func(appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		humanCalls++
		return appwire.TurnStartResponse{}, nil
	}})
	if _, err := s.handleAppTurnStart(withSessionActor(context.Background(), schema.SessionActor{Kind: "manager", ManagerRef: "local:manager"}), params); err == nil {
		t.Fatal("manager fell back to human callback")
	}
	if humanCalls != 0 {
		t.Fatalf("human callback called %d times", humanCalls)
	}
}
