package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

func TestHubAtomicRejoinUsesRelaySessionRead(t *testing.T) {
	thread := appwire.Thread{
		ID:        "thread-atomic",
		SessionID: "thread-atomic",
		Source:    "local",
		Evener:    appwire.EvenerThread{Ref: "local:thread-atomic"},
	}
	handoff := &recordingRelayHandoff{
		committed: make(chan struct{}),
		aborted:   make(chan struct{}),
	}
	lease := &scriptedRelaySessionLease{
		readResult: appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: thread},
			Handoff:  handoff,
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	source := &relaySessionTestSource{
		thread: thread,
		lease:  lease,
	}

	sources := appsource.NewRegistry()
	sources.Add(source)
	appServer := newHubAppServer(hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
	}, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	defer hub.Close()

	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	response, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref:       thread.Evener.Ref,
		Subscribe: true,
	})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if response.Thread.ID != thread.ID {
		t.Fatalf("thread ID = %q, want %q", response.Thread.ID, thread.ID)
	}
	if got := source.legacyReadCallCount(); got != 0 {
		t.Fatalf("legacy ReadThread calls = %d, want no second upstream connection", got)
	}
	if got := lease.readCallCount(); got != 1 {
		t.Fatalf("RelaySession Read calls = %d, want 1", got)
	}
	if got := lease.listenCallCount(); got != 1 {
		t.Fatalf("RelaySession Listen calls = %d, want 1 Hub downstream owner", got)
	}
	// The hub commits the handoff after the response enters the connection's
	// send queue, so this goroutine can get here before Commit runs (the same
	// scheduling race app_rpc_test.go's retry test hit on a starved runner).
	select {
	case <-handoff.committed:
	case <-time.After(time.Second):
		t.Fatal("relay handoff was not committed after the thread/read response entered the downstream queue")
	}
	select {
	case <-handoff.aborted:
		t.Fatal("committed relay handoff was also aborted")
	default:
	}
}

func TestHubRelayCanonicalIdleRetiresChildBeforeRoot(t *testing.T) {
	previousInterval := hubRelayIdleInterval
	hubRelayIdleInterval = time.Millisecond
	t.Cleanup(func() { hubRelayIdleInterval = previousInterval })

	const (
		rootRef  = "local:canonical-root"
		childRef = "local:canonical-child"
	)
	deliveries := make(chan appsource.RelayDelivery)
	leaseClosed := make(chan struct{})
	lease := &scriptedRelaySessionLease{
		readFunc: func(params appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
			ref, err := appwire.ParseRef(params.Ref)
			if err != nil {
				return appsource.RelayReadResult{}, err
			}
			return appsource.RelayReadResult{
				Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
					ID: ref.ThreadID, Source: ref.SourceID,
					Evener: appwire.EvenerThread{Ref: params.Ref},
				}},
				Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
			}, nil
		},
		deliveries: deliveries,
		closeHook:  func() { close(leaseClosed) },
	}
	source := &relaySessionTestSource{
		lease: lease,
		resolveRelay: func(appwire.ThreadReadParams) (appwire.Ref, error) {
			return appwire.ParseRef(rootRef)
		},
	}
	sources := appsource.NewRegistry()
	sources.Add(source)
	idleDeletes := make(chan string, 2)
	appServer := newHubAppServer(hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
		RelayHooks: hubcore.RelayLifecycleHooks{
			AfterIdleDelete: func(threadID string) { idleDeletes <- threadID },
		},
	}, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	defer hub.Close()

	root := dialHubRPC(t, hub)
	defer root.Close()
	child := dialHubRPC(t, hub)
	defer child.Close()
	for _, client := range []*appwire.Client{root, child} {
		if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
	}
	if _, err := root.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: rootRef, Subscribe: true}); err != nil {
		t.Fatalf("root ThreadRead: %v", err)
	}
	if _, err := child.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: childRef, Subscribe: true}); err != nil {
		t.Fatalf("child ThreadRead: %v", err)
	}
	if got := lease.listenCallCount(); got != 1 {
		t.Fatalf("RelaySession Listen calls = %d, want one canonical listener", got)
	}

	if _, err := child.ThreadUnsubscribe(context.Background(), appwire.ThreadUnsubscribeParams{Ref: childRef}); err != nil {
		t.Fatalf("child ThreadUnsubscribe: %v", err)
	}
	select {
	case <-idleDeletes:
	case <-time.After(time.Second):
		t.Fatal("inactive child relay key was not retired while the root remained subscribed")
	}
	if got := lease.closeCallCount(); got != 0 {
		t.Fatalf("lease closes after child retirement = %d, want 0 while root remains", got)
	}

	childAck := make(chan struct{})
	deliveries <- appsource.RelayDelivery{
		Notification: appwire.Notification{
			Method: appwire.NotifyAgentMessageDelta,
			Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
				Ref: childRef, ThreadID: "canonical-child", TurnID: "turn-child", ItemID: "item-child", Delta: "stale",
			}),
		},
		Acknowledge: func() { close(childAck) },
	}
	<-childAck
	select {
	case notification := <-child.Notifications():
		t.Fatalf("retired child route delivered notification %+v", notification)
	default:
	}

	rootAck := make(chan struct{})
	deliveries <- appsource.RelayDelivery{
		Notification: appwire.Notification{
			Method: appwire.NotifyAgentMessageDelta,
			Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
				Ref: rootRef, ThreadID: "canonical-root", TurnID: "turn-root", ItemID: "item-root", Delta: "live",
			}),
		},
		Acknowledge: func() { close(rootAck) },
	}
	if got := <-root.Notifications(); got.Method != appwire.NotifyAgentMessageDelta {
		t.Fatalf("root notification method = %q, want %q", got.Method, appwire.NotifyAgentMessageDelta)
	}
	<-rootAck

	if _, err := root.ThreadUnsubscribe(context.Background(), appwire.ThreadUnsubscribeParams{Ref: rootRef}); err != nil {
		t.Fatalf("root ThreadUnsubscribe: %v", err)
	}
	select {
	case <-idleDeletes:
	case <-time.After(time.Second):
		t.Fatal("final relay key was not retired")
	}
	select {
	case <-leaseClosed:
	case <-time.After(time.Second):
		t.Fatal("final relay key retirement did not close its canonical lease")
	}
	if got := lease.closeCallCount(); got != 1 {
		t.Fatalf("final lease closes = %d, want exactly 1", got)
	}
}

func TestHubRelayReconnectRoutesOneResyncThroughCanonicalListener(t *testing.T) {
	const (
		rootRef  = "local:reconnect-root"
		childRef = "local:reconnect-child"
	)
	deliveries := make(chan appsource.RelayDelivery)
	lease := &scriptedRelaySessionLease{
		readFunc: func(params appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
			ref, err := appwire.ParseRef(params.Ref)
			if err != nil {
				return appsource.RelayReadResult{}, err
			}
			return appsource.RelayReadResult{
				Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
					ID: ref.ThreadID, Source: ref.SourceID,
					Evener: appwire.EvenerThread{Ref: params.Ref},
				}},
				Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
			}, nil
		},
		deliveries: deliveries,
	}
	source := &relaySessionTestSource{
		lease: lease,
		resolveRelay: func(appwire.ThreadReadParams) (appwire.Ref, error) {
			return appwire.ParseRef(rootRef)
		},
	}
	sources := appsource.NewRegistry()
	sources.Add(source)
	appServer := newHubAppServer(hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
	}, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	defer hub.Close()
	root := dialHubRPC(t, hub)
	defer root.Close()
	child := dialHubRPC(t, hub)
	defer child.Close()
	for _, client := range []*appwire.Client{root, child} {
		if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
	}
	if _, err := root.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: rootRef, Subscribe: true}); err != nil {
		t.Fatalf("root ThreadRead: %v", err)
	}
	if _, err := child.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: childRef, Subscribe: true}); err != nil {
		t.Fatalf("child ThreadRead: %v", err)
	}

	acknowledged := make(chan struct{})
	deliveries <- appsource.RelayDelivery{
		Notification: *appwire.NotificationMessage(appwire.NotifyEvenerThreadResync, appwire.ThreadResyncParams{
			ThreadID: "reconnect-root",
			Ref:      rootRef,
		}).Notification,
		Acknowledge: func() { close(acknowledged) },
	}
	got := <-root.Notifications()
	if got.Method != appwire.NotifyEvenerThreadResync {
		t.Fatalf("root recovery method = %q, want %q", got.Method, appwire.NotifyEvenerThreadResync)
	}
	<-acknowledged
	select {
	case extra := <-root.Notifications():
		t.Fatalf("reconnect emitted extra root notification %+v", extra)
	default:
	}
	select {
	case extra := <-child.Notifications():
		t.Fatalf("targeted reconnect resync reached child %+v", extra)
	default:
	}
	if got := lease.listenCallCount(); got != 1 {
		t.Fatalf("RelaySession Listen calls after reconnect resync = %d, want one resumed canonical listener", got)
	}
}

func TestHubAtomicRejoinFansOutAndAcknowledgesAfterResponse(t *testing.T) {
	thread := appwire.Thread{
		ID:        "thread-delivery",
		SessionID: "thread-delivery",
		Source:    "local",
		Evener:    appwire.EvenerThread{Ref: "local:thread-delivery"},
	}
	deliveries := make(chan appsource.RelayDelivery, 1)
	acknowledged := make(chan struct{})
	handoff := &recordingRelayHandoff{
		committed: make(chan struct{}),
		aborted:   make(chan struct{}),
		onCommit: func() {
			deliveries <- appsource.RelayDelivery{
				Notification: appwire.Notification{
					Method: appwire.NotifyAgentMessageDelta,
					Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
						ThreadID: thread.ID,
						Ref:      thread.Evener.Ref,
						TurnID:   "turn-delivery",
						ItemID:   "item-delivery",
						Delta:    "after snapshot",
					}),
				},
				Acknowledge: func() { close(acknowledged) },
			}
		},
	}
	lease := &scriptedRelaySessionLease{
		readResult: appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: thread},
			Handoff:  handoff,
		},
		deliveries: deliveries,
	}
	source := &relaySessionTestSource{
		thread: thread,
		lease:  lease,
	}
	sources := appsource.NewRegistry()
	sources.Add(source)
	appServer := newHubAppServer(hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
	}, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref:       thread.Evener.Ref,
		Subscribe: true,
	}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if got := appServer.SubscriberCount("local:" + thread.ID); got != 1 {
		t.Fatalf("qualified relay subscriber count = %d, want 1", got)
	}
	notification := <-client.Notifications()
	if notification.Method != appwire.NotifyAgentMessageDelta {
		t.Fatalf("notification method = %q, want %q", notification.Method, appwire.NotifyAgentMessageDelta)
	}
	<-acknowledged
}

