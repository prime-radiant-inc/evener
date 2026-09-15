package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

func TestRemoteHubSourceAdminCallReturnsRemoteResultVerbatim(t *testing.T) {
	source, calls := newScriptedRemote(t, "host", func(method string, params json.RawMessage) scriptedReply {
		if method != appwire.MethodEvenerInstanceList {
			t.Errorf("method = %q, want %q", method, appwire.MethodEvenerInstanceList)
		}
		return scriptedReply{result: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{{Name: "openai"}}}}
	})

	var out json.RawMessage
	if err := source.AdminCall(context.Background(), appwire.MethodEvenerInstanceList, json.RawMessage(`{"limit":1}`), &out); err != nil {
		t.Fatalf("AdminCall: %v", err)
	}
	if got := string(lastMethodCall(t, calls(), appwire.MethodEvenerInstanceList)); got != `{"limit":1}` {
		t.Fatalf("forwarded params = %s, want the caller's params unchanged", got)
	}
	var decoded appwire.InstanceListResponse
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("decode result %s: %v", out, err)
	}
	if len(decoded.Instances) != 1 || decoded.Instances[0].Name != "openai" {
		t.Fatalf("result = +%v, want the remote's list", decoded.Instances)
	}
}

func TestRemoteHubSourceAdminCallPreservesSemanticWireError(t *testing.T) {
	refusal := appwire.InvalidParams("refused by the host")
	source, _ := newScriptedRemote(t, "host", func(string, json.RawMessage) scriptedReply {
		return scriptedReply{wireErr: &refusal}
	})

	var out json.RawMessage
	err := source.AdminCall(context.Background(), appwire.MethodEvenerLaunchSetLayer, nil, &out)
	if err == nil {
		t.Fatal("AdminCall succeeded despite the remote's refusal")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error = %T %v, want appwire.WireError", err, err)
	}
	if wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("code = %d, want %d", wire.Code, appwire.CodeInvalidParams)
	}
}

// TestRemoteHubSourceAdminCallTransportFailureBecomesSessionUnavailable pins the
// component-07a error mapping: a channel that dies mid-call must reach the proxy
// as a typed SessionUnavailable, not a raw transport error that WireError would
// flatten to InternalError. It matches the non-proxy forwarding paths (e.g.
// TestRemoteHubSourceEOFBecomesSessionUnavailable).
func TestRemoteHubSourceAdminCallTransportFailureBecomesSessionUnavailable(t *testing.T) {
	source, _ := newScriptedRemote(t, "host", func(string, json.RawMessage) scriptedReply {
		return scriptedReply{closeConn: true}
	})

	var out json.RawMessage
	err := source.AdminCall(context.Background(), appwire.MethodEvenerInstanceList, nil, &out)
	if err == nil {
		t.Fatal("AdminCall succeeded after the remote closed the pipe")
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
}

// TestRemoteHubSourceAdminMutationCallReturnsRemoteResultVerbatim pins the
// success path of the mutating twin: params and result pass through exactly as
// AdminCall passes them.
func TestRemoteHubSourceAdminMutationCallReturnsRemoteResultVerbatim(t *testing.T) {
	source, calls := newScriptedRemote(t, "host", func(method string, params json.RawMessage) scriptedReply {
		if method != appwire.MethodEvenerPluginInstall {
			t.Errorf("method = %q, want %q", method, appwire.MethodEvenerPluginInstall)
		}
		return scriptedReply{result: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{{Name: "openai"}}}}
	})

	var out json.RawMessage
	if err := source.AdminMutationCall(context.Background(), appwire.MethodEvenerPluginInstall, json.RawMessage(`{"name":"p"}`), &out); err != nil {
		t.Fatalf("AdminMutationCall: %v", err)
	}
	if got := string(lastMethodCall(t, calls(), appwire.MethodEvenerPluginInstall)); got != `{"name":"p"}` {
		t.Fatalf("forwarded params = %s, want the caller's params unchanged", got)
	}
	if len(out) == 0 {
		t.Fatal("result is empty, want the remote's result verbatim")
	}
}

