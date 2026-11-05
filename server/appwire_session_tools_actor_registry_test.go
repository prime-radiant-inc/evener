package server

import (
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

func TestSessionToolsActorResolverStaysWithRuntimeRegistry(t *testing.T) {
	first := newNotesTestSession(t)
	current := first
	s := NewServer(ServerConfig{})
	defer s.Close()
	ref := "local:" + first.ID()
	prepareIdentity := func(session *agent.Session) {
		t.Helper()
		prepared, err := PrepareAppIdentityForRef("local", session.ID(), ref, "")
		if err != nil {
			t.Fatal(err)
		}
		s.ReplaceAppIdentity(prepared, nil)
	}
	prepareIdentity(first)
	s.SetSessionTools(func() *agent.Session { return current })
	client := dialServerAppWire(t, s)

	bind := func(session *agent.Session, hubID, bindingID string) {
		t.Helper()
		binding := appwire.SessionBinding{
			Ref:        ref,
			InstanceID: session.ID(),
			HubID:      hubID,
			BindingID:  bindingID,
		}
		var bound appwire.SessionBinding
		if err := client.Request(t.Context(), appwire.MethodRuntimeSessionBind, binding, &bound); err != nil {
			t.Fatal(err)
		}
	}
	resolver := func() func(appwire.SessionActorProof) (schema.SessionActor, error) {
		t.Helper()
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.resolveManagerActor
	}
	proof := func(bindingID string) appwire.SessionActorProof {
		return appwire.SessionActorProof{BindingID: bindingID, CallerRef: "local:manager-origin", Actor: appwire.SessionActor{Kind: "manager"}}
	}
	bind(first, "first-hub", "first-binding")
	oldResolver := resolver()
	if _, err := oldResolver(proof("first-binding")); err != nil {
		t.Fatalf("first resolver rejected its binding: %v", err)
	}

	second := newNotesTestSession(t)
	current = second
	prepareIdentity(second)
	client = dialServerAppWire(t, s)
	bind(second, "second-hub", "second-binding")
	currentResolver := resolver()
	if _, err := oldResolver(proof("second-binding")); err == nil {
		t.Fatal("old resolver accepted replacement runtime binding")
	}
	if _, err := currentResolver(proof("second-binding")); err != nil {
		t.Fatalf("current resolver rejected replacement binding: %v", err)
	}
	if _, err := currentResolver(proof("first-binding")); err == nil {
		t.Fatal("current resolver accepted stale binding proof")
	}
}
