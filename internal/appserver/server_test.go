package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
)

func TestConnectionRequiresInitialize(t *testing.T) {
	server := NewServer(ServerConfig{
		ServerName: "serf-hub",
		Version:    "test",
		SourceID:   "local",
	})
	// Register a live handler so that without the initialize gate this request
	// would succeed (MessageResponse). The assertion only holds if the gate is
	// the sole source of the error, not a MethodNotFound fallback.
	HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		return appwire.ThreadListResponse{}, nil
	})
	conn := server.NewConnection("conn-1")
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodThreadList, appwire.ThreadListParams{}))
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("kind=%v, want error", resp.Kind())
	}
}

func TestConnectionPingAnsweredWithoutInitialize(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "serf-hub", Version: "test", SourceID: "local"})
	conn := server.NewConnection("conn-1")
	// The browser heartbeat must succeed regardless of initialize state and
	// without touching the router, so a hung daemon can't make the keepalive
	// probe spuriously fail.
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(7), appwire.MethodPing, nil))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("ping kind=%v, want response", resp.Kind())
	}
}

func TestConnectionInitializeAllowsLaterRequests(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "serf-hub", Version: "test", SourceID: "local"})
	HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		return appwire.ThreadListResponse{}, nil
	})
	conn := server.NewConnection("conn-1")
	initResp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if initResp.Kind() != appwire.MessageResponse {
		t.Fatalf("init kind=%v", initResp.Kind())
	}
	listResp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadList, appwire.ThreadListParams{}))
	if listResp.Kind() != appwire.MessageResponse {
		t.Fatalf("list kind=%v", listResp.Kind())
	}
}

func TestConnectionInitializeRejectsMissingOrMismatchedProtocolVersion(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params map[string]any
	}{
		{name: "missing", params: map[string]any{}},
		{name: "mismatched", params: map[string]any{"protocolVersion": "serf-appwire-v1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := NewServer(ServerConfig{ServerName: "serf-hub", Version: "test", SourceID: "local"})
			conn := server.NewConnection("conn-1")

			resp := conn.HandleMessage(
				context.Background(),
				appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, tc.params),
			)

			if resp.Kind() != appwire.MessageError {
				t.Fatalf("initialize kind=%v, want error", resp.Kind())
			}
		})
	}
}

func TestConnectionValidatesExpectedQueueRevisionBeforeDispatch(t *testing.T) {
	tests := []struct {
		name       string
		value      string
		wantKind   appwire.MessageKind
		wantCalled bool
	}{
		{name: "zero", value: "0", wantKind: appwire.MessageResponse, wantCalled: true},
		{name: "null", value: "null", wantKind: appwire.MessageError},
		{name: "fractional", value: "1.5", wantKind: appwire.MessageError},
		{name: "negative", value: "-1", wantKind: appwire.MessageError},
		{name: "string", value: `"0"`, wantKind: appwire.MessageError},
		{name: "overflow", value: "18446744073709551616", wantKind: appwire.MessageError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := NewServer(ServerConfig{ServerName: "serf-hub", Version: "test", SourceID: "local"})
			called := false
			HandleTyped(server.Router(), appwire.MethodTurnDrainAsSteer, func(_ context.Context, params appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
				called = true
				if params.ExpectedQueueRevision != 0 {
					t.Fatalf("expectedQueueRevision=%d, want 0", params.ExpectedQueueRevision)
				}
				return appwire.TurnDrainAsSteerResponse{}, nil
			})
			conn := server.NewConnection("conn-1")
			initResp := conn.HandleMessage(context.Background(), appwire.RequestMessage(
				appwire.NewIntID(1),
				appwire.MethodInitialize,
				appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion},
			))
			if initResp.Kind() != appwire.MessageResponse {
				t.Fatalf("init kind=%v, want response", initResp.Kind())
			}

			params := json.RawMessage(`{"clientMutationId":"m1","expectedTurnId":"t1","expectedQueueRevision":` + tc.value + `}`)
			resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(
				appwire.NewIntID(2),
				appwire.MethodTurnDrainAsSteer,
				params,
			))

			if resp.Kind() != tc.wantKind {
				t.Errorf("kind=%v, want %v", resp.Kind(), tc.wantKind)
			}
			if called != tc.wantCalled {
				t.Errorf("handler called=%t, want %t", called, tc.wantCalled)
			}
		})
	}
}

func TestConnectionAcceptsInitializedNotification(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "serf-hub", Version: "test", SourceID: "local"})
	HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		return appwire.ThreadListResponse{}, nil
	})
	conn := server.NewConnection("conn-1")
	initResp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if initResp.Kind() != appwire.MessageResponse {
		t.Fatalf("init kind=%v", initResp.Kind())
	}
	ack := conn.HandleMessage(context.Background(), appwire.NotificationMessage(appwire.MethodInitialized, nil))
	if ack.Kind() != appwire.MessageInvalid {
		t.Fatalf("initialized notification kind=%v, want no response", ack.Kind())
	}
	listResp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadList, appwire.ThreadListParams{}))
	if listResp.Kind() != appwire.MessageResponse {
		t.Fatalf("list kind=%v", listResp.Kind())
	}
}

func TestConnectionRejectsRepeatedInitialize(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "serf-hub", Version: "test", SourceID: "local"})
	conn := server.NewConnection("conn-1")
	first := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if first.Kind() != appwire.MessageResponse {
		t.Fatalf("first init kind=%v", first.Kind())
	}
	second := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if second.Kind() != appwire.MessageError {
		t.Fatalf("second init kind=%v, want error", second.Kind())
	}
}

func TestInitializeIsConnectionScoped(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "serf-hub", Version: "test", SourceID: "local"})
	conn1 := server.NewConnection("conn-1")
	conn2 := server.NewConnection("conn-2")

	resp := conn1.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("init kind=%v", resp.Kind())
	}
	other := conn2.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadList, appwire.ThreadListParams{}))
	if other.Kind() != appwire.MessageError {
		t.Fatalf("other kind=%v, want error", other.Kind())
	}
}

func TestConnectionResponseEnqueueWaitsForCapacity(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "serf-hub", Version: "test", SourceID: "local"})
	conn := server.NewConnection("conn-1")
	for i := 0; i < cap(conn.send); i++ {
		conn.enqueue(appwire.NotificationMessage("notice", map[string]any{"i": i}))
	}

	response := appwire.ResponseMessage(appwire.NewIntID(42), map[string]string{"ok": "true"})
	done := make(chan error, 1)
	go func() {
		done <- conn.enqueueResponse(context.Background(), response)
	}()

	<-conn.send
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("enqueueResponse: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("enqueueResponse did not complete after capacity became available")
	}

	found := false
	for len(conn.send) > 0 {
		msg := <-conn.send
		if msg.Response != nil && msg.IDString() == "42" {
			found = true
		}
	}
	if !found {
		t.Fatal("response was not delivered after capacity became available")
	}
}

