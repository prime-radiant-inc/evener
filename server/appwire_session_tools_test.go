package server

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
)

func TestSessionToolsRealConnectionBinding(t *testing.T) {
	t.Parallel()
	session := newNotesTestSession(t)
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", session.WorkspaceID())
	srv.SetSessionTools(func() *agent.Session { return session })
	client := dialServerAppWire(t, srv)
	binding := appwire.SessionBinding{Ref: "local:" + session.WorkspaceID(), InstanceID: session.ID(), HubID: "hub-test", BindingID: "connection-test"}
	var bound appwire.SessionBinding
	forged := binding
	forged.InstanceID = "another-instance"
	if err := client.Request(context.Background(), appwire.MethodRuntimeSessionBind, forged, &bound); err == nil {
		t.Fatal("forged runtime identity accepted")
	}
	if err := client.Request(context.Background(), appwire.MethodRuntimeSessionBind, binding, &bound); err != nil {
		t.Fatalf("bind discovered live runtime over real connection: %v", err)
	}
	if bound != binding {
		t.Fatalf("binding = %+v, want %+v", bound, binding)
	}
	if err := client.Request(context.Background(), appwire.MethodRuntimeSessionBind, binding, &bound); err == nil {
		t.Fatal("duplicate connection binding accepted")
	}
}

type authorityState struct{ view appwire.ManagerView }

func (s *authorityState) State(context.Context) (appwire.ManagerView, error) { return s.view, nil }
func (*authorityState) Invoke(context.Context, appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	return appwire.SessionOperationResponse{}, nil
}
func (*authorityState) Watch(ctx context.Context, _ appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
	out := make(chan appwire.SessionWatchUpdate)
	go func() { <-ctx.Done(); close(out) }()
	return out, nil
}

func TestSessionToolsAuthoritySelection(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	session := newNotesTestSession(t)
	clients := &runtimeHubClients{clients: map[string]*runtimeHubClient{}}
	states := map[string]*authorityState{}
	for _, hub := range []string{"hub-a", "hub-b"} {
		life, cancel := context.WithCancel(t.Context())
		defer cancel()
		state := &authorityState{view: appwire.ManagerView{Setting: appwire.ManagerSetting{HubID: hub}}}
		states[hub] = state
		clients.clients[hub] = &runtimeHubClient{current: func() *agent.Session { return session }, session: session, binding: appwire.SessionBinding{InstanceID: session.ID()}, client: state, ctx: life, cancel: cancel}
	}
	states["hub-a"].view.Setting.Enabled = true
	states["hub-b"].view.Setting.Enabled = true
	if _, err := clients.State(t.Context()); err == nil {
		t.Fatal("conflicting enabled hubs selected by connection order")
	}
	states["hub-b"].view.Setting.Enabled = false
	view, err := clients.State(t.Context())
	if err != nil || view.Setting.HubID != "hub-a" {
		t.Fatalf("human-enabled authority=%+v err=%v", view, err)
	}
	updates, err := clients.Watch(t.Context(), appwire.SessionWatchRequest{})
	if err != nil {
		t.Fatal(err)
	}
	clients.clients["hub-a"].cancel()
	states["hub-b"].view.Setting.Enabled = true
	if _, err := clients.State(t.Context()); err == nil {
		t.Fatal("disconnected authority silently switched hubs")
	}
	select {
	case _, ok := <-updates:
		if ok {
			t.Fatal("replaced binding delivered observation")
		}
	case <-ctx.Done():
		t.Fatal("replaced binding did not cancel observation")
	}
}
