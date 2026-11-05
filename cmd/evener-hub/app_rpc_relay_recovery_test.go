package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

func TestHubRPCThreadReadRelaysDaemonNotifications(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		appserver.SubscribeWithAdmission(ctx, "th_1", false)
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "th_1", SessionID: "sess_1", Evener: appwire.EvenerThread{Ref: "local:th_1"}}}, nil
	})
	_, roster, runDir := startDaemonRoster(t, daemon, 103, "th_1", "sess_1")

	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir: runDir,
		Roster: roster,
		Past:   hubcore.NewPastIndex(""),
	})
	defer hub.Close()

	client := dialHubRPCInitialized(t, hub)
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:th_1"}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}

	daemon.Broadcast("th_1", appwire.NotifyOverlayDelta, appwire.OverlayDeltaParams{
		ThreadID: "th_1",
		Ref:      "local:th_1",
		Delta:    "hi",
	})

	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyOverlayDelta {
			t.Fatalf("method=%q", got.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for relayed notification")
	}
}

func TestHubRPCThreadReadRelaysEnrichedOutputImageNotification(t *testing.T) {
	cwd := t.TempDir()
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 'p', 'a', 'y'}
	if err := os.WriteFile(filepath.Join(cwd, "plot.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	sessionID := "01RELAYIMG"
	source := &relayBroadcastSource{
		id: "local",
		thread: appwire.Thread{
			ID:        "th_img",
			SessionID: sessionID,
			CWD:       cwd,
			Source:    "local",
			Evener:    appwire.EvenerThread{Ref: "local:th_img", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPCInitialized(t, srv)
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:th_img"}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	expectRelaySubscription(t, source.subscribed)

	source.notifications <- *appwire.NotificationMessage(appwire.NotifyHistoryUpdated, appwire.HistoryUpdatedParams{
		ThreadID: "th_img",
		Ref:      "local:th_img",
		Items: []appwire.ThreadItem{{
			Type:          "commandExecution",
			ID:            "item_write",
			ToolName:      "write_file",
			CallID:        "call_write",
			ArgumentsJSON: `{"file_path":"plot.png"}`,
			Output:        "wrote",
			Status:        appwire.TurnStatusCompleted,
		}},
	}).Notification

	imgs := relayedHistoryItem(t, client.Notifications()).OutputImages
	if len(imgs) != 1 || imgs[0].Source != "written-file" || imgs[0].Path != "plot.png" || imgs[0].URL != "/doc/image?session="+sessionID+"&path=plot.png" {
		t.Fatalf("OutputImages=%+v, want written-file plot.png /doc/image descriptor", imgs)
	}
}

// TestHubRPCRelaysSHARoutedToolResultImageFromARealDaemon is the same check as
// TestHubRPCThreadReadRelaysSHARoutedToolResultImage on the fanout a REAL local
// daemon takes: LocalDaemonSource is a RelaySessionSource, so its frames reach
// the browser through the acknowledged-fanout loop rather than the
// non-atomic broadcast loop, and the two are separate call sites.
func TestHubRPCRelaysSHARoutedToolResultImageFromARealDaemon(t *testing.T) {
	sessionID := "02wMz5Txv733WHFsVy66SR"
	sha := strings.Repeat("b", 64)
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		appserver.SubscribeWithAdmission(ctx, sessionID, false)
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID: sessionID, SessionID: sessionID, Source: "local",
			Evener: appwire.EvenerThread{Ref: params.Ref},
		}}, nil
	})
	_, roster, runDir := startDaemonRoster(t, daemon, 21*1000, sessionID, sessionID)
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Past: hubcore.NewPastIndex("")})
	defer hub.Close()

	client := dialHubRPCInitialized(t, hub)
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID, Subscribe: true}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}

	daemon.Broadcast(sessionID, appwire.NotifyHistoryUpdated, appwire.HistoryUpdatedParams{
		ThreadID: sessionID, Ref: "local:" + sessionID,
		Items: []appwire.ThreadItem{{
			Type: "commandExecution", ID: "item_shot", TurnID: "turn_1",
			ToolName: "screenshot", CallID: "call_shot", Status: appwire.TurnStatusCompleted,
			OutputImages: []appwire.OutputImage{{Source: "tool-result", Name: "screenshot", MediaType: "image/png", Size: 11, SHA: sha}},
		}},
	})

	imgs := relayedHistoryItem(t, client.Notifications()).OutputImages
	if len(imgs) != 1 || imgs[0].URL != "/s/"+sessionID+"/images/"+sha {
		t.Fatalf("OutputImages=%+v, want the sha route stamped on the fanned-out descriptor", imgs)
	}
}

// relayedHistoryItem waits for the next relayed history/updated and returns
// its one item.
func relayedHistoryItem(t *testing.T, notifications <-chan appwire.Notification) appwire.ThreadItem {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case got := <-notifications:
			if got.Method != appwire.NotifyHistoryUpdated {
				continue
			}
			var params appwire.HistoryUpdatedParams
			if err := json.Unmarshal(got.Params, &params); err != nil || len(params.Items) != 1 {
				t.Fatalf("relayed history/updated = %s (%v), want one item", got.Params, err)
			}
			return params.Items[0]
		case <-deadline:
			t.Fatal("timed out waiting for the relayed history/updated")
		}
	}
}