func TestHubAtomicRejoinWritesResponseBeforePostCutNotification(t *testing.T) {
	thread := appwire.Thread{
		ID:        "thread-wire-order",
		SessionID: "thread-wire-order",
		Source:    "local",
		Evener:    appwire.EvenerThread{Ref: "local:thread-wire-order"},
	}
	deliveries := make(chan appsource.RelayDelivery, 1)
	handoff := &recordingRelayHandoff{
		committed: make(chan struct{}),
		aborted:   make(chan struct{}),
		onCommit: func() {
			deliveries <- appsource.RelayDelivery{
				Notification: appwire.Notification{
					Method: appwire.NotifyAgentMessageDelta,
					Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
						ThreadID: thread.ID,
						Ref:      thread.Evener.Ref,
						TurnID:   "turn-wire-order",
						ItemID:   "item-wire-order",
						Delta:    "after snapshot",
					}),
				},
				Acknowledge: func() {},
			}
		},
	}
	source := &relaySessionTestSource{
		thread: thread,
		lease: &scriptedRelaySessionLease{
			readResult: appsource.RelayReadResult{
				Response: appwire.ThreadReadResponse{Thread: thread},
				Handoff:  handoff,
			},
			deliveries: deliveries,
		},
	}
	sources := appsource.NewRegistry()
	sources.Add(source)
	appServer := newHubAppServer(hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
	}, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	defer hub.Close()

	transport, err := appwire.DialWebSocket(context.Background(), "ws"+hub.URL[len("http"):]+"/rpc", hub.Client())
	if err != nil {
		t.Fatalf("dial hub rpc: %v", err)
	}
	client := appwire.NewClient(transport)
	type observedFrame struct {
		message appwire.Message
		err     error
	}
	frames := make(chan observedFrame, 3)
	client.SetOrderedFrameHandler(func(message appwire.Message, err error) {
		frames <- observedFrame{message: message, err: err}
	})
	client.Start(context.Background())
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if frame := <-frames; frame.err != nil || frame.message.Response == nil {
		t.Fatalf("initialize frame = %+v, want response", frame)
	}

	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref:       thread.Evener.Ref,
		Subscribe: true,
	}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	response := <-frames
	if response.err != nil || response.message.Response == nil {
		t.Fatalf("first hydration frame = %+v, want matching response", response)
	}
	notification := <-frames
	if notification.err != nil || notification.message.Notification == nil {
		t.Fatalf("second hydration frame = %+v, want post-cut notification", notification)
	}
	if notification.message.Notification.Method != appwire.NotifyAgentMessageDelta {
		t.Fatalf("post-cut method = %q", notification.message.Notification.Method)
	}
}

func TestHubAtomicRejoinRejectsSnapshotWithoutLiveHandoff(t *testing.T) {
	thread := appwire.Thread{
		ID:     "thread-no-handoff",
		Source: "local",
		Evener: appwire.EvenerThread{Ref: "local:thread-no-handoff"},
	}
	lease := &scriptedRelaySessionLease{
		readResult: appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: thread},
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	source := &relaySessionTestSource{
		thread: thread,
		lease:  lease,
	}
	relays := newHubRelayFunctions(
		appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"}),
		hubcore.WebConfig{},
		appsource.NewRegistry(),
	)

	_, err := relays.readThread(context.Background(), source, appwire.ThreadReadParams{
		Ref:       thread.Evener.Ref,
		Subscribe: true,
	})
	if err == nil {
		t.Fatal("atomic ThreadRead accepted a snapshot without a live subscribed continuation")
	}
}

func TestHubAtomicRejoinDoesNotConfirmSnapshotWhenLiveHandoffCannotPrepare(t *testing.T) {
	thread := appwire.Thread{
		ID:        "thread-stale-handoff",
		SessionID: "thread-stale-handoff",
		Source:    "local",
		Evener:    appwire.EvenerThread{Ref: "local:thread-stale-handoff"},
	}
	handoff := &recordingRelayHandoff{
		committed: make(chan struct{}),
		aborted:   make(chan struct{}),
		stale:     true,
	}
	lease := &scriptedRelaySessionLease{
		readResult: appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: thread},
			Handoff:  handoff,
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	source := &relaySessionTestSource{
		thread: thread,
		lease:  lease,
	}
	sources := appsource.NewRegistry()
	sources.Add(source)
	appServer := newHubAppServer(hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
	}, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	defer hub.Close()

	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref:       thread.Evener.Ref,
		Subscribe: true,
	}); err == nil {
		t.Fatal("thread/read confirmed a snapshot after its live handoff could not be prepared")
	}
	if got := appServer.SubscriberCount("local:" + thread.ID); got != 0 {
		t.Fatalf("subscriber count after failed handoff = %d, want capture withdrawn", got)
	}
	select {
	case <-handoff.aborted:
	default:
		t.Fatal("stale handoff was not aborted")
	}
	select {
	case <-handoff.committed:
		t.Fatal("stale handoff committed")
	default:
	}
}

func TestHubAtomicRejoinCaptureFailureAbortsPreparedHandoff(t *testing.T) {
	handoff := &recordingRelayHandoff{committed: make(chan struct{}), aborted: make(chan struct{})}
	read := &hubThreadReadResult{
		response: appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:     "thread-no-response-connection",
			Source: "local",
			Evener: appwire.EvenerThread{Ref: "local:thread-no-response-connection"},
		}},
		handoff: handoff,
	}
	relays := newHubRelayFunctions(
		appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"}),
		hubcore.WebConfig{},
		appsource.NewRegistry(),
	)

	if relays.captureThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref:       read.response.Thread.Evener.Ref,
		Subscribe: true,
	}, read) {
		t.Fatal("capture without a downstream response connection succeeded")
	}
	select {
	case <-handoff.aborted:
	default:
		t.Fatal("failed downstream capture did not abort its prepared actor handoff")
	}
	select {
	case <-handoff.committed:
		t.Fatal("failed downstream capture committed its actor handoff")
	default:
	}
}

func TestHubTurnStartPreparesCanonicalRelaySession(t *testing.T) {
	thread := appwire.Thread{
		ID:     "thread-turn-start",
		Source: "local",
		Evener: appwire.EvenerThread{Ref: "local:thread-turn-start"},
	}
	handoff := &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true}
	lease := &scriptedRelaySessionLease{
		readResult: appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: thread},
			Handoff:  handoff,
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	source := &relaySessionTestSource{
		thread: thread,
		lease:  lease,
	}
	relays := newHubRelayFunctions(
		appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"}),
		hubcore.WebConfig{RelayHooks: hubcore.RelayLifecycleHooks{
			RegisterSubscription: func(context.Context, string, bool) bool {
				if !handoff.isPrepared() {
					t.Error("downstream subscription registered before the live continuation was prepared")
				}
				return true
			},
		}},
		appsource.NewRegistry(),
	)

	if _, err := relays.startTurn(context.Background(), source, appwire.TurnStartParams{
		Ref:              thread.Evener.Ref,
		ThreadID:         thread.ID,
		ClientMutationID: "mutation-turn-start",
		Input:            []appwire.InputItem{{Type: "text", Text: "continue"}},
	}); err != nil {
		t.Fatalf("startTurn: %v", err)
	}
	if source.legacyReadCallCount() != 0 || source.legacySubscribeCallCount() != 0 {
		t.Fatalf(
			"legacy LocalDaemon relay calls: read=%d subscribe=%d, want neither",
			source.legacyReadCallCount(),
			source.legacySubscribeCallCount(),
		)
	}
	if lease.readCallCount() != 1 || lease.listenCallCount() != 1 {
		t.Fatalf("RelaySession calls: Read=%d Listen=%d, want one canonical feed", lease.readCallCount(), lease.listenCallCount())
	}
	if prepareCalls, commitCalls, abortCalls := handoff.callCounts(); prepareCalls != 1 || commitCalls != 1 || abortCalls != 0 {
		t.Fatalf(
			"handoff calls: Prepare=%d Commit=%d Abort=%d, want Prepare=1 Commit=1 Abort=0",
			prepareCalls,
			commitCalls,
			abortCalls,
		)
	}
}

