package appserver

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

// This is a connection-frame unit test. The hub tests cover the same path over
// real authenticated WebSockets and the actual runtime registry.
func TestSessionToolsReverseResponsesBypassRequestQueue(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	server := NewServer(ServerConfig{ServerName: "runtime", SourceID: "local"})
	var client appwire.SessionHubClient
	server.InstallSessionTools(func(_ context.Context, _ appwire.SessionBinding, bound appwire.SessionHubClient) error {
		client = bound
		return nil
	})
	conn := server.NewConnection("runtime-test")
	conn.sessionLifetime = ctx
	defer conn.closeSessionTools()
	response := conn.HandleMessage(ctx, appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	response = conn.HandleMessage(ctx, appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodRuntimeSessionBind, appwire.SessionBinding{Ref: "local:workspace", InstanceID: "instance", HubID: "hub", BindingID: "binding"}))
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	result := make(chan error, 1)
	go func() {
		view, err := client.State(ctx)
		if err == nil && view.Setting.Enabled {
			t.Error("disabled state changed")
		}
		result <- err
	}()
	var request appwire.Message
	select {
	case request = <-conn.send:
	case <-ctx.Done():
		t.Fatal("no reverse request")
	}
	if request.Request == nil || request.Request.Method != appwire.MethodRuntimeSessionState || request.IDString() != "-1" {
		t.Fatalf("reverse namespace/request=%+v", request)
	}
	// There is deliberately no request worker: the receive boundary must route
	// this response straight to the waiter, not queue it behind runtime work.
	if !conn.receiveInbound(ctx, appwire.ResponseMessage(request.Request.ID, appwire.ManagerView{})) {
		t.Fatal("reverse response refused")
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("reverse response waited for request worker")
	}
	callCtx, stop := context.WithCancel(ctx)
	go func() { _, err := client.State(callCtx); result <- err }()
	select {
	case <-conn.send:
	case <-ctx.Done():
		t.Fatal("no cancellable request")
	}
	stop()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled request succeeded")
		}
	case <-ctx.Done():
		t.Fatal("cancelled request did not return")
	}
	conn.sessionTools.mu.Lock()
	pending := len(conn.sessionTools.pending)
	conn.sessionTools.mu.Unlock()
	if pending != 0 {
		t.Fatalf("cancelled pending requests=%d", pending)
	}
	watchCtx, stopWatch := context.WithCancel(ctx)
	go func() {
		_, err := client.Watch(watchCtx, appwire.SessionWatchRequest{Ref: "local:workspace"})
		result <- err
	}()
	select {
	case <-conn.send:
	case <-ctx.Done():
		t.Fatal("no pending watch request")
	}
	stopWatch()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled pending watch succeeded")
		}
	case <-ctx.Done():
		t.Fatal("pending watch did not cancel")
	}
	select {
	case unwatch := <-conn.send:
		if unwatch.Request == nil || unwatch.Request.Method != appwire.MethodRuntimeSessionUnwatch {
			t.Fatalf("cleanup=%+v", unwatch)
		}
	case <-ctx.Done():
		t.Fatal("cancelled pending watch left the remote consumer attached")
	}
}
