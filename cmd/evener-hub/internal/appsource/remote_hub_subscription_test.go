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
	client *appwire.Client
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
		client: client,
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

// Test 9: a routed notification whose ref names the stable thread but whose
// threadId names the replacement session instance still reaches the
// subscription. ReplaceAppIdentity keeps the stable ref while moving the bare
// threadId, so keying on threadId alone would silently drop every delta after
// an identity swap.
func TestRemoteHubSubscribeThreadRoutesStableRefNotification(t *testing.T) {
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.ThreadReadResponse{}}
	})
	ctx := t.Context()

	out, err := remote.source.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: "host:stable"})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}

	if err := remote.push(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
		ThreadID: "new-instance",
		Ref:      "local:stable",
	}); err != nil {
		t.Fatalf("push: %v", err)
	}
	n, ok := recvNotification(t, out)
	if !ok {
		t.Fatal("channel closed before the stable-ref notification")
	}
	status := decodeNotificationParams[appwire.ThreadStatusChangedParams](t, n)
	if status.Ref != "host:stable" {
		t.Fatalf("status Ref = %q, want host:stable", status.Ref)
	}
	if status.ThreadID != "new-instance" {
		t.Fatalf("status ThreadID = %q, want new-instance (bare threadId must not be rewritten)", status.ThreadID)
	}
}

// Test 10: the nested thread object keeps a field this hub does not understand
// while still having its refs translated. Round-tripping the object through
// appwire.Thread would drop it.
func TestRemoteHubSubscribeThreadNestedThreadPreservesUnknownFields(t *testing.T) {
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.ThreadReadResponse{}}
	})
	ctx := t.Context()

	out, err := remote.source.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: "host:S"})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}

	if err := remote.push(appwire.NotifyThreadStarted, map[string]any{
		"threadId": "S",
		"ref":      "local:S",
		"thread": map[string]any{
			"id":          "S",
			"source":      "local",
			"evener":      map[string]any{"ref": "local:S", "parentRef": "local:P"},
			"futureField": map[string]any{"x": 1},
		},
	}); err != nil {
		t.Fatalf("push: %v", err)
	}
	n, ok := recvNotification(t, out)
	if !ok {
		t.Fatal("channel closed before the started notification")
	}
	var decoded struct {
		Thread map[string]json.RawMessage `json:"thread"`
	}
	if err := json.Unmarshal(n.Params, &decoded); err != nil {
		t.Fatalf("decode notification params: %v", err)
	}
	if _, ok := decoded.Thread["futureField"]; !ok {
		t.Fatalf("nested thread dropped the unknown field; params = %s", n.Params)
	}
	var source string
	if err := json.Unmarshal(decoded.Thread["source"], &source); err != nil || source != "host" {
		t.Fatalf("nested source = %q err=%v, want host", source, err)
	}
	var evener map[string]json.RawMessage
	if err := json.Unmarshal(decoded.Thread["evener"], &evener); err != nil {
		t.Fatalf("decode nested evener: %v", err)
	}
	var ref, parentRef string
	_ = json.Unmarshal(evener["ref"], &ref)
	_ = json.Unmarshal(evener["parentRef"], &parentRef)
	if ref != "host:S" {
		t.Fatalf("nested Evener.Ref = %q, want host:S", ref)
	}
	if parentRef != "host:P" {
		t.Fatalf("nested Evener.ParentRef = %q, want host:P", parentRef)
	}
}

// Test 11: controller-level replacement semantics are never forwarded to the
// shared remote client. A replaceSubscription read would scope the whole remote
// connection to this one thread, dropping every other thread's remote
// subscription while its local routing entry stayed live.
func TestRemoteHubSubscribeThreadDoesNotForwardReplaceSubscription(t *testing.T) {
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.ThreadReadResponse{}}
	})
	ctx := t.Context()

	if _, err := remote.source.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: "host:S", ReplaceSubscription: true}); err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}

	var params appwire.ThreadReadParams
	found := false
	var raw json.RawMessage
	for _, call := range remote.calls() {
		if call.method != appwire.MethodThreadRead {
			continue
		}
		found = true
		raw = call.params
		if err := json.Unmarshal(call.params, &params); err != nil {
			t.Fatalf("decode subscribe params: %v", err)
		}
	}
	if !found {
		t.Fatal("no thread/read was forwarded")
	}
	if params.ReplaceSubscription {
		t.Fatalf("subscribe forwarded replaceSubscription=true (%s); the shared remote client must never be scoped to one thread", string(raw))
	}
	if !params.Subscribe {
		t.Fatal("subscribe request did not set Subscribe")
	}
}