func TestHubTurnStartStopsBeforeMutationWhenRelayHandoffCannotCommit(t *testing.T) {
	for _, test := range []struct {
		name           string
		prepareAllowed bool
		commitAllowed  bool
		wantRegistered bool
	}{
		{name: "prepare fails", commitAllowed: true},
		{name: "commit fails", prepareAllowed: true, wantRegistered: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			thread := appwire.Thread{
				ID:     "thread-turn-start-failed-handoff",
				Source: "local",
				Evener: appwire.EvenerThread{Ref: "local:thread-turn-start-failed-handoff"},
			}
			handoff := &guardedRelayHandoff{
				prepareAllowed: test.prepareAllowed,
				commitAllowed:  test.commitAllowed,
			}
			lease := &scriptedRelaySessionLease{
				readResult: appsource.RelayReadResult{
					Response: appwire.ThreadReadResponse{Thread: thread},
					Handoff:  handoff,
				},
				deliveries: make(chan appsource.RelayDelivery),
			}
			source := &relaySessionTestSource{
				thread: thread,
				lease:  lease,
			}
			registered := false
			relays := newHubRelayFunctions(
				appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"}),
				hubcore.WebConfig{RelayHooks: hubcore.RelayLifecycleHooks{
					RegisterSubscription: func(context.Context, string, bool) bool {
						registered = true
						return true
					},
				}},
				appsource.NewRegistry(),
			)

			if _, err := relays.startTurn(context.Background(), source, appwire.TurnStartParams{
				Ref:              thread.Evener.Ref,
				ThreadID:         thread.ID,
				ClientMutationID: "mutation-failed-handoff",
				Input:            []appwire.InputItem{{Type: "text", Text: "must not run"}},
			}); err == nil {
				t.Fatal("turn/start succeeded without a committed live relay continuation")
			}
			if registered != test.wantRegistered {
				t.Fatalf("downstream subscription registered = %t, want %t", registered, test.wantRegistered)
			}
			if got := source.startTurnCallCount(); got != 0 {
				t.Fatalf("upstream turn/start calls = %d, want 0", got)
			}
			prepareCalls, commitCalls, abortCalls := handoff.callCounts()
			if prepareCalls != 1 {
				t.Fatalf("Prepare calls = %d, want 1", prepareCalls)
			}
			if test.prepareAllowed && commitCalls != 1 {
				t.Fatalf("Commit calls = %d, want 1", commitCalls)
			}
			wantAbortCalls := 1
			if test.prepareAllowed {
				wantAbortCalls = 0
			}
			if abortCalls != wantAbortCalls {
				t.Fatalf("Abort calls = %d, want %d after failed handoff", abortCalls, wantAbortCalls)
			}
		})
	}
}

func TestHubLifecycleRelaySetupUsesCanonicalRelaySession(t *testing.T) {
	thread := appwire.Thread{
		ID:     "thread-lifecycle",
		Source: "local",
		Evener: appwire.EvenerThread{Ref: "local:thread-lifecycle"},
	}
	handoff := &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true}
	lease := &scriptedRelaySessionLease{
		readResult: appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: thread},
			Handoff:  handoff,
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	source := &relaySessionTestSource{
		thread: thread,
		lease:  lease,
	}
	sources := appsource.NewRegistry()
	sources.Add(source)
	relays := newHubRelayFunctions(
		appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"}),
		hubcore.WebConfig{},
		sources,
	)

	if err := relays.startRelayForThread(context.Background(), thread); err != nil {
		t.Fatalf("startRelayForThread: %v", err)
	}
	if source.legacyReadCallCount() != 0 || source.legacySubscribeCallCount() != 0 {
		t.Fatalf(
			"legacy LocalDaemon relay calls: read=%d subscribe=%d, want neither",
			source.legacyReadCallCount(),
			source.legacySubscribeCallCount(),
		)
	}
	if lease.readCallCount() != 1 || lease.listenCallCount() != 1 {
		t.Fatalf("RelaySession calls: Read=%d Listen=%d, want one canonical feed", lease.readCallCount(), lease.listenCallCount())
	}
	if prepareCalls, commitCalls, abortCalls := handoff.callCounts(); prepareCalls != 1 || commitCalls != 1 || abortCalls != 0 {
		t.Fatalf(
			"handoff calls: Prepare=%d Commit=%d Abort=%d, want Prepare=1 Commit=1 Abort=0",
			prepareCalls,
			commitCalls,
			abortCalls,
		)
	}
}

func TestHubRelayIdleRetirementYieldsToConcurrentActorCommand(t *testing.T) {
	previousInterval := hubRelayIdleInterval
	hubRelayIdleInterval = time.Millisecond
	t.Cleanup(func() {
		hubRelayIdleInterval = previousInterval
	})

	thread := appwire.Thread{
		ID:     "thread-idle-command",
		Source: "local",
		Evener: appwire.EvenerThread{Ref: "local:thread-idle-command"},
	}
	handoff := &recordingRelayHandoff{committed: make(chan struct{}), aborted: make(chan struct{})}
	lease := &scriptedRelaySessionLease{
		readResult: appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: thread},
			Handoff:  handoff,
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	source := &relaySessionTestSource{
		thread: thread,
		lease:  lease,
	}
	idleEntered := make(chan struct{})
	releaseIdle := make(chan struct{})
	var idleOnce sync.Once
	relays := newHubRelayFunctions(
		appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"}),
		hubcore.WebConfig{RelayHooks: hubcore.RelayLifecycleHooks{
			IdleExit: func(string) {
				idleOnce.Do(func() {
					close(idleEntered)
					<-releaseIdle
				})
			},
		}},
		appsource.NewRegistry(),
	)
	params := appwire.ThreadReadParams{Ref: thread.Evener.Ref, Subscribe: true}
	first, err := relays.readThread(context.Background(), source, params)
	if err != nil {
		t.Fatalf("first readThread: %v", err)
	}
	first.finish(true)
	<-idleEntered

	second, err := relays.readThread(context.Background(), source, params)
	if err != nil {
		close(releaseIdle)
		t.Fatalf("second readThread: %v", err)
	}
	if got := lease.listenCallCount(); got != 1 {
		close(releaseIdle)
		t.Fatalf("RelaySession Listen calls = %d, want the in-flight command to retain the existing Hub owner", got)
	}
	close(releaseIdle)
	acknowledged := make(chan struct{})
	lease.deliveries <- appsource.RelayDelivery{
		Notification: appwire.Notification{
			Method: appwire.NotifyAgentMessageDelta,
			Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
				Ref: thread.Evener.Ref, ThreadID: thread.ID, TurnID: "turn-idle-command", ItemID: "item-idle-command", Delta: "live",
			}),
		},
		Acknowledge: func() { close(acknowledged) },
	}
	<-acknowledged // The same fanout goroutine has completed final idle revalidation.
	if got := lease.closeCallCount(); got != 0 {
		t.Fatalf("lease closes while command ownership is held across final revalidation = %d, want 0", got)
	}
	second.finish(true)
}

func TestHubRelayIdleRetirementYieldsToSubscriptionAtCaptureBoundary(t *testing.T) {
	previousInterval := hubRelayIdleInterval
	hubRelayIdleInterval = time.Millisecond
	t.Cleanup(func() { hubRelayIdleInterval = previousInterval })

	thread := appwire.Thread{
		ID:     "thread-idle-subscribe",
		Source: "local",
		Evener: appwire.EvenerThread{Ref: "local:thread-idle-subscribe"},
	}
	lease := &scriptedRelaySessionLease{
		readResult: appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: thread},
			Handoff:  &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	source := &relaySessionTestSource{thread: thread, lease: lease}
	sources := appsource.NewRegistry()
	sources.Add(source)
	idleEntered := make(chan struct{})
	releaseIdle := make(chan struct{})
	var idleOnce sync.Once
	appServer := newHubAppServer(hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
		RelayHooks: hubcore.RelayLifecycleHooks{
			IdleExit: func(string) {
				idleOnce.Do(func() {
					close(idleEntered)
					<-releaseIdle
				})
			},
		},
	}, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	defer hub.Close()

	initial := dialHubRPC(t, hub)
	if _, err := initial.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("initial Initialize: %v", err)
	}
	if _, err := initial.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: thread.Evener.Ref, Subscribe: true}); err != nil {
		t.Fatalf("initial ThreadRead: %v", err)
	}
	if _, err := initial.ThreadUnsubscribe(context.Background(), appwire.ThreadUnsubscribeParams{Ref: thread.Evener.Ref}); err != nil {
		t.Fatalf("initial ThreadUnsubscribe: %v", err)
	}
	<-idleEntered

	subscribeAtGate := make(chan struct{})
	releaseSubscription := make(chan struct{})
	var gateOnce sync.Once
	appServer.SetBeforeSubscriptionGate(func() {
		gateOnce.Do(func() {
			close(subscribeAtGate)
			<-releaseSubscription
		})
	})
	rejoin := dialHubRPC(t, hub)
	defer rejoin.Close()
	if _, err := rejoin.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("rejoin Initialize: %v", err)
	}
	rejoinResult := make(chan error, 1)
	go func() {
		_, err := rejoin.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: thread.Evener.Ref, Subscribe: true})
		rejoinResult <- err
	}()
	<-subscribeAtGate
	close(releaseIdle)
	close(releaseSubscription)
	if err := <-rejoinResult; err != nil {
		t.Fatalf("rejoin ThreadRead: %v", err)
	}
	if got := lease.listenCallCount(); got != 1 {
		t.Fatalf("RelaySession Listen calls = %d, want the boundary subscription to retain the existing listener", got)
	}
	if got := lease.closeCallCount(); got != 0 {
		t.Fatalf("lease closes = %d, want 0 after boundary subscription wins revalidation", got)
	}
	initial.Close()
}

func TestHubRelayBlockedActorDoesNotBlockUnrelatedThread(t *testing.T) {
	blockedThread := appwire.Thread{
		ID:     "thread-blocked",
		Source: "local-a",
		Evener: appwire.EvenerThread{Ref: "local-a:thread-blocked"},
	}
	fastThread := appwire.Thread{
		ID:     "thread-fast",
		Source: "local-b",
		Evener: appwire.EvenerThread{Ref: "local-b:thread-fast"},
	}
	blockedReadEntered := make(chan struct{})
	releaseBlockedRead := make(chan struct{})
	blockedLease := &scriptedRelaySessionLease{
		readResult: appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: blockedThread},
			Handoff:  &recordingRelayHandoff{committed: make(chan struct{}), aborted: make(chan struct{})},
		},
		readHook: func() {
			close(blockedReadEntered)
			<-releaseBlockedRead
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	fastLease := &scriptedRelaySessionLease{
		readResult: appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: fastThread},
			Handoff:  &recordingRelayHandoff{committed: make(chan struct{}), aborted: make(chan struct{})},
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	blockedSource := &relaySessionTestSource{
		thread: blockedThread,
		id:     "local-a",
		lease:  blockedLease,
	}
	fastSource := &relaySessionTestSource{
		thread: fastThread,
		id:     "local-b",
		lease:  fastLease,
	}
	relays := newHubRelayFunctions(
		appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"}),
		hubcore.WebConfig{},
		appsource.NewRegistry(),
	)

	blockedResult := make(chan error, 1)
	go func() {
		read, err := relays.readThread(context.Background(), blockedSource, appwire.ThreadReadParams{
			Ref:       blockedThread.Evener.Ref,
			Subscribe: true,
		})
		if err == nil {
			read.finish(true)
		}
		blockedResult <- err
	}()
	<-blockedReadEntered

	fastRead, err := relays.readThread(context.Background(), fastSource, appwire.ThreadReadParams{
		Ref:       fastThread.Evener.Ref,
		Subscribe: true,
	})
	if err != nil {
		close(releaseBlockedRead)
		t.Fatalf("unrelated readThread: %v", err)
	}
	fastRead.finish(true)
	close(releaseBlockedRead)
	if err := <-blockedResult; err != nil {
		t.Fatalf("blocked readThread: %v", err)
	}
}

