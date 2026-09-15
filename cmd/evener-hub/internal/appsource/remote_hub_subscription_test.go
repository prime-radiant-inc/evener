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
			// thread/unsubscribe is framework teardown, not a scenario: the
			// real server answers it with an empty response, and every
			// subscription emits one when its relay ends. Answer it here so
			// a scenario's handle never sees it; the call is still recorded
			// above for tests that assert the teardown unsubscribe.
			if msg.Request.Method == appwire.MethodThreadUnsubscribe {
				data, _ := json.Marshal(appwire.EmptyResponse{})
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
	// The caller addressed the thread by ref alone, so no bare threadId is
	// forwarded: the remote resolves the ref itself (a bare threadId it cannot
	// resolve is rejected, and after an identity replacement "S" is no longer
	// the live thread ID).
	if params.Ref != "local:S" || params.ThreadID != "" || !params.Subscribe {
		t.Fatalf("subscribe params = %+v, want ref=local:S threadId= subscribe=true", params)
	}

	// The snapshot response must never surface on the channel; only pushed
	// notifications do — and ahead of them the hub-originated resync that tells
	// the relay its pre-subscribe read is superseded by the atomic snapshot.
	if err := remote.push(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
		ThreadID: "S",
		Ref:      "local:S",
	}); err != nil {
		t.Fatalf("push: %v", err)
	}
	first, ok := recvNotification(t, out)
	if !ok {
		t.Fatal("subscription channel closed before the leading resync")
	}
	if first.Method != appwire.NotifyEvenerThreadResync {
		t.Fatalf("first notification = %q, want the leading %q resync", first.Method, appwire.NotifyEvenerThreadResync)
	}
	resync := decodeNotificationParams[appwire.ThreadResyncParams](t, first)
	if resync.ThreadID != "S" || resync.Ref != "host:S" {
		t.Fatalf("resync = %+v, want threadId S ref host:S", resync)
	}
	n, ok := recvNotification(t, out)
	if !ok {
		t.Fatal("subscription channel closed before the pushed notification")
	}
	if n.Method != appwire.NotifyThreadStatusChanged {
		t.Fatalf("notification after the resync = %q, want the pushed %q (snapshot leaked)", n.Method, appwire.NotifyThreadStatusChanged)
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

	// The restored previous subscription still owns the remote ref, so the
	// failed install must not have unsubscribed it on the way out.
	for _, call := range remote.calls() {
		if call.method == appwire.MethodThreadUnsubscribe {
			t.Fatalf("failed replacement unsubscribed a ref the restored previous still owns: %s", string(call.params))
		}
	}
}

// Test 15: routing at a full subscription buffer must not park the shared
// drain, and discarding that failed install must leave its subscription marked
// terminated. A fan-out parked here would freeze every thread on the host, and
// an install that was never pumped must never be mistaken for a live one.
func TestRemoteHubDiscardSubscriberUnblocksRouter(t *testing.T) {
	source := NewRemoteHubSource("host", nil, nil)
	canceled := 0
	sub := &remoteHubSubscription{
		threadID: "S",
		in:       make(chan appwire.Notification, 1),
		pumpDone: make(chan struct{}),
		cancel:   func() { canceled++ },
	}
	notification := appwire.Notification{Method: appwire.NotifyThreadStatusChanged, Params: json.RawMessage(`{"threadId":"S","ref":"local:S"}`)}
	sub.in <- notification
	source.subs["S"] = sub

	done := make(chan struct{})
	go func() {
		source.routeNotification(nil, notification)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("routeNotification blocked on a subscription whose buffer was full")
	}
	if canceled != 1 {
		t.Fatalf("stalled subscription cancelled %d times, want 1 (it must be retired, not waited for)", canceled)
	}
	if len(sub.in) != 1 {
		t.Fatal("a notification with no room was buffered anyway")
	}

	source.discardSubscriber(sub, nil)
	select {
	case <-sub.pumpDone:
	default:
		t.Fatal("discardSubscriber left an unpumped subscription looking live")
	}
	if _, ok := source.subs["S"]; ok {
		t.Fatal("discardSubscriber kept a failed install in the routing table")
	}
}