// TestHubRPCThreadReadRelaysSHARoutedToolResultImage is the live-streaming path
// end to end through the relay (kata 2fxm): the daemon publishes a
// history/updated whose tool-result image is named by sha and nothing else, and
// the browser must receive a descriptor it can actually fetch.
func TestHubRPCThreadReadRelaysSHARoutedToolResultImage(t *testing.T) {
	sessionID := "02wMz5Txv733WHFsVy66SR"
	sha := strings.Repeat("a", 64)
	source := &relayBroadcastSource{
		id: "local",
		thread: appwire.Thread{
			ID:        "th_shot",
			SessionID: sessionID,
			Source:    "local",
			Evener:    appwire.EvenerThread{Ref: "local:th_shot", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	srv := newWebSourceTestServer(t, source)

	client := dialHubRPCInitialized(t, srv)
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:th_shot"}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	expectRelaySubscription(t, source.subscribed)

	source.notifications <- *appwire.NotificationMessage(appwire.NotifyHistoryUpdated, appwire.HistoryUpdatedParams{
		ThreadID: "th_shot",
		Ref:      "local:th_shot",
		Items: []appwire.ThreadItem{{
			Type: "commandExecution", ID: "item_shot", ToolName: "screenshot", CallID: "call_shot",
			Output: "captured", Status: appwire.TurnStatusCompleted,
			OutputImages: []appwire.OutputImage{{
				Source: "tool-result", Name: "screenshot", MediaType: "image/png", Size: 11, SHA: sha,
			}},
		}},
	}).Notification

	imgs := relayedHistoryItem(t, client.Notifications()).OutputImages
	if len(imgs) != 1 || imgs[0].URL != "/s/"+sessionID+"/images/"+sha {
		t.Fatalf("OutputImages=%+v, want the sha route stamped on the relayed descriptor", imgs)
	}
}

func TestHubRPCThreadReadRelaysNotificationsBySourceQualifiedThread(t *testing.T) {
	threadID := "shared_thread"
	sourceA := &relayBroadcastSource{
		id: "codex-a",
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex-a",
			Evener:    appwire.EvenerThread{Ref: "codex-a:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		canceled:      make(chan struct{}, 2),
	}
	sourceB := &relayBroadcastSource{
		id: "codex-b",
		thread: appwire.Thread{
			ID:        threadID,
			SessionID: threadID,
			Source:    "codex-b",
			Evener:    appwire.EvenerThread{Ref: "codex-b:" + threadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		canceled:      make(chan struct{}, 2),
	}
	srv := newWebSourceTestServer(t, sourceA, sourceB)

	clientA := dialHubRPC(t, srv)
	defer clientA.Close()
	if _, err := clientA.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize clientA: %v", err)
	}
	if _, err := clientA.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex-a:" + threadID}); err != nil {
		t.Fatalf("ThreadRead clientA: %v", err)
	}
	clientB := dialHubRPC(t, srv)
	defer clientB.Close()
	if _, err := clientB.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize clientB: %v", err)
	}
	if _, err := clientB.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex-b:" + threadID}); err != nil {
		t.Fatalf("ThreadRead clientB: %v", err)
	}

	sourceB.notifications <- appwire.Notification{
		Method: appwire.NotifyOverlayDelta,
		Params: testRawJSON(t, appwire.OverlayDeltaParams{
			ThreadID: threadID,
			Ref:      "codex-b:" + threadID,
			Delta:    "from source b",
		}),
	}

	select {
	case got := <-clientB.Notifications():
		if got.Method != appwire.NotifyOverlayDelta {
			t.Fatalf("clientB method=%q", got.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for source b notification")
	}
	// Prove isolation: send a sentinel on sourceA so clientA receives something.
	// If sourceB's notification had leaked to clientA it would have arrived
	// first (it was broadcast before the sentinel), so receiving the sentinel
	// as the first notification structurally proves no cross-source leak.
	sourceA.notifications <- appwire.Notification{
		Method: appwire.NotifyOverlayDelta,
		Params: testRawJSON(t, appwire.OverlayDeltaParams{
			ThreadID: threadID,
			Ref:      "codex-a:" + threadID,
			Delta:    "sentinel",
		}),
	}
	select {
	case got := <-clientA.Notifications():
		var params appwire.OverlayDeltaParams
		if err := json.Unmarshal(got.Params, &params); err != nil {
			t.Fatalf("clientA: unmarshal params: %v", err)
		}
		if params.Ref != "codex-a:"+threadID {
			t.Fatalf("clientA received cross-source notification before sentinel: ref=%q", params.Ref)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for sentinel notification on clientA")
	}
}

func TestHubRPCThreadReadSubscribeOverridesSourceReadRelayPolicy(t *testing.T) {
	threadID := "th_codex_live"
	source := &readRelayDisabledSource{
		id:            "codex",
		thread:        codexRelayThread(threadID),
		notifications: make(chan appwire.Notification, 4),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	srv := newWebSourceTestServer(t, source)

	client := dialHubRPCInitialized(t, srv)
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	expectRelaySubscription(t, source.subscribed)

	source.notifications <- appwire.Notification{
		Method: appwire.NotifyOverlayDelta,
		Params: testRawJSON(t, appwire.OverlayDeltaParams{
			ThreadID: threadID,
			Ref:      "codex:" + threadID,
			Delta:    "from codex",
		}),
	}

	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyOverlayDelta {
			t.Fatalf("method=%q", got.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscribed codex notification")
	}
}

// thread/unsubscribe must drop the calling connection's downstream
// subscription — the registry entry thread/read's relay created — so the
// relay's idle ticker sees SubscriberCount zero and can retire, and a second
// call stays a quiet no-op.
func TestHubRPCThreadUnsubscribeDropsDownstreamSubscription(t *testing.T) {
	const threadID = "th_unsub"
	source := &relayBroadcastSource{
		id:            "codex",
		thread:        codexRelayThread(threadID),
		notifications: make(chan appwire.Notification, 4),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPCInitialized(t, srv)
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	expectRelaySubscription(t, source.subscribed)
	if got := web.appRPC.SubscriberCount("codex:" + threadID); got != 1 {
		t.Fatalf("subscriber count after subscribed read = %d, want 1", got)
	}

	if _, err := client.ThreadUnsubscribe(context.Background(), appwire.ThreadUnsubscribeParams{Ref: "codex:" + threadID}); err != nil {
		t.Fatalf("ThreadUnsubscribe: %v", err)
	}
	if got := web.appRPC.SubscriberCount("codex:" + threadID); got != 0 {
		t.Fatalf("subscriber count after unsubscribe = %d, want 0", got)
	}

	// The relay's notifications no longer reach this connection.
	source.notifications <- appwire.Notification{
		Method: appwire.NotifyOverlayDelta,
		Params: testRawJSON(t, appwire.OverlayDeltaParams{
			ThreadID: threadID,
			Ref:      "codex:" + threadID,
			Delta:    "after unsubscribe",
		}),
	}
	select {
	case got := <-client.Notifications():
		t.Fatalf("notification delivered after unsubscribe: %+v", got)
	case <-time.After(200 * time.Millisecond):
	}

	// Idempotent: unsubscribing again succeeds quietly.
	if _, err := client.ThreadUnsubscribe(context.Background(), appwire.ThreadUnsubscribeParams{Ref: "codex:" + threadID}); err != nil {
		t.Fatalf("second ThreadUnsubscribe: %v", err)
	}
	if got := web.appRPC.SubscriberCount("codex:" + threadID); got != 0 {
		t.Fatalf("subscriber count after second unsubscribe = %d, want 0", got)
	}
}

// The fallback branch: when no source resolves (an exited session removed
// its rendezvous entry), an unsubscribe still quietly succeeds. The handler
// resolves through the plain registry (sourceForThread, never the
// managed-launch path), so no launcher is even consulted — an unsubscribe is
// a "stop caring" operation and must not spawn.
func TestHubRPCThreadUnsubscribeUnresolvedSourceIsQuiet(t *testing.T) {
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPCInitialized(t, srv)
	// A ref whose source never existed: quiet success, no error.
	if _, err := client.ThreadUnsubscribe(context.Background(), appwire.ThreadUnsubscribeParams{Ref: "codex:th_missing"}); err != nil {
		t.Fatalf("ThreadUnsubscribe for unresolvable source: %v", err)
	}
	// The same for a bare threadID with no ref.
	if _, err := client.ThreadUnsubscribe(context.Background(), appwire.ThreadUnsubscribeParams{ThreadID: "th_missing"}); err != nil {
		t.Fatalf("ThreadUnsubscribe for unresolvable thread: %v", err)
	}
}

func TestHubRPCThreadReadRecoversEstablishedRelayAfterSourceClose(t *testing.T) {
	const threadID = "th_recover"
	results := make(chan relaySubscribeResult)
	subscribeCalls := make(chan struct{})
	source := &scriptedRelaySource{
		thread:         codexRelayThread(threadID),
		results:        results,
		subscribeCalls: subscribeCalls,
	}
	srv := newWebSourceTestServer(t, source)

	client := dialHubRPCInitialized(t, srv)

	readErr := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true})
		readErr <- err
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	notificationsA := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: notificationsA}
	select {
	case err := <-readErr:
		if err != nil {
			t.Fatalf("ThreadRead: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial ThreadRead")
	}

	notificationsA <- relayDeltaNotification(t, threadID, "event A")
	expectRelayDelta(t, client.Notifications(), "event A")
	close(notificationsA)

	awaitRelaySubscribeCall(t, subscribeCalls)
	notificationsB := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: notificationsB}
	expectRelayResync(t, client.Notifications(), threadID, "codex:"+threadID)
	notificationsB <- relayDeltaNotification(t, threadID, "event B")
	expectRelayDelta(t, client.Notifications(), "event B")
}

func TestHubRelayRecoveryEmitsThreadResyncBeforeReplacementNotifications(t *testing.T) {
	const threadID = "th_resync"
	results := make(chan relaySubscribeResult)
	subscribeCalls := make(chan struct{})
	retryClock := newScriptedRelayRetryClock()
	source := &scriptedRelaySource{
		thread:         codexRelayThread(threadID),
		results:        results,
		subscribeCalls: subscribeCalls,
	}
	srv := httptest.NewUnstartedServer(nil)
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	cfg.RelayHooks.RetryWait = retryClock.Wait
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPCInitialized(t, srv)

	readErr := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true})
		readErr <- err
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	notificationsA := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: notificationsA}
	select {
	case err := <-readErr:
		if err != nil {
			t.Fatalf("ThreadRead: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial ThreadRead")
	}
	select {
	case got := <-client.Notifications():
		t.Fatalf("initial relay emitted notification %+v, want none", got)
	default:
	}
	notificationsA <- relayDeltaNotification(t, threadID, "initial event")
	expectRelayDelta(t, client.Notifications(), "initial event")

	close(notificationsA)
	awaitRelaySubscribeCall(t, subscribeCalls)
	results <- relaySubscribeResult{err: errors.New("replacement unavailable")}
	retryClock.releaseWait(t, 100*time.Millisecond)
	awaitRelaySubscribeCall(t, subscribeCalls)
	notificationsB := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: notificationsB}
	expectRelayResync(t, client.Notifications(), threadID, "codex:"+threadID)

	notificationsB <- relayDeltaNotification(t, threadID, "replacement event")
	expectRelayDelta(t, client.Notifications(), "replacement event")
}

// A source that hands its subscription a leading resync — the remote hub source
// folds the subscribed read's atomic snapshot in this way — must not have that
// frame duplicated by the relay's own recovery resync, or the client re-reads
// twice for one recovery. The source's frame carries the subscription's own
// authoritative identity, so it is the one the client must see.
func TestHubRelayRecoveryDoesNotDuplicateSourceResync(t *testing.T) {
	const threadID = "th_resync_once"
	results := make(chan relaySubscribeResult)
	subscribeCalls := make(chan struct{})
	source := &scriptedRelaySource{
		thread:         codexRelayThread(threadID),
		results:        results,
		subscribeCalls: subscribeCalls,
	}
	srv := httptest.NewUnstartedServer(nil)
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPCInitialized(t, srv)

	readErr := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true})
		readErr <- err
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	notificationsA := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: notificationsA}
	select {
	case err := <-readErr:
		if err != nil {
			t.Fatalf("ThreadRead: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial ThreadRead")
	}

	close(notificationsA)
	awaitRelaySubscribeCall(t, subscribeCalls)
	// The replacement subscription's first frame is the source's own resync,
	// already buffered when the relay peeks for a leading frame.
	notificationsB := make(chan appwire.Notification, 1)
	notificationsB <- *appwire.NotificationMessage(appwire.NotifyEvenerThreadResync, appwire.ThreadResyncParams{
		ThreadID: threadID,
		Ref:      "codex:canonical",
	}).Notification
	results <- relaySubscribeResult{notifications: notificationsB}

	// Exactly one resync, and it is the source's authoritative one — not the
	// relay's own ("codex:"+threadID).
	expectRelayResync(t, client.Notifications(), threadID, "codex:canonical")
	select {
	case got := <-client.Notifications():
		t.Fatalf("duplicate recovery resync delivered: method=%q params=%s", got.Method, got.Params)
	case <-time.After(200 * time.Millisecond):
	}
}