func TestHubRelayStaleRelayKeyReleaseDoesNotAffectReplacement(t *testing.T) {
	previousInterval := hubRelayIdleInterval
	hubRelayIdleInterval = time.Millisecond
	t.Cleanup(func() { hubRelayIdleInterval = previousInterval })

	const relayKey = "local:remapped-key"
	canonicalRef := appwire.Ref{SourceID: "local", ThreadID: "canonical-old"}
	thread := appwire.Thread{
		ID:     "remapped-key",
		Source: "local",
		Evener: appwire.EvenerThread{Ref: relayKey},
	}
	newLease := func() *scriptedRelaySessionLease {
		return &scriptedRelaySessionLease{
			readResult: appsource.RelayReadResult{
				Response: appwire.ThreadReadResponse{Thread: thread},
				Handoff:  &recordingRelayHandoff{committed: make(chan struct{}), aborted: make(chan struct{})},
			},
			deliveries: make(chan appsource.RelayDelivery),
		}
	}
	oldLease := newLease()
	oldClosed := make(chan struct{})
	oldLease.closeHook = func() { close(oldClosed) }
	staleReadEntered := make(chan struct{})
	releaseStaleRead := make(chan struct{})
	oldLease.readHook = func() {
		close(staleReadEntered)
		<-releaseStaleRead
	}
	replacementLease := newLease()
	source := &relaySessionTestSource{
		thread: thread,
		resolveRelay: func(appwire.ThreadReadParams) (appwire.Ref, error) {
			return canonicalRef, nil
		},
		acquireRelay: func(ref appwire.Ref) (appsource.RelaySessionRoutePublicationLease, error) {
			if ref.ThreadID == "canonical-old" {
				return routeAwareTestLease(oldLease), nil
			}
			return routeAwareTestLease(replacementLease), nil
		},
	}
	relays := newHubRelayFunctions(
		appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"}),
		hubcore.WebConfig{},
		appsource.NewRegistry(),
	)
	params := appwire.ThreadReadParams{Ref: relayKey, Subscribe: true}

	type readResult struct {
		read *hubThreadReadResult
		err  error
	}
	staleResult := make(chan readResult, 1)
	go func() {
		read, err := relays.readThread(context.Background(), source, params)
		staleResult <- readResult{read: read, err: err}
	}()
	<-staleReadEntered
	canonicalRef = appwire.Ref{SourceID: "local", ThreadID: "canonical-replacement"}
	replacementRead, err := relays.readThread(context.Background(), source, params)
	if err != nil {
		close(releaseStaleRead)
		t.Fatalf("replacement readThread: %v", err)
	}
	if got := source.acquireCallCount(); got != 2 {
		replacementRead.finish(false)
		close(releaseStaleRead)
		t.Fatalf("relay acquisitions = %d, want replacement canonical handle", got)
	}
	oldDeliveryAcknowledged := make(chan struct{})
	oldDeliveryAccepted := make(chan struct{})
	go func() {
		oldLease.deliveries <- appsource.RelayDelivery{
			Notification: appwire.Notification{Method: appwire.NotifyThreadStatusChanged},
			Acknowledge:  func() { close(oldDeliveryAcknowledged) },
		}
		close(oldDeliveryAccepted)
	}()
	<-oldDeliveryAccepted
	<-oldDeliveryAcknowledged
	if got := oldLease.closeCallCount(); got != 0 {
		replacementRead.finish(false)
		close(releaseStaleRead)
		t.Fatalf("displaced lease closes while its stale command remains in flight = %d, want 0", got)
	}

	close(releaseStaleRead)
	stale := <-staleResult
	if stale.err != nil {
		replacementRead.finish(false)
		t.Fatalf("stale readThread: %v", stale.err)
	}
	stale.read.finish(false)
	if got := relays.relayCommandCount(relayKey); got != 1 {
		replacementRead.finish(false)
		t.Fatalf("replacement command owners after stale release = %d, want 1", got)
	}
	select {
	case <-oldClosed:
	case <-time.After(time.Second):
		replacementRead.finish(false)
		t.Fatal("displaced canonical handle did not close after its stale command released")
	}
	replacementRead.finish(false)
}

func TestHubRelayRemapFencesOldFanoutBeforeBroadcast(t *testing.T) {
	const (
		downstreamRef = "local:publication-fence-downstream"
		oldTargetRef  = "local:publication-fence-old"
		newTargetRef  = "local:publication-fence-new"
	)
	canonicalOld := appwire.Ref{SourceID: "local", ThreadID: "publication-fence-canonical-old"}
	canonicalNew := appwire.Ref{SourceID: "local", ThreadID: "publication-fence-canonical-new"}
	newLease := func(targetRef string) *scriptedRelaySessionLease {
		return &scriptedRelaySessionLease{
			readResult: appsource.RelayReadResult{
				Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
					ID: "publication-fence-downstream", Source: "local",
					Evener: appwire.EvenerThread{Ref: targetRef},
				}},
				Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
			},
			deliveries: make(chan appsource.RelayDelivery),
		}
	}
	oldLease := newLease(oldTargetRef)
	replacementLease := newLease(newTargetRef)
	var resolveMu sync.Mutex
	resolved := canonicalOld
	source := &relaySessionTestSource{
		resolveRelay: func(appwire.ThreadReadParams) (appwire.Ref, error) {
			resolveMu.Lock()
			defer resolveMu.Unlock()
			return resolved, nil
		},
		acquireRelay: func(ref appwire.Ref) (appsource.RelaySessionRoutePublicationLease, error) {
			if ref == canonicalOld {
				return routeAwareTestLease(oldLease), nil
			}
			return routeAwareTestLease(replacementLease), nil
		},
	}
	oldFanoutLookedUp := make(chan struct{})
	releaseOldFanout := make(chan struct{})
	var parkOnce sync.Once
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
	cfg.RelayHooks.BeforeCanonicalPublish = func(_ string, notification appwire.Notification) {
		var params appwire.AgentMessageDeltaParams
		if json.Unmarshal(notification.Params, &params) != nil || params.Delta != "stale old publication" {
			return
		}
		parkOnce.Do(func() {
			close(oldFanoutLookedUp)
			<-releaseOldFanout
		})
	}
	sources := appsource.NewRegistry()
	sources.Add(source)
	appServer := newHubAppServer(cfg, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: downstreamRef, Subscribe: true}); err != nil {
		t.Fatalf("initial ThreadRead: %v", err)
	}

	oldAcknowledged := make(chan struct{})
	go func() {
		oldLease.deliveries <- appsource.RelayDelivery{
			Notification: appwire.Notification{
				Method: appwire.NotifyAgentMessageDelta,
				Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
					Ref: oldTargetRef, ThreadID: "publication-fence-old", Delta: "stale old publication",
				}),
			},
			Acknowledge: func() { close(oldAcknowledged) },
		}
	}()
	<-oldFanoutLookedUp

	resolveMu.Lock()
	resolved = canonicalNew
	resolveMu.Unlock()
	replacementResult := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{
			Ref: downstreamRef, Subscribe: true, ReplaceSubscription: true,
		})
		replacementResult <- err
	}()
	select {
	case err := <-replacementResult:
		if err != nil {
			close(releaseOldFanout)
			t.Fatalf("replacement ThreadRead: %v", err)
		}
	case <-time.After(time.Second):
		close(releaseOldFanout)
		t.Fatal("replacement ownership did not publish while old fanout was parked before publication entry")
	}
	close(releaseOldFanout)
	<-oldAcknowledged

	liveAcknowledged := make(chan struct{})
	replacementLease.deliveries <- appsource.RelayDelivery{
		Notification: appwire.Notification{
			Method: appwire.NotifyAgentMessageDelta,
			Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
				Ref: newTargetRef, ThreadID: "publication-fence-new", Delta: "live replacement publication",
			}),
		},
		Acknowledge: func() { close(liveAcknowledged) },
	}
	select {
	case notification := <-client.Notifications():
		var params appwire.AgentMessageDeltaParams
		if err := json.Unmarshal(notification.Params, &params); err != nil {
			t.Fatal(err)
		}
		if params.Delta != "live replacement publication" {
			t.Fatalf("first post-remap publication = %q, want replacement frame; displaced listener broadcast stale ownership", params.Delta)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement publication did not reach the downstream subscriber")
	}
	<-liveAcknowledged
}