// Test 16: a subscription whose consumer stalls is retired instead of wedging
// the one drain goroutine shared by every thread on the host. The healthy
// subscription on the same client keeps flowing, and the stalled one's out
// closes so the controller relay resyncs it.
func TestRemoteHubRouteStallRetiresSubscriptionAndKeepsOthersFlowing(t *testing.T) {
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

	// Stall S's consumer by never reading outS. Fill its out buffer, then its in
	// buffer, so the next S notification finds the hand-off with no room and
	// retires the subscription.
	for i := range 2*remoteHubSubBuffer + 16 {
		if err := remote.push(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{ThreadID: "S", Ref: "local:S"}); err != nil {
			t.Fatalf("push S %d: %v", i, err)
		}
	}
	// T must still be served: the stalled subscription is retired rather than
	// allowed to hold the drain for every thread.
	if err := remote.push(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{ThreadID: "T", Ref: "local:T"}); err != nil {
		t.Fatalf("push T: %v", err)
	}

	n, ok := recvNotification(t, outT)
	if !ok {
		t.Fatal("healthy T subscription closed while S stalled")
	}
	status := decodeNotificationParams[appwire.ThreadStatusChangedParams](t, n)
	if status.Ref != "host:T" {
		t.Fatalf("T subscriber got ref=%q, want host:T", status.Ref)
	}

	// S's out closes: the relay's resync signal. Drain whatever was buffered,
	// then require the close.
	deadline := time.Now().Add(2 * time.Second)
	for {
		select {
		case _, ok := <-outS:
			if !ok {
				return
			}
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("stalled subscription's out never closed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Test 17: jitter that fits the buffer is delivered, not resynced. The hand-off
// only retires a subscription whose buffers are full, so a consumer that is
// merely behind is still served in full.
func TestRemoteHubRouteJitterWithinBufferIsDelivered(t *testing.T) {
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.ThreadReadResponse{}}
	})
	ctx := t.Context()

	out, err := remote.source.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: "host:S"})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}

	const burst = remoteHubSubBuffer
	for i := range burst {
		if err := remote.push(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{ThreadID: "S", Ref: "local:S"}); err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
	}
	for i := range burst {
		n, ok := recvNotification(t, out)
		if !ok {
			t.Fatalf("out closed after %d of %d notifications in a burst that fits the buffer", i, burst)
		}
		status := decodeNotificationParams[appwire.ThreadStatusChangedParams](t, n)
		if status.Ref != "host:S" {
			t.Fatalf("notification %d got ref=%q, want host:S", i, status.Ref)
		}
	}

	// The subscription must still be live after the burst.
	if err := remote.push(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{ThreadID: "S", Ref: "local:S"}); err != nil {
		t.Fatalf("push after burst: %v", err)
	}
	if _, ok := recvNotification(t, out); !ok {
		t.Fatal("subscription was retired by a burst within its buffer")
	}
}

// Test 18: notifications from a retired client's stream must never reach the
// replacement client's subscription. After a reconnect the old drain can still
// hand over frames it buffered before its stream closed, concurrently with the
// relay re-subscribing against the new client: routing them into the recovered
// feed interleaves stale-generation events, and with a full buffer they would
// cancel the healthy new subscription instead of being dropped.
func TestRemoteHubRouteNotificationIgnoresRetiredClient(t *testing.T) {
	retired := &appwire.Client{}
	current := &appwire.Client{}
	canceled := 0
	source := NewRemoteHubSource("host", nil, nil)
	sub := &remoteHubSubscription{
		threadID: "S",
		client:   current,
		in:       make(chan appwire.Notification, 1),
		pumpDone: make(chan struct{}),
		cancel:   func() { canceled++ },
	}
	source.subs["S"] = sub
	stale := appwire.Notification{
		Method: appwire.NotifyThreadStatusChanged,
		Params: json.RawMessage(`{"threadId":"S","ref":"local:S"}`),
	}

	// Room in the buffer: the stale frame must still be dropped.
	source.routeNotification(retired, stale)
	if len(sub.in) != 0 {
		t.Fatal("a retired client's notification was delivered to the replacement subscription")
	}

	// No room: the stale frame must not be mistaken for a stalled consumer.
	sub.in <- stale
	source.routeNotification(retired, stale)
	if canceled != 0 {
		t.Fatalf("a retired client's notification cancelled the live subscription %d times, want 0", canceled)
	}
	if len(sub.in) != 1 {
		t.Fatal("a retired client's notification was buffered into the replacement subscription")
	}
	<-sub.in

	// Positive control: the client the subscription is bound to still delivers.
	source.routeNotification(current, stale)
	select {
	case <-sub.in:
	default:
		t.Fatal("the subscribed client's notification was dropped")
	}
}

// Test 19: a failed install must not resurrect a previous subscription that can
// no longer serve. Restoring one leaves the routing table pointing at a
// subscription nothing pumps or retires (leaking its remote-side counterpart),
// and, when its client's notification stream is gone, leaves a relay blocked on
// an out that will never close.
func TestRemoteHubDiscardSubscriberDoesNotRestoreDeadPrevious(t *testing.T) {
	t.Run("pump exited", func(t *testing.T) {
		source := NewRemoteHubSource("host", nil, nil)
		dead := &remoteHubSubscription{
			threadID: "S",
			in:       make(chan appwire.Notification, 1),
			pumpDone: make(chan struct{}),
			cancel:   func() {},
		}
		close(dead.pumpDone)
		sub := &remoteHubSubscription{
			threadID: "S",
			in:       make(chan appwire.Notification, 1),
			pumpDone: make(chan struct{}),
			cancel:   func() {},
		}
		source.subs["S"] = sub

		source.discardSubscriber(sub, dead)

		if restored, ok := source.subs["S"]; ok {
			t.Fatalf("discardSubscriber restored a subscription whose pump had exited: %+v", restored)
		}
	})

	t.Run("client no longer drained", func(t *testing.T) {
		client := &appwire.Client{}
		source := NewRemoteHubSource("host", nil, nil)
		canceled := 0
		stranded := &remoteHubSubscription{
			threadID: "S",
			client:   client,
			in:       make(chan appwire.Notification, 1),
			pumpDone: make(chan struct{}),
			cancel:   func() { canceled++ },
		}
		sub := &remoteHubSubscription{
			threadID: "S",
			client:   client,
			in:       make(chan appwire.Notification, 1),
			pumpDone: make(chan struct{}),
			cancel:   func() {},
		}
		source.subs["S"] = sub

		source.discardSubscriber(sub, stranded)

		if restored, ok := source.subs["S"]; ok {
			t.Fatalf("discardSubscriber restored a subscription whose client is no longer drained: %+v", restored)
		}
		if canceled != 1 {
			t.Fatalf("stranded subscription cancelled %d times, want 1; a relay blocked on its out would hang", canceled)
		}
	})

	t.Run("live previous restored", func(t *testing.T) {
		client := &appwire.Client{}
		source := NewRemoteHubSource("host", nil, nil)
		source.drains[client] = struct{}{}
		live := &remoteHubSubscription{
			threadID: "S",
			client:   client,
			in:       make(chan appwire.Notification, 1),
			pumpDone: make(chan struct{}),
			cancel:   func() { t.Error("a live previous subscription was cancelled") },
		}
		sub := &remoteHubSubscription{
			threadID: "S",
			client:   client,
			in:       make(chan appwire.Notification, 1),
			pumpDone: make(chan struct{}),
			cancel:   func() {},
		}
		source.subs["S"] = sub

		source.discardSubscriber(sub, live)

		if source.subs["S"] != live {
			t.Fatal("a live previous subscription was not restored")
		}
	})
}

