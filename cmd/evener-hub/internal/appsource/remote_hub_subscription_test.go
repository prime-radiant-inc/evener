package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

// pushableRemote wires a RemoteHubSource to an in-memory StreamTransport like
// newScriptedRemote, but additionally lets the test push unsolicited
// notifications to the client from the server side. StreamTransport.Send is
// mutex-guarded, so a test push and the responder loop can interleave safely.
type pushableRemote struct {
	source *RemoteHubSource
	server *appwire.StreamTransport
	ctx    context.Context
	calls  func() []remoteCall
	push   func(method string, params any) error
}

func newPushableRemote(t *testing.T, id string, handle func(method string, params json.RawMessage) scriptedReply) *pushableRemote {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	server := appwire.NewStreamTransport(serverConn)

	var mu sync.Mutex
	var calls []remoteCall

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			msg, err := server.Recv(ctx)
			if err != nil {
				return
			}
			if msg.Request == nil {
				continue
			}
			mu.Lock()
			calls = append(calls, remoteCall{method: msg.Request.Method, params: msg.Request.Params})
			mu.Unlock()
			if msg.Request.Method == appwire.MethodInitialize {
				data, _ := json.Marshal(appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"})
				if err := server.Send(ctx, appwire.ResponseMessage(msg.Request.ID, json.RawMessage(data))); err != nil {
					return
				}
				continue
			}
			reply := handle(msg.Request.Method, msg.Request.Params)
			if reply.closeConn {
				_ = server.Close()
				return
			}
			if reply.wireErr != nil {
				if err := server.Send(ctx, appwire.ErrorMessage(msg.Request.ID, *reply.wireErr)); err != nil {
					return
				}
				continue
			}
			data, err := json.Marshal(reply.result)
			if err != nil {
				return
			}
			if err := server.Send(ctx, appwire.ResponseMessage(msg.Request.ID, json.RawMessage(data))); err != nil {
				return
			}
		}
	}()

	client := appwire.NewClient(appwire.NewStreamTransport(clientConn))
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{}); err != nil {
		cancel()
		t.Fatalf("initialize pushable remote: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = client.Close()
		<-done
	})

	source := NewRemoteHubSource(id, nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})
	return &pushableRemote{
		source: source,
		server: server,
		ctx:    ctx,
		calls: func() []remoteCall {
			mu.Lock()
			defer mu.Unlock()
			out := make([]remoteCall, len(calls))
			copy(out, calls)
			return out
		},
		push: func(method string, params any) error {
			return server.Send(ctx, appwire.NotificationMessage(method, params))
		},
	}
}

func recvNotification(t *testing.T, ch <-chan appwire.Notification) (appwire.Notification, bool) {
	t.Helper()
	select {
	case n, ok := <-ch:
		return n, ok
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a notification")
		return appwire.Notification{}, false
	}
}

func assertNoNotification(t *testing.T, ch <-chan appwire.Notification, wait time.Duration) {
	t.Helper()
	select {
	case n, ok := <-ch:
		if !ok {
			t.Fatal("subscription channel closed unexpectedly")
		}
		t.Fatalf("unexpected notification: method=%q params=%s", n.Method, n.Params)
	case <-time.After(wait):
	}
}

func decodeNotificationParams[T any](t *testing.T, n appwire.Notification) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(n.Params, &out); err != nil {
		t.Fatalf("decode notification %q params %s: %v", n.Method, n.Params, err)
	}
	return out
}

// Test 1: wire contract + snapshot discarded.
func TestRemoteHubSubscribeThreadWireContract(t *testing.T) {
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		if method == appwire.MethodThreadRead {
			return scriptedReply{result: appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID:     "S",
				Source: "local",
				Evener: appwire.EvenerThread{Ref: "local:S"},
			}}}
		}
		t.Errorf("unexpected method %q", method)
		return scriptedReply{result: appwire.ThreadReadResponse{}}
	})
	ctx := t.Context()

	out, err := remote.source.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: "host:S"})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}

	reads := 0
	var params appwire.ThreadReadParams
	for _, call := range remote.calls() {
		if call.method != appwire.MethodThreadRead {
			continue
		}
		reads++
		if err := json.Unmarshal(call.params, &params); err != nil {
			t.Fatalf("decode subscribe params: %v", err)
		}
	}
	if reads != 1 {
		t.Fatalf("thread/read sent %d times, want exactly 1", reads)
	}
	if params.Ref != "local:S" || params.ThreadID != "S" || !params.Subscribe {
		t.Fatalf("subscribe params = %+v, want ref=local:S threadId=S subscribe=true", params)
	}

	// The snapshot response must never surface on the channel; only pushed
	// notifications do.
	if err := remote.push(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
		ThreadID: "S",
		Ref:      "local:S",
	}); err != nil {
		t.Fatalf("push: %v", err)
	}
	n, ok := recvNotification(t, out)
	if !ok {
		t.Fatal("subscription channel closed before the pushed notification")
	}
	if n.Method != appwire.NotifyThreadStatusChanged {
		t.Fatalf("first notification = %q, want the pushed %q (snapshot leaked)", n.Method, appwire.NotifyThreadStatusChanged)
	}
}