func TestHubRelayCanceledRemapStopsWaitingForPublicationDrain(t *testing.T) {
	const (
		relayKey  = "local:publication-drain-cancel"
		oldTarget = "local:publication-drain-cancel-old"
		newTarget = "local:publication-drain-cancel-new"
	)
	canonicalOld := appwire.Ref{SourceID: "local", ThreadID: "publication-drain-owner-old"}
	canonicalNew := appwire.Ref{SourceID: "local", ThreadID: "publication-drain-owner-new"}
	resultFor := func(target string) appsource.RelayReadResult {
		return appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID: "publication-drain-cancel", Source: "local", Evener: appwire.EvenerThread{Ref: target},
			}},
			Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
		}
	}
	oldLease := &scriptedRelaySessionLease{readResult: resultFor(oldTarget), deliveries: make(chan appsource.RelayDelivery)}
	newLease := &scriptedRelaySessionLease{readResult: resultFor(newTarget), deliveries: make(chan appsource.RelayDelivery)}
	var resolveMu sync.Mutex
	resolved := canonicalOld
	source := &relaySessionTestSource{
		resolveRelay: func(appwire.ThreadReadParams) (appwire.Ref, error) {
			resolveMu.Lock()
			defer resolveMu.Unlock()
			return resolved, nil
		},
		acquireRelay: func(ref appwire.Ref) (appsource.RelaySessionRoutePublicationLease, error) {
			if ref == canonicalOld {
				return routeAwareTestLease(oldLease), nil
			}
			return routeAwareTestLease(newLease), nil
		},
	}
	publicationEntered := make(chan struct{})
	releasePublication := make(chan struct{})
	var entryOnce sync.Once
	cfg := hubcore.WebConfig{}
	cfg.RelayHooks.AfterCanonicalPublishEntry = func(_ string, notification appwire.Notification) {
		var params appwire.AgentMessageDeltaParams
		if json.Unmarshal(notification.Params, &params) != nil || params.Delta != "hold old publication" {
			return
		}
		entryOnce.Do(func() {
			close(publicationEntered)
			<-releasePublication
		})
	}
	relays := newHubRelayFunctions(
		appserver.NewServer(appserver.ServerConfig{ServerName: "publication-drain-cancel", SourceID: "local"}),
		cfg, appsource.NewRegistry(),
	)
	initial, err := relays.readThread(t.Context(), source, appwire.ThreadReadParams{Ref: relayKey, Subscribe: true})
	if err != nil {
		t.Fatal(err)
	}
	initial.finish(false)
	oldAcknowledged := make(chan struct{})
	go func() {
		oldLease.deliveries <- appsource.RelayDelivery{
			Notification: appwire.Notification{
				Method: appwire.NotifyAgentMessageDelta,
				Params: testRawJSON(t, appwire.AgentMessageDeltaParams{Ref: oldTarget, Delta: "hold old publication"}),
			},
			Acknowledge: func() { close(oldAcknowledged) },
		}
	}()
	<-publicationEntered
	resolveMu.Lock()
	resolved = canonicalNew
	resolveMu.Unlock()
	drainWait := make(chan struct{})
	var drainOnce sync.Once
	previousWait := observeHubRelayWait
	observeHubRelayWait = func() { drainOnce.Do(func() { close(drainWait) }) }
	t.Cleanup(func() { observeHubRelayWait = previousWait })
	readCtx, cancelRead := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		read, err := relays.readThread(readCtx, source, appwire.ThreadReadParams{Ref: relayKey, Subscribe: true})
		if read != nil {
			read.finish(false)
		}
		result <- err
	}()
	<-drainWait
	cancelRead()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			close(releasePublication)
			t.Fatalf("canceled remap error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		close(releasePublication)
		<-result
		t.Fatal("canceled remap remained blocked on displaced publication drain")
	}
	if got := newLease.closeCallCount(); got != 1 {
		close(releasePublication)
		t.Fatalf("canceled unpublished replacement lease closes = %d, want 1", got)
	}
	close(releasePublication)
	<-oldAcknowledged
	if !relays.relayPublished(relayKey) {
		t.Fatal("canceled replacement retired the still-current displaced generation")
	}
	if got := relays.relayCommandCount(relayKey); got != 0 {
		t.Fatalf("commands after canceled publication drain = %d, want 0", got)
	}
}

func TestHubRelayUnknownTargetDoesNotHeadOfLineBlockKnownDelivery(t *testing.T) {
	const relayKey = "local:review-hol"
	thread := appwire.Thread{ID: "review-hol", Source: "local", Evener: appwire.EvenerThread{Ref: relayKey}}
	secondEntered := make(chan struct{})
	releaseSecond := make(chan struct{})
	var readsMu sync.Mutex
	reads := 0
	deliveries := make(chan appsource.RelayDelivery, 2)
	lease := &scriptedRelaySessionLease{
		readFunc: func(appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
			readsMu.Lock()
			reads++
			call := reads
			readsMu.Unlock()
			if call == 2 {
				close(secondEntered)
				<-releaseSecond
			}
			return appsource.RelayReadResult{
				Response: appwire.ThreadReadResponse{Thread: thread},
				Handoff:  &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
			}, nil
		},
		deliveries: deliveries,
	}
	source := &relaySessionTestSource{lease: lease}
	relays := newHubRelayFunctions(
		appserver.NewServer(appserver.ServerConfig{ServerName: "review", SourceID: "local"}),
		hubcore.WebConfig{},
		appsource.NewRegistry(),
	)
	first, err := relays.readThread(t.Context(), source, appwire.ThreadReadParams{Ref: relayKey, Subscribe: true})
	if err != nil {
		t.Fatal(err)
	}
	first.finish(false)
	type outcome struct {
		read *hubThreadReadResult
		err  error
	}
	secondResult := make(chan outcome, 1)
	go func() {
		read, err := relays.readThread(t.Context(), source, appwire.ThreadReadParams{Ref: relayKey, Subscribe: true})
		secondResult <- outcome{read: read, err: err}
	}()
	<-secondEntered
	waitEntered := make(chan struct{})
	var waitOnce sync.Once
	previous := observeHubRelayWait
	observeHubRelayWait = func() { waitOnce.Do(func() { close(waitEntered) }) }
	t.Cleanup(func() { observeHubRelayWait = previous })
	unknownAck := make(chan struct{})
	deliveries <- appsource.RelayDelivery{
		Notification: appwire.Notification{Method: appwire.NotifyAgentMessageDelta, Params: []byte(`{"ref":"local:foreign"}`)},
		Acknowledge:  func() { close(unknownAck) },
	}
	<-waitEntered
	knownAck := make(chan struct{})
	deliveries <- appsource.RelayDelivery{
		Notification: appwire.Notification{Method: appwire.NotifyAgentMessageDelta, Params: []byte(`{"ref":"local:review-hol"}`)},
		Acknowledge:  func() { close(knownAck) },
	}
	select {
	case <-knownAck:
	case <-time.After(time.Second):
		close(releaseSecond)
		t.Fatal("known delivery remained behind unrelated pending target")
	}
	select {
	case <-unknownAck:
		close(releaseSecond)
		t.Fatal("unresolved target was acknowledged before the pending Read published routes")
	default:
	}
	close(releaseSecond)
	second := <-secondResult
	if second.err != nil {
		t.Fatal(second.err)
	}
	second.read.finish(false)
	<-unknownAck
}

func TestHubRelayPendingTargetPreservesOrderWhileKnownTargetProgresses(t *testing.T) {
	const (
		rootRef      = "local:pending-order-root"
		childRef     = "local:pending-order-child"
		childTarget  = "local:pending-order-authoritative-child"
		canonicalRef = "local:pending-order-canonical"
	)
	childEntered := make(chan struct{})
	releaseChild := make(chan struct{})
	lease := &scriptedRelaySessionLease{
		readFunc: func(params appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
			threadRef := rootRef
			if params.Ref == childRef {
				close(childEntered)
				<-releaseChild
				threadRef = childTarget
			}
			parsed, err := appwire.ParseRef(params.Ref)
			if err != nil {
				return appsource.RelayReadResult{}, err
			}
			return appsource.RelayReadResult{
				Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
					ID: parsed.ThreadID, Source: parsed.SourceID, Evener: appwire.EvenerThread{Ref: threadRef},
				}},
				Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
			}, nil
		},
		deliveries: make(chan appsource.RelayDelivery, 3),
	}
	canonical, err := appwire.ParseRef(canonicalRef)
	if err != nil {
		t.Fatal(err)
	}
	source := &relaySessionTestSource{
		lease:        lease,
		resolveRelay: func(appwire.ThreadReadParams) (appwire.Ref, error) { return canonical, nil },
	}
	relays := newHubRelayFunctions(
		appserver.NewServer(appserver.ServerConfig{ServerName: "pending-order", SourceID: "local"}),
		hubcore.WebConfig{},
		appsource.NewRegistry(),
	)
	root, err := relays.readThread(t.Context(), source, appwire.ThreadReadParams{Ref: rootRef, Subscribe: true})
	if err != nil {
		t.Fatal(err)
	}
	root.finish(false)
	type outcome struct {
		read *hubThreadReadResult
		err  error
	}
	childResult := make(chan outcome, 1)
	go func() {
		read, err := relays.readThread(t.Context(), source, appwire.ThreadReadParams{Ref: childRef, Subscribe: true})
		childResult <- outcome{read: read, err: err}
	}()
	<-childEntered

	ackOrder := make(chan string, 2)
	for _, delta := range []string{"first", "second"} {
		delta := delta
		lease.deliveries <- appsource.RelayDelivery{
			Notification: appwire.Notification{
				Method: appwire.NotifyAgentMessageDelta,
				Params: testRawJSON(t, appwire.AgentMessageDeltaParams{Ref: childTarget, Delta: delta}),
			},
			Acknowledge: func() { ackOrder <- delta },
		}
	}
	knownAck := make(chan struct{})
	lease.deliveries <- appsource.RelayDelivery{
		Notification: appwire.Notification{
			Method: appwire.NotifyAgentMessageDelta,
			Params: testRawJSON(t, appwire.AgentMessageDeltaParams{Ref: rootRef, Delta: "known"}),
		},
		Acknowledge: func() { close(knownAck) },
	}
	select {
	case <-knownAck:
	case <-time.After(time.Second):
		close(releaseChild)
		t.Fatal("known sibling target did not progress around pending target queue")
	}
	select {
	case got := <-ackOrder:
		close(releaseChild)
		t.Fatalf("pending target delivery %q acknowledged before route publication", got)
	default:
	}
	close(releaseChild)
	child := <-childResult
	if child.err != nil {
		t.Fatal(child.err)
	}
	child.read.finish(false)
	if got := <-ackOrder; got != "first" {
		t.Fatalf("first pending acknowledgement = %q, want first", got)
	}
	if got := <-ackOrder; got != "second" {
		t.Fatalf("second pending acknowledgement = %q, want second", got)
	}
}