// Test 20: a stalled subscription must not stop the shared drain from reading
// its client. The drain is the only reader of client.Notifications(), and
// appwire tears the whole connection down once that buffer
// (NotificationBufferCap) fills — losing every subscription on the host. A
// consumer that stops reading must therefore be retired, not waited for.
func TestRemoteHubStalledSubscriptionCannotOverflowSharedClient(t *testing.T) {
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		switch method {
		case appwire.MethodThreadRead:
			return scriptedReply{result: appwire.ThreadReadResponse{}}
		case appwire.MethodModelList:
			return scriptedReply{result: appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "p", Model: "m"}}}}
		default:
			return scriptedReply{result: appwire.EmptyResponse{}}
		}
	})
	ctx := t.Context()

	if _, err := remote.source.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: "host:S"}); err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}

	// Never read outS: fill its out buffer, then its in buffer, so the hand-off
	// has nowhere to put anything more for S.
	for i := range 2*remoteHubSubBuffer + 16 {
		if err := remote.push(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{ThreadID: "S", Ref: "local:S"}); err != nil {
			t.Fatalf("push S %d: %v", i, err)
		}
	}

	// The client's notification buffer must keep draining through all of this.
	// A drain parked on the stalled subscription stops reading, this buffer
	// overflows, and appwire tears the connection down under it. The periodic
	// yield keeps the drain's own throughput (it unmarshals every frame) ahead
	// of this loop, so a failure here can only be the parked drain.
	for i := 0; i <= appwire.NotificationBufferCap; i++ {
		if err := remote.push(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{ThreadID: "U", Ref: "local:U"}); err != nil {
			t.Fatalf("client stopped reading its notification stream at push %d of %d (shared drain blocked on a stalled subscription): %v",
				i, appwire.NotificationBufferCap+1, err)
		}
		if i%32 == 0 {
			time.Sleep(time.Millisecond)
		}
	}

	if _, err := remote.source.ListModels(ctx, appwire.ModelListParams{}); err != nil {
		t.Fatalf("client was torn down by notification overflow: %v", err)
	}
}

// Test 21: a subscribe that reaches the server and then fails locally must
// still release the remote subscription the request may have installed. The
// failure path used to drop only local state, leaving the long-lived shared
// client forwarding a thread nobody listens to for the rest of the
// connection's life. A live subscription that still owns the ref keeps it (see
// TestRemoteHubSubscribeThreadFailedReplacementPreservesPrevious).
//
// The refused reply is what makes this deterministic: the request definitely
// reached the server. The same code path covers the harder trigger — a caller
// context cancelled after the request was sent, whose response is lost — which
// cannot be made deterministic here, because this harness's serial server loop
// cannot both withhold that response and answer the cleanup request.
func TestRemoteHubSubscribeThreadFailedInstallUnsubscribesRemote(t *testing.T) {
	wireErr := appwire.InvalidParams("subscribe refused")
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		if method == appwire.MethodThreadRead {
			return scriptedReply{wireErr: &wireErr}
		}
		return scriptedReply{result: appwire.EmptyResponse{}}
	})

	if _, err := remote.source.SubscribeThread(context.Background(), appwire.ThreadReadParams{Ref: "host:S"}); err == nil {
		t.Fatal("SubscribeThread succeeded despite the refused subscribe")
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
			t.Fatalf("no thread/unsubscribe for local:S after a failed subscribe; calls = %+v", remote.calls())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Test 22: a controller-level replacement read is never forwarded to the
// shared remote client. The remote hub scopes the whole connection to the
// read's thread on replaceSubscription, so forwarding it from ReadThread drops
// every other remote thread's subscription on this host while their local
// routing entries stay live. Replacement belongs to the controller's own
// subscriptions (app_relay.go).
func TestRemoteHubReadThreadDoesNotForwardReplaceSubscription(t *testing.T) {
	source, calls := newScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.ThreadReadResponse{}}
	})

	if _, err := source.ReadThread(t.Context(), appwire.ThreadReadParams{
		Ref:                 "host:S",
		Subscribe:           true,
		ReplaceSubscription: true,
	}); err != nil {
		t.Fatalf("ReadThread: %v", err)
	}

	raw := lastMethodCall(t, calls(), appwire.MethodThreadRead)
	var params appwire.ThreadReadParams
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("decode read params: %v", err)
	}
	if params.ReplaceSubscription {
		t.Fatalf("read forwarded replaceSubscription=true (%s); the shared remote client must never be scoped to one thread", string(raw))
	}
	if params.Subscribe {
		t.Fatalf("read forwarded subscribe=true (%s); the snapshot read must not create a remote subscription nothing local owns", string(raw))
	}
}