// A non-atomic source whose thread's current ID differs from its stable ref
// suffix (a remote hub after an identity replacement) must register its relay
// under the identity a ref-only thread/unsubscribe resolves. Keying the
// downstream entry by thread.ID left a ref-addressed subscription — and, with
// it, the source-side subscription — live forever.
func TestHubRPCThreadUnsubscribeDropsStableRefRelay(t *testing.T) {
	const stableRef = "host:stable"
	source := &relayBroadcastSource{
		id: "host",
		thread: appwire.Thread{
			ID:        "current",
			SessionID: "current",
			Source:    "host",
			Evener:    appwire.EvenerThread{Ref: stableRef, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPCInitialized(t, srv)
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: stableRef, Subscribe: true}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	expectRelaySubscription(t, source.subscribed)
	if got := web.appRPC.SubscriberCount(stableRef); got != 1 {
		t.Fatalf("subscriber count at the stable ref = %d, want 1 (relay keyed by thread.ID instead of the ref)", got)
	}
	if got := web.appRPC.SubscriberCount("host:current"); got != 0 {
		t.Fatalf("subscriber count at the live thread id = %d, want 0", got)
	}

	if _, err := client.ThreadUnsubscribe(context.Background(), appwire.ThreadUnsubscribeParams{Ref: stableRef}); err != nil {
		t.Fatalf("ThreadUnsubscribe: %v", err)
	}
	if got := web.appRPC.SubscriberCount(stableRef); got != 0 {
		t.Fatalf("subscriber count after ref-only unsubscribe = %d, want 0", got)
	}

	source.notifications <- appwire.Notification{
		Method: appwire.NotifyOverlayDelta,
		Params: testRawJSON(t, appwire.OverlayDeltaParams{
			ThreadID: "current",
			Ref:      stableRef,
			Delta:    "after unsubscribe",
		}),
	}
	select {
	case got := <-client.Notifications():
		t.Fatalf("notification delivered after ref-only unsubscribe: %+v", got)
	case <-time.After(200 * time.Millisecond):
	}
}

// A federated (non-atomic) source is addressed by its ref, so a thread/read that
// carries both the stable ref and the thread's current ID must register its relay
// under the ref identity thread/unsubscribe resolves — otherwise the ref-only
// unsubscribe names a key that was never subscribed and the downstream entry (and
// the source-side subscription behind it) stays live forever.
func TestHubRPCThreadUnsubscribeDropsStableRefRelayWhenReadAlsoNamedThreadID(t *testing.T) {
	const stableRef = "host:stable"
	source := &relayBroadcastSource{
		id: "host",
		thread: appwire.Thread{
			ID:        "current",
			SessionID: "current",
			Source:    "host",
			Evener:    appwire.EvenerThread{Ref: stableRef, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPCInitialized(t, srv)
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: stableRef, ThreadID: "current", Subscribe: true}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	expectRelaySubscription(t, source.subscribed)
	if got := web.appRPC.SubscriberCount(stableRef); got != 1 {
		t.Fatalf("subscriber count at the stable ref = %d, want 1 (relay keyed by the caller's current ID instead of the ref)", got)
	}
	if got := web.appRPC.SubscriberCount("host:current"); got != 0 {
		t.Fatalf("subscriber count at the live thread id = %d, want 0 (the ref is the delivery identity)", got)
	}

	if _, err := client.ThreadUnsubscribe(context.Background(), appwire.ThreadUnsubscribeParams{Ref: stableRef}); err != nil {
		t.Fatalf("ThreadUnsubscribe: %v", err)
	}
	if got := web.appRPC.SubscriberCount(stableRef); got != 0 {
		t.Fatalf("subscriber count after ref-only unsubscribe = %d, want 0", got)
	}

	source.notifications <- appwire.Notification{
		Method: appwire.NotifyOverlayDelta,
		Params: testRawJSON(t, appwire.OverlayDeltaParams{
			ThreadID: "current",
			Ref:      stableRef,
			Delta:    "after unsubscribe",
		}),
	}
	select {
	case got := <-client.Notifications():
		t.Fatalf("notification delivered after ref-only unsubscribe: %+v", got)
	case <-time.After(200 * time.Millisecond):
	}
}

// A relay's recovery must re-address the thread by the authoritative ref the read
// named, not by the caller's bare threadId. A federated source's daemon edge
// resolves a bare threadId in preference to the ref, so once an identity
// replacement moves the live thread ID the original ID names nothing while the
// stable ref beside it still resolves; reusing the original request verbatim
// makes every recovery attempt in the stale state fail forever.
func TestHubRelayRecoveryUsesAuthoritativeRefNotStaleThreadID(t *testing.T) {
	const (
		stableRef = "host:stable"
		liveID    = "current"
	)
	results := make(chan relaySubscribeResult)
	subscribeCalls := make(chan struct{})
	source := &recordingSubscribeParamsSource{
		results:        results,
		subscribeCalls: subscribeCalls,
	}
	source.thread = appwire.Thread{
		ID:        liveID,
		SessionID: liveID,
		Source:    "host",
		Evener:    appwire.EvenerThread{Ref: stableRef, Capabilities: appwire.ThreadCapabilities{Send: true}},
	}
	srv := httptest.NewUnstartedServer(nil)
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPCInitialized(t, srv)

	readErr := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: stableRef, ThreadID: liveID, Subscribe: true})
		readErr <- err
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	notificationsA := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: notificationsA}
	select {
	case err := <-readErr:
		if err != nil {
			t.Fatalf("ThreadRead: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial ThreadRead")
	}

	// The subscription ends, so the relay recovers. The current ID has moved on
	// (an identity replacement), leaving the original bare threadId stale.
	close(notificationsA)
	awaitRelaySubscribeCall(t, subscribeCalls)
	notificationsB := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: notificationsB}
	// The source claimed its subscription again, so the recovered relay delivers.
	expectRelayResync(t, client.Notifications(), liveID, stableRef)
	notificationsB <- relayDeltaNotification(t, liveID, "recovered event")
	expectRelayDelta(t, client.Notifications(), "recovered event")

	params := source.subscribeParams()
	if len(params) < 2 {
		t.Fatalf("subscribe calls = %d, want an initial and a recovery call", len(params))
	}
	recovery := params[1]
	if recovery.Ref != stableRef {
		t.Fatalf("recovery subscribe ref = %q, want the authoritative %q", recovery.Ref, stableRef)
	}
	if recovery.ThreadID != "" {
		t.Fatalf("recovery subscribe resent the caller's bare threadId %q, which a stale identity no longer resolves", recovery.ThreadID)
	}
}

func TestRelayRetryClockWaitStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := newRelayRetryClock().Wait(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait error=%v, want context canceled", err)
	}
}

func TestRelayRetryBackoffCapsAtFiveSeconds(t *testing.T) {
	var backoff relayRetryBackoff
	want := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		1600 * time.Millisecond,
		3200 * time.Millisecond,
		5 * time.Second,
		5 * time.Second,
	}
	for i, delay := range want {
		if got := backoff.Next(); got != delay {
			t.Fatalf("Next call %d=%s, want %s", i+1, got, delay)
		}
	}
}

func TestHubRPCThreadReadRelayRecoveryBackoffAndReset(t *testing.T) {
	const threadID = "th_retry_backoff"
	results := make(chan relaySubscribeResult)
	subscribeCalls := make(chan struct{})
	retryClock := newScriptedRelayRetryClock()
	source := &scriptedRelaySource{
		thread:         codexRelayThread(threadID),
		results:        results,
		subscribeCalls: subscribeCalls,
	}
	srv := httptest.NewUnstartedServer(nil)
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	cfg.RelayHooks.RetryWait = retryClock.Wait
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPCInitialized(t, srv)
	readErr := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true})
		readErr <- err
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	notificationsA := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: notificationsA}
	select {
	case err := <-readErr:
		if err != nil {
			t.Fatalf("ThreadRead: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial ThreadRead")
	}

	close(notificationsA)
	awaitRelaySubscribeCall(t, subscribeCalls)
	results <- relaySubscribeResult{err: errors.New("recovery failed once")}
	retryClock.releaseWait(t, 100*time.Millisecond)
	awaitRelaySubscribeCall(t, subscribeCalls)
	results <- relaySubscribeResult{err: errors.New("recovery failed twice")}
	retryClock.releaseWait(t, 200*time.Millisecond)
	awaitRelaySubscribeCall(t, subscribeCalls)
	notificationsB := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: notificationsB}
	expectRelayResync(t, client.Notifications(), threadID, "codex:"+threadID)
	notificationsB <- relayDeltaNotification(t, threadID, "recovered")
	expectRelayDelta(t, client.Notifications(), "recovered")

	close(notificationsB)
	awaitRelaySubscribeCall(t, subscribeCalls)
	results <- relaySubscribeResult{err: errors.New("recovery failed after success")}
	retryClock.expectWait(t, 100*time.Millisecond)
}

// relayActiveStatusNotification builds the thread/status/changed a live
// daemon publishes when a turn starts running, which seeds the relay's
// running-turn tracking.
func relayActiveStatusNotification(t *testing.T, threadID, turnID string) appwire.Notification {
	t.Helper()
	return appwire.Notification{
		Method: appwire.NotifyThreadStatusChanged,
		Params: testRawJSON(t, appwire.ThreadStatusChangedParams{
			ThreadID:     threadID,
			Ref:          "codex:" + threadID,
			Status:       appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
			ActiveTurnID: turnID,
		}),
	}
}