// Test 12: the client's notification stream closing is a dead connection. The
// subscription's out channel must close so the relay's recovery path re-attaches
// to the replacement client instead of waiting forever on a stranded pump.
func TestRemoteHubSubscribeThreadClosesOnClientDisconnect(t *testing.T) {
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.ThreadReadResponse{}}
	})
	ctx := t.Context()

	out, err := remote.source.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: "host:S"})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}

	if err := remote.client.Close(); err != nil {
		t.Fatalf("close client: %v", err)
	}

	select {
	case _, ok := <-out:
		if ok {
			t.Fatal("received a notification after the client was closed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscription out stayed open after its client's notification stream closed")
	}
}

// Test 13: ending a subscription unsubscribes its remote counterpart. The
// remote client is long-lived, so without this the remote hub keeps forwarding
// the thread's notifications for the rest of the connection's life.
func TestRemoteHubSubscribeThreadUnsubscribesOnTeardown(t *testing.T) {
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		switch method {
		case appwire.MethodThreadRead:
			return scriptedReply{result: appwire.ThreadReadResponse{}}
		case appwire.MethodThreadUnsubscribe:
			return scriptedReply{result: appwire.EmptyResponse{}}
		default:
			t.Errorf("unexpected method %q", method)
			return scriptedReply{result: appwire.EmptyResponse{}}
		}
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
		t.Fatal("subscription out did not close on cancellation")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		for _, call := range remote.calls() {
			if call.method != appwire.MethodThreadUnsubscribe {
				continue
			}
			var params appwire.ThreadUnsubscribeParams
			if err := json.Unmarshal(call.params, &params); err != nil {
				t.Fatalf("decode unsubscribe params: %v", err)
			}
			if params.Ref == "local:S" {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no thread/unsubscribe for local:S was sent; calls = %+v", remote.calls())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Test 14: a failed subscribe leaves the previous healthy subscription serving.
// Replacing it before the request succeeds would drop a live subscription on a
// transient failure.
func TestRemoteHubSubscribeThreadFailedReplacementPreservesPrevious(t *testing.T) {
	var mu sync.Mutex
	reads := 0
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		if method != appwire.MethodThreadRead {
			return scriptedReply{result: appwire.EmptyResponse{}}
		}
		mu.Lock()
		reads++
		n := reads
		mu.Unlock()
		if n >= 2 {
			wireErr := appwire.InvalidParams("subscribe refused")
			return scriptedReply{wireErr: &wireErr}
		}
		return scriptedReply{result: appwire.ThreadReadResponse{}}
	})
	ctx := t.Context()

	out, err := remote.source.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: "host:S"})
	if err != nil {
		t.Fatalf("first SubscribeThread: %v", err)
	}
	if _, err := remote.source.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: "host:S"}); err == nil {
		t.Fatal("second SubscribeThread succeeded despite the refused subscribe")
	}

	if err := remote.push(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{ThreadID: "S", Ref: "local:S"}); err != nil {
		t.Fatalf("push: %v", err)
	}
	n, ok := recvNotification(t, out)
	if !ok {
		t.Fatal("the previous subscription's channel closed after a failed replacement")
	}
	status := decodeNotificationParams[appwire.ThreadStatusChangedParams](t, n)
	if status.Ref != "host:S" {
		t.Fatalf("status Ref = %q, want host:S", status.Ref)
	}
}

// Test 15: a fan-out already blocked on a failed subscription's full in buffer
// unblocks when the failed install is discarded. Otherwise it would wait forever
// and freeze the shared drain for every thread on the host.
func TestRemoteHubDiscardSubscriberUnblocksRouter(t *testing.T) {
	source := NewRemoteHubSource("host", nil, nil)
	sub := &remoteHubSubscription{
		threadID: "S",
		in:       make(chan appwire.Notification, 1),
		pumpDone: make(chan struct{}),
		cancel:   func() {},
	}
	sub.in <- appwire.Notification{Method: appwire.NotifyThreadStatusChanged, Params: json.RawMessage(`{"threadId":"S","ref":"local:S"}`)}
	source.subs["S"] = sub

	done := make(chan struct{})
	go func() {
		source.routeNotification(appwire.Notification{Method: appwire.NotifyThreadStatusChanged, Params: json.RawMessage(`{"threadId":"S","ref":"local:S"}`)})
		close(done)
	}()
	// Let the router reach the send so the discard is what unblocks it.
	time.Sleep(50 * time.Millisecond)

	source.discardSubscriber(sub, nil)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("routeNotification stayed blocked after the failed subscription was discarded")
	}
}