// forwardedReadParams returns the params of the most recent thread/read the
// source forwarded, failing the test when none was recorded.
func forwardedReadParams(t *testing.T, remote *pushableRemote) appwire.ThreadReadParams {
	t.Helper()
	var params appwire.ThreadReadParams
	if err := json.Unmarshal(lastMethodCall(t, remote.calls(), appwire.MethodThreadRead), &params); err != nil {
		t.Fatalf("decode forwarded thread/read params: %v", err)
	}
	return params
}

// sawRemoteUnsubscribe reports whether remote recorded a thread/unsubscribe
// naming ref.
func sawRemoteUnsubscribe(t *testing.T, remote *pushableRemote, ref string) bool {
	t.Helper()
	for _, call := range remote.calls() {
		if call.method != appwire.MethodThreadUnsubscribe {
			continue
		}
		var params appwire.ThreadUnsubscribeParams
		if err := json.Unmarshal(call.params, &params); err != nil {
			t.Fatalf("decode unsubscribe params: %v", err)
		}
		if params.Ref == ref {
			return true
		}
	}
	return false
}

// Test 23: the translated ref's suffix is this hub's local routing key, not the
// thread identity the remote hub is addressed by. The remote resolves a bare
// non-empty ThreadID in preference to Ref, so forwarding the stable ref suffix
// would address the thread by its stable identity and be rejected as soon as an
// identity replacement moved the live thread ID. The caller's ThreadID — empty
// or not — is what must be forwarded; an empty one lets the remote resolve the
// (stable-aware) ref.
func TestRemoteHubSubscribeThreadForwardsCallerThreadID(t *testing.T) {
	t.Run("ref only keeps threadId empty", func(t *testing.T) {
		remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
			return scriptedReply{result: appwire.ThreadReadResponse{}}
		})

		if _, err := remote.source.SubscribeThread(t.Context(), appwire.ThreadReadParams{Ref: "host:stable"}); err != nil {
			t.Fatalf("SubscribeThread: %v", err)
		}

		params := forwardedReadParams(t, remote)
		if params.Ref != "local:stable" {
			t.Fatalf("forwarded Ref = %q, want local:stable", params.Ref)
		}
		if params.ThreadID != "" {
			t.Fatalf("forwarded threadId = %q, want empty; a bare threadId overrides the ref the remote resolves", params.ThreadID)
		}
	})

	t.Run("explicit threadId is preserved", func(t *testing.T) {
		remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
			return scriptedReply{result: appwire.ThreadReadResponse{}}
		})

		if _, err := remote.source.SubscribeThread(t.Context(), appwire.ThreadReadParams{
			Ref:      "host:stable",
			ThreadID: "new-instance",
		}); err != nil {
			t.Fatalf("SubscribeThread: %v", err)
		}

		params := forwardedReadParams(t, remote)
		if params.ThreadID != "new-instance" {
			t.Fatalf("forwarded threadId = %q, want the caller's new-instance", params.ThreadID)
		}
	})
}

// Test 24: ReadThread and ListTurns must preserve the caller's ThreadID for the
// same reason SubscribeThread does — the ref's suffix is a local routing key,
// and a bare threadId the remote does not recognize is rejected outright.
func TestRemoteHubReadAndListTurnsForwardCallerThreadID(t *testing.T) {
	t.Run("ReadThread", func(t *testing.T) {
		source, calls := newScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
			return scriptedReply{result: appwire.ThreadReadResponse{}}
		})

		if _, err := source.ReadThread(t.Context(), appwire.ThreadReadParams{Ref: "host:stable"}); err != nil {
			t.Fatalf("ReadThread: %v", err)
		}

		var params appwire.ThreadReadParams
		if err := json.Unmarshal(lastMethodCall(t, calls(), appwire.MethodThreadRead), &params); err != nil {
			t.Fatalf("decode read params: %v", err)
		}
		if params.Ref != "local:stable" {
			t.Fatalf("forwarded Ref = %q, want local:stable", params.Ref)
		}
		if params.ThreadID != "" {
			t.Fatalf("forwarded threadId = %q, want empty (the caller sent none)", params.ThreadID)
		}
	})

	t.Run("ListTurns", func(t *testing.T) {
		source, calls := newScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
			return scriptedReply{result: appwire.ThreadTurnsListResponse{}}
		})

		if _, err := source.ListTurns(t.Context(), appwire.ThreadTurnsListParams{Ref: "host:stable"}); err != nil {
			t.Fatalf("ListTurns: %v", err)
		}

		var params appwire.ThreadTurnsListParams
		if err := json.Unmarshal(lastMethodCall(t, calls(), appwire.MethodThreadTurnsList), &params); err != nil {
			t.Fatalf("decode turns params: %v", err)
		}
		if params.Ref != "local:stable" {
			t.Fatalf("forwarded Ref = %q, want local:stable", params.Ref)
		}
		if params.ThreadID != "" {
			t.Fatalf("forwarded threadId = %q, want empty (the caller sent none)", params.ThreadID)
		}
	})
}