// expectRelaySynthesizedIdleStatus asserts the status frame the relay
// broadcasts once it gives up on a running turn's daemon. Without it the
// reducer keeps the session active (Stop and Steer still showing, Send
// withheld) with nothing left to end the turn. It must carry the relay's
// target. The action
// set is the hub's own only for a local (resumable) session; a non-local
// source must carry none, preserving the masked set it sent.
func expectRelaySynthesizedIdleStatus(t *testing.T, notifications <-chan appwire.Notification, wantThreadID, wantRef string, wantCapabilities bool) {
	t.Helper()
	select {
	case got := <-notifications:
		if got.Method != appwire.NotifyThreadStatusChanged {
			t.Fatalf("notification method=%q, want %q", got.Method, appwire.NotifyThreadStatusChanged)
		}
		var params appwire.ThreadStatusChangedParams
		if err := json.Unmarshal(got.Params, &params); err != nil {
			t.Fatalf("unmarshal thread/status/changed: %v", err)
		}
		if params.ThreadID != wantThreadID || params.Ref != wantRef {
			t.Fatalf("thread/status/changed target threadId=%q ref=%q, want %q/%q", params.ThreadID, params.Ref, wantThreadID, wantRef)
		}
		if params.Status.Type != appwire.ThreadStatusIdle {
			t.Fatalf("status.type=%q, want %q", params.Status.Type, appwire.ThreadStatusIdle)
		}
		if !wantCapabilities {
			if params.Capabilities != nil {
				t.Fatalf("capabilities present for a non-local source, want absent: %+v", *params.Capabilities)
			}
		} else if params.Capabilities == nil {
			t.Fatal("capabilities absent, want the hub's past-session set")
		} else if !params.Capabilities.Send {
			t.Fatal("capabilities.send=false, want true so the reader can resume the session")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the synthesized idle status")
	}
}

// expectRelayGaveUpNotice asserts the overlay notice the relay publishes when
// it gives up on a running turn: an error notice in that turn, anchored after
// the latest history the relay forwarded, naming why, so the reader sees the
// cause rather than a turn that silently stopped.
func expectRelayGaveUpNotice(t *testing.T, notifications <-chan appwire.Notification, wantThreadID, wantRef, wantTurnID string, wantAnchorEntry uint64, wantCause string) {
	t.Helper()
	select {
	case got := <-notifications:
		if got.Method != appwire.NotifyOverlayUpserted {
			t.Fatalf("notification method=%q, want %q", got.Method, appwire.NotifyOverlayUpserted)
		}
		var params appwire.OverlayUpsertedParams
		if err := json.Unmarshal(got.Params, &params); err != nil {
			t.Fatalf("unmarshal overlay/upserted: %v", err)
		}
		notice := params.Item
		if params.ThreadID != wantThreadID || params.Ref != wantRef {
			t.Fatalf("notice target threadId=%q ref=%q, want %q/%q", params.ThreadID, params.Ref, wantThreadID, wantRef)
		}
		if notice.Kind != appwire.OverlayNotice || notice.TurnID != wantTurnID || notice.Item.EventKind != appwire.ThreadItemEventKindError ||
			notice.Anchor == nil || *notice.Anchor != (appwire.ThreadItemPosition{Entry: wantAnchorEntry, Item: appwire.NoticeAnchorItem}) {
			t.Fatalf("notice = %+v, want an anchored error notice in turn %s", notice, wantTurnID)
		}
		if !strings.Contains(notice.Item.Text, wantCause) {
			t.Fatalf("notice text=%q, want it to name %q", notice.Item.Text, wantCause)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the give-up notice")
	}
}

// relayGaveUpCapabilities decides the action set the synthesized idle status
// carries: the hub's past-session set for a local thread it can resume, and
// nothing for a non-local source, whose own masked set must stand (absent
// means "no update"). A federated source told it could Send or Compact would
// be offered actions the hub cannot honour.
func TestRelayGaveUpCapabilitiesOnlyForLocalThreads(t *testing.T) {
	cfg := hubcore.WebConfig{}
	local := appwire.Thread{Evener: appwire.EvenerThread{Ref: "local:th_caps"}}
	got := relayGaveUpCapabilities(cfg, "local:th_caps", local)
	want := pastThreadCapabilities()
	// No state dir and no past entry: the fork fence floors ForkFromTurn
	// exactly as applyHubForkCapability does on a past read.
	want.ForkFromTurn = false
	if got == nil || *got != want {
		t.Fatalf("local relay: capabilities=%v, want %+v", got, want)
	}
	for _, relayKey := range []string{"codex:th_caps", "host:th_caps"} {
		thread := appwire.Thread{Evener: appwire.EvenerThread{Ref: relayKey}}
		if nonLocal := relayGaveUpCapabilities(cfg, relayKey, thread); nonLocal != nil {
			t.Fatalf("relay %q: capabilities=%+v, want nil (source keeps its own set)", relayKey, *nonLocal)
		}
	}
}

// TestHubRelayGivesUpOnARunningTurnAfterRepeatedRedialFailures covers kata
// 3h02: a daemon SIGKILLed mid-turn leaves the recovery loop re-dialing a
// socket nothing answers, forever, with no diagnostic. After
// relayGiveUpAfterFailures consecutive re-dial failures while a turn is
// running, the relay publishes thread/status/changed(idle), which owns the
// session status, and evener/thread/resync, so the client re-reads the
// transcript: the turn the daemon never completed reads as an open turn with
// nothing running it. It fires the pair exactly once per stall, not on every
// subsequent retry. Both frames must name the relay's target or a client
// drops them at the routing guard.
func TestHubRelayGivesUpOnARunningTurnAfterRepeatedRedialFailures(t *testing.T) {
	const threadID = "th_dead_mid_turn"
	const turnID = "turn_dead"
	results := make(chan relaySubscribeResult)
	subscribeCalls := make(chan struct{})
	retryClock := newScriptedRelayRetryClock()
	source := &scriptedRelaySource{
		thread:         codexRelayThread(threadID),
		results:        results,
		subscribeCalls: subscribeCalls,
	}
	srv := httptest.NewUnstartedServer(nil)
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	cfg.RelayHooks.RetryWait = retryClock.Wait
	logged := make(chan string, 16)
	cfg.Logf = func(format string, args ...any) { logged <- fmt.Sprintf(format, args...) }
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPCInitialized(t, srv)
	readErr := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true})
		readErr <- err
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	notifications := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: notifications}
	select {
	case err := <-readErr:
		if err != nil {
			t.Fatalf("ThreadRead: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial ThreadRead")
	}

	// The turn opens; the reader is now watching a spinner. Then the daemon
	// is gone (kill -9): its notification channel closes with no error and
	// no persisted TurnFailure, exactly like a SIGKILLed process.
	notifications <- relayActiveStatusNotification(t, threadID, turnID)
	// The turn records history: the latest item the relay forwards sits at
	// entry 7, so the give-up notice anchors after it, at entry 8 (ordinal +
	// 1 of the preceding entry, the spec's notice-anchor rule).
	notifications <- *appwire.NotificationMessage(appwire.NotifyHistoryUpdated, appwire.HistoryUpdatedParams{
		ThreadID: threadID, Ref: "codex:" + threadID,
		Items: []appwire.ThreadItem{
			{Type: "agentMessage", ID: "late", Position: &appwire.ThreadItemPosition{Entry: 7, Item: 1}},
			{Type: "userMessage", ID: "early", Position: &appwire.ThreadItemPosition{Entry: 5}},
		},
	}).Notification
	for _, want := range []string{appwire.NotifyThreadStatusChanged, appwire.NotifyHistoryUpdated} {
		select {
		case got := <-client.Notifications():
			if got.Method != want {
				t.Fatalf("notification method=%q, want %q", got.Method, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s", want)
		}
	}
	close(notifications)

	// Two re-dial failures is a blip: no diagnostic yet, no button - the
	// spinner is exactly as legible (or illegible) as it always was.
	awaitRelaySubscribeCall(t, subscribeCalls)
	results <- relaySubscribeResult{err: errors.New("local daemon unavailable: connection refused (1)")}
	retryClock.releaseWait(t, 100*time.Millisecond)
	awaitRelaySubscribeCall(t, subscribeCalls)
	results <- relaySubscribeResult{err: errors.New("local daemon unavailable: connection refused (2)")}
	retryClock.releaseWait(t, 200*time.Millisecond)

	// The third consecutive failure crosses relayGiveUpAfterFailures: the
	// relay must stop retrying in silence and tell the reader nothing runs
	// the turn any more.
	awaitRelaySubscribeCall(t, subscribeCalls)
	results <- relaySubscribeResult{err: errors.New("local daemon unavailable: connection refused (3)")}
	expectRelaySynthesizedIdleStatus(t, client.Notifications(), threadID, "codex:"+threadID, false)
	expectRelayGaveUpNotice(t, client.Notifications(), threadID, "codex:"+threadID, turnID, 8, "connection refused (3)")
	expectRelayResync(t, client.Notifications(), threadID, "codex:"+threadID)
	// The cause is recorded in the hub's log: the target, how many re-dials
	// failed, and the last one's error.
	select {
	case line := <-logged:
		for _, want := range []string{"codex:" + threadID, "3", "connection refused (3)"} {
			if !strings.Contains(line, want) {
				t.Fatalf("give-up log = %q, want it to name %q", line, want)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("give-up logged nothing")
	}
	retryClock.releaseWait(t, 400*time.Millisecond)

	// The loop keeps retrying afterward (recovery is still worth having if
	// the reader clicks "Reconnect & retry" and a fresh relay never
	// replaces this one before it retires) but must not re-broadcast the
	// same pair it already reported.
	awaitRelaySubscribeCall(t, subscribeCalls)
	results <- relaySubscribeResult{err: errors.New("local daemon unavailable: connection refused (4)")}
	select {
	case got := <-client.Notifications():
		t.Fatalf("unexpected third notification after give-up: %+v", got)
	case <-time.After(150 * time.Millisecond):
	}
	retryClock.expectWait(t, 800*time.Millisecond)
}

// TestHubRelayGivesUpAnchorsAtHistoryStartWhenNothingWasForwarded covers the
// other half of the notice-anchor rule: when the relay never forwarded any
// history item before giving up, there is no preceding entry, so the notice
// anchors at the start of history (entry 0), not at some earlier entry the
// hub happens to remember from nowhere.
func TestHubRelayGivesUpAnchorsAtHistoryStartWhenNothingWasForwarded(t *testing.T) {
	const threadID = "th_dead_before_history"
	const turnID = "turn_dead_early"
	results := make(chan relaySubscribeResult)
	subscribeCalls := make(chan struct{})
	retryClock := newScriptedRelayRetryClock()
	source := &scriptedRelaySource{
		thread:         codexRelayThread(threadID),
		results:        results,
		subscribeCalls: subscribeCalls,
	}
	srv := httptest.NewUnstartedServer(nil)
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	cfg.RelayHooks.RetryWait = retryClock.Wait
	logged := make(chan string, 16)
	cfg.Logf = func(format string, args ...any) { logged <- fmt.Sprintf(format, args...) }
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPCInitialized(t, srv)
	readErr := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true})
		readErr <- err
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	notifications := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: notifications}
	select {
	case err := <-readErr:
		if err != nil {
			t.Fatalf("ThreadRead: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial ThreadRead")
	}

	// The turn opens, but the daemon dies before recording anything: the
	// relay forwards a running status and nothing else.
	notifications <- relayActiveStatusNotification(t, threadID, turnID)
	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyThreadStatusChanged {
			t.Fatalf("notification method=%q, want %q", got.Method, appwire.NotifyThreadStatusChanged)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for thread/status/changed")
	}
	close(notifications)

	awaitRelaySubscribeCall(t, subscribeCalls)
	results <- relaySubscribeResult{err: errors.New("local daemon unavailable: connection refused (1)")}
	retryClock.releaseWait(t, 100*time.Millisecond)
	awaitRelaySubscribeCall(t, subscribeCalls)
	results <- relaySubscribeResult{err: errors.New("local daemon unavailable: connection refused (2)")}
	retryClock.releaseWait(t, 200*time.Millisecond)

	awaitRelaySubscribeCall(t, subscribeCalls)
	results <- relaySubscribeResult{err: errors.New("local daemon unavailable: connection refused (3)")}
	expectRelaySynthesizedIdleStatus(t, client.Notifications(), threadID, "codex:"+threadID, false)
	expectRelayGaveUpNotice(t, client.Notifications(), threadID, "codex:"+threadID, turnID, 0, "connection refused (3)")
	expectRelayResync(t, client.Notifications(), threadID, "codex:"+threadID)
	select {
	case <-logged:
	case <-time.After(time.Second):
		t.Fatal("give-up logged nothing")
	}
}

// TestHubRelayNoSyntheticFailureWithoutActiveTurn covers the scoping half of
// kata 3h02's fix: a daemon that dies BETWEEN turns (no spinner on screen,
// nothing for the reader to be confused by) must not manufacture a failed
// turn out of nothing. The relay keeps retrying in silence exactly as
// before - there is no turn id to attach a failure to, and no ambiguity for
// the reader to resolve.
func TestHubRelayNoSyntheticFailureWithoutActiveTurn(t *testing.T) {
	const threadID = "th_dead_between_turns"
	results := make(chan relaySubscribeResult)
	subscribeCalls := make(chan struct{})
	retryClock := newScriptedRelayRetryClock()
	source := &scriptedRelaySource{
		thread:         codexRelayThread(threadID),
		results:        results,
		subscribeCalls: subscribeCalls,
	}
	srv := httptest.NewUnstartedServer(nil)
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	cfg.RelayHooks.RetryWait = retryClock.Wait
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPCInitialized(t, srv)
	readErr := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true})
		readErr <- err
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	notifications := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: notifications}
	select {
	case err := <-readErr:
		if err != nil {
			t.Fatalf("ThreadRead: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial ThreadRead")
	}
	close(notifications)

	for i, delay := range []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
	} {
		awaitRelaySubscribeCall(t, subscribeCalls)
		results <- relaySubscribeResult{err: fmt.Errorf("local daemon unavailable: connection refused (%d)", i+1)}
		retryClock.releaseWait(t, delay)
	}
	select {
	case got := <-client.Notifications():
		t.Fatalf("unexpected notification with no active turn: %+v", got)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestHubRPCThreadReadRecoveryBacksOffUnusableChannelsWithoutDroppingFirstNotification(t *testing.T) {
	const threadID = "th_retry_unusable"
	results := make(chan relaySubscribeResult)
	subscribeCalls := make(chan struct{})
	retryClock := newScriptedRelayRetryClock()
	source := &scriptedRelaySource{
		thread:         codexRelayThread(threadID),
		results:        results,
		subscribeCalls: subscribeCalls,
	}
	srv := httptest.NewUnstartedServer(nil)
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	cfg.RelayHooks.RetryWait = retryClock.Wait
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPCInitialized(t, srv)
	readErr := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true})
		readErr <- err
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	established := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: established}
	select {
	case err := <-readErr:
		if err != nil {
			t.Fatalf("ThreadRead: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial ThreadRead")
	}

	close(established)
	awaitRelaySubscribeCall(t, subscribeCalls)
	results <- relaySubscribeResult{notifications: nil}
	retryClock.releaseWait(t, 100*time.Millisecond)

	awaitRelaySubscribeCall(t, subscribeCalls)
	closed := make(chan appwire.Notification)
	close(closed)
	results <- relaySubscribeResult{notifications: closed}
	retryClock.releaseWait(t, 200*time.Millisecond)

	awaitRelaySubscribeCall(t, subscribeCalls)
	buffered := make(chan appwire.Notification, 1)
	buffered <- relayDeltaNotification(t, threadID, "first notification")
	close(buffered)
	results <- relaySubscribeResult{notifications: buffered}
	expectRelayResync(t, client.Notifications(), threadID, "codex:"+threadID)
	expectRelayDelta(t, client.Notifications(), "first notification")

	awaitRelaySubscribeCall(t, subscribeCalls)
	results <- relaySubscribeResult{err: errors.New("failed after usable channel")}
	retryClock.expectWait(t, 100*time.Millisecond)
}