func TestConnectionEnqueueAfterUnregisterDoesNotPanic(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "serf-hub", Version: "test", SourceID: "local"})
	conn := server.NewConnection("conn-1")
	server.registerConnection(conn)
	server.unregisterConnection(conn)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("enqueue after unregister panicked: %v", r)
		}
	}()
	conn.enqueue(appwire.NotificationMessage("notice", nil))
}

func TestStaleConnectionTeardownPreservesSameIDReplacement(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "serf-hub", Version: "test", SourceID: "local"})
	stale := server.NewConnection("conn-shared")
	replacement := server.NewConnection("conn-shared")
	replacementCanceled := make(chan struct{})
	replacement.setCancel(func() { close(replacementCanceled) })
	server.registerConnection(stale)
	server.registerConnection(replacement)
	replacement.Subscribe("th_replacement")

	server.unregisterConnection(stale)

	server.mu.RLock()
	registered := server.conns[replacement.ID()]
	server.mu.RUnlock()
	if registered != replacement {
		t.Fatal("stale teardown removed same-ID replacement")
	}
	if !server.subs.IsSubscribed(replacement.ID(), "th_replacement") {
		t.Fatal("stale teardown removed replacement subscription")
	}
	select {
	case <-replacementCanceled:
		t.Fatal("stale teardown canceled same-ID replacement")
	default:
	}
	if !replacement.enqueue(appwire.NotificationMessage("notice", nil)) {
		t.Fatal("stale teardown closed same-ID replacement send channel")
	}
}

func TestStaleBroadcastFailurePreservesSameIDReplacement(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "serf-hub", Version: "test", SourceID: "local"})
	stale := server.NewConnection("conn-shared")
	replacement := server.NewConnection("conn-shared")
	replacementCanceled := make(chan struct{})
	replacement.setCancel(func() { close(replacementCanceled) })
	server.registerConnection(stale)
	stale.Subscribe("th_broadcast")

	broadcastSelected := make(chan struct{})
	releaseBroadcast := make(chan struct{})
	server.afterBroadcastConnectionLookup = func(got *Connection) {
		if got == stale {
			close(broadcastSelected)
			<-releaseBroadcast
		}
	}
	broadcastDone := make(chan struct{})
	go func() {
		server.Broadcast("th_broadcast", "notice", nil)
		close(broadcastDone)
	}()
	select {
	case <-broadcastSelected:
	case <-time.After(time.Second):
		t.Fatal("broadcast did not retain stale concrete connection")
	}

	server.registerConnection(replacement)
	replacement.Subscribe("th_broadcast")
	stale.closeSend()
	close(releaseBroadcast)
	select {
	case <-broadcastDone:
	case <-time.After(time.Second):
		t.Fatal("stale broadcast failure did not return")
	}

	server.mu.RLock()
	registered := server.conns[replacement.ID()]
	server.mu.RUnlock()
	if registered != replacement {
		t.Fatal("stale broadcast failure removed same-ID replacement")
	}
	if !server.subs.IsSubscribed(replacement.ID(), "th_broadcast") {
		t.Fatal("stale broadcast failure removed replacement subscription")
	}
	select {
	case <-replacementCanceled:
		t.Fatal("stale broadcast failure canceled same-ID replacement")
	default:
	}
}

func TestServer_BroadcastAll(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "serf-hub", Version: "test", SourceID: "local"})
	conn1 := server.NewConnection("conn-1")
	conn2 := server.NewConnection("conn-2")
	server.registerConnection(conn1)
	server.registerConnection(conn2)

	server.BroadcastAll("test/notify", map[string]string{"key": "value"})

	for _, tc := range []struct {
		name string
		conn *Connection
	}{
		{"conn-1", conn1},
		{"conn-2", conn2},
	} {
		select {
		case msg := <-tc.conn.send:
			if msg.Notification == nil {
				t.Fatalf("%s: expected notification, got %+v", tc.name, msg)
			}
			if msg.Notification.Method != "test/notify" {
				t.Fatalf("%s: method=%q, want %q", tc.name, msg.Notification.Method, "test/notify")
			}
			var params map[string]string
			if err := json.Unmarshal(msg.Notification.Params, &params); err != nil {
				t.Fatalf("%s: unmarshal params: %v", tc.name, err)
			}
			if params["key"] != "value" {
				t.Fatalf("%s: params[key]=%q, want %q", tc.name, params["key"], "value")
			}
		default:
			t.Fatalf("%s: no message received", tc.name)
		}
	}
}

func TestBroadcastDisconnectsSlowSubscriberInsteadOfDroppingNotification(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "serf-hub", Version: "test", SourceID: "local"})
	conn := server.NewConnection("conn-1")
	server.registerConnection(conn)
	conn.Subscribe("th_1")
	for i := 0; i < cap(conn.send); i++ {
		conn.enqueue(appwire.NotificationMessage("notice", map[string]any{"i": i}))
	}

	server.Broadcast("th_1", "notice", map[string]any{"overflow": true})

	if got := server.SubscriberCount("th_1"); got != 0 {
		t.Fatalf("subscriber count=%d, want slow subscriber disconnected", got)
	}
	server.mu.RLock()
	_, registered := server.conns[conn.ID()]
	server.mu.RUnlock()
	if registered {
		t.Fatal("slow subscriber connection remained registered after overflow")
	}
}

func TestReplaceSubscriptionsScopesConnectionToLatestThread(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "serf-hub", Version: "test", SourceID: "local"})
	conn := server.NewConnection("conn-1")
	server.registerConnection(conn)
	conn.Subscribe("th_old")

	conn.ReplaceSubscriptions("th_new")

	if got := server.SubscriberCount("th_old"); got != 0 {
		t.Fatalf("old subscriber count=%d, want 0", got)
	}
	if got := server.SubscriberCount("th_new"); got != 1 {
		t.Fatalf("new subscriber count=%d, want 1", got)
	}
	if conn.server.subs.IsSubscribed(conn.ID(), "th_old") {
		t.Fatal("connection remained subscribed to old thread")
	}
	if !conn.server.subs.IsSubscribed(conn.ID(), "th_new") {
		t.Fatal("connection was not subscribed to new thread")
	}
}