// Test 25: a JSON null nested thread is not a thread object, so the translator
// leaves it byte-for-byte alone rather than materializing an empty object and
// stamping a source on it.
func TestRemoteHubPreservesNullNestedThread(t *testing.T) {
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.ThreadReadResponse{}}
	})

	out, err := remote.source.SubscribeThread(t.Context(), appwire.ThreadReadParams{Ref: "host:S"})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}

	if err := remote.push(appwire.NotifyThreadStatusChanged, map[string]any{
		"threadId": "S",
		"ref":      "local:S",
		"thread":   nil,
	}); err != nil {
		t.Fatalf("push: %v", err)
	}
	n, ok := recvNotification(t, out)
	if !ok {
		t.Fatal("subscription closed before the null-thread notification")
	}
	var decoded struct {
		Thread json.RawMessage `json:"thread"`
	}
	if err := json.Unmarshal(n.Params, &decoded); err != nil {
		t.Fatalf("decode notification params: %v", err)
	}
	if string(decoded.Thread) != "null" {
		t.Fatalf("nested thread = %s, want null (rewritten to a source-bearing object); params = %s", decoded.Thread, n.Params)
	}
}

// Test 26: retiring a subscription whose routing entry now holds a replacement
// bound to a DIFFERENT client must still drop the retiring subscription's own
// remote subscription. A replacement on another connection cannot own this
// connection's remote subscription, so skipping the unsubscribe (as the code
// did whenever the entry had changed) leaves a live connection forwarding a
// thread nothing local watches.
func TestRemoteHubRetireSubscriptionDropsRemoteRefWhenReplacementUsesAnotherClient(t *testing.T) {
	handler := func(string, json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.EmptyResponse{}}
	}
	oldRemote := newPushableRemote(t, "host", handler)
	newRemote := newPushableRemote(t, "host", handler)

	source := oldRemote.source
	displaced := &remoteHubSubscription{
		threadID:  "S",
		remoteRef: "local:S",
		client:    oldRemote.client,
		cancel:    func() {},
	}
	source.subs["S"] = &remoteHubSubscription{
		threadID:  "S",
		remoteRef: "local:S",
		client:    newRemote.client,
		cancel:    func() {},
	}

	source.retireSubscription(displaced)

	if !sawRemoteUnsubscribe(t, oldRemote, "local:S") {
		t.Fatalf("no thread/unsubscribe for local:S on the displaced connection; calls = %+v", oldRemote.calls())
	}
	if sawRemoteUnsubscribe(t, newRemote, "local:S") {
		t.Fatalf("the replacement's connection was unsubscribed; calls = %+v", newRemote.calls())
	}
}

// Test 27: the control for Test 26 — a replacement on the SAME connection owns
// the remote subscription, so the retiring predecessor must not unsubscribe it
// out from under the live replacement.
func TestRemoteHubRetireSubscriptionKeepsRemoteRefForSameClientReplacement(t *testing.T) {
	remote := newPushableRemote(t, "host", func(string, json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.EmptyResponse{}}
	})

	source := remote.source
	displaced := &remoteHubSubscription{
		threadID:  "S",
		remoteRef: "local:S",
		client:    remote.client,
		cancel:    func() {},
	}
	source.subs["S"] = &remoteHubSubscription{
		threadID:  "S",
		remoteRef: "local:S",
		client:    remote.client,
		cancel:    func() {},
	}

	source.retireSubscription(displaced)

	if sawRemoteUnsubscribe(t, remote, "local:S") {
		t.Fatalf("retiring predecessor unsubscribed the live same-connection replacement; calls = %+v", remote.calls())
	}
}

// expectResync consumes the leading resync every authoritative subscribe hands
// the relay, asserting the controller-namespace identity it names.
func expectResync(t *testing.T, out <-chan appwire.Notification, threadID, ref string) {
	t.Helper()
	n, ok := recvNotification(t, out)
	if !ok {
		t.Fatal("subscription channel closed before the leading resync")
	}
	if n.Method != appwire.NotifyEvenerThreadResync {
		t.Fatalf("first frame = %q, want the leading %q resync", n.Method, appwire.NotifyEvenerThreadResync)
	}
	resync := decodeNotificationParams[appwire.ThreadResyncParams](t, n)
	if resync.ThreadID != threadID || resync.Ref != ref {
		t.Fatalf("resync = %+v, want threadId %q ref %q", resync, threadID, ref)
	}
}