// Test 2: translation end-to-end.
func TestRemoteHubSubscribeThreadTranslatesNotifications(t *testing.T) {
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.ThreadReadResponse{}}
	})
	ctx := t.Context()

	out, err := remote.source.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: "host:S"})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}

	if err := remote.push(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
		ThreadID: "S",
		Ref:      "local:S",
	}); err != nil {
		t.Fatalf("push status: %v", err)
	}
	n, ok := recvNotification(t, out)
	if !ok {
		t.Fatal("channel closed before status notification")
	}
	status := decodeNotificationParams[appwire.ThreadStatusChangedParams](t, n)
	if status.Ref != "host:S" {
		t.Fatalf("status Ref = %q, want host:S", status.Ref)
	}
	if status.ThreadID != "S" {
		t.Fatalf("status ThreadID = %q, want S (must not be rewritten)", status.ThreadID)
	}

	if err := remote.push(appwire.NotifyThreadStarted, appwire.ThreadStartedParams{
		ThreadID: "S",
		Ref:      "local:S",
		Thread: appwire.Thread{
			ID:     "S",
			Source: "local",
			Evener: appwire.EvenerThread{Ref: "local:S", ParentRef: "local:P", InstanceID: "xyz"},
		},
	}); err != nil {
		t.Fatalf("push started: %v", err)
	}
	n, ok = recvNotification(t, out)
	if !ok {
		t.Fatal("channel closed before started notification")
	}
	started := decodeNotificationParams[appwire.ThreadStartedParams](t, n)
	if started.Thread.Source != "host" {
		t.Fatalf("nested Source = %q, want host", started.Thread.Source)
	}
	if started.Thread.Evener.Ref != "host:S" {
		t.Fatalf("nested Evener.Ref = %q, want host:S", started.Thread.Evener.Ref)
	}
	if started.Thread.Evener.ParentRef != "host:P" {
		t.Fatalf("nested Evener.ParentRef = %q, want host:P", started.Thread.Evener.ParentRef)
	}
	if started.Thread.Evener.InstanceID != "xyz" {
		t.Fatalf("nested InstanceID = %q, want xyz untouched", started.Thread.Evener.InstanceID)
	}
}

// Test 3: fan-out isolation over one client.
func TestRemoteHubSubscribeThreadFanOutIsolation(t *testing.T) {
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.ThreadReadResponse{}}
	})
	ctx := t.Context()

	outS, err := remote.source.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: "host:S"})
	if err != nil {
		t.Fatalf("subscribe S: %v", err)
	}
	outT, err := remote.source.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: "host:T"})
	if err != nil {
		t.Fatalf("subscribe T: %v", err)
	}

	if err := remote.push(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{ThreadID: "S", Ref: "local:S"}); err != nil {
		t.Fatalf("push S: %v", err)
	}
	if err := remote.push(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{ThreadID: "T", Ref: "local:T"}); err != nil {
		t.Fatalf("push T: %v", err)
	}
	// A notification for a thread nobody subscribed to must be dropped.
	if err := remote.push(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{ThreadID: "U", Ref: "local:U"}); err != nil {
		t.Fatalf("push U: %v", err)
	}

	nS, ok := recvNotification(t, outS)
	if !ok {
		t.Fatal("S channel closed")
	}
	statusS := decodeNotificationParams[appwire.ThreadStatusChangedParams](t, nS)
	if statusS.ThreadID != "S" || statusS.Ref != "host:S" {
		t.Fatalf("S subscriber got threadId=%q ref=%q, want S / host:S", statusS.ThreadID, statusS.Ref)
	}

	nT, ok := recvNotification(t, outT)
	if !ok {
		t.Fatal("T channel closed")
	}
	statusT := decodeNotificationParams[appwire.ThreadStatusChangedParams](t, nT)
	if statusT.ThreadID != "T" || statusT.Ref != "host:T" {
		t.Fatalf("T subscriber got threadId=%q ref=%q, want T / host:T", statusT.ThreadID, statusT.Ref)
	}

	assertNoNotification(t, outS, 200*time.Millisecond)
	assertNoNotification(t, outT, 200*time.Millisecond)
}