func TestHubRPCThreadReadClientCloseCancelsRelayRecoveryWait(t *testing.T) {
	const threadID = "th_retry_cancel"
	results := make(chan relaySubscribeResult)
	subscribeCalls := make(chan struct{})
	retryClock := newScriptedRelayRetryClock()
	source := &scriptedRelaySource{
		thread:         codexRelayThread(threadID),
		results:        results,
		subscribeCalls: subscribeCalls,
	}
	idleExit := make(chan struct{})
	afterIdleDelete := make(chan struct{})
	var idleOnce sync.Once
	var afterIdleOnce sync.Once
	srv := httptest.NewUnstartedServer(nil)
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	cfg.RelayHooks.RetryWait = retryClock.Wait
	cfg.RelayHooks.IdleExit = func(gotThreadID string) {
		if gotThreadID == threadID {
			idleOnce.Do(func() { close(idleExit) })
		}
	}
	cfg.RelayHooks.AfterIdleDelete = func(gotThreadID string) {
		if gotThreadID == threadID {
			afterIdleOnce.Do(func() { close(afterIdleDelete) })
		}
	}
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	mustInitializeHubClient(t, client)
	readErr := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true})
		readErr <- err
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	notifications := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: notifications}
	select {
	case err := <-readErr:
		if err != nil {
			t.Fatalf("ThreadRead: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial ThreadRead")
	}

	close(notifications)
	awaitRelaySubscribeCall(t, subscribeCalls)
	results <- relaySubscribeResult{err: errors.New("recovery failed")}
	wait := retryClock.nextWait(t)
	if wait.delay != relayRetryMinDelay {
		t.Fatalf("relay retry delay=%s, want %s", wait.delay, relayRetryMinDelay)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("client close: %v", err)
	}
	select {
	case <-idleExit:
	case <-time.After(time.Second):
		t.Fatal("relay did not observe zero subscribers while retry waited")
	}
	select {
	case <-afterIdleDelete:
	case <-time.After(time.Second):
		t.Fatal("relay handle was not removed while retry waited")
	}
	select {
	case <-wait.canceled:
	case <-time.After(time.Second):
		t.Fatal("relay retry wait was not canceled")
	}
	select {
	case <-subscribeCalls:
		t.Fatal("relay subscribed again after recovery cancellation")
	default:
	}

	replacementClient := dialHubRPC(t, srv)
	defer replacementClient.Close()
	if _, err := replacementClient.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize replacement client: %v", err)
	}
	replacementReadErr := make(chan error, 1)
	go func() {
		_, err := replacementClient.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true})
		replacementReadErr <- err
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	replacementNotifications := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: replacementNotifications}
	select {
	case err := <-replacementReadErr:
		if err != nil {
			t.Fatalf("replacement ThreadRead: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for replacement ThreadRead")
	}
}

func TestHubRPCThreadReadClientCloseCancelsBlockingRecoverySubscribe(t *testing.T) {
	const threadID = "th_blocking_recovery_cancel"
	established := make(chan appwire.Notification)
	replacementResults := make(chan relaySubscribeResult)
	source := &blockingRecoveryRelaySource{
		thread:             codexRelayThread(threadID),
		established:        established,
		recoveryStarted:    make(chan struct{}),
		recoveryCanceled:   make(chan struct{}),
		recoveryReturned:   make(chan struct{}),
		replacementStarted: make(chan struct{}),
		replacementResults: replacementResults,
	}
	idleExit := make(chan struct{})
	afterIdleDelete := make(chan struct{})
	var idleOnce sync.Once
	var deleteOnce sync.Once
	srv := httptest.NewUnstartedServer(nil)
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	cfg.RelayHooks.IdleExit = func(gotThreadID string) {
		if gotThreadID == threadID {
			idleOnce.Do(func() { close(idleExit) })
		}
	}
	cfg.RelayHooks.AfterIdleDelete = func(gotThreadID string) {
		if gotThreadID == threadID {
			deleteOnce.Do(func() { close(afterIdleDelete) })
		}
	}
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	mustInitializeHubClient(t, client)
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	close(established)
	select {
	case <-source.recoveryStarted:
	case <-time.After(time.Second):
		t.Fatal("blocking recovery subscribe did not start")
	}
	if err := client.Close(); err != nil {
		t.Fatalf("client close: %v", err)
	}
	select {
	case <-idleExit:
	case <-time.After(time.Second):
		t.Fatal("relay did not service idle tick during blocking recovery subscribe")
	}
	select {
	case <-source.recoveryCanceled:
	case <-time.After(time.Second):
		t.Fatal("blocking recovery source context was not canceled")
	}
	select {
	case <-source.recoveryReturned:
	case <-time.After(time.Second):
		t.Fatal("blocking recovery subscribe was not joined")
	}
	select {
	case <-afterIdleDelete:
	case <-time.After(time.Second):
		t.Fatal("blocking recovery relay handle was not removed")
	}

	replacementClient := dialHubRPC(t, srv)
	defer replacementClient.Close()
	if _, err := replacementClient.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize replacement: %v", err)
	}
	replacementRead := make(chan error, 1)
	go func() {
		_, err := replacementClient.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true})
		replacementRead <- err
	}()
	select {
	case <-source.replacementStarted:
	case <-time.After(time.Second):
		t.Fatal("fresh replacement relay did not subscribe")
	}
	replacementNotifications := make(chan appwire.Notification)
	replacementResults <- relaySubscribeResult{notifications: replacementNotifications}
	select {
	case err := <-replacementRead:
		if err != nil {
			t.Fatalf("replacement ThreadRead: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("fresh replacement relay did not become ready")
	}
}

func TestHubRPCThreadReadRereadJoinsRelayRecovery(t *testing.T) {
	const threadID = "th_retry_join"
	results := make(chan relaySubscribeResult)
	subscribeCalls := make(chan struct{})
	retryClock := newScriptedRelayRetryClock()
	source := &scriptedRelaySource{
		thread:         codexRelayThread(threadID),
		results:        results,
		subscribeCalls: subscribeCalls,
	}
	srv := httptest.NewUnstartedServer(nil)
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	cfg.RelayHooks.RetryWait = retryClock.Wait
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPCInitialized(t, srv)
	readErr := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true})
		readErr <- err
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	notificationsA := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: notificationsA}
	select {
	case err := <-readErr:
		if err != nil {
			t.Fatalf("ThreadRead: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial ThreadRead")
	}

	close(notificationsA)
	awaitRelaySubscribeCall(t, subscribeCalls)
	results <- relaySubscribeResult{err: errors.New("recovery failed")}
	wait := retryClock.nextWait(t)
	if wait.delay != relayRetryMinDelay {
		t.Fatalf("relay retry delay=%s, want %s", wait.delay, relayRetryMinDelay)
	}
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true}); err != nil {
		t.Fatalf("concurrent recovery ThreadRead: %v", err)
	}
	select {
	case <-subscribeCalls:
		t.Fatal("reread created a duplicate relay supervisor")
	default:
	}
	close(wait.release)
	awaitRelaySubscribeCall(t, subscribeCalls)
	notificationsB := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: notificationsB}
	expectRelayResync(t, client.Notifications(), threadID, "codex:"+threadID)
	notificationsB <- relayDeltaNotification(t, threadID, "after joined recovery")
	expectRelayDelta(t, client.Notifications(), "after joined recovery")
}