// waitForRemoteUnsubscribe waits until remote recorded a thread/unsubscribe
// naming ref.
func waitForRemoteUnsubscribe(t *testing.T, remote *pushableRemote, ref string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if sawRemoteUnsubscribe(t, remote, ref) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no thread/unsubscribe for %s; calls = %+v", ref, remote.calls())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Test 28: a successful subscribe hands the relay a leading resync carrying the
// controller-namespace identity. The controller relay read the thread before
// subscribing (its non-atomic prepareRelay branch), so the subscription's atomic
// snapshot may be newer than the controller's copy. The resync makes the client
// re-read and fold that snapshot in; without it a delta that landed between the
// read and the attach is absent from both the controller snapshot and the
// notification stream, and the relay stays stale.
func TestRemoteHubSubscribeThreadResyncsFromSnapshot(t *testing.T) {
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:     "S",
			Source: "local",
			Evener: appwire.EvenerThread{Ref: "local:S"},
		}}}
	})

	out, err := remote.source.SubscribeThread(t.Context(), appwire.ThreadReadParams{Ref: "host:S"})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}
	expectResync(t, out, "S", "host:S")

	// The resync must not consume the pushed stream behind it.
	if err := remote.push(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{ThreadID: "S", Ref: "local:S"}); err != nil {
		t.Fatalf("push: %v", err)
	}
	if _, ok := recvNotification(t, out); !ok {
		t.Fatal("channel closed before the pushed notification")
	}
}

// Test 29: the subscription snapshot's own ref is the routing authority. When a
// caller sends a ref and a threadId that name different threads, the remote hub
// resolves the bare threadId (threadRelayTarget prefers it), so the notification
// stream carries the subscribed thread's ref. Routing keyed from the caller's
// ref would drop every one of those notifications, and teardown would unsubscribe
// a ref the remote never subscribed, stranding the subscription on the remote.
func TestRemoteHubSubscribeThreadRoutesBySnapshotIdentity(t *testing.T) {
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		if method == appwire.MethodThreadRead {
			return scriptedReply{result: appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID:     "child",
				Source: "local",
				Evener: appwire.EvenerThread{Ref: "local:child"},
			}}}
		}
		return scriptedReply{result: appwire.EmptyResponse{}}
	})
	ctx, cancel := context.WithCancel(context.Background())

	out, err := remote.source.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: "host:root", ThreadID: "child"})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}
	// The forwarded request still carries the caller's own ref and threadId; the
	// snapshot, not the request, is what routing is keyed from.
	forwarded := forwardedReadParams(t, remote)
	if forwarded.Ref != "local:root" || forwarded.ThreadID != "child" {
		t.Fatalf("forwarded params = ref %q threadId %q, want local:root / child", forwarded.Ref, forwarded.ThreadID)
	}
	expectResync(t, out, "child", "host:child")

	// The remote delivers the subscribed thread's notifications: they must reach
	// the subscription, which the caller's ref suffix ("root") would have missed.
	if err := remote.push(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{ThreadID: "child", Ref: "local:child"}); err != nil {
		t.Fatalf("push child: %v", err)
	}
	n, ok := recvNotification(t, out)
	if !ok {
		t.Fatal("channel closed before the child notification")
	}
	status := decodeNotificationParams[appwire.ThreadStatusChangedParams](t, n)
	if status.Ref != "host:child" {
		t.Fatalf("child notification ref = %q, want host:child (routed under the caller's ref?)", status.Ref)
	}

	// Teardown names the identity the remote subscribed under — the threadId it
	// resolved — not the caller's ref suffix.
	cancel()
	waitForRemoteUnsubscribe(t, remote, "local:child")
	if sawRemoteUnsubscribe(t, remote, "local:root") {
		t.Fatalf("teardown unsubscribed the caller's ref suffix, which the remote never subscribed: %+v", remote.calls())
	}
}

