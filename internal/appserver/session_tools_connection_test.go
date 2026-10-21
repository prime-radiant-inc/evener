package appserver

import (
	"context"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

type blockedSessionInvokeOwner struct {
	started chan context.Context
}

func (*blockedSessionInvokeOwner) State(context.Context) (appwire.ManagerView, error) {
	return appwire.ManagerView{Ref: "local:workspace"}, nil
}
func (o *blockedSessionInvokeOwner) Invoke(ctx context.Context, _ appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	o.started <- ctx
	<-ctx.Done()
	return appwire.SessionOperationResponse{}, ctx.Err()
}
func (*blockedSessionInvokeOwner) Watch(context.Context, appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
	return nil, appwire.Unavailable("unused")
}

func TestSessionToolsCancelledInvokeReleasesOwner(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	server := NewServer(ServerConfig{})
	bound := make(chan appwire.SessionHubClient, 1)
	server.InstallSessionTools(func(_ context.Context, _ appwire.SessionBinding, client appwire.SessionHubClient) error {
		bound <- client
		return nil
	})
	transport := dialRawAppWire(t, serveWebSocketHTTP(t, server))
	client := appwire.NewClient(transport)
	defer client.Close()
	owner := &blockedSessionInvokeOwner{started: make(chan context.Context, 1)}
	client.SetSessionTools(appwire.SessionBinding{Ref: "local:workspace", InstanceID: "instance", HubID: "hub", BindingID: "binding"}, owner)
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	if err := client.BindSessionTools(ctx); err != nil {
		t.Fatal(err)
	}
	runtime := <-bound
	callCtx, stop := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() {
		_, err := runtime.Invoke(callCtx, appwire.SessionOperationRequest{Read: &appwire.ThreadReadParams{Ref: "local:workspace"}})
		result <- err
	}()
	var ownerCtx context.Context
	select {
	case ownerCtx = <-owner.started:
	case <-ctx.Done():
		t.Fatal("owner did not start")
	}
	stop()
	if err := <-result; err == nil {
		t.Fatal("cancelled invoke succeeded")
	}
	select {
	case <-ownerCtx.Done():
	case <-ctx.Done():
		t.Fatal("cancelled invoke left owner running on healthy connection")
	}
	view, err := runtime.State(ctx)
	if err != nil || view.Ref != "local:workspace" {
		t.Fatalf("subsequent operation failed without disconnect: %+v %v", view, err)
	}
}

func TestSessionToolsReverseResponseWithSaturatedIngress(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	server := NewServer(ServerConfig{})
	server.requestQueueCapacity = 1
	blocked := make(chan struct{})
	server.blockedEnqueue = sync.OnceFunc(func() { close(blocked) })
	started, release := parkThreadList(t, server)
	defer release()
	bound := make(chan appwire.SessionHubClient, 1)
	server.InstallSessionTools(func(_ context.Context, _ appwire.SessionBinding, client appwire.SessionHubClient) error {
		bound <- client
		return nil
	})
	transport := dialRawAppWire(t, serveWebSocketHTTP(t, server))
	initializeRaw(t, transport)
	sendRaw(t, transport, rawRequest(t, 2, appwire.MethodRuntimeSessionBind, appwire.SessionBinding{Ref: "local:workspace", InstanceID: "instance", HubID: "hub", BindingID: "binding"}))
	for {
		message, err := transport.Recv(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if message.IDString() == "2" {
			if message.Error != nil {
				t.Fatal(message.Error)
			}
			break
		}
	}
	client := <-bound
	result := make(chan error, 1)
	go func() { _, err := client.State(ctx); result <- err }()
	var reverse appwire.Message
	for reverse.Request == nil {
		var err error
		reverse, err = transport.Recv(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}
	sendRaw(t, transport, rawRequest(t, 3, appwire.MethodThreadList, appwire.ThreadListParams{}))
	waitFor(t, "blocked serial handler", started)
	sendRaw(t, transport, rawRequest(t, 4, appwire.MethodThreadList, appwire.ThreadListParams{}))
	sendRaw(t, transport, rawRequest(t, 5, appwire.MethodThreadList, appwire.ThreadListParams{}))
	waitFor(t, "full queue blocking receive loop", blocked)
	sendRaw(t, transport, appwire.ResponseMessage(reverse.Request.ID, appwire.ManagerView{}))
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("reverse response trapped behind saturated ordinary ingress: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("reverse response trapped behind saturated ordinary ingress")
	}
}

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
	select {
	case frame := <-conn.send:
		if frame.Request == nil || frame.Request.Method != appwire.MethodRuntimeSessionCancel {
			t.Fatalf("missing owner cancellation: %+v", frame)
		}
	case <-ctx.Done():
		t.Fatal("cancelled request left remote owner running")
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
	for _, method := range []string{appwire.MethodRuntimeSessionCancel, appwire.MethodRuntimeSessionUnwatch} {
		select {
		case frame := <-conn.send:
			if frame.Request == nil || frame.Request.Method != method {
				t.Fatalf("cleanup=%+v, want %s", frame, method)
			}
		case <-ctx.Done():
			t.Fatal("cancelled pending watch left the remote consumer attached")
		}
	}
}