func TestHubRPCThreadReadReplacementStopsOldRelayRecovery(t *testing.T) {
	const oldThreadID = "th_retry_replaced"
	results := make(chan relaySubscribeResult)
	subscribeCalls := make(chan struct{})
	retryClock := newScriptedRelayRetryClock()
	oldSource := &scriptedRelaySource{
		thread: appwire.Thread{
			ID:        oldThreadID,
			SessionID: oldThreadID,
			Source:    "codex-a",
			Evener:    appwire.EvenerThread{Ref: "codex-a:" + oldThreadID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		id:             "codex-a",
		results:        results,
		subscribeCalls: subscribeCalls,
	}
	newSource := &relayBroadcastSource{
		id: "codex-b",
		thread: appwire.Thread{
			ID:        "th_active_replacement",
			SessionID: "th_active_replacement",
			Source:    "codex-b",
			Evener:    appwire.EvenerThread{Ref: "codex-b:th_active_replacement", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 1),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	idleExit := make(chan struct{})
	afterIdleDelete := make(chan struct{})
	var idleOnce sync.Once
	srv := httptest.NewUnstartedServer(nil)
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	cfg.RelayHooks.RetryWait = retryClock.Wait
	cfg.RelayHooks.IdleExit = func(threadID string) {
		if threadID == oldThreadID {
			idleOnce.Do(func() { close(idleExit) })
		}
	}
	cfg.RelayHooks.AfterIdleDelete = func(threadID string) {
		if threadID == oldThreadID {
			close(afterIdleDelete)
		}
	}
	web := NewWebServer(cfg)
	web.sources.Add(oldSource)
	web.sources.Add(newSource)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPCInitialized(t, srv)
	readErr := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex-a:" + oldThreadID, Subscribe: true, ReplaceSubscription: true})
		readErr <- err
	}()
	awaitRelaySubscribeCall(t, subscribeCalls)
	oldNotifications := make(chan appwire.Notification)
	results <- relaySubscribeResult{notifications: oldNotifications}
	select {
	case err := <-readErr:
		if err != nil {
			t.Fatalf("old ThreadRead: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for old ThreadRead")
	}
	close(oldNotifications)
	awaitRelaySubscribeCall(t, subscribeCalls)
	results <- relaySubscribeResult{err: errors.New("old recovery failed")}
	wait := retryClock.nextWait(t)

	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex-b:th_active_replacement", Subscribe: true, ReplaceSubscription: true}); err != nil {
		t.Fatalf("replacement ThreadRead: %v", err)
	}
	expectRelaySubscription(t, newSource.subscribed)
	select {
	case <-idleExit:
	case <-time.After(time.Second):
		t.Fatal("replaced relay did not reach zero-subscriber idle retirement")
	}
	select {
	case <-afterIdleDelete:
	case <-time.After(time.Second):
		t.Fatal("replaced relay handle was not removed")
	}
	select {
	case <-wait.canceled:
	case <-time.After(time.Second):
		t.Fatal("replaced relay recovery wait was not canceled")
	}
	select {
	case <-subscribeCalls:
		t.Fatal("replaced relay subscribed again after idle retirement")
	default:
	}

	newSource.notifications <- relayDeltaNotification(t, "th_active_replacement", "active replacement")
	expectRelayDelta(t, client.Notifications(), "active replacement")
}

func TestHubRPCThreadReadReplaceSubscriptionDropsPreviousRelaySubscriber(t *testing.T) {
	sourceA := &relayBroadcastSource{
		id: "codex-a",
		thread: appwire.Thread{
			ID:        "th_a",
			SessionID: "th_a",
			Source:    "codex-a",
			Evener:    appwire.EvenerThread{Ref: "codex-a:th_a", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	sourceB := &relayBroadcastSource{
		id: "codex-b",
		thread: appwire.Thread{
			ID:        "th_b",
			SessionID: "th_b",
			Source:    "codex-b",
			Evener:    appwire.EvenerThread{Ref: "codex-b:th_b", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	srv := newWebSourceTestServer(t, sourceA, sourceB)

	client := dialHubRPCInitialized(t, srv)
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex-a:th_a", Subscribe: true, ReplaceSubscription: true}); err != nil {
		t.Fatalf("ThreadRead sourceA: %v", err)
	}
	expectRelaySubscription(t, sourceA.subscribed)
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex-b:th_b", Subscribe: true, ReplaceSubscription: true}); err != nil {
		t.Fatalf("ThreadRead sourceB: %v", err)
	}
	expectRelaySubscription(t, sourceB.subscribed)

	sourceA.notifications <- appwire.Notification{
		Method: appwire.NotifyOverlayDelta,
		Params: testRawJSON(t, appwire.OverlayDeltaParams{
			ThreadID: "th_a",
			Ref:      "codex-a:th_a",
			Delta:    "from source a",
		}),
	}
	// Send sourceB notification immediately after sourceA. If the replaced
	// subscription leaked, sourceA's notification would arrive first; the
	// client must receive sourceB's as the first notification.
	sourceB.notifications <- appwire.Notification{
		Method: appwire.NotifyOverlayDelta,
		Params: testRawJSON(t, appwire.OverlayDeltaParams{
			ThreadID: "th_b",
			Ref:      "codex-b:th_b",
			Delta:    "from source b",
		}),
	}
	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyOverlayDelta {
			t.Fatalf("notification method=%q", got.Method)
		}
		var params appwire.OverlayDeltaParams
		if err := json.Unmarshal(got.Params, &params); err != nil {
			t.Fatalf("unmarshal params: %v", err)
		}
		if params.Ref != "codex-b:th_b" {
			t.Fatalf("client received notification for replaced subscription before sentinel: ref=%q", params.Ref)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for active replacement subscription notification")
	}
	select {
	case <-sourceA.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("replaced relay subscriber did not retire the old source relay")
	}
}

func TestHubRPCThreadReadAdditiveSubscriptionsReceiveBothRelays(t *testing.T) {
	sourceA := &relayBroadcastSource{
		id: "codex-a",
		thread: appwire.Thread{
			ID:        "th_a",
			SessionID: "th_a",
			Source:    "codex-a",
			Evener:    appwire.EvenerThread{Ref: "codex-a:th_a", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	sourceB := &relayBroadcastSource{
		id: "codex-b",
		thread: appwire.Thread{
			ID:        "th_b",
			SessionID: "th_b",
			Source:    "codex-b",
			Evener:    appwire.EvenerThread{Ref: "codex-b:th_b", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	srv := newWebSourceTestServer(t, sourceA, sourceB)

	client := dialHubRPCInitialized(t, srv)
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex-a:th_a", Subscribe: true}); err != nil {
		t.Fatalf("ThreadRead sourceA: %v", err)
	}
	expectRelaySubscription(t, sourceA.subscribed)
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex-b:th_b", Subscribe: true}); err != nil {
		t.Fatalf("ThreadRead sourceB: %v", err)
	}
	expectRelaySubscription(t, sourceB.subscribed)

	sourceA.notifications <- appwire.Notification{
		Method: appwire.NotifyOverlayDelta,
		Params: testRawJSON(t, appwire.OverlayDeltaParams{
			ThreadID: "th_a",
			Ref:      "codex-a:th_a",
			Delta:    "from source a",
		}),
	}
	sourceB.notifications <- appwire.Notification{
		Method: appwire.NotifyOverlayDelta,
		Params: testRawJSON(t, appwire.OverlayDeltaParams{
			ThreadID: "th_b",
			Ref:      "codex-b:th_b",
			Delta:    "from source b",
		}),
	}

	gotRefs := make(map[string]bool, 2)
	for len(gotRefs) < 2 {
		select {
		case got := <-client.Notifications():
			if got.Method != appwire.NotifyOverlayDelta {
				continue
			}
			var params appwire.OverlayDeltaParams
			if err := json.Unmarshal(got.Params, &params); err != nil {
				t.Fatalf("unmarshal params: %v", err)
			}
			gotRefs[params.Ref] = true
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for both relay notifications; got refs %v", gotRefs)
		}
	}
	if !gotRefs["codex-a:th_a"] || !gotRefs["codex-b:th_b"] {
		t.Fatalf("received refs %v, want both additive subscriptions", gotRefs)
	}
}

func TestHubRPCThreadReadRetiresRelayWhenClientDisconnects(t *testing.T) {
	source := &relayLifecycleSource{
		thread: appwire.Thread{
			ID:        "th_1",
			SessionID: "th_1",
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:th_1", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		canceled: make(chan struct{}),
	}
	srv := httptest.NewUnstartedServer(nil)
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	mustInitializeHubClient(t, client)
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_1"}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("client close: %v", err)
	}

	select {
	case <-source.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("source relay context was not canceled after client disconnect")
	}
}

func TestHubRPCThreadReadKeepsRelayWhenSubscriberArrivesDuringIdleRetirement(t *testing.T) {
	source := &relayBroadcastSource{
		thread: appwire.Thread{
			ID:        "th_1",
			SessionID: "th_1",
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:th_1", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		canceled:      make(chan struct{}, 2),
	}
	srv := httptest.NewUnstartedServer(nil)

	idleReached := make(chan struct{})
	releaseIdle := make(chan struct{})
	var idleOnce sync.Once
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	cfg.RelayHooks.IdleExit = func(threadID string) {
		if threadID != "th_1" {
			return
		}
		idleOnce.Do(func() { close(idleReached) })
		<-releaseIdle
	}
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client1 := dialHubRPC(t, srv)
	if _, err := client1.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize client1: %v", err)
	}
	if _, err := client1.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_1"}); err != nil {
		t.Fatalf("ThreadRead client1: %v", err)
	}
	if err := client1.Close(); err != nil {
		t.Fatalf("client1 close: %v", err)
	}

	select {
	case <-idleReached:
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not reach idle retirement window")
	}

	client2 := dialHubRPC(t, srv)
	defer client2.Close()
	if _, err := client2.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize client2: %v", err)
	}
	if _, err := client2.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_1"}); err != nil {
		t.Fatalf("ThreadRead client2: %v", err)
	}
	close(releaseIdle)

	source.notifications <- appwire.Notification{
		Method: appwire.NotifyOverlayDelta,
		Params: testRawJSON(t, appwire.OverlayDeltaParams{
			ThreadID: "th_1",
			Ref:      "codex:th_1",
			Delta:    "still live",
		}),
	}

	select {
	case got := <-client2.Notifications():
		if got.Method != appwire.NotifyOverlayDelta {
			t.Fatalf("method=%q", got.Method)
		}
	case <-source.canceled:
		t.Fatal("relay was canceled despite a new subscriber")
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for notification after idle-race subscriber")
	}
}

func TestHubRPCThreadReadSerializesRereadRegistrationAgainstIdleRetirement(t *testing.T) {
	const threadID = "th_join_idle"
	source := &relayBroadcastSource{
		thread:        codexRelayThread(threadID),
		notifications: make(chan appwire.Notification, 1),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	reregistrationReached := make(chan struct{})
	releaseReregistration := make(chan struct{})
	idleReached := make(chan struct{})
	var registrationOnce sync.Once
	var idleOnce sync.Once
	srv := httptest.NewUnstartedServer(nil)
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	cfg.RelayHooks.BeforeExistingRegistration = func(gotThreadID string) {
		if gotThreadID == threadID {
			registrationOnce.Do(func() {
				close(reregistrationReached)
				<-releaseReregistration
			})
		}
	}
	cfg.RelayHooks.IdleExit = func(gotThreadID string) {
		if gotThreadID == threadID {
			idleOnce.Do(func() { close(idleReached) })
		}
	}
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client1 := dialHubRPC(t, srv)
	if _, err := client1.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize client1: %v", err)
	}
	if _, err := client1.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true}); err != nil {
		t.Fatalf("ThreadRead client1: %v", err)
	}
	expectRelaySubscription(t, source.subscribed)

	client2 := dialHubRPC(t, srv)
	defer client2.Close()
	if _, err := client2.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize client2: %v", err)
	}
	rereadResult := make(chan error, 1)
	go func() {
		_, err := client2.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID, Subscribe: true})
		rereadResult <- err
	}()
	select {
	case <-reregistrationReached:
	case <-time.After(time.Second):
		t.Fatal("reread did not reach post-ready pre-registration boundary")
	}
	if err := client1.Close(); err != nil {
		t.Fatalf("client1 close: %v", err)
	}
	select {
	case <-idleReached:
	case <-time.After(time.Second):
		t.Fatal("relay did not reach idle retirement while reread registration paused")
	}
	close(releaseReregistration)
	select {
	case err := <-rereadResult:
		if err != nil {
			t.Fatalf("reread ThreadRead: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reread registration did not complete")
	}

	source.notifications <- relayDeltaNotification(t, threadID, "joined before idle delete")
	expectRelayDelta(t, client2.Notifications(), "joined before idle delete")
	select {
	case <-source.canceled:
		t.Fatal("idle retirement canceled relay after serialized reread registration")
	default:
	}
}

func TestHubRPCThreadReadKeepsReplacementRelayTrackedAfterIdleCleanup(t *testing.T) {
	const threadID = "th_cleanup"
	source := &relayBroadcastSource{
		thread:        codexRelayThread(threadID),
		notifications: make(chan appwire.Notification, 4),
		subscribed:    make(chan struct{}, 4),
		canceled:      make(chan struct{}, 2),
	}
	srv := httptest.NewUnstartedServer(nil)

	afterIdleDelete := make(chan struct{})
	releaseCleanup := make(chan struct{})
	cleanupDone := make(chan struct{})
	var idleOnce sync.Once
	var cleanupOnce sync.Once
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	cfg.RelayHooks.AfterIdleDelete = func(threadID string) {
		if threadID != "th_cleanup" {
			return
		}
		idleOnce.Do(func() { close(afterIdleDelete) })
		<-releaseCleanup
		cleanupOnce.Do(func() { close(cleanupDone) })
	}
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client1 := dialHubRPC(t, srv)
	if _, err := client1.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize client1: %v", err)
	}
	if _, err := client1.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID}); err != nil {
		t.Fatalf("ThreadRead client1: %v", err)
	}
	expectRelaySubscription(t, source.subscribed)
	if err := client1.Close(); err != nil {
		t.Fatalf("client1 close: %v", err)
	}

	select {
	case <-afterIdleDelete:
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not reach post-delete cleanup window")
	}

	client2 := dialHubRPC(t, srv)
	defer client2.Close()
	if _, err := client2.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize client2: %v", err)
	}
	if _, err := client2.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID}); err != nil {
		t.Fatalf("ThreadRead client2: %v", err)
	}
	expectRelaySubscription(t, source.subscribed)
	close(releaseCleanup)
	select {
	case <-cleanupDone:
	case <-time.After(2 * time.Second):
		t.Fatal("idle cleanup goroutine did not complete")
	}
	drainRelaySubscriptions(source.subscribed)

	if _, err := client2.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID}); err != nil {
		t.Fatalf("ThreadRead client2 again: %v", err)
	}
	select {
	case <-source.subscribed:
		t.Fatal("second read started a duplicate replacement relay")
	default:
	}
}