// Test 30: a caller that cancels after the subscribe request is sent must not
// let cleanup unsubscribe before the remote has finished the subscribe. The
// remote hub handles the two requests concurrently, so an unsubscribe issued
// while the subscribe is still in flight can land first and leave the later
// subscribe active with no local owner. The withheld reply is what makes the
// ordering observable: nothing may be unsubscribed until the subscribe answers.
func TestRemoteHubSubscribeCancelWaitsForSubscribeBeforeUnsubscribe(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	server := appwire.NewStreamTransport(serverConn)

	serverCtx, stopServer := context.WithCancel(context.Background())
	subscribeSeen := make(chan struct{})
	releaseSubscribe := make(chan struct{})
	unsubscribed := make(chan string, 4)

	go func() {
		for {
			msg, err := server.Recv(serverCtx)
			if err != nil {
				return
			}
			if msg.Request == nil {
				continue
			}
			switch msg.Request.Method {
			case appwire.MethodInitialize:
				data, _ := json.Marshal(appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"})
				if err := server.Send(serverCtx, appwire.ResponseMessage(msg.Request.ID, json.RawMessage(data))); err != nil {
					return
				}
			case appwire.MethodThreadRead:
				close(subscribeSeen)
				select {
				case <-releaseSubscribe:
				case <-serverCtx.Done():
					return
				}
				data, _ := json.Marshal(appwire.ThreadReadResponse{Thread: appwire.Thread{
					ID: "S", Source: "local", Evener: appwire.EvenerThread{Ref: "local:S"},
				}})
				if err := server.Send(serverCtx, appwire.ResponseMessage(msg.Request.ID, json.RawMessage(data))); err != nil {
					return
				}
			case appwire.MethodThreadUnsubscribe:
				var params appwire.ThreadUnsubscribeParams
				_ = json.Unmarshal(msg.Request.Params, &params)
				unsubscribed <- params.Ref
				data, _ := json.Marshal(appwire.EmptyResponse{})
				if err := server.Send(serverCtx, appwire.ResponseMessage(msg.Request.ID, json.RawMessage(data))); err != nil {
					return
				}
			}
		}
	}()

	client := appwire.NewClient(appwire.NewStreamTransport(clientConn))
	client.Start(serverCtx)
	if _, err := client.Initialize(serverCtx, appwire.InitializeParams{}); err != nil {
		stopServer()
		t.Fatalf("initialize: %v", err)
	}
	t.Cleanup(func() {
		stopServer()
		_ = client.Close()
	})

	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})

	subCtx, cancelSub := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := source.SubscribeThread(subCtx, appwire.ThreadReadParams{Ref: "host:S"})
		result <- err
	}()

	select {
	case <-subscribeSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("the subscribe request never reached the remote")
	}
	cancelSub()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("SubscribeThread after the caller cancelled = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SubscribeThread did not return after the caller cancelled")
	}

	// The remote has not answered the subscribe, so nothing may be unsubscribed
	// yet: an unsubscribe now can overtake the subscribe and strand it.
	select {
	case ref := <-unsubscribed:
		t.Fatalf("thread/unsubscribe for %q was sent while the subscribe was still in flight", ref)
	case <-time.After(200 * time.Millisecond):
	}

	// Only once the subscribe completes does cleanup release the remote side.
	close(releaseSubscribe)
	select {
	case ref := <-unsubscribed:
		if ref != "local:S" {
			t.Fatalf("unsubscribed ref = %q, want local:S", ref)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no thread/unsubscribe followed the completed subscribe")
	}
}

// Test 31: nested session handles are translated, not just the top-level routing
// ref. A job's or delegate's transcriptRef names the child session a remote hub
// hosts, so leaving it as "local:<thread>" would send the controller UI back to
// its own local source for a session it does not have.
func TestRemoteHubNestedSessionRefsTranslated(t *testing.T) {
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.ThreadReadResponse{}}
	})

	out, err := remote.source.SubscribeThread(t.Context(), appwire.ThreadReadParams{Ref: "host:S"})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}

	if err := remote.push(appwire.NotifyEvenerJobFinished, map[string]any{
		"threadId": "S",
		"ref":      "local:S",
		"job":      map[string]any{"jobId": "j1", "transcriptRef": "local:child"},
	}); err != nil {
		t.Fatalf("push job: %v", err)
	}
	job := decodeNotificationParams[struct {
		Job struct {
			TranscriptRef string `json:"transcriptRef"`
		} `json:"job"`
	}](t, recvOrFatal(t, out))
	if job.Job.TranscriptRef != "host:child" {
		t.Fatalf("job transcriptRef = %q, want host:child", job.Job.TranscriptRef)
	}

	if err := remote.push(appwire.NotifyEvenerDelegateUpdated, map[string]any{
		"threadId": "S",
		"ref":      "local:S",
		"delegate": map[string]any{"delegateId": "d1", "transcriptRef": "local:child"},
	}); err != nil {
		t.Fatalf("push delegate: %v", err)
	}
	delegate := decodeNotificationParams[struct {
		Delegate struct {
			TranscriptRef string `json:"transcriptRef"`
		} `json:"delegate"`
	}](t, recvOrFatal(t, out))
	if delegate.Delegate.TranscriptRef != "host:child" {
		t.Fatalf("delegate transcriptRef = %q, want host:child", delegate.Delegate.TranscriptRef)
	}
}

// Test 32: opaque handles a notification nests must survive the nested-ref pass
// untouched. "job:<id>" is a transcript handle this hub cannot resolve and
// "proj:<project>:<thread>" names another project's storage; rewriting either
// would corrupt a transcript lookup, and refusing to translate them must not
// drop the notification either.
func TestRemoteHubNestedOpaqueRefsPreserved(t *testing.T) {
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.ThreadReadResponse{}}
	})

	out, err := remote.source.SubscribeThread(t.Context(), appwire.ThreadReadParams{Ref: "host:S"})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}

	if err := remote.push(appwire.NotifyEvenerJobFinished, map[string]any{
		"threadId": "S",
		"ref":      "local:S",
		"job":      map[string]any{"jobId": "j1", "transcriptRef": "job:job_abc"},
	}); err != nil {
		t.Fatalf("push job: %v", err)
	}
	n := recvOrFatal(t, out)
	params := decodeNotificationParams[struct {
		Job struct {
			TranscriptRef string `json:"transcriptRef"`
		} `json:"job"`
	}](t, n)
	if params.Job.TranscriptRef != "job:job_abc" {
		t.Fatalf("job transcriptRef = %q, want the opaque job:job_abc untouched; notification = %s", params.Job.TranscriptRef, n.Params)
	}

	if err := remote.push(appwire.NotifyEvenerDelegateUpdated, map[string]any{
		"threadId": "S",
		"ref":      "local:S",
		"delegate": map[string]any{"delegateId": "d1", "transcriptRef": "proj:p1:sess"},
	}); err != nil {
		t.Fatalf("push delegate: %v", err)
	}
	delegate := decodeNotificationParams[struct {
		Delegate struct {
			TranscriptRef string `json:"transcriptRef"`
		} `json:"delegate"`
	}](t, recvOrFatal(t, out))
	if delegate.Delegate.TranscriptRef != "proj:p1:sess" {
		t.Fatalf("delegate transcriptRef = %q, want the opaque proj:p1:sess untouched", delegate.Delegate.TranscriptRef)
	}
}

