package appserver

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
)

type overflowWatchOwner struct {
	started          chan context.Context
	stalled, healthy chan appwire.SessionWatchUpdate
}

func (*overflowWatchOwner) State(context.Context) (appwire.ManagerView, error) {
	return appwire.ManagerView{Manager: true}, nil
}
func (*overflowWatchOwner) Invoke(context.Context, appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	return appwire.SessionOperationResponse{}, nil
}
func (o *overflowWatchOwner) Watch(ctx context.Context, r appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
	o.started <- ctx
	if r.Ref == "local:stalled" {
		return o.stalled, nil
	}
	return o.healthy, nil
}

// Mutation caught: overflow no longer tears down the bound connection, cleanup
// retains watch consumers, or an unrelated healthy watch cannot reconnect.
// This exercises actual WebSockets and both sides' runtime watch lifetimes.
func TestSessionToolsWatchOverflowCancelsSharedBindingAndReconnects(t *testing.T) {
	server := NewServer(ServerConfig{})
	type installation struct {
		ctx     context.Context
		runtime *sessionToolsConnection
	}
	installed := make(chan installation, 1)
	server.InstallSessionTools(func(ctx context.Context, _ appwire.SessionBinding, c appwire.SessionHubClient) error {
		installed <- installation{ctx, c.(*sessionToolsConnection)}
		return nil
	})
	endpoint := serveWebSocketHTTP(t, server)
	connect := func() (*appwire.Client, installation, *overflowWatchOwner) {
		t.Helper()
		c := appwire.NewClient(dialRawAppWire(t, endpoint))
		t.Cleanup(func() { _ = c.Close() })
		o := &overflowWatchOwner{started: make(chan context.Context, 2), stalled: make(chan appwire.SessionWatchUpdate), healthy: make(chan appwire.SessionWatchUpdate)}
		c.SetSessionTools(appwire.SessionBinding{Ref: "local:manager", InstanceID: "instance", HubID: "hub", BindingID: "binding"}, o)
		c.Start(t.Context())
		if _, err := c.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
			t.Fatal(err)
		}
		if err := c.BindSessionTools(t.Context()); err != nil {
			t.Fatal(err)
		}
		return c, waitFor(t, "runtime installation", installed), o
	}
	_, first, owner := connect()
	stalled, err := first.runtime.Watch(t.Context(), appwire.SessionWatchRequest{Ref: "local:stalled"})
	if err != nil {
		t.Fatal(err)
	}
	stalledCtx := waitFor(t, "stalled watch owner", owner.started)
	healthy, err := first.runtime.Watch(t.Context(), appwire.SessionWatchRequest{Ref: "local:healthy"})
	if err != nil {
		t.Fatal(err)
	}
	healthyCtx := waitFor(t, "healthy watch owner", owner.started)
	owner.healthy <- appwire.SessionWatchUpdate{Event: "question"}
	if update := waitFor(t, "healthy observation before overflow", healthy); update.Event != "question" {
		t.Fatalf("healthy update=%+v", update)
	}
	if cap(stalled) != 32 {
		t.Fatalf("consumer capacity=%d, want 32", cap(stalled))
	}
	// Never read this consumer until overflow has canceled the shared lifetime.
	for range 33 {
		select {
		case owner.stalled <- appwire.SessionWatchUpdate{Event: "question"}:
		case <-first.ctx.Done():
			t.Fatal("connection canceled before overflowing frame was admitted")
		case <-t.Context().Done():
			t.Fatal("producer stalled")
		}
	}
	waitFor(t, "overflow cancellation", first.ctx.Done())
	waitFor(t, "stalled producer cancellation", stalledCtx.Done())
	waitFor(t, "healthy producer cancellation", healthyCtx.Done())
	waitUnregistered(t, server)
	count := 0
	for range stalled {
		count++
	}
	if count != 32 {
		t.Fatalf("retained observations=%d, want bounded 32", count)
	}
	if _, open := <-healthy; open {
		t.Fatal("healthy consumer not closed by shared teardown")
	}
	first.runtime.mu.Lock()
	watches, pending := len(first.runtime.watches), len(first.runtime.pending)
	first.runtime.mu.Unlock()
	if watches != 0 || pending != 0 {
		t.Fatalf("teardown retained watches=%d pending=%d", watches, pending)
	}
	if _, err := first.runtime.State(t.Context()); err == nil {
		t.Fatal("disconnected binding accepted request")
	}
	// The actual connection/binding owners permit a replacement connection.
	// Durable agent rebind and completion dedup are checked separately, not faked here.
	_, second, replacement := connect()
	for _, ref := range []string{"local:stalled", "local:healthy"} {
		updates, err := second.runtime.Watch(t.Context(), appwire.SessionWatchRequest{Ref: ref})
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "replacement owner", replacement.started)
		source := replacement.healthy
		if ref == "local:stalled" {
			source = replacement.stalled
		}
		source <- appwire.SessionWatchUpdate{Event: "communicate"}
		if update := waitFor(t, "replacement observation", updates); update.Event != "communicate" {
			t.Fatalf("replacement update=%+v", update)
		}
	}
}