func TestHubRelayPendingDeliveryLimitCleansUpOnCanonicalStop(t *testing.T) {
	const relayKey = "local:pending-limit"
	thread := appwire.Thread{ID: "pending-limit", Source: "local", Evener: appwire.EvenerThread{Ref: relayKey}}
	secondEntered := make(chan struct{})
	releaseSecond := make(chan struct{})
	var reads int
	var readsMu sync.Mutex
	lease := &scriptedRelaySessionLease{
		readFunc: func(appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
			readsMu.Lock()
			reads++
			call := reads
			readsMu.Unlock()
			if call == 2 {
				close(secondEntered)
				<-releaseSecond
			}
			return appsource.RelayReadResult{
				Response: appwire.ThreadReadResponse{Thread: thread},
				Handoff:  &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
			}, nil
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	source := &relaySessionTestSource{lease: lease}
	relays := newHubRelayFunctions(
		appserver.NewServer(appserver.ServerConfig{ServerName: "pending-limit", SourceID: "local"}),
		hubcore.WebConfig{},
		appsource.NewRegistry(),
	)
	first, err := relays.readThread(t.Context(), source, appwire.ThreadReadParams{Ref: relayKey, Subscribe: true})
	if err != nil {
		t.Fatal(err)
	}
	first.finish(false)
	type outcome struct {
		read *hubThreadReadResult
		err  error
	}
	secondResult := make(chan outcome, 1)
	go func() {
		read, err := relays.readThread(t.Context(), source, appwire.ThreadReadParams{Ref: relayKey, Subscribe: true})
		secondResult <- outcome{read: read, err: err}
	}()
	<-secondEntered
	acknowledged := make(chan int, hubRelayPendingDeliveryLimit)
	for i := 0; i < hubRelayPendingDeliveryLimit; i++ {
		proceeded := make(chan struct{})
		delivery := appsource.RelayDelivery{
			Notification: appwire.Notification{
				Method: appwire.NotifyAgentMessageDelta,
				Params: testRawJSON(t, map[string]any{"ref": fmt.Sprintf("local:pending-foreign-%d", i)}),
			},
			Acknowledge: func() { acknowledged <- i },
			Proceed:     func() { close(proceeded) },
		}
		go func() { lease.deliveries <- delivery }()
		select {
		case <-proceeded:
		case <-time.After(time.Second):
			close(releaseSecond)
			t.Fatalf("pending delivery %d was not transferred into the bounded queue", i)
		}
	}
	select {
	case got := <-acknowledged:
		close(releaseSecond)
		t.Fatalf("pending delivery %d acknowledged before route resolution", got)
	default:
	}
	ref, err := appwire.ParseRef(relayKey)
	if err != nil {
		t.Fatal(err)
	}
	relays.stopCanonicalRelay(ref)
	if got := lease.closeCallCount(); got != 0 {
		close(releaseSecond)
		t.Fatalf("canonical stop closed lease before pending command release: %d", got)
	}
	close(releaseSecond)
	second := <-secondResult
	if second.err != nil {
		t.Fatal(second.err)
	}
	second.read.finish(false)
	seen := make(map[int]struct{}, hubRelayPendingDeliveryLimit)
	for len(seen) < hubRelayPendingDeliveryLimit {
		select {
		case got := <-acknowledged:
			seen[got] = struct{}{}
		case <-time.After(time.Second):
			t.Fatalf("canonical cancellation acknowledged %d/%d bounded pending deliveries", len(seen), hubRelayPendingDeliveryLimit)
		}
	}
	if got := lease.closeCallCount(); got != 1 {
		t.Fatalf("lease closes after bounded pending cleanup = %d, want 1", got)
	}
}

func TestHubRelayRemapRetainsAuthoritativeRouteDuringReplacementRead(t *testing.T) {
	const (
		downstreamRef       = "local:remap-read-downstream"
		oldAuthoritativeRef = "local:remap-read-authoritative-old"
		newAuthoritativeRef = "local:remap-read-authoritative-new"
	)
	canonicalOld := appwire.Ref{SourceID: "local", ThreadID: "remap-read-old"}
	canonicalNew := appwire.Ref{SourceID: "local", ThreadID: "remap-read-new"}
	newLease := func(authoritativeRef string) *scriptedRelaySessionLease {
		return &scriptedRelaySessionLease{
			readResult: appsource.RelayReadResult{
				Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
					ID: "remap-read-downstream", Source: "local",
					Evener: appwire.EvenerThread{Ref: authoritativeRef},
				}},
				Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
			},
			deliveries: make(chan appsource.RelayDelivery),
		}
	}
	oldLease := newLease(oldAuthoritativeRef)
	replacementLease := newLease(newAuthoritativeRef)
	replacementReadEntered := make(chan struct{})
	releaseReplacementRead := make(chan struct{})
	replacementLease.readHook = func() {
		close(replacementReadEntered)
		<-releaseReplacementRead
	}
	var resolveMu sync.Mutex
	resolved := canonicalOld
	source := &relaySessionTestSource{
		resolveRelay: func(appwire.ThreadReadParams) (appwire.Ref, error) {
			resolveMu.Lock()
			defer resolveMu.Unlock()
			return resolved, nil
		},
		acquireRelay: func(ref appwire.Ref) (appsource.RelaySessionRoutePublicationLease, error) {
			if ref == canonicalOld {
				return routeAwareTestLease(oldLease), nil
			}
			return routeAwareTestLease(replacementLease), nil
		},
	}
	sources := appsource.NewRegistry()
	sources.Add(source)
	appServer := newHubAppServer(hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: downstreamRef, Subscribe: true}); err != nil {
		t.Fatalf("initial ThreadRead: %v", err)
	}
	resolveMu.Lock()
	resolved = canonicalNew
	resolveMu.Unlock()
	replacementResult := make(chan error, 1)
	deliveryPending := make(chan struct{})
	previousObserveWait := observeHubRelayWait
	observeHubRelayWait = func() { close(deliveryPending) }
	t.Cleanup(func() { observeHubRelayWait = previousObserveWait })
	go func() {
		_, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
			Ref: downstreamRef, Subscribe: true, ReplaceSubscription: true,
		})
		replacementResult <- err
	}()
	<-replacementReadEntered
	deliveryAccepted := make(chan struct{})
	acknowledged := make(chan struct{})
	go func() {
		replacementLease.deliveries <- appsource.RelayDelivery{
			Notification: appwire.Notification{
				Method: appwire.NotifyAgentMessageDelta,
				Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
					Ref: newAuthoritativeRef, ThreadID: "remap-read-authoritative-new", TurnID: "turn-remap-read", ItemID: "item-remap-read", Delta: "during read",
				}),
			},
			Acknowledge: func() { close(acknowledged) },
		}
		close(deliveryAccepted)
	}()
	<-deliveryAccepted
	select {
	case <-deliveryPending:
	case <-time.After(time.Second):
		close(releaseReplacementRead)
		t.Fatal("changed-target notification was not held for the pending replacement generation")
	}
	select {
	case <-acknowledged:
		close(releaseReplacementRead)
		t.Fatal("changed-target notification was acknowledged before replacement routes published")
	default:
	}
	close(releaseReplacementRead)
	if err := <-replacementResult; err != nil {
		t.Fatalf("replacement ThreadRead: %v", err)
	}
	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyAgentMessageDelta {
			t.Fatalf("notification during replacement read method = %q, want %q", got.Method, appwire.NotifyAgentMessageDelta)
		}
	case <-time.After(time.Second):
		t.Fatal("changed-target notification was lost after replacement routes published")
	}
	<-acknowledged
	if got := replacementLease.listenCallCount(); got != 1 {
		t.Fatalf("replacement Listen calls = %d, want 1", got)
	}
}

