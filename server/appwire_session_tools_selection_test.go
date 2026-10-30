package server

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
)

func TestSessionToolsSelectedHubDoesNotFallBackAfterDisconnect(t *testing.T) {
	t.Parallel()
	session := newNotesTestSession(t)
	clients := &runtimeHubClients{clients: make(map[string]*runtimeHubClient)}
	owners := map[string]*selectionProbeOwner{"hub-a": {}, "hub-b": {}}
	install := func(hub string) context.CancelFunc {
		life, cancel := context.WithCancel(t.Context())
		clients.clients[hub] = &runtimeHubClient{
			current: func() *agent.Session { return session },
			session: session,
			binding: appwire.SessionBinding{Ref: "local:" + session.WorkspaceID(), InstanceID: session.ID(), HubID: hub},
			client:  owners[hub],
			ctx:     life,
			cancel:  cancel,
		}
		return cancel
	}

	cancelA := install("hub-a")
	cancelB := install("hub-b")
	cancelB()
	request := appwire.SessionOperationRequest{Discover: &appwire.ThreadListParams{}}
	if _, err := clients.Invoke(t.Context(), request); err != nil {
		t.Fatalf("initial invocation through A: %v", err)
	}
	if owners["hub-a"].invokes != 1 || owners["hub-b"].invokes != 0 {
		t.Fatalf("initial invocation counts A/B = %d/%d", owners["hub-a"].invokes, owners["hub-b"].invokes)
	}

	cancelB = install("hub-b")
	defer cancelB()
	cancelA()
	if _, err := clients.Invoke(t.Context(), request); err == nil {
		t.Fatal("invocation succeeded after selected A disconnected")
	}
	if owners["hub-b"].invokes != 0 {
		t.Fatalf("disconnected selected A fell back to B, B invocation count = %d", owners["hub-b"].invokes)
	}

	// Rebinding A restores service even when B is no longer live.
	cancelB()
	install("hub-a")
	if _, err := clients.Invoke(t.Context(), request); err != nil {
		t.Fatalf("invocation after A reconnected: %v", err)
	}
	if owners["hub-a"].invokes != 2 || owners["hub-b"].invokes != 0 {
		t.Fatalf("post-reconnect invocation counts A/B = %d/%d", owners["hub-a"].invokes, owners["hub-b"].invokes)
	}

	// Two live owners remain a conflict, including after A has been selected.
	cancelB = install("hub-b")
	defer cancelB()
	if _, err := clients.admitted(t.Context()); err == nil {
		t.Fatal("simultaneous live A and B bindings were admitted")
	} else {
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
			t.Fatalf("simultaneous live bindings error = %v, want conflict", err)
		}
	}
}

type selectionProbeOwner struct{ invokes int }

func (*selectionProbeOwner) State(context.Context) (appwire.ManagerView, error) {
	return appwire.ManagerView{Manager: true}, nil
}
func (o *selectionProbeOwner) Invoke(context.Context, appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	o.invokes++
	return appwire.SessionOperationResponse{Discover: &appwire.SessionDiscovery{}}, nil
}
func (*selectionProbeOwner) Watch(context.Context, appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
	return nil, nil
}