func TestHubRPCThreadReadPropagatesInFlightRelaySubscribeFailure(t *testing.T) {
	thread := appwire.Thread{
		ID:        "th_subscribe_fail",
		SessionID: "th_subscribe_fail",
		Source:    "codex",
		Evener:    appwire.EvenerThread{Ref: "codex:th_subscribe_fail", Capabilities: appwire.ThreadCapabilities{Send: true}},
	}
	source := &blockingFailingRelaySource{
		thread:  thread,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	waiterJoined := make(chan struct{}, 1)
	previousObserveWait := observeHubRelayWait
	observeHubRelayWait = func() { waiterJoined <- struct{}{} }
	t.Cleanup(func() { observeHubRelayWait = previousObserveWait })
	srv := httptest.NewUnstartedServer(nil)
	cfg := hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")}
	web := NewWebServer(cfg)
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client1 := dialHubRPC(t, srv)
	defer client1.Close()
	if _, err := client1.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize client1: %v", err)
	}
	client2 := dialHubRPC(t, srv)
	defer client2.Close()
	if _, err := client2.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize client2: %v", err)
	}

	readErrs := make(chan error, 2)
	go func() {
		_, err := client1.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_subscribe_fail"})
		readErrs <- err
	}()
	select {
	case <-source.started:
	case <-time.After(time.Second):
		t.Fatal("first relay subscribe did not start")
	}

	go func() {
		_, err := client2.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_subscribe_fail"})
		readErrs <- err
	}()
	select {
	case <-waiterJoined:
	case <-time.After(time.Second):
		t.Fatal("concurrent read did not join in-flight relay subscribe")
	}
	close(source.release)

	for range 2 {
		select {
		case err := <-readErrs:
			if err == nil || !strings.Contains(err.Error(), "subscribe failed") {
				t.Fatalf("read error=%v, want subscribe failed", err)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for relay subscribe error")
		}
	}
	if calls := source.subscribeCalls(); calls != 1 {
		t.Fatalf("subscribe calls=%d, want 1", calls)
	}
}