func TestAtomicRejoinDuringSnapshotCloneDeliversDeltaOnce(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test", SourceID: "local"})
	conn := server.NewConnection("conn-snapshot")
	server.registerConnection(conn)
	conn.setInitialized()

	type snapshotResponse struct {
		Text string `json:"text"`
	}
	var projectedMu sync.Mutex
	projectedText := ""
	notifier := NewNotifier(10)
	snapshotCloneEntered := make(chan struct{})
	releaseSnapshotClone := make(chan struct{})
	HandleTyped(server.Router(), "test/snapshot", func(ctx context.Context, _ struct{}) (snapshotResponse, error) {
		var response snapshotResponse
		if !CaptureSubscription(
			ctx,
			false,
			func() string { return "th_1" },
			notifier.CurrentSequence,
			func() bool {
				close(snapshotCloneEntered)
				<-releaseSnapshotClone
				projectedMu.Lock()
				defer projectedMu.Unlock()
				response = snapshotResponse{Text: projectedText}
				return true
			},
		) {
			t.Fatal("snapshot subscription was rejected")
		}
		return response, nil
	})

	responseReady := make(chan appwire.Message, 1)
	go func() {
		responseReady <- conn.HandleMessage(
			context.Background(),
			appwire.RequestMessage(appwire.NewIntID(1), "test/snapshot", struct{}{}),
		)
	}()
	<-snapshotCloneEntered

	eventStarted := make(chan struct{})
	eventCommitted := make(chan struct{})
	go func() {
		close(eventStarted)
		server.CommitProjection(func() []SequencedNotification {
			projectedMu.Lock()
			projectedText += "delta"
			projectedMu.Unlock()
			record := notifier.Record("th_1", appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{Delta: "delta"})
			return []SequencedNotification{record}
		})
		close(eventCommitted)
	}()
	<-eventStarted
	close(releaseSnapshotClone)
	<-eventCommitted
	response := <-responseReady
	if err := conn.enqueueResponse(context.Background(), response); err != nil {
		t.Fatalf("enqueue response: %v", err)
	}

	conn.closeSend()
	var messages []appwire.Message
	for msg := range conn.send {
		messages = append(messages, msg)
	}
	combined := ""
	for _, msg := range messages {
		switch {
		case msg.Response != nil:
			got, ok := msg.Response.Result.(snapshotResponse)
			if !ok {
				t.Fatalf("snapshot result = %T, want snapshotResponse", msg.Response.Result)
			}
			combined += got.Text
		case msg.Notification != nil:
			var params appwire.AgentMessageDeltaParams
			if err := json.Unmarshal(msg.Notification.Params, &params); err != nil {
				t.Fatalf("decode delta: %v", err)
			}
			combined += params.Delta
		default:
			t.Fatalf("unexpected message: %+v", msg)
		}
	}
	if combined != "delta" {
		t.Fatalf("snapshot plus released deltas = %q, want one append-only delta", combined)
	}
}

func TestAtomicRejoinWaitsForMatchingResponseEnqueue(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test", SourceID: "local"})
	conn := server.NewConnection("conn-matching-response")
	server.registerConnection(conn)
	conn.setInitialized()

	notifier := NewNotifier(10)
	HandleTyped(server.Router(), "test/matching-response", func(ctx context.Context, _ struct{}) (struct{}, error) {
		if !CaptureSubscription(
			ctx,
			false,
			func() string { return "th_1" },
			notifier.CurrentSequence,
			func() bool { return true },
		) {
			t.Fatal("snapshot subscription was rejected")
		}
		return struct{}{}, nil
	})

	snapshotResponse := conn.HandleMessage(
		context.Background(),
		appwire.RequestMessage(appwire.NewIntID(1), "test/matching-response", struct{}{}),
	)
	server.CommitProjection(func() []SequencedNotification {
		return []SequencedNotification{
			notifier.Record("th_1", appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{Delta: "post-cut"}),
		}
	})

	unrelatedResponse := appwire.ResponseMessage(appwire.NewIntID(2), struct{}{})
	if err := conn.enqueueResponse(context.Background(), unrelatedResponse); err != nil {
		t.Fatalf("enqueue unrelated response: %v", err)
	}
	if got := len(conn.send); got != 1 {
		t.Fatalf("messages after unrelated response = %d, want response only", got)
	}
	if unrelated := <-conn.send; unrelated.Response == nil || unrelated.IDString() != "2" {
		t.Fatalf("unrelated delivery = %+v, want unrelated response", unrelated)
	}

	if err := conn.enqueueResponse(context.Background(), snapshotResponse); err != nil {
		t.Fatalf("enqueue snapshot response: %v", err)
	}
	first := <-conn.send
	second := <-conn.send
	if first.Response == nil || first.IDString() != "1" {
		t.Fatalf("first matching delivery = %+v, want snapshot response", first)
	}
	if second.Notification == nil || second.Notification.Method != appwire.NotifyAgentMessageDelta {
		t.Fatalf("second matching delivery = %+v, want released post-cut delta", second)
	}
}