// Test 33: a nested thread's diagnostics carry the same nested session handles,
// and they are translated at the JSON level so a field this hub does not
// understand survives. Turn contents are deliberately skipped: their items carry
// arbitrary model/tool JSON, and a "transcriptRef" inside it is not a session
// handle.
func TestRemoteHubThreadDiagnosticsRefsTranslated(t *testing.T) {
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.ThreadReadResponse{}}
	})

	out, err := remote.source.SubscribeThread(t.Context(), appwire.ThreadReadParams{Ref: "host:S"})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}

	if err := remote.push(appwire.NotifyThreadStarted, map[string]any{
		"threadId": "S",
		"ref":      "local:S",
		"thread": map[string]any{
			"id":     "S",
			"source": "local",
			"evener": map[string]any{
				"ref": "local:S",
				"diagnostics": map[string]any{
					"jobs": []any{
						map[string]any{"jobId": "j1", "transcriptRef": "local:child"},
						map[string]any{"jobId": "j2", "transcriptRef": "job:job_abc"},
					},
					"delegates": []any{
						map[string]any{
							"delegateId": "d1", "transcriptRef": "local:child", "childRef": "local:child",
							"message": map[string]any{"transcriptRef": "local:notAHandle"},
						},
					},
				},
			},
			"turns": []any{
				map[string]any{"id": "turn-1", "items": []any{map[string]any{"raw": map[string]any{"transcriptRef": "local:notAHandle"}}}},
			},
			"futureField": map[string]any{"x": 1},
		},
	}); err != nil {
		t.Fatalf("push: %v", err)
	}
	n := recvOrFatal(t, out)

	var decoded struct {
		Thread struct {
			Source string `json:"source"`
			Evener struct {
				Diagnostics struct {
					Jobs []struct {
						TranscriptRef string `json:"transcriptRef"`
					} `json:"jobs"`
					Delegates []struct {
						TranscriptRef string `json:"transcriptRef"`
						ChildRef      string `json:"childRef"`
						Message       struct {
							TranscriptRef string `json:"transcriptRef"`
						} `json:"message"`
					} `json:"delegates"`
				} `json:"diagnostics"`
			} `json:"evener"`
			Turns []struct {
				Items []struct {
					Raw map[string]json.RawMessage `json:"raw"`
				} `json:"items"`
			} `json:"turns"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(n.Params, &decoded); err != nil {
		t.Fatalf("decode notification params: %v", err)
	}
	if decoded.Thread.Source != "host" {
		t.Fatalf("nested source = %q, want host", decoded.Thread.Source)
	}
	jobs := decoded.Thread.Evener.Diagnostics.Jobs
	if len(jobs) != 2 {
		t.Fatalf("jobs = %+v, want 2", jobs)
	}
	if jobs[0].TranscriptRef != "host:child" {
		t.Fatalf("job transcriptRef = %q, want host:child", jobs[0].TranscriptRef)
	}
	if jobs[1].TranscriptRef != "job:job_abc" {
		t.Fatalf("opaque job transcriptRef = %q, want job:job_abc untouched", jobs[1].TranscriptRef)
	}
	delegates := decoded.Thread.Evener.Diagnostics.Delegates
	if len(delegates) != 1 || delegates[0].TranscriptRef != "host:child" || delegates[0].ChildRef != "host:child" {
		t.Fatalf("delegates = %+v, want transcriptRef and childRef host:child", delegates)
	}
	if delegates[0].Message.TranscriptRef != "local:notAHandle" {
		t.Fatalf("delegate message transcriptRef = %q, want the arbitrary result packet untouched", delegates[0].Message.TranscriptRef)
	}
	if len(decoded.Thread.Turns) != 1 || len(decoded.Thread.Turns[0].Items) != 1 {
		t.Fatalf("turns = %+v, want one item preserved", decoded.Thread.Turns)
	}
	var rawRef string
	if err := json.Unmarshal(decoded.Thread.Turns[0].Items[0].Raw["transcriptRef"], &rawRef); err != nil {
		t.Fatalf("decode turn item raw transcriptRef: %v", err)
	}
	if rawRef != "local:notAHandle" {
		t.Fatalf("turn item raw transcriptRef = %q, want the arbitrary model/tool JSON untouched", rawRef)
	}
}

// recvOrFatal is recvNotification with only the notification returned.
func recvOrFatal(t *testing.T, out <-chan appwire.Notification) appwire.Notification {
	t.Helper()
	n, ok := recvNotification(t, out)
	if !ok {
		t.Fatal("subscription channel closed before the expected notification")
	}
	return n
}