// TestRemoteHubSourceAdminMutationCallMapsResponseLossToOutcomeUnknown pins the
// round-three retry-safety contract: a non-idempotent forwarded admin mutation
// whose response is lost must NOT reach the proxy as SessionUnavailable (which
// invites a blind retry), but as an explicit outcome-unknown error that says
// the change may or may not have been applied. The read path keeps the
// SessionUnavailable mapping pinned by
// TestRemoteHubSourceAdminCallTransportFailureBecomesSessionUnavailable.
func TestRemoteHubSourceAdminMutationCallMapsResponseLossToOutcomeUnknown(t *testing.T) {
	source, _ := newScriptedRemote(t, "host", func(string, json.RawMessage) scriptedReply {
		return scriptedReply{closeConn: true}
	})

	var out json.RawMessage
	err := source.AdminMutationCall(context.Background(), appwire.MethodEvenerPluginInstall, nil, &out)
	if err == nil {
		t.Fatal("AdminMutationCall succeeded after the remote closed the pipe")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error = %T %v, want appwire.WireError", err, err)
	}
	if wire.Code != appwire.CodeInternalError {
		t.Fatalf("code = %d, want %d", wire.Code, appwire.CodeInternalError)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok {
		t.Fatalf("Data = %T %v, want appwire.ErrorData", wire.Data, wire.Data)
	}
	if data.EvenerErrorInfo != appwire.ErrorMutationOutcomeUnknown {
		t.Fatalf("evenerErrorInfo = %q, want %q", data.EvenerErrorInfo, appwire.ErrorMutationOutcomeUnknown)
	}
	if data.MutationOutcome != appwire.MutationOutcomeUnknown {
		t.Fatalf("mutationOutcome = %q, want %q", data.MutationOutcome, appwire.MutationOutcomeUnknown)
	}
	// Blocked, not Automatic: unlike a forwarded thread mutation, an admin
	// forward carries no clientMutationId (HostRequestParams has none) and no
	// admin method dedups, so the caller must not retry automatically.
	if data.RetryDisposition != appwire.RetryDispositionBlocked {
		t.Fatalf("retryDisposition = %q, want %q", data.RetryDisposition, appwire.RetryDispositionBlocked)
	}
	if !strings.Contains(wire.Message, "host") {
		t.Fatalf("message = %q, want it to name the host", wire.Message)
	}
}

// TestRemoteHubSourceAdminMutationCallPreservesSemanticWireError pins that the
// mutating twin launders nothing: a refusal the remote itself sends keeps its
// code and message, exactly as on AdminCall.
func TestRemoteHubSourceAdminMutationCallPreservesSemanticWireError(t *testing.T) {
	refusal := appwire.InvalidParams("refused by the host")
	source, _ := newScriptedRemote(t, "host", func(string, json.RawMessage) scriptedReply {
		return scriptedReply{wireErr: &refusal}
	})

	var out json.RawMessage
	err := source.AdminMutationCall(context.Background(), appwire.MethodEvenerAuthApiKeySet, nil, &out)
	if err == nil {
		t.Fatal("AdminMutationCall succeeded despite the remote's refusal")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error = %T %v, want appwire.WireError", err, err)
	}
	if wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("code = %d, want %d", wire.Code, appwire.CodeInvalidParams)
	}
	if !strings.Contains(wire.Message, "refused by the host") {
		t.Fatalf("message = %q, want the remote's own text", wire.Message)
	}
}

// TestRemoteHubSourceAdminMutationCallClientFailureIsNotOutcomeUnknown pins the
// round-five medium finding: a connector failure — the host is offline, the
// attach is refused, the dial fails — happens before client.Request is ever
// issued, so the mutation provably did not reach the host. It must report a
// plain SessionUnavailable (a safe retry), never
// ErrorMutationOutcomeUnknown/RetryDispositionBlocked, which would tell the
// caller the change may have been applied and discourage a retry that cannot
// double-apply anything. The received-loss mapping is pinned separately by
// TestRemoteHubSourceAdminMutationCallMapsResponseLossToOutcomeUnknown.
func TestRemoteHubSourceAdminMutationCallClientFailureIsNotOutcomeUnknown(t *testing.T) {
	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
		return nil, fmt.Errorf("dial remote host: %w", syscall.ECONNREFUSED)
	})

	var out json.RawMessage
	err := source.AdminMutationCall(context.Background(), appwire.MethodEvenerPluginInstall, nil, &out)
	if err == nil {
		t.Fatal("AdminMutationCall succeeded despite the connector failure")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error = %T %v, want appwire.WireError", err, err)
	}
	if wire.Code != appwire.CodeUnavailable {
		t.Fatalf("code = %d, want %d (session unavailable, a safe retry)", wire.Code, appwire.CodeUnavailable)
	}
	if info := wireErrorInfo(wire); info != string(appwire.ErrorSessionUnavailable) {
		t.Fatalf("evenerErrorInfo = %q, want %q", info, appwire.ErrorSessionUnavailable)
	}
	if data, ok := wire.Data.(appwire.ErrorData); ok && data.EvenerErrorInfo == appwire.ErrorMutationOutcomeUnknown {
		t.Fatalf("connector failure was reported as %q; the mutation never reached the host", data.EvenerErrorInfo)
	}
}