func TestAtomicRejoinResponseEnqueueCancellationRestoresSubscription(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test", SourceID: "local"})
	conn := server.NewConnection("conn-canceled-response")
	server.registerConnection(conn)
	conn.setInitialized()
	conn.Subscribe("th_1")

	notifier := NewNotifier(10)
	var commits atomic.Int32
	var aborts atomic.Int32
	HandleTyped(server.Router(), "test/canceled-response", func(ctx context.Context, _ struct{}) (struct{}, error) {
		if !CaptureSubscriptionWithHandoff(
			ctx,
			false,
			func() string { return "th_1" },
			notifier.CurrentSequence,
			func() bool { return true },
			CaptureSubscriptionHandoff{
				Commit: func() {
					commits.Add(1)
				},
				Abort: func() {
					server.subs.mu.RLock()
					sub := server.subs.byConn[conn.id]["th_1"]
					resumed := sub != nil && !sub.buffering
					server.subs.mu.RUnlock()
					if !resumed {
						t.Error("abort ran before the previous live subscription was restored")
					}
					aborts.Add(1)
				},
			},
		) {
			t.Fatal("snapshot subscription was rejected")
		}
		return struct{}{}, nil
	})

	snapshotResponse := conn.HandleMessage(
		context.Background(),
		appwire.RequestMessage(appwire.NewIntID(1), "test/canceled-response", struct{}{}),
	)
	for i := 0; i < cap(conn.send); i++ {
		if !conn.enqueue(appwire.NotificationMessage("fill", map[string]any{"i": i})) {
			t.Fatal("fill enqueue failed")
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := conn.enqueueResponse(canceled, snapshotResponse); !errors.Is(err, context.Canceled) {
		t.Fatalf("enqueue canceled response = %v, want context canceled", err)
	}
	for len(conn.send) > 0 {
		<-conn.send
	}

	server.Broadcast("th_1", "after-cancel", struct{}{})
	if got := len(conn.send); got != 1 {
		t.Fatalf("messages after canceled hydration = %d, want resumed live delivery", got)
	}
	if got := commits.Load(); got != 0 {
		t.Fatalf("commit callbacks = %d, want 0", got)
	}
	if got := aborts.Load(); got != 1 {
		t.Fatalf("abort callbacks = %d, want 1", got)
	}
	if err := conn.enqueueResponse(context.Background(), snapshotResponse); err != nil {
		t.Fatalf("late enqueue response: %v", err)
	}
	if got := aborts.Load(); got != 1 {
		t.Fatalf("abort callbacks after repeated terminal signal = %d, want 1", got)
	}
}

func TestCaptureSubscriptionHandoffCommitsAfterMatchingResponseEnqueue(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test", SourceID: "local"})
	conn := server.NewConnection("conn-handoff-commit")
	server.registerConnection(conn)
	conn.setInitialized()

	notifier := NewNotifier(10)
	var commits atomic.Int32
	var aborts atomic.Int32
	HandleTyped(server.Router(), "test/handoff-commit", func(ctx context.Context, _ struct{}) (struct{}, error) {
		if !CaptureSubscriptionWithHandoff(
			ctx,
			false,
			func() string { return "th_1" },
			notifier.CurrentSequence,
			func() bool { return true },
			CaptureSubscriptionHandoff{
				Commit: func() {
					server.subs.mu.RLock()
					sub := server.subs.byConn[conn.id]["th_1"]
					released := sub != nil && !sub.buffering
					server.subs.mu.RUnlock()
					if !released {
						t.Error("commit ran before the hydration capture was released")
					}
					commits.Add(1)
				},
				Abort: func() {
					aborts.Add(1)
				},
			},
		) {
			t.Fatal("snapshot subscription was rejected")
		}
		return struct{}{}, nil
	})

	response := conn.HandleMessage(
		context.Background(),
		appwire.RequestMessage(appwire.NewIntID(11), "test/handoff-commit", struct{}{}),
	)
	server.CommitProjection(func() []SequencedNotification {
		return []SequencedNotification{
			notifier.Record("th_1", appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{Delta: "post-cut"}),
		}
	})
	if got := commits.Load(); got != 0 {
		t.Fatalf("commit callbacks before response enqueue = %d, want 0", got)
	}

	if err := conn.enqueueResponse(context.Background(), appwire.ResponseMessage(appwire.NewIntID(12), struct{}{})); err != nil {
		t.Fatalf("enqueue unrelated response: %v", err)
	}
	if got := commits.Load(); got != 0 {
		t.Fatalf("commit callbacks after unrelated response = %d, want 0", got)
	}
	<-conn.send

	if err := conn.enqueueResponse(context.Background(), response); err != nil {
		t.Fatalf("enqueue matching response: %v", err)
	}
	if got := commits.Load(); got != 1 {
		t.Fatalf("commit callbacks = %d, want 1", got)
	}
	if got := aborts.Load(); got != 0 {
		t.Fatalf("abort callbacks = %d, want 0", got)
	}
	first := <-conn.send
	second := <-conn.send
	if first.Response == nil || first.IDString() != "11" || second.Notification == nil {
		t.Fatalf("matching delivery = [%+v, %+v], want response then released notification", first, second)
	}

	if err := conn.enqueueResponse(context.Background(), response); err != nil {
		t.Fatalf("enqueue repeated matching response: %v", err)
	}
	server.unregisterConnection(conn)
	if got := commits.Load(); got != 1 {
		t.Fatalf("commit callbacks after repeated terminal signals = %d, want 1", got)
	}
	if got := aborts.Load(); got != 0 {
		t.Fatalf("abort callbacks after commit = %d, want 0", got)
	}
}

func TestCaptureSubscriptionHandoffAbortsFailedCaptureAfterRestoringOwnership(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test", SourceID: "local"})
	conn := server.NewConnection("conn-handoff-capture-failure")
	server.registerConnection(conn)
	conn.setInitialized()
	conn.Subscribe("th_1")

	var commits atomic.Int32
	var aborts atomic.Int32
	HandleTyped(server.Router(), "test/handoff-capture-failure", func(ctx context.Context, _ struct{}) (struct{}, error) {
		if CaptureSubscriptionWithHandoff(
			ctx,
			false,
			func() string { return "th_1" },
			func() uint64 { return 0 },
			func() bool { return false },
			CaptureSubscriptionHandoff{
				Commit: func() {
					commits.Add(1)
				},
				Abort: func() {
					server.subs.mu.RLock()
					sub := server.subs.byConn[conn.id]["th_1"]
					restored := sub != nil && !sub.buffering
					server.subs.mu.RUnlock()
					if !restored {
						t.Error("capture failure abort ran before ownership was restored")
					}
					aborts.Add(1)
				},
			},
		) {
			t.Fatal("failed snapshot capture succeeded")
		}
		return struct{}{}, nil
	})

	response := conn.HandleMessage(
		context.Background(),
		appwire.RequestMessage(appwire.NewIntID(21), "test/handoff-capture-failure", struct{}{}),
	)
	if got := commits.Load(); got != 0 {
		t.Fatalf("commit callbacks = %d, want 0", got)
	}
	if got := aborts.Load(); got != 1 {
		t.Fatalf("abort callbacks = %d, want 1", got)
	}
	if err := conn.enqueueResponse(context.Background(), response); err != nil {
		t.Fatalf("enqueue response after capture failure: %v", err)
	}
	if got := aborts.Load(); got != 1 {
		t.Fatalf("abort callbacks after response = %d, want 1", got)
	}
}

func TestCaptureSubscriptionHandoffRequiresResponseConnection(t *testing.T) {
	var snapshotCalled atomic.Bool
	var commits atomic.Int32
	var aborts atomic.Int32

	captured := CaptureSubscriptionWithHandoff(
		context.Background(),
		false,
		func() string { return "th_1" },
		func() uint64 { return 0 },
		func() bool {
			snapshotCalled.Store(true)
			return true
		},
		CaptureSubscriptionHandoff{
			Commit: func() { commits.Add(1) },
			Abort:  func() { aborts.Add(1) },
		},
	)

	if captured {
		t.Fatal("handoff capture without a response connection succeeded")
	}
	if snapshotCalled.Load() {
		t.Fatal("snapshot ran without a response connection")
	}
	if got := commits.Load(); got != 0 {
		t.Fatalf("commit callbacks = %d, want 0", got)
	}
	if got := aborts.Load(); got != 1 {
		t.Fatalf("abort callbacks = %d, want 1", got)
	}
}

func TestCaptureSubscriptionPreservesLegacyReleaseAfterMatchingErrorResponse(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test", SourceID: "local"})
	conn := server.NewConnection("conn-legacy-error")
	server.registerConnection(conn)
	conn.setInitialized()
	conn.Subscribe("th_1")

	notifier := NewNotifier(10)
	HandleTyped(server.Router(), "test/legacy-error", func(ctx context.Context, _ struct{}) (struct{}, error) {
		if !CaptureSubscription(
			ctx,
			false,
			func() string { return "th_1" },
			notifier.CurrentSequence,
			func() bool { return true },
		) {
			t.Fatal("snapshot subscription was rejected")
		}
		return struct{}{}, appwire.InvalidRequest("snapshot unavailable")
	})

	errorResponse := conn.HandleMessage(
		context.Background(),
		appwire.RequestMessage(appwire.NewIntID(24), "test/legacy-error", struct{}{}),
	)
	if errorResponse.Error == nil {
		t.Fatalf("handler response = %+v, want error", errorResponse)
	}
	server.CommitProjection(func() []SequencedNotification {
		return []SequencedNotification{
			notifier.Record("th_1", appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{Delta: "post-cut"}),
		}
	})

	if err := conn.enqueueResponse(context.Background(), errorResponse); err != nil {
		t.Fatalf("enqueue error response: %v", err)
	}
	if got := len(conn.send); got != 2 {
		t.Fatalf("messages after legacy error response = %d, want error then released post-cut delta", got)
	}
	first := <-conn.send
	second := <-conn.send
	if first.Error == nil || first.IDString() != "24" {
		t.Fatalf("first delivery = %+v, want matching error response", first)
	}
	if second.Notification == nil || second.Notification.Method != appwire.NotifyAgentMessageDelta {
		t.Fatalf("second delivery = %+v, want released post-cut delta", second)
	}
}

func TestCaptureSubscriptionHandoffAbortsMatchingErrorResponse(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test", SourceID: "local"})
	conn := server.NewConnection("conn-handoff-error")
	server.registerConnection(conn)
	conn.setInitialized()
	conn.Subscribe("th_1")

	notifier := NewNotifier(10)
	var commits atomic.Int32
	var aborts atomic.Int32
	HandleTyped(server.Router(), "test/handoff-error", func(ctx context.Context, _ struct{}) (struct{}, error) {
		if !CaptureSubscriptionWithHandoff(
			ctx,
			false,
			func() string { return "th_1" },
			notifier.CurrentSequence,
			func() bool { return true },
			CaptureSubscriptionHandoff{
				Commit: func() {
					commits.Add(1)
				},
				Abort: func() {
					server.subs.mu.RLock()
					sub := server.subs.byConn[conn.id]["th_1"]
					restored := sub != nil && !sub.buffering
					server.subs.mu.RUnlock()
					if !restored {
						t.Error("error response abort ran before ownership was restored")
					}
					aborts.Add(1)
				},
			},
		) {
			t.Fatal("snapshot subscription was rejected")
		}
		return struct{}{}, appwire.InvalidRequest("snapshot unavailable")
	})

	errorResponse := conn.HandleMessage(
		context.Background(),
		appwire.RequestMessage(appwire.NewIntID(25), "test/handoff-error", struct{}{}),
	)
	if errorResponse.Error == nil {
		t.Fatalf("handler response = %+v, want error", errorResponse)
	}
	server.CommitProjection(func() []SequencedNotification {
		return []SequencedNotification{
			notifier.Record("th_1", appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{Delta: "held"}),
		}
	})

	if err := conn.enqueueResponse(context.Background(), errorResponse); err != nil {
		t.Fatalf("enqueue error response: %v", err)
	}
	if got := commits.Load(); got != 0 {
		t.Fatalf("commit callbacks = %d, want 0", got)
	}
	if got := aborts.Load(); got != 1 {
		t.Fatalf("abort callbacks = %d, want 1", got)
	}
	if got := len(conn.send); got != 1 {
		t.Fatalf("messages after error response = %d, want error without held delta", got)
	}
	if msg := <-conn.send; msg.Error == nil || msg.IDString() != "25" {
		t.Fatalf("error delivery = %+v, want matching error", msg)
	}

	server.Broadcast("th_1", "after-error", struct{}{})
	if got := len(conn.send); got != 1 {
		t.Fatalf("messages after error abort = %d, want resumed live delivery", got)
	}
}

func TestCaptureSubscriptionHandoffUnregisterWithdrawsBeforeAbort(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test", SourceID: "local"})
	conn := server.NewConnection("conn-handoff-unregister")
	server.registerConnection(conn)
	conn.setInitialized()

	var commits atomic.Int32
	var aborts atomic.Int32
	HandleTyped(server.Router(), "test/handoff-unregister", func(ctx context.Context, _ struct{}) (struct{}, error) {
		if !CaptureSubscriptionWithHandoff(
			ctx,
			false,
			func() string { return "th_1" },
			func() uint64 { return 0 },
			func() bool { return true },
			CaptureSubscriptionHandoff{
				Commit: func() {
					commits.Add(1)
				},
				Abort: func() {
					if server.subs.IsSubscribed(conn.id, "th_1") {
						t.Error("unregister abort ran before subscription withdrawal")
					}
					aborts.Add(1)
				},
			},
		) {
			t.Fatal("snapshot subscription was rejected")
		}
		return struct{}{}, nil
	})

	response := conn.HandleMessage(
		context.Background(),
		appwire.RequestMessage(appwire.NewIntID(31), "test/handoff-unregister", struct{}{}),
	)
	server.unregisterConnection(conn)
	if got := commits.Load(); got != 0 {
		t.Fatalf("commit callbacks = %d, want 0", got)
	}
	if got := aborts.Load(); got != 1 {
		t.Fatalf("abort callbacks = %d, want 1", got)
	}
	if err := conn.enqueueResponse(context.Background(), response); !errors.Is(err, context.Canceled) {
		t.Fatalf("enqueue after unregister = %v, want context canceled", err)
	}
	if got := aborts.Load(); got != 1 {
		t.Fatalf("abort callbacks after repeated terminal signal = %d, want 1", got)
	}
}

func TestCaptureSubscriptionHandoffSupersessionFencesStaleResponse(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test", SourceID: "local"})
	conn := server.NewConnection("conn-handoff-supersession")
	server.registerConnection(conn)
	conn.setInitialized()

	var firstCommits atomic.Int32
	var firstAborts atomic.Int32
	var secondCommits atomic.Int32
	var firstGeneration uint64
	install := func(method string, handoff CaptureSubscriptionHandoff) {
		HandleTyped(server.Router(), method, func(ctx context.Context, _ struct{}) (struct{}, error) {
			if !CaptureSubscriptionWithHandoff(
				ctx,
				false,
				func() string { return "th_1" },
				func() uint64 { return 0 },
				func() bool { return true },
				handoff,
			) {
				t.Fatal("snapshot subscription was rejected")
			}
			return struct{}{}, nil
		})
	}
	install("test/handoff-first", CaptureSubscriptionHandoff{
		Commit: func() { firstCommits.Add(1) },
		Abort: func() {
			server.subs.mu.RLock()
			sub := server.subs.byConn[conn.id]["th_1"]
			firstWithdrawn := sub == nil || sub.generation != firstGeneration
			server.subs.mu.RUnlock()
			if !firstWithdrawn {
				t.Error("superseded abort ran before the first generation was withdrawn")
			}
			firstAborts.Add(1)
		},
	})
	install("test/handoff-second", CaptureSubscriptionHandoff{
		Commit: func() { secondCommits.Add(1) },
		Abort:  func() { t.Error("second handoff unexpectedly aborted") },
	})

	firstResponse := conn.HandleMessage(
		context.Background(),
		appwire.RequestMessage(appwire.NewIntID(41), "test/handoff-first", struct{}{}),
	)
	server.subs.mu.RLock()
	firstGeneration = server.subs.byConn[conn.id]["th_1"].generation
	server.subs.mu.RUnlock()
	secondResponse := conn.HandleMessage(
		context.Background(),
		appwire.RequestMessage(appwire.NewIntID(42), "test/handoff-second", struct{}{}),
	)
	if got := firstAborts.Load(); got != 1 {
		t.Fatalf("first abort callbacks after supersession = %d, want 1", got)
	}

	if err := conn.enqueueResponse(context.Background(), firstResponse); err != nil {
		t.Fatalf("enqueue stale first response: %v", err)
	}
	if got := firstCommits.Load(); got != 0 {
		t.Fatalf("stale first commits = %d, want 0", got)
	}
	<-conn.send
	if err := conn.enqueueResponse(context.Background(), secondResponse); err != nil {
		t.Fatalf("enqueue second response: %v", err)
	}
	if got := secondCommits.Load(); got != 1 {
		t.Fatalf("second commits = %d, want 1", got)
	}
}

func TestCaptureSubscriptionHandoffCommitAbortRaceHasOneWinner(t *testing.T) {
	for i := range 100 {
		server := NewServer(ServerConfig{ServerName: "test", SourceID: "local"})
		conn := server.NewConnection(fmt.Sprintf("conn-handoff-race-%d", i))
		server.registerConnection(conn)
		conn.setInitialized()

		var commits atomic.Int32
		var aborts atomic.Int32
		HandleTyped(server.Router(), "test/handoff-race", func(ctx context.Context, _ struct{}) (struct{}, error) {
			if !CaptureSubscriptionWithHandoff(
				ctx,
				false,
				func() string { return "th_1" },
				func() uint64 { return 0 },
				func() bool { return true },
				CaptureSubscriptionHandoff{
					Commit: func() { commits.Add(1) },
					Abort:  func() { aborts.Add(1) },
				},
			) {
				t.Fatal("snapshot subscription was rejected")
			}
			return struct{}{}, nil
		})
		response := conn.HandleMessage(
			context.Background(),
			appwire.RequestMessage(appwire.NewIntID(51), "test/handoff-race", struct{}{}),
		)

		start := make(chan struct{})
		var racers sync.WaitGroup
		racers.Add(2)
		go func() {
			defer racers.Done()
			<-start
			_ = conn.enqueueResponse(context.Background(), response)
		}()
		go func() {
			defer racers.Done()
			<-start
			server.unregisterConnection(conn)
		}()
		close(start)
		racers.Wait()

		if got := commits.Load() + aborts.Load(); got != 1 {
			t.Fatalf("iteration %d terminal callbacks = %d (commit=%d abort=%d), want exactly 1", i, got, commits.Load(), aborts.Load())
		}
		if commits.Load() > 1 || aborts.Load() > 1 {
			t.Fatalf("iteration %d repeated callback: commit=%d abort=%d", i, commits.Load(), aborts.Load())
		}
	}
}

func TestAtomicRejoinBeforeSubscriberInsertionIncludesDeltaOnceInSnapshot(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test", SourceID: "local"})
	conn := server.NewConnection("conn-before-insert")
	server.registerConnection(conn)
	conn.setInitialized()

	type snapshotResponse struct {
		Text string `json:"text"`
	}
	var projectedText string
	notifier := NewNotifier(10)
	beforeSubscriberInsertion := make(chan struct{})
	releaseSubscriberInsertion := make(chan struct{})
	server.beforeSubscriptionRegistration = func() {
		close(beforeSubscriberInsertion)
		<-releaseSubscriberInsertion
	}
	HandleTyped(server.Router(), "test/before-insert", func(ctx context.Context, _ struct{}) (snapshotResponse, error) {
		var response snapshotResponse
		if !CaptureSubscription(
			ctx,
			false,
			func() string { return "th_1" },
			notifier.CurrentSequence,
			func() bool {
				response.Text = projectedText
				return true
			},
		) {
			t.Fatal("snapshot subscription was rejected")
		}
		return response, nil
	})

	responseReady := make(chan appwire.Message, 1)
	go func() {
		responseReady <- conn.HandleMessage(
			context.Background(),
			appwire.RequestMessage(appwire.NewIntID(1), "test/before-insert", struct{}{}),
		)
	}()
	<-beforeSubscriberInsertion

	server.CommitProjection(func() []SequencedNotification {
		projectedText += "delta"
		return []SequencedNotification{
			notifier.Record("th_1", appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{Delta: "delta"}),
		}
	})
	close(releaseSubscriberInsertion)

	response := <-responseReady
	if err := conn.enqueueResponse(context.Background(), response); err != nil {
		t.Fatalf("enqueue response: %v", err)
	}
	conn.closeSend()
	messages := make([]appwire.Message, 0)
	for msg := range conn.send {
		messages = append(messages, msg)
	}
	if len(messages) != 1 || messages[0].Response == nil {
		t.Fatalf("delivery = %+v, want snapshot response only", messages)
	}
	got, ok := messages[0].Response.Result.(snapshotResponse)
	if !ok {
		t.Fatalf("snapshot result = %T, want snapshotResponse", messages[0].Response.Result)
	}
	if got.Text != "delta" {
		t.Fatalf("snapshot text = %q, want append-only delta once", got.Text)
	}
}

func TestSnapshotCutDiscardsRecordsAtOrBeforeCut(t *testing.T) {
	subscriptions := NewSubscriptions()
	subscriptions.beginBuffered("conn", "th_1", false, 7)
	for seq := uint64(1); seq <= 2; seq++ {
		subscriptions.Route(SequencedNotification{Seq: seq, ThreadID: "th_1"})
	}
	if !subscriptions.SetCut("conn", "th_1", 7, 2) {
		t.Fatal("set snapshot cut failed")
	}
	for seq := uint64(3); seq <= 4; seq++ {
		subscriptions.Route(SequencedNotification{Seq: seq, ThreadID: "th_1"})
	}
	records, ok := subscriptions.Release("conn", "th_1", 7)
	if !ok {
		t.Fatal("release snapshot generation failed")
	}
	if len(records) != 2 || records[0].Seq != 3 || records[1].Seq != 4 {
		t.Fatalf("released sequences = %#v, want [3 4] in producer order", records)
	}
}

// TestSnapshotCutReleasesBroadcastToBufferingSubscriber pins the sequence
// Broadcast allocates for itself. A relay frame, a serf/thread/resync, or the
// synthesized failed turn/completed that replaces a dead spinner all reach a
// subscriber through Broadcast, and each is silent when lost. With no sequence
// of its own the record carries Seq 0, which never clears a non-zero cut, so
// every one that lands inside a hydration window is discarded on release.
func TestSnapshotCutReleasesBroadcastToBufferingSubscriber(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test", SourceID: "local"})
	conn := server.NewConnection("conn-broadcast-cut")
	server.registerConnection(conn)
	conn.setInitialized()

	notifier := NewNotifier(10)
	// Advance the notifier so the capture's cut is non-zero, which is the only
	// state a live thread is ever in by the time a client rejoins it.
	server.CommitProjection(func() []SequencedNotification {
		return []SequencedNotification{
			notifier.Record("th_1", appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{Delta: "before"}),
		}
	})
	if cut := notifier.CurrentSequence(); cut == 0 {
		t.Fatal("cut is zero; the test cannot tell a dropped record from a released one")
	}

	HandleTyped(server.Router(), "test/snapshot", func(ctx context.Context, _ struct{}) (struct{}, error) {
		if !CaptureSubscription(
			ctx,
			false,
			func() string { return "th_1" },
			notifier.CurrentSequence,
			func() bool { return true },
		) {
			t.Error("snapshot subscription was rejected")
		}
		return struct{}{}, nil
	})

	response := conn.HandleMessage(
		context.Background(),
		appwire.RequestMessage(appwire.NewIntID(1), "test/snapshot", struct{}{}),
	)

	server.Broadcast("th_1", appwire.NotifySerfThreadResync, appwire.ThreadResyncParams{ThreadID: "th_1"})

	if err := conn.enqueueResponse(context.Background(), response); err != nil {
		t.Fatalf("enqueue response: %v", err)
	}
	conn.closeSend()

	var methods []string
	for msg := range conn.send {
		if msg.Notification != nil {
			methods = append(methods, msg.Notification.Method)
		}
	}
	if len(methods) != 1 || methods[0] != appwire.NotifySerfThreadResync {
		t.Fatalf("released notifications = %v, want one %s", methods, appwire.NotifySerfThreadResync)
	}
}

// TestSnapshotCutAbortsCaptureWhoseRequestContextDied pins the pre-check that
// runs between setting the cut and registering the response finalizer. A
// request whose context died while the snapshot was being taken can never have
// its response enqueued as a commit, so registering a hydration for it would
// leave the connection buffering on the outcome of a race in enqueueResponse's
// select. The capture must instead be rejected and the previous ownership --
// here, live delivery -- restored.
func TestSnapshotCutAbortsCaptureWhoseRequestContextDied(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test", SourceID: "local"})
	conn := server.NewConnection("conn-dead-request")
	server.registerConnection(conn)
	conn.setInitialized()
	conn.Subscribe("th_1")

	notifier := NewNotifier(10)
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	captured := true
	HandleTyped(server.Router(), "test/snapshot", func(ctx context.Context, _ struct{}) (struct{}, error) {
		captured = CaptureSubscription(
			ctx,
			false,
			func() string { return "th_1" },
			notifier.CurrentSequence,
			func() bool {
				// The client goes away while the daemon is still cloning. This
				// runs under the projection gate, so the cancellation is ordered
				// strictly between the cut and the finalizer registration.
				cancelRequest()
				return true
			},
		)
		return struct{}{}, nil
	})

	conn.HandleMessage(requestCtx, appwire.RequestMessage(appwire.NewIntID(1), "test/snapshot", struct{}{}))
	if captured {
		t.Fatal("capture succeeded for a request whose context was already cancelled")
	}

	server.CommitProjection(func() []SequencedNotification {
		return []SequencedNotification{
			notifier.Record("th_1", appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{Delta: "after"}),
		}
	})
	conn.closeSend()

	delivered := 0
	for msg := range conn.send {
		if msg.Notification != nil {
			delivered++
		}
	}
	if delivered != 1 {
		t.Fatalf("live notifications after aborted capture = %d, want 1", delivered)
	}
}

// TestSnapshotCutWithdrawalFencesSupersededGeneration pins withdrawBuffered's
// generation check. A withdrawal names the generation it opened; without the
// check a late one would tear down whichever capture happens to be installed
// now, restoring that older attempt's rollback over a newer, live hydration.
func TestSnapshotCutWithdrawalFencesSupersededGeneration(t *testing.T) {
	subscriptions := NewSubscriptions()
	subscriptions.Subscribe("conn", "th_1")
	stale := subscriptions.beginBuffered("conn", "th_1", false, 1)
	subscriptions.beginBuffered("conn", "th_1", false, 2)

	if subscriptions.withdrawBuffered("conn", "th_1", 1, stale) {
		t.Fatal("superseded withdrawal reported success against a newer generation")
	}

	subscriptions.Route(SequencedNotification{Seq: 5, ThreadID: "th_1"})
	records, ok := subscriptions.Release("conn", "th_1", 2)
	if !ok {
		t.Fatal("the newer generation is no longer buffering; the stale withdrawal tore it down")
	}
	if len(records) != 1 || records[0].Seq != 5 {
		t.Fatalf("released records = %#v, want the one record routed after supersession", records)
	}
}

func TestAtomicReplaceSubscriptionOwnsSnapshotAndPostCutStream(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test", SourceID: "local"})
	conn := server.NewConnection("conn-replace")
	server.registerConnection(conn)
	conn.setInitialized()
	conn.Subscribe("th_old")

	type snapshotResponse struct {
		Text string `json:"text"`
	}
	notifier := NewNotifier(10)
	var projectedMu sync.Mutex
	projectedText := ""
	snapshotCloneEntered := make(chan struct{})
	releaseSnapshotClone := make(chan struct{})
	HandleTyped(server.Router(), "test/replace-snapshot", func(ctx context.Context, _ struct{}) (snapshotResponse, error) {
		var response snapshotResponse
		if !CaptureSubscription(
			ctx,
			true,
			func() string { return "th_new" },
			notifier.CurrentSequence,
			func() bool {
				close(snapshotCloneEntered)
				<-releaseSnapshotClone
				projectedMu.Lock()
				defer projectedMu.Unlock()
				response.Text = projectedText
				return true
			},
		) {
			t.Fatal("replacement snapshot was rejected")
		}
		return response, nil
	})

	responseReady := make(chan appwire.Message, 1)
	go func() {
		responseReady <- conn.HandleMessage(
			context.Background(),
			appwire.RequestMessage(appwire.NewIntID(1), "test/replace-snapshot", struct{}{}),
		)
	}()
	<-snapshotCloneEntered

	eventStarted := make(chan struct{})
	eventCommitted := make(chan struct{})
	go func() {
		close(eventStarted)
		server.CommitProjection(func() []SequencedNotification {
			projectedMu.Lock()
			projectedText += "new"
			projectedMu.Unlock()
			return []SequencedNotification{
				notifier.Record("th_new", appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{Delta: "new"}),
			}
		})
		close(eventCommitted)
	}()
	<-eventStarted
	close(releaseSnapshotClone)
	<-eventCommitted

	response := <-responseReady
	if err := conn.enqueueResponse(context.Background(), response); err != nil {
		t.Fatalf("enqueue response: %v", err)
	}

	conn.closeSend()
	var messages []appwire.Message
	for msg := range conn.send {
		messages = append(messages, msg)
	}
	if len(messages) != 2 || messages[0].Response == nil || messages[1].Notification == nil {
		t.Fatalf("replacement delivery = %+v, want response then post-cut notification", messages)
	}
	if server.subs.IsSubscribed(conn.ID(), "th_old") || !server.subs.IsSubscribed(conn.ID(), "th_new") {
		t.Fatalf("replacement ownership: old=%v new=%v", server.subs.IsSubscribed(conn.ID(), "th_old"), server.subs.IsSubscribed(conn.ID(), "th_new"))
	}
}

func TestContextSubscriptionRegistrationRejectsRemovedConnection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		register func(context.Context, string) bool
	}{
		{name: "subscribe", register: Subscribe},
		{name: "replace", register: ReplaceSubscriptions},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := NewServer(ServerConfig{ServerName: "serf-hub", Version: "test", SourceID: "local"})
			conn := server.NewConnection("conn-removed")
			server.registerConnection(conn)
			ctx := context.WithValue(context.Background(), connectionContextKey{}, conn)
			server.unregisterConnection(conn)

			if tc.register(ctx, "th_after_remove") {
				t.Fatal("registration succeeded for removed connection")
			}
			if got := server.SubscriberCount("th_after_remove"); got != 0 {
				t.Fatalf("subscriber count=%d, want no phantom subscriber", got)
			}
		})
	}
}

