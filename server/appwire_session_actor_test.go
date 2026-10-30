package server

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

func TestManagerInputNeverFallsBackToHumanCallback(t *testing.T) {
	s := NewServer(ServerConfig{})
	defer s.Close()
	s.SetAppIdentity("local", "worker")
	s.UpdateSessionInfo("worker", "", "")
	s.SetState("idle")
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

func TestManagerInputProofAuthenticatesBindingButLabelsCaller(t *testing.T) {
	session := newNotesTestSession(t)
	s := NewServer(ServerConfig{})
	defer s.Close()
	s.SetAppIdentity("local", session.WorkspaceID())
	s.SetSessionTools(func() *agent.Session { return session })
	client := dialServerAppWire(t, s)
	workerRef := "local:" + session.WorkspaceID()
	binding := appwire.SessionBinding{Ref: workerRef, InstanceID: session.ID(), HubID: "hub", BindingID: "worker-binding"}
	var bound appwire.SessionBinding
	if err := client.Request(t.Context(), appwire.MethodRuntimeSessionBind, binding, &bound); err != nil {
		t.Fatal(err)
	}
	proof := appwire.SessionActorProof{BindingID: binding.BindingID, CallerRef: "local:manager-origin", Actor: appwire.SessionActor{Kind: "manager"}}
	actor, manager, err := s.inputActor(t.Context(), &proof)
	if err != nil || !manager || actor.Kind != "manager" || actor.ManagerRef != proof.CallerRef {
		t.Fatalf("valid caller proof: actor=%+v manager=%v err=%v", actor, manager, err)
	}
	for _, caller := range []string{"", "not-a-ref", workerRef} {
		bad := proof
		bad.CallerRef = caller
		if _, _, err := s.inputActor(t.Context(), &bad); err == nil {
			t.Fatalf("unauthentic caller ref %q accepted", caller)
		}
	}
	bad := proof
	bad.BindingID = "not-this-worker-binding"
	if _, _, err := s.inputActor(t.Context(), &bad); err == nil {
		t.Fatal("forged binding ID accepted")
	}
	bad = proof
	bad.Actor.ManagerRef = workerRef
	if _, _, err := s.inputActor(t.Context(), &bad); err == nil {
		t.Fatal("forged actor label accepted")
	}
}