// TestRemoteHubSourceHostSubscriptionClosesOnContextEnd pins the documented
// channel-closure contract: when the subscription's context ends the returned
// channel is closed, not merely unregistered. A caller ranging over it (the
// 07a fanOut) must observe the close so its pump goroutine ends with the cycle.
func TestRemoteHubSourceHostSubscriptionClosesOnContextEnd(t *testing.T) {
	client, _ := newScriptedClient(t, func(string, json.RawMessage) scriptedReply {
		return scriptedReply{}
	})
	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})

	subCtx, cancel := context.WithCancel(t.Context())
	notifications, err := source.SubscribeHostNotifications(subCtx)
	if err != nil {
		t.Fatalf("SubscribeHostNotifications: %v", err)
	}
	cancel()

	select {
	case _, ok := <-notifications:
		if ok {
			t.Fatal("received a notification, want the subscription to close")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("subscription channel was not closed after its context ended")
	}
}

// TestRemoteHubSourceHostSubscriptionClosesWhenClientStreamEnds pins the other
// half of the contract: when the client's notification stream ends (a reconnect
// or a dead channel) the subscription is torn down and the returned channel is
// closed so the consumer rebinds the next client.
func TestRemoteHubSourceHostSubscriptionClosesWhenClientStreamEnds(t *testing.T) {
	source, _ := newScriptedRemote(t, "host", func(string, json.RawMessage) scriptedReply {
		return scriptedReply{closeConn: true}
	})

	subCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	notifications, err := source.SubscribeHostNotifications(subCtx)
	if err != nil {
		t.Fatalf("SubscribeHostNotifications: %v", err)
	}

	// Kill the client's connection mid-subscription; the drain loop exits and
	// the subscription must follow.
	var out json.RawMessage
	_ = source.AdminCall(context.Background(), appwire.MethodEvenerInstanceList, nil, &out)

	select {
	case _, ok := <-notifications:
		if ok {
			t.Fatal("received a notification, want the subscription to close")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("subscription channel was not closed after the client's stream ended")
	}
}

// TestRemoteHubSourceHostFanOutDoesNotBlockOnFullConsumer pins the
// head-of-line property: a host-level consumer that stops reading must never
// stall the shared drain goroutine, which also routes every thread
// notification. Delivery to a full consumer is dropped rather than blocked.
func TestRemoteHubSourceHostFanOutDoesNotBlockOnFullConsumer(t *testing.T) {
	client, _ := newScriptedClient(t, func(string, json.RawMessage) scriptedReply {
		return scriptedReply{}
	})
	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})

	subCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// The returned channel is deliberately never read, so its buffer fills and
	// the subscription's pump parks on the out send.
	if _, err := source.SubscribeHostNotifications(subCtx); err != nil {
		t.Fatalf("SubscribeHostNotifications: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range remoteHubSubBuffer * 4 {
			source.publishHostNotification(client, appwire.Notification{Method: appwire.NotifyEvenerAuthUpdated})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publishHostNotification blocked on a consumer that stopped reading")
	}
	if source.hostNotifyDropped.Load() == 0 {
		t.Fatal("no notification was dropped against a full consumer buffer")
	}
}

// TestRemoteHubSourceHostNotificationScopedToOwningClient pins the reconnect
// safety property behind the dedicated teardown signal: delivery is scoped to
// the client that owns the subscription, so the old client's teardown can never
// race a send from the new client's drain goroutine (the send-on-closed-channel
// panic), and one connection's traffic never leaks into another's subscription.
func TestRemoteHubSourceHostNotificationScopedToOwningClient(t *testing.T) {
	owner, _ := newScriptedClient(t, func(string, json.RawMessage) scriptedReply {
		return scriptedReply{}
	})
	other, _ := newScriptedClient(t, func(string, json.RawMessage) scriptedReply {
		return scriptedReply{}
	})
	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
		return owner, nil
	})

	subCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	notifications, err := source.SubscribeHostNotifications(subCtx)
	if err != nil {
		t.Fatalf("SubscribeHostNotifications: %v", err)
	}

	// Another client's drain goroutine must not reach this subscription.
	source.publishHostNotification(other, appwire.Notification{Method: appwire.NotifyEvenerAuthUpdated})
	select {
	case notification := <-notifications:
		t.Fatalf("subscription received %q published by a foreign client", notification.Method)
	case <-time.After(100 * time.Millisecond):
	}

	// The owning client's drain goroutine still delivers.
	source.publishHostNotification(owner, appwire.Notification{Method: appwire.NotifyEvenerAuthUpdated})
	select {
	case notification := <-notifications:
		if notification.Method != appwire.NotifyEvenerAuthUpdated {
			t.Fatalf("notification method = %q", notification.Method)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the owning client's notification")
	}
}