func TestHubRPCThreadReadSubscribeFailureDoesNotLeaveClientSubscribed(t *testing.T) {
	threadID := "th_retry_subscribe"
	source := &failFirstRelaySource{
		thread:        codexRelayThread(threadID),
		notifications: make(chan appwire.Notification, 4),
		canceled:      make(chan struct{}, 2),
	}
	srv := newWebSourceTestServer(t, source)

	failedClient := dialHubRPC(t, srv)
	defer failedClient.Close()
	if _, err := failedClient.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize failedClient: %v", err)
	}
	if _, err := failedClient.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID}); err == nil || !strings.Contains(err.Error(), "subscribe failed once") {
		t.Fatalf("ThreadRead failedClient error=%v, want subscribe failed once", err)
	}

	okClient := dialHubRPC(t, srv)
	defer okClient.Close()
	if _, err := okClient.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize okClient: %v", err)
	}
	if _, err := okClient.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "codex:" + threadID}); err != nil {
		t.Fatalf("ThreadRead okClient: %v", err)
	}
	source.notifications <- appwire.Notification{
		Method: appwire.NotifyOverlayDelta,
		Params: testRawJSON(t, appwire.OverlayDeltaParams{
			ThreadID: threadID,
			Ref:      "codex:" + threadID,
			Delta:    "after retry",
		}),
	}
	select {
	case got := <-okClient.Notifications():
		if got.Method != appwire.NotifyOverlayDelta {
			t.Fatalf("okClient method=%q", got.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for retry relay notification")
	}
	// Send a sentinel and wait for okClient to receive it. After okClient
	// receives the sentinel the relay pipeline has fully flushed: any
	// notification that failedClient (if wrongly subscribed) would have
	// received from the first broadcast is already in its buffer, because
	// Broadcast enqueues to all subscribers synchronously before moving on.
	source.notifications <- appwire.Notification{
		Method: appwire.NotifyOverlayDelta,
		Params: testRawJSON(t, appwire.OverlayDeltaParams{
			ThreadID: threadID,
			Ref:      "codex:" + threadID,
			Delta:    "sentinel",
		}),
	}
	select {
	case <-okClient.Notifications():
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for sentinel notification")
	}
	select {
	case got := <-failedClient.Notifications():
		t.Fatalf("failed client received stale relay notification: %+v", got)
	default:
	}
}

// recordingSubscribeParamsSource is a non-atomic relay source that records the
// params of every SubscribeThread call, so a recovery re-subscribe can be
// inspected. ID is "host", standing in for a federated source.
type recordingSubscribeParamsSource struct {
	relayLifecycleSource
	results        <-chan relaySubscribeResult
	subscribeCalls chan<- struct{}

	mu     sync.Mutex
	params []appwire.ThreadReadParams
}

func (s *recordingSubscribeParamsSource) ID() string { return "host" }

func (s *recordingSubscribeParamsSource) SubscribeThread(ctx context.Context, params appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	s.mu.Lock()
	s.params = append(s.params, params)
	s.mu.Unlock()
	if s.subscribeCalls != nil {
		s.subscribeCalls <- struct{}{}
	}
	result := <-s.results
	return result.notifications, result.err
}

func (s *recordingSubscribeParamsSource) subscribeParams() []appwire.ThreadReadParams {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]appwire.ThreadReadParams(nil), s.params...)
}

type blockingRecoveryRelaySource struct {
	relayLifecycleSource
	mu                 sync.Mutex
	calls              int
	established        <-chan appwire.Notification
	recoveryStarted    chan struct{}
	recoveryCanceled   chan struct{}
	recoveryReturned   chan struct{}
	replacementStarted chan struct{}
	replacementResults <-chan relaySubscribeResult
}

func (s *blockingRecoveryRelaySource) SubscribeThread(ctx context.Context, _ appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	switch call {
	case 1:
		return s.established, nil
	case 2:
		close(s.recoveryStarted)
		<-ctx.Done()
		close(s.recoveryCanceled)
		close(s.recoveryReturned)
		return nil, ctx.Err()
	default:
		if call == 3 {
			close(s.replacementStarted)
		}
		select {
		case result := <-s.replacementResults:
			return result.notifications, result.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

type relayRetryWait struct {
	delay    time.Duration
	release  chan struct{}
	canceled chan struct{}
}

type scriptedRelayRetryClock struct {
	waits chan relayRetryWait
}

func newScriptedRelayRetryClock() *scriptedRelayRetryClock {
	return &scriptedRelayRetryClock{waits: make(chan relayRetryWait)}
}

func (c *scriptedRelayRetryClock) Wait(ctx context.Context, delay time.Duration) error {
	wait := relayRetryWait{delay: delay, release: make(chan struct{}), canceled: make(chan struct{})}
	select {
	case c.waits <- wait:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-wait.release:
		return nil
	case <-ctx.Done():
		close(wait.canceled)
		return ctx.Err()
	}
}

func (c *scriptedRelayRetryClock) nextWait(t *testing.T) relayRetryWait {
	t.Helper()
	select {
	case wait := <-c.waits:
		return wait
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for relay retry delay")
		return relayRetryWait{}
	}
}

func (c *scriptedRelayRetryClock) releaseWait(t *testing.T, want time.Duration) {
	t.Helper()
	wait := c.nextWait(t)
	if wait.delay != want {
		t.Fatalf("relay retry delay=%s, want %s", wait.delay, want)
	}
	close(wait.release)
}

func (c *scriptedRelayRetryClock) expectWait(t *testing.T, want time.Duration) {
	t.Helper()
	wait := c.nextWait(t)
	if wait.delay != want {
		t.Fatalf("relay retry delay=%s, want %s", wait.delay, want)
	}
}

func expectRelayResync(t *testing.T, notifications <-chan appwire.Notification, wantThreadID, wantRef string) {
	t.Helper()
	select {
	case got := <-notifications:
		if got.Method != appwire.NotifyEvenerThreadResync {
			t.Fatalf("recovery notification method=%q, want %q", got.Method, appwire.NotifyEvenerThreadResync)
		}
		expectResyncParams(t, got, wantThreadID, wantRef)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for thread resync after relay recovery")
	}
}

// relayResyncTimeout is the bound awaitRelayResync waits: the requested timeout,
// raised to relayResyncRaceFloor under the race detector. The relay's ordered
// goroutines run ~10x slower under -race, so a request-sized bound can expire
// while the resync is genuinely on its way (issue #2977). Outside -race the
// requested bound stands. The floor is a build-tagged constant, not t.Deadline:
// the CI shards run a prebuilt test binary directly with no -test.timeout, so a
// test there has no deadline to scale against.
func relayResyncTimeout(requested time.Duration) time.Duration {
	return max(requested, relayResyncRaceFloor)
}

// awaitRelayResync is expectRelayResync for a client that also receives other
// traffic (navigation invalidations, status frames): those are skipped, and
// the resync may take as long as timeout to arrive.
//
// The resync is awaited on its channel; the bound is only a tripwire. It is
// scaled under -race (relayResyncTimeout) because a fixed wall-clock bound can
// expire before a resync that is genuinely on its way — which is what failed
// under -race on a loaded runner.
func awaitRelayResync(t *testing.T, notifications <-chan appwire.Notification, wantThreadID, wantRef string, timeout time.Duration) {
	t.Helper()
	bound := relayResyncTimeout(timeout)
	timer := time.After(bound)
	for {
		select {
		case got := <-notifications:
			if got.Method != appwire.NotifyEvenerThreadResync {
				continue
			}
			expectResyncParams(t, got, wantThreadID, wantRef)
			return
		case <-timer:
			t.Fatalf("no thread resync for %s within %v", wantRef, bound)
		}
	}
}

// TestRelayResyncTimeout pins the scaling in both build modes: under -race a
// request-sized bound is raised to the floor, and a larger bound is untouched.
func TestRelayResyncTimeout(t *testing.T) {
	want := time.Millisecond
	if raceDetectorEnabled {
		want = relayResyncRaceFloor
	}
	if got := relayResyncTimeout(time.Millisecond); got != want {
		t.Errorf("relayResyncTimeout(1ms) = %v, want %v", got, want)
	}
	if got := relayResyncTimeout(time.Hour); got != time.Hour {
		t.Errorf("relayResyncTimeout(1h) = %v, want 1h", got)
	}
}

// TestAwaitRelayResyncWaitsPastTheRequestedBound pins the -race scaling: a
// resync delivered after a small requested bound is still accepted because the
// wait is raised to relayResyncRaceFloor. Under the old fixed time.After(bound)
// this 200ms-late delivery against a 50ms bound failed at once (issue #2977).
func TestAwaitRelayResyncWaitsPastTheRequestedBound(t *testing.T) {
	if !raceDetectorEnabled {
		t.Skip("relay resync scaling is a -race guarantee; run with -race")
	}
	const (
		threadID = "late"
		ref      = "codex:late"
	)
	notifications := make(chan appwire.Notification, 1)
	go func() {
		time.Sleep(200 * time.Millisecond)
		notifications <- *appwire.NotificationMessage(appwire.NotifyEvenerThreadResync, appwire.ThreadResyncParams{
			ThreadID: threadID,
			Ref:      ref,
		}).Notification
	}()
	awaitRelayResync(t, notifications, threadID, ref, 50*time.Millisecond)
}

func expectResyncParams(t *testing.T, got appwire.Notification, wantThreadID, wantRef string) {
	t.Helper()
	var params appwire.ThreadResyncParams
	if err := json.Unmarshal(got.Params, &params); err != nil {
		t.Fatalf("unmarshal thread resync: %v", err)
	}
	want := appwire.ThreadResyncParams{ThreadID: wantThreadID, Ref: wantRef}
	if params != want {
		t.Fatalf("thread resync params=%+v, want %+v", params, want)
	}
}

func expectRelayDelta(t *testing.T, notifications <-chan appwire.Notification, want string) {
	t.Helper()
	select {
	case got := <-notifications:
		var params appwire.OverlayDeltaParams
		if err := json.Unmarshal(got.Params, &params); err != nil {
			t.Fatalf("unmarshal relay notification: %v", err)
		}
		if params.Delta != want {
			t.Fatalf("relay delta=%q, want %q", params.Delta, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for relay delta %q", want)
	}
}

type readRelayDisabledSource struct {
	relayBroadcastSource
}

func (s *readRelayDisabledSource) RelayOnThreadRead() bool {
	return false
}

type blockingFailingRelaySource struct {
	relayLifecycleSource
	mu      sync.Mutex
	calls   int
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

type failFirstRelaySource struct {
	relayBroadcastSource
	mu    sync.Mutex
	calls int
}

func (s *failFirstRelaySource) SubscribeThread(ctx context.Context, params appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	s.mu.Lock()
	s.calls++
	calls := s.calls
	s.mu.Unlock()
	if calls == 1 {
		return nil, errors.New("subscribe failed once")
	}
	return s.relayBroadcastSource.SubscribeThread(ctx, params)
}

func (s *blockingFailingRelaySource) SubscribeThread(ctx context.Context, _ appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return nil, errors.New("subscribe failed")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *blockingFailingRelaySource) subscribeCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func drainRelaySubscriptions(subscribed <-chan struct{}) {
	for {
		select {
		case <-subscribed:
		default:
			return
		}
	}
}