func TestContextSubscriptionRegistrationWithoutConnectionRemainsNoop(t *testing.T) {
	if !Subscribe(context.Background(), "th_no_connection") {
		t.Fatal("Subscribe without connection should preserve no-op success")
	}
	if !ReplaceSubscriptions(context.Background(), "th_no_connection") {
		t.Fatal("ReplaceSubscriptions without connection should preserve no-op success")
	}
}

func TestContextSubscriptionRegistrationSerializesWithConnectionTeardown(t *testing.T) {
	for _, tc := range []struct {
		name     string
		register func(context.Context, string) bool
	}{
		{name: "subscribe", register: Subscribe},
		{name: "replace", register: ReplaceSubscriptions},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := NewServer(ServerConfig{ServerName: "serf-hub", Version: "test", SourceID: "local"})
			conn := server.NewConnection("conn-race")
			server.registerConnection(conn)
			conn.Subscribe("th_old")
			ctx := context.WithValue(context.Background(), connectionContextKey{}, conn)

			connectionDeleted := make(chan struct{})
			registrationAttempted := make(chan struct{})
			releaseTeardown := make(chan struct{})
			server.afterUnregisterDelete = func() {
				close(connectionDeleted)
				<-registrationAttempted
				<-releaseTeardown
			}
			server.beforeSubscriptionRegistration = func() {
				close(registrationAttempted)
			}

			teardownDone := make(chan struct{})
			go func() {
				server.unregisterConnection(conn)
				close(teardownDone)
			}()
			select {
			case <-connectionDeleted:
			case <-time.After(time.Second):
				t.Fatal("connection teardown did not reach registry deletion")
			}

			registrationResult := make(chan bool, 1)
			go func() {
				registrationResult <- tc.register(ctx, "th_new")
			}()
			select {
			case <-registrationAttempted:
			case <-time.After(time.Second):
				t.Fatal("subscription registration did not reach teardown interleaving")
			}
			select {
			case result := <-registrationResult:
				t.Fatalf("registration returned before connection-registry teardown unlocked: %v", result)
			default:
			}
			close(releaseTeardown)
			select {
			case <-teardownDone:
			case <-time.After(time.Second):
				t.Fatal("connection teardown did not complete")
			}
			select {
			case result := <-registrationResult:
				if result {
					t.Fatal("registration succeeded after connection-registry deletion")
				}
			case <-time.After(time.Second):
				t.Fatal("subscription registration did not return after teardown")
			}
			if got := server.SubscriberCount("th_old"); got != 0 {
				t.Fatalf("old subscriber count=%d, want teardown cleanup", got)
			}
			if got := server.SubscriberCount("th_new"); got != 0 {
				t.Fatalf("new subscriber count=%d, want no phantom subscriber", got)
			}
		})
	}
}