// TestRemoteHubSourceHostSubscriptionClosesWhenClientStreamEndsWhilePumpBlocked
// pins the teardown path a full consumer buffer would otherwise hide: a pump
// parked on the out send must still observe the owning client's teardown and
// close the returned channel, or the fan-out could never rebind.
func TestRemoteHubSourceHostSubscriptionClosesWhenClientStreamEndsWhilePumpBlocked(t *testing.T) {
	source, _ := newScriptedRemote(t, "host", func(string, json.RawMessage) scriptedReply {
		return scriptedReply{closeConn: true}
	})

	subCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// The returned channel is deliberately never read, so the pump fills out and
	// parks on the send.
	notifications, err := source.SubscribeHostNotifications(subCtx)
	if err != nil {
		t.Fatalf("SubscribeHostNotifications: %v", err)
	}
	client := source.hostSubClient(t)
	for range remoteHubSubBuffer * 3 {
		source.publishHostNotification(client, appwire.Notification{Method: appwire.NotifyEvenerAuthUpdated})
	}

	// Kill the client's connection mid-subscription; the drain loop exits and the
	// blocked pump must follow.
	var out json.RawMessage
	_ = source.AdminCall(context.Background(), appwire.MethodEvenerInstanceList, nil, &out)

	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-notifications:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("subscription channel was not closed while its pump was parked on a full out")
		}
	}
}

// TestRemoteHubSourceHostSubscriptionClosesWhenThreadDeliveryBackpressures pins
// the round-four lifecycle fix: the shared drain goroutine must keep observing
// the client's own notification stream while a thread subscription's consumer is
// backpressured, so the client's teardown still runs the drain's deferred cleanup
// and closes every host subscription. Without that, a full thread in buffer parks
// the drain on the thread send, clientDone is never closed, and the 07a host
// fan-out stays attached to a dead client across reconnect.
func TestRemoteHubSourceHostSubscriptionClosesWhenThreadDeliveryBackpressures(t *testing.T) {
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		switch method {
		case appwire.MethodThreadRead:
			return scriptedReply{result: appwire.ThreadReadResponse{}}
		case appwire.MethodModelList:
			// Fired on demand, once the drain is parked, to close the pipe.
			return scriptedReply{closeConn: true}
		default:
			t.Errorf("unexpected method %q", method)
			return scriptedReply{result: appwire.EmptyResponse{}}
		}
	})
	ctx := t.Context()

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Reject every notification at publish time: this test is about the returned
	// channel closing, not about which host notifications are delivered, and a
	// rejecting filter keeps the host pump from parking on its own out buffer
	// first.
	remote.source.SetHostNotificationFilter(func(string) bool { return false })
	notifications, err := remote.source.SubscribeHostNotifications(subCtx)
	if err != nil {
		t.Fatalf("SubscribeHostNotifications: %v", err)
	}

	// A live thread subscription whose consumer is deliberately never read.
	out, err := remote.source.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: "host:S"})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}
	_ = out

	// Fill the thread pump's out buffer and then its in buffer, so the drain
	// goroutine parks on the thread send.
	for i := range remoteHubSubBuffer*2 + 16 {
		if err := remote.push(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{ThreadID: "S", Ref: "local:S"}); err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
	}

	// Kill the client's connection while the drain is parked on the thread send.
	_, _ = remote.source.ListModels(context.Background(), appwire.ModelListParams{})

	select {
	case _, ok := <-notifications:
		if ok {
			t.Fatal("received a notification, want the subscription to close")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("host subscription was not closed after the client died while thread delivery was backpressured")
	}
}