func TestHubRelayRemapDoesNotStealSiblingRouteCollision(t *testing.T) {
	const (
		siblingRef     = "local:collision-sibling"
		remapRef       = "local:collision-remap"
		collisionRef   = "local:collision-authoritative"
		replacementRef = "local:collision-replacement"
	)
	canonicalA := appwire.Ref{SourceID: "local", ThreadID: "collision-canonical-a"}
	canonicalB := appwire.Ref{SourceID: "local", ThreadID: "collision-canonical-b"}
	resultFor := func(downstreamRef, authoritativeRef string) appsource.RelayReadResult {
		ref, err := appwire.ParseRef(downstreamRef)
		if err != nil {
			t.Fatal(err)
		}
		return appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID: ref.ThreadID, Source: ref.SourceID,
				Evener: appwire.EvenerThread{Ref: authoritativeRef},
			}},
			Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
		}
	}
	leaseA := &scriptedRelaySessionLease{
		readFunc: func(appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
			return resultFor(remapRef, collisionRef), nil
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	leaseB := &scriptedRelaySessionLease{
		readFunc: func(params appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
			if params.Ref == siblingRef {
				return resultFor(siblingRef, collisionRef), nil
			}
			return resultFor(remapRef, replacementRef), nil
		},
		deliveries: make(chan appsource.RelayDelivery),
	}
	var resolveMu sync.Mutex
	remapCanonical := canonicalA
	source := &relaySessionTestSource{
		resolveRelay: func(params appwire.ThreadReadParams) (appwire.Ref, error) {
			if params.Ref == siblingRef {
				return canonicalB, nil
			}
			resolveMu.Lock()
			defer resolveMu.Unlock()
			return remapCanonical, nil
		},
		acquireRelay: func(ref appwire.Ref) (appsource.RelaySessionRoutePublicationLease, error) {
			if ref == canonicalA {
				return routeAwareTestLease(leaseA), nil
			}
			return routeAwareTestLease(leaseB), nil
		},
	}
	sources := appsource.NewRegistry()
	sources.Add(source)
	appServer := newHubAppServer(hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	defer hub.Close()
	sibling := dialHubRPC(t, hub)
	defer sibling.Close()
	remap := dialHubRPC(t, hub)
	defer remap.Close()
	for _, client := range []*appwire.Client{sibling, remap} {
		if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
	}
	if _, err := sibling.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: siblingRef, Subscribe: true}); err != nil {
		t.Fatalf("sibling ThreadRead: %v", err)
	}
	if _, err := remap.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: remapRef, Subscribe: true}); err != nil {
		t.Fatalf("remap initial ThreadRead: %v", err)
	}

	replacementReadEntered := make(chan struct{})
	releaseReplacementRead := make(chan struct{})
	var releaseOnce sync.Once
	releaseReplacement := func() { releaseOnce.Do(func() { close(releaseReplacementRead) }) }
	defer releaseReplacement()
	leaseB.mu.Lock()
	leaseB.readHook = func() {
		close(replacementReadEntered)
		<-releaseReplacementRead
	}
	leaseB.mu.Unlock()
	resolveMu.Lock()
	remapCanonical = canonicalB
	resolveMu.Unlock()
	replacementResult := make(chan error, 1)
	go func() {
		_, err := remap.ThreadRead(context.Background(), appwire.ThreadReadParams{
			Ref: remapRef, Subscribe: true, ReplaceSubscription: true,
		})
		replacementResult <- err
	}()
	<-replacementReadEntered

	deliver := func(delta string) {
		t.Helper()
		acknowledged := make(chan struct{})
		leaseB.deliveries <- appsource.RelayDelivery{
			Notification: appwire.Notification{
				Method: appwire.NotifyAgentMessageDelta,
				Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
					Ref: collisionRef, ThreadID: "collision-authoritative", TurnID: "turn-collision", ItemID: "item-collision", Delta: delta,
				}),
			},
			Acknowledge: func() { close(acknowledged) },
		}
		select {
		case <-sibling.Notifications():
		case got := <-remap.Notifications():
			t.Fatalf("colliding inherited route was stolen from sibling: %+v", got)
		case <-time.After(time.Second):
			t.Fatal("colliding authoritative route no longer reached its sibling owner")
		}
		<-acknowledged
	}
	deliver("during replacement read")
	releaseReplacement()
	if err := <-replacementResult; err != nil {
		t.Fatalf("replacement ThreadRead: %v", err)
	}
	deliver("after replacement cleanup")
}

func TestHubRelayRemapMovesDownstreamAndTargetRouteOwnership(t *testing.T) {
	previousInterval := hubRelayIdleInterval
	hubRelayIdleInterval = time.Millisecond
	t.Cleanup(func() { hubRelayIdleInterval = previousInterval })

	const (
		downstreamRef = "local:remap-downstream"
		oldTargetRef  = "local:authoritative-old"
		newTargetRef  = "local:authoritative-new"
	)
	canonicalOld := appwire.Ref{SourceID: "local", ThreadID: "canonical-old"}
	canonicalNew := appwire.Ref{SourceID: "local", ThreadID: "canonical-new"}
	newLease := func(targetRef string) *scriptedRelaySessionLease {
		return &scriptedRelaySessionLease{
			readFunc: func(appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
				return appsource.RelayReadResult{
					Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
						ID: "remap-downstream", Source: "local",
						Evener: appwire.EvenerThread{Ref: targetRef},
					}},
					Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true},
				}, nil
			},
			deliveries: make(chan appsource.RelayDelivery),
		}
	}
	oldLease := newLease(oldTargetRef)
	oldClosed := make(chan struct{})
	oldLease.closeHook = func() { close(oldClosed) }
	newLeaseValue := newLease(newTargetRef)
	var resolveMu sync.Mutex
	resolved := canonicalOld
	source := &relaySessionTestSource{
		resolveRelay: func(appwire.ThreadReadParams) (appwire.Ref, error) {
			resolveMu.Lock()
			defer resolveMu.Unlock()
			return resolved, nil
		},
		acquireRelay: func(ref appwire.Ref) (appsource.RelaySessionRoutePublicationLease, error) {
			if ref == canonicalOld {
				return routeAwareTestLease(oldLease), nil
			}
			return routeAwareTestLease(newLeaseValue), nil
		},
	}
	sources := appsource.NewRegistry()
	sources.Add(source)
	appServer := newHubAppServer(hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
	}, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: downstreamRef, Subscribe: true}); err != nil {
		t.Fatalf("old ThreadRead: %v", err)
	}
	resolveMu.Lock()
	resolved = canonicalNew
	resolveMu.Unlock()
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref: downstreamRef, Subscribe: true, ReplaceSubscription: true,
	}); err != nil {
		t.Fatalf("replacement ThreadRead: %v", err)
	}

	staleAck := make(chan struct{})
	newLeaseValue.deliveries <- appsource.RelayDelivery{
		Notification: appwire.Notification{
			Method: appwire.NotifyAgentMessageDelta,
			Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
				Ref: oldTargetRef, ThreadID: "authoritative-old", TurnID: "turn-old", ItemID: "item-old", Delta: "stale",
			}),
		},
		Acknowledge: func() { close(staleAck) },
	}
	<-staleAck
	select {
	case got := <-client.Notifications():
		t.Fatalf("stale remap route delivered notification %+v", got)
	default:
	}

	liveAck := make(chan struct{})
	newLeaseValue.deliveries <- appsource.RelayDelivery{
		Notification: appwire.Notification{
			Method: appwire.NotifyAgentMessageDelta,
			Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
				Ref: newTargetRef, ThreadID: "authoritative-new", TurnID: "turn-new", ItemID: "item-new", Delta: "live",
			}),
		},
		Acknowledge: func() { close(liveAck) },
	}
	if got := <-client.Notifications(); got.Method != appwire.NotifyAgentMessageDelta {
		t.Fatalf("replacement route method = %q, want %q", got.Method, appwire.NotifyAgentMessageDelta)
	}
	<-liveAck
	select {
	case <-oldClosed:
	case <-time.After(time.Second):
		t.Fatal("remapped canonical handle did not retire after its stale command released")
	}
	if got := oldLease.closeCallCount(); got != 1 {
		t.Fatalf("old remapped lease closes = %d, want 1", got)
	}
	if got := newLeaseValue.closeCallCount(); got != 0 {
		t.Fatalf("replacement lease closes = %d, want 0", got)
	}
}

func TestHubAtomicRelayReadLetsDeletionWinAndAbortsHandoff(t *testing.T) {
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const threadID = "02wMz5Txv1C3Hut0M8GCeB"
	ref := localAppRef(threadID)
	handoff := &recordingRelayHandoff{committed: make(chan struct{}), aborted: make(chan struct{})}
	resumeLocks := hubcore.NewResumeLocks()
	lease := &scriptedRelaySessionLease{
		readResult: appsource.RelayReadResult{
			Response: appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID:        threadID,
				SessionID: threadID,
				Source:    "local",
				Evener:    appwire.EvenerThread{Ref: ref},
			}},
			Handoff: handoff,
		},
		deliveries: make(chan appsource.RelayDelivery),
		readHook: func() {
			targetLock := resumeLocks.For(threadID)
			if !targetLock.TryLock() {
				t.Fatal("atomic relay held deletion ownership across upstream read")
			}
			defer targetLock.Unlock()
			if _, err := store.Begin("project-atomic-read-0123456789", []hubcore.DeletionTarget{{
				Ref:      ref,
				ThreadID: threadID,
			}}); err != nil {
				t.Fatal(err)
			}
		},
	}
	source := &relaySessionTestSource{
		thread: lease.readResult.Response.Thread,
		lease:  lease,
	}
	relays := newHubRelayFunctions(
		appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"}),
		hubcore.WebConfig{DeletionStore: store, ResumeLocks: resumeLocks},
		appsource.NewRegistry(),
	)

	if _, err := relays.readThread(context.Background(), source, appwire.ThreadReadParams{
		Ref:       ref,
		Subscribe: true,
	}); !isTargetDeletedError(err) {
		t.Fatalf("atomic read error = %T %v, want targetDeleted", err, err)
	}
	// Abort also fires after the error response is enqueued - same bounded
	// wait as the commit checks, for the same scheduling-race reason.
	select {
	case <-handoff.aborted:
	case <-time.After(time.Second):
		t.Fatal("deletion-winning atomic read did not abort its live handoff")
	}
	select {
	case <-handoff.committed:
		t.Fatal("deletion-winning atomic read committed its handoff")
	default:
	}
}

func TestHubAtomicRejoinExcludesDeletionAtHandoffPrepareBoundary(t *testing.T) {
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const threadID = "02wMz5Txv1C3Hut0M8GCeB"
	ref := localAppRef(threadID)
	resumeLocks := hubcore.NewResumeLocks()
	deletionCommitted := false
	var deletionErr error
	handoff := &guardedRelayHandoff{
		prepareAllowed: true,
		commitAllowed:  true,
		onPrepare: func() {
			targetLock := resumeLocks.For(threadID)
			if !targetLock.TryLock() {
				return
			}
			defer targetLock.Unlock()
			_, deletionErr = store.Begin("project-prepare-race-0123456789", []hubcore.DeletionTarget{{
				Ref:      ref,
				ThreadID: threadID,
			}})
			deletionCommitted = deletionErr == nil
		},
	}
	thread := appwire.Thread{
		ID:        threadID,
		SessionID: threadID,
		Source:    "local",
		Evener:    appwire.EvenerThread{Ref: ref},
	}
	source := &relaySessionTestSource{
		thread: thread,
		lease: &scriptedRelaySessionLease{
			readResult: appsource.RelayReadResult{
				Response: appwire.ThreadReadResponse{Thread: thread},
				Handoff:  handoff,
			},
			deliveries: make(chan appsource.RelayDelivery),
		},
	}
	sources := appsource.NewRegistry()
	sources.Add(source)
	appServer := newHubAppServer(hubcore.WebConfig{
		HubStateRoot:  t.TempDir(),
		Past:          hubcore.NewPastIndex(""),
		DeletionStore: store,
		ResumeLocks:   resumeLocks,
	}, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	_, readErr := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref:       ref,
		Subscribe: true,
	})
	if deletionErr != nil {
		t.Fatalf("commit deletion at handoff Prepare: %v", deletionErr)
	}
	if deletionCommitted && readErr == nil {
		t.Fatal("thread/read succeeded after deletion committed between its post-read fence and handoff capture")
	}
	if !deletionCommitted && readErr != nil {
		t.Fatalf("thread/read lost deletion ownership at its final capture boundary: %v", readErr)
	}
}