// Test 4: continuous draining prevents notification-buffer overflow teardown.
func TestRemoteHubSubscribeThreadDrainPreventsOverflow(t *testing.T) {
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		switch method {
		case appwire.MethodThreadRead:
			return scriptedReply{result: appwire.ThreadReadResponse{}}
		case appwire.MethodModelList:
			return scriptedReply{result: appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "p", Model: "m"}}}}
		default:
			t.Errorf("unexpected method %q", method)
			return scriptedReply{result: appwire.EmptyResponse{}}
		}
	})
	ctx := t.Context()

	if _, err := remote.source.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: "host:S"}); err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}

	for i := 0; i <= appwire.NotificationBufferCap; i++ {
		if err := remote.push(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{ThreadID: "U", Ref: "local:U"}); err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
	}

	// Without a continuously draining reader, the client would have torn the
	// connection down at the first overflow; a later request would fail.
	if _, err := remote.source.ListModels(ctx, appwire.ModelListParams{}); err != nil {
		t.Fatalf("client was torn down by notification overflow: %v", err)
	}
}

// Test 5: the returned channel closes on cancellation, and re-subscribing works.
func TestRemoteHubSubscribeThreadClosesOnCancel(t *testing.T) {
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.ThreadReadResponse{}}
	})

	ctx, cancel := context.WithCancel(context.Background())
	out, err := remote.source.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: "host:S"})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}
	cancel()

	select {
	case _, ok := <-out:
		if ok {
			t.Fatal("received a notification after cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscription channel did not close on cancellation")
	}

	// A fresh subscription for the same thread must work after the first ended.
	ctx2 := t.Context()
	out2, err := remote.source.SubscribeThread(ctx2, appwire.ThreadReadParams{Ref: "host:S"})
	if err != nil {
		t.Fatalf("re-subscribe: %v", err)
	}
	if err := remote.push(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{ThreadID: "S", Ref: "local:S"}); err != nil {
		t.Fatalf("push after re-subscribe: %v", err)
	}
	n, ok := recvNotification(t, out2)
	if !ok {
		t.Fatal("re-subscription channel closed before the pushed notification")
	}
	status := decodeNotificationParams[appwire.ThreadStatusChangedParams](t, n)
	if status.Ref != "host:S" {
		t.Fatalf("re-subscription Ref = %q, want host:S", status.Ref)
	}
}

// Test 6: a foreign ref is refused before any wire request.
func TestRemoteHubSubscribeThreadForeignRefRefused(t *testing.T) {
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		t.Errorf("unexpected method %q", method)
		return scriptedReply{result: appwire.ThreadReadResponse{}}
	})

	_, err := remote.source.SubscribeThread(context.Background(), appwire.ThreadReadParams{Ref: "other:X"})
	if err == nil {
		t.Fatal("SubscribeThread accepted a foreign ref")
	}
	if !strings.Contains(err.Error(), "source not found: other") {
		t.Fatalf("error = %q, want it to mention source not found: other", err.Error())
	}
	for _, call := range remote.calls() {
		if call.method == appwire.MethodThreadRead {
			t.Fatalf("foreign ref was forwarded: %+v", remote.calls())
		}
	}
}

// Test 7: a notification whose ref names a nested remote hub is dropped.
func TestRemoteHubSubscribeThreadNestedRefDropped(t *testing.T) {
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.ThreadReadResponse{}}
	})
	ctx := t.Context()

	out, err := remote.source.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: "host:S"})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}

	if err := remote.push(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{ThreadID: "X", Ref: "otherhub:X"}); err != nil {
		t.Fatalf("push nested: %v", err)
	}
	assertNoNotification(t, out, 200*time.Millisecond)

	// The subscription must still be usable afterwards.
	if err := remote.push(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{ThreadID: "S", Ref: "local:S"}); err != nil {
		t.Fatalf("push valid: %v", err)
	}
	n, ok := recvNotification(t, out)
	if !ok {
		t.Fatal("channel closed after the dropped notification")
	}
	status := decodeNotificationParams[appwire.ThreadStatusChangedParams](t, n)
	if status.Ref != "host:S" {
		t.Fatalf("Ref = %q, want host:S", status.Ref)
	}
}

// Test 8: a transport failure on the subscribe request maps to
// SessionUnavailable naming the host.
func TestRemoteHubSubscribeThreadErrorMapping(t *testing.T) {
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		if method == appwire.MethodThreadRead {
			return scriptedReply{closeConn: true}
		}
		return scriptedReply{result: appwire.ThreadReadResponse{}}
	})

	_, err := remote.source.SubscribeThread(context.Background(), appwire.ThreadReadParams{Ref: "host:S"})
	if err == nil {
		t.Fatal("SubscribeThread succeeded after the remote closed the connection")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error = %T %v, want appwire.WireError", err, err)
	}
	if wire.Code != appwire.CodeUnavailable {
		t.Fatalf("code = %d, want %d", wire.Code, appwire.CodeUnavailable)
	}
	if info := wireErrorInfo(wire); info != string(appwire.ErrorSessionUnavailable) {
		t.Fatalf("evenerErrorInfo = %q, want %q", info, appwire.ErrorSessionUnavailable)
	}
	if !strings.Contains(wire.Message, "host") {
		t.Fatalf("message = %q, want it to name the host", wire.Message)
	}
}