// TestRemoteHubSourceHostNotificationFilterShortCircuitsBeforeSnapshot pins the
// publish-time filter's contract and the hot-path ordering it depends on: the
// filter is what keeps the high-frequency thread/streaming families — the
// overwhelming majority of the shared drain goroutine's traffic — from building
// a per-notification snapshot of the host subscriptions.
//
// A rejected notification must not reach a consumer, must not count as a drop,
// and must leave the reject path allocation-free with a live subscription. (The
// snapshot slice is stack-allocated in this function — escape analysis reports
// "does not escape" — so the allocation check guards the property rather than
// pinning a heap regression; see the round-three PR comment.) An accepted
// notification still delivers.
func TestRemoteHubSourceHostNotificationFilterShortCircuitsBeforeSnapshot(t *testing.T) {
	client, _ := newScriptedClient(t, func(string, json.RawMessage) scriptedReply {
		return scriptedReply{}
	})
	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})
	source.SetHostNotificationFilter(func(method string) bool {
		return method == appwire.NotifyEvenerAuthUpdated
	})

	subCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	notifications, err := source.SubscribeHostNotifications(subCtx)
	if err != nil {
		t.Fatalf("SubscribeHostNotifications: %v", err)
	}

	// A subscription is live, so a snapshot of it is real work rather than an
	// empty iteration.
	rejected := appwire.Notification{Method: appwire.NotifyThreadStatusChanged}
	if allocs := testing.AllocsPerRun(100, func() {
		source.publishHostNotification(client, rejected)
	}); allocs > 0 {
		t.Fatalf("publishHostNotification allocated %v per call for a filtered-out notification; want 0", allocs)
	}
	if dropped := source.hostNotifyDropped.Load(); dropped != 0 {
		t.Fatalf("hostNotifyDropped = %d after filtered-out publishes, want 0 (a filter rejection is not a drop)", dropped)
	}
	select {
	case notification := <-notifications:
		t.Fatalf("subscription received filtered-out notification %q", notification.Method)
	case <-time.After(100 * time.Millisecond):
	}

	// The accepted family still reaches the consumer.
	source.publishHostNotification(client, appwire.Notification{Method: appwire.NotifyEvenerAuthUpdated})
	select {
	case notification := <-notifications:
		if notification.Method != appwire.NotifyEvenerAuthUpdated {
			t.Fatalf("notification method = %q", notification.Method)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the accepted notification")
	}
}

// hostSubClient returns the client of the single registered host subscription.
func (s *RemoteHubSource) hostSubClient(t *testing.T) *appwire.Client {
	t.Helper()
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for sub := range s.hostSubs {
		return sub.client
	}
	t.Fatal("no host subscription registered")
	return nil
}

// TestRemoteHubSourceSubscribeHostNotificationsDeliversAndUnregisters covers
// the component-07a broker seam: registration starts the drain and delivers
// host-level notifications that carry no thread route, and a cancelled context
// removes the consumer so a departed reader cannot wedge the shared drain.
func TestRemoteHubSourceSubscribeHostNotificationsDeliversAndUnregisters(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	server := appwire.NewStreamTransport(serverConn)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			msg, err := server.Recv(ctx)
			if err != nil {
				return
			}
			if msg.Request == nil || msg.Request.Method != appwire.MethodInitialize {
				continue
			}
			data, _ := json.Marshal(appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"})
			if err := server.Send(ctx, appwire.ResponseMessage(msg.Request.ID, json.RawMessage(data))); err != nil {
				return
			}
		}
	}()

	client := appwire.NewClient(appwire.NewStreamTransport(clientConn))
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{}); err != nil {
		t.Fatalf("initialize scripted remote: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = client.Close()
		<-done
	})

	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})

	subCtx, stopSub := context.WithCancel(context.Background())
	notifications, err := source.SubscribeHostNotifications(subCtx)
	if err != nil {
		t.Fatalf("SubscribeHostNotifications: %v", err)
	}

	if err := server.Send(ctx, appwire.NotificationMessage(appwire.NotifyEvenerAuthUpdated, map[string]string{"provider": "openai"})); err != nil {
		t.Fatalf("send notification: %v", err)
	}
	select {
	case notification := <-notifications:
		if notification.Method != appwire.NotifyEvenerAuthUpdated {
			t.Fatalf("notification method = %q, want %q", notification.Method, appwire.NotifyEvenerAuthUpdated)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the host-level notification")
	}

	stopSub()
	deadline := time.Now().Add(5 * time.Second)
	for {
		source.subMu.Lock()
		remaining := len(source.hostSubs)
		source.subMu.Unlock()
		if remaining == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("hostSubs = %d after cancel, want 0", remaining)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