func TestHubAtomicRelayPublicationStopsAfterDeletionWins(t *testing.T) {
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const threadID = "02wMz5Txv1C3Hut0M8GCeB"
	ref := localAppRef(threadID)
	thread := appwire.Thread{
		ID:        threadID,
		SessionID: threadID,
		Source:    "local",
		Evener:    appwire.EvenerThread{Ref: ref},
	}
	deliveries := make(chan appsource.RelayDelivery, 1)
	source := &relaySessionTestSource{
		thread: thread,
		lease: &scriptedRelaySessionLease{
			readResult: appsource.RelayReadResult{
				Response: appwire.ThreadReadResponse{Thread: thread},
				Handoff:  &recordingRelayHandoff{committed: make(chan struct{}), aborted: make(chan struct{})},
			},
			deliveries: deliveries,
		},
	}
	sources := appsource.NewRegistry()
	sources.Add(source)
	appServer := newHubAppServer(hubcore.WebConfig{
		HubStateRoot:  t.TempDir(),
		Past:          hubcore.NewPastIndex(""),
		DeletionStore: store,
		ResumeLocks:   hubcore.NewResumeLocks(),
	}, sources)
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref:       ref,
		Subscribe: true,
	}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}

	if _, err := store.Begin("project-atomic-publication-0123456789", []hubcore.DeletionTarget{{
		Ref:      ref,
		ThreadID: threadID,
	}}); err != nil {
		t.Fatal(err)
	}
	acknowledged := make(chan struct{})
	deliveries <- appsource.RelayDelivery{
		Notification: appwire.Notification{
			Method: appwire.NotifyAgentMessageDelta,
			Params: testRawJSON(t, appwire.AgentMessageDeltaParams{
				ThreadID: threadID,
				Ref:      ref,
				TurnID:   "turn-deleted",
				ItemID:   "item-deleted",
				Delta:    "must not publish",
			}),
		},
		Acknowledge: func() { close(acknowledged) },
	}
	<-acknowledged
	select {
	case notification := <-client.Notifications():
		t.Fatalf("deleted target published notification %+v", notification)
	default:
	}
}

type relaySessionTestSource struct {
	relayLifecycleSource
	id    string
	lease *scriptedRelaySessionLease

	mu              sync.Mutex
	acquireCalls    int
	legacyReadCalls int
	legacySubCalls  int
	startTurnCalls  int
	resolveRelay    func(appwire.ThreadReadParams) (appwire.Ref, error)
	acquireRelay    func(appwire.Ref) (appsource.RelaySessionRoutePublicationLease, error)
}

type baseOnlyCanonicalRelaySource struct {
	relayLifecycleSource
	lease appsource.RelaySessionLease
}

func (*baseOnlyCanonicalRelaySource) ResolveRelaySession(params appwire.ThreadReadParams) (appwire.Ref, error) {
	return appwire.ParseRef(params.Ref)
}

func (s *baseOnlyCanonicalRelaySource) AcquireRelaySession(appwire.Ref) (appsource.RelaySessionLease, error) {
	return s.lease, nil
}

func TestHubRelayCanonicalSourceCannotAcquireBaseOnlyLease(t *testing.T) {
	source := any(&baseOnlyCanonicalRelaySource{lease: &scriptedRelaySessionLease{}})
	if _, valid := source.(appsource.RelaySessionSource); valid {
		t.Fatal("base-only RelaySessionLease still satisfies canonical RelaySessionSource acquisition")
	}
}

func (s *relaySessionTestSource) ID() string {
	if s.id != "" {
		return s.id
	}
	return "local"
}

func (s *relaySessionTestSource) ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	s.mu.Lock()
	s.legacyReadCalls++
	s.mu.Unlock()
	return appwire.ThreadReadResponse{}, errors.New("legacy ReadThread must not be used for an atomic LocalDaemon rejoin")
}

func (s *relaySessionTestSource) SubscribeThread(context.Context, appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	s.mu.Lock()
	s.legacySubCalls++
	s.mu.Unlock()
	return nil, errors.New("legacy SubscribeThread must not be used for a LocalDaemon relay")
}

func (s *relaySessionTestSource) StartTurn(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	s.mu.Lock()
	s.startTurnCalls++
	s.mu.Unlock()
	return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn-started"}}, nil
}

func (s *relaySessionTestSource) ResolveRelaySession(params appwire.ThreadReadParams) (appwire.Ref, error) {
	if s.resolveRelay != nil {
		return s.resolveRelay(params)
	}
	if params.Ref != "" {
		return appwire.ParseRef(params.Ref)
	}
	if params.ThreadID == "" {
		return appwire.Ref{}, errors.New("missing relay target")
	}
	return appwire.Ref{SourceID: s.ID(), ThreadID: params.ThreadID}, nil
}

func (s *relaySessionTestSource) AcquireRelaySession(ref appwire.Ref) (appsource.RelaySessionRoutePublicationLease, error) {
	s.mu.Lock()
	s.acquireCalls++
	s.mu.Unlock()
	if s.acquireRelay != nil {
		return s.acquireRelay(ref)
	}
	if s.lease == nil {
		return nil, nil
	}
	return routeAwareTestLease(s.lease), nil
}

func (s *relaySessionTestSource) acquireCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acquireCalls
}

func (s *relaySessionTestSource) legacyReadCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.legacyReadCalls
}

func (s *relaySessionTestSource) legacySubscribeCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.legacySubCalls
}

func (s *relaySessionTestSource) startTurnCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startTurnCalls
}

type scriptedRelaySessionLease struct {
	mu sync.Mutex

	readResult  appsource.RelayReadResult
	readErr     error
	readFunc    func(appwire.ThreadReadParams) (appsource.RelayReadResult, error)
	readHook    func()
	deliveries  chan appsource.RelayDelivery
	listenErr   error
	closeHook   func()
	readCalls   int
	listenCalls int
	closeCalls  int
}

func (l *scriptedRelaySessionLease) Read(_ context.Context, params appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
	l.mu.Lock()
	l.readCalls++
	result, err, readFunc, hook := l.readResult, l.readErr, l.readFunc, l.readHook
	l.mu.Unlock()
	if hook != nil {
		hook()
	}
	if readFunc != nil {
		return readFunc(params)
	}
	return result, err
}

type routePublishingTestLease struct {
	appsource.RelaySessionLease
}

func routeAwareTestLease(lease appsource.RelaySessionLease) appsource.RelaySessionRoutePublicationLease {
	return &routePublishingTestLease{RelaySessionLease: lease}
}

func (l *routePublishingTestLease) ReadWithRoutePublication(
	ctx context.Context,
	params appwire.ThreadReadParams,
	publish func(context.Context, appwire.Thread) error,
) (appsource.RelayReadResult, error) {
	result, err := l.RelaySessionLease.Read(ctx, params)
	if err == nil && publish != nil {
		if err := publish(ctx, result.Response.Thread); err != nil {
			return appsource.RelayReadResult{}, err
		}
	}
	return result, err
}

func (l *scriptedRelaySessionLease) Listen(context.Context) (<-chan appsource.RelayDelivery, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.listenCalls++
	return l.deliveries, l.listenErr
}

func (l *scriptedRelaySessionLease) Close() {
	l.mu.Lock()
	l.closeCalls++
	hook := l.closeHook
	l.mu.Unlock()
	if hook != nil {
		hook()
	}
}

func (l *scriptedRelaySessionLease) readCallCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.readCalls
}

func (l *scriptedRelaySessionLease) listenCallCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.listenCalls
}

func (l *scriptedRelaySessionLease) closeCallCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closeCalls
}

type recordingRelayHandoff struct {
	once      sync.Once
	committed chan struct{}
	aborted   chan struct{}
	onCommit  func()
	stale     bool
}

type guardedRelayHandoff struct {
	mu sync.Mutex

	prepareAllowed bool
	commitAllowed  bool
	onPrepare      func()
	prepared       bool
	prepareCalls   int
	commitCalls    int
	abortCalls     int
}

func (h *guardedRelayHandoff) Prepare() bool {
	h.mu.Lock()
	h.prepareCalls++
	hook := h.onPrepare
	allowed := h.prepareAllowed
	if allowed {
		h.prepared = true
	}
	h.mu.Unlock()
	if hook != nil {
		hook()
	}
	return allowed
}

func (h *guardedRelayHandoff) Commit() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.commitCalls++
	return h.prepared && h.commitAllowed
}

func (h *guardedRelayHandoff) Abort() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.abortCalls++
	return true
}

func (h *guardedRelayHandoff) isPrepared() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.prepared
}

func (h *guardedRelayHandoff) callCounts() (int, int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.prepareCalls, h.commitCalls, h.abortCalls
}

func (h *recordingRelayHandoff) Prepare() bool {
	return !h.stale
}

func (h *recordingRelayHandoff) Commit() bool {
	won := false
	h.once.Do(func() {
		won = !h.stale
		close(h.committed)
		if h.onCommit != nil {
			h.onCommit()
		}
	})
	return won
}

func (h *recordingRelayHandoff) Abort() bool {
	won := false
	h.once.Do(func() {
		won = true
		close(h.aborted)
	})
	return won
}
