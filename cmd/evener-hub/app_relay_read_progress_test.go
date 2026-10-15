package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
	daemonserver "primeradiant.com/evener/server"
)

// This exercises the source's real response-cut acknowledgement barrier. A
// scripted lease returning a response independently of delivery acknowledgements
// cannot reproduce the circular wait between a read and its unbound child event.
func TestHubRelayRetiredChildDoesNotBlockRootRead(t *testing.T) {
	testHubRelayRetiredChildReadProgress(t, false, 1)
}

func TestHubRelayQueuedReadDoesNotHoldPreCutAcknowledgement(t *testing.T) {
	testHubRelayRetiredChildReadProgress(t, true, 1)
}

func TestHubRelayPendingTargetBurstMakesProgress(t *testing.T) {
	testHubRelayRetiredChildReadProgress(t, true, hubRelayPendingDeliveryLimit+1)
}

func testHubRelayRetiredChildReadProgress(t *testing.T, queuedRead bool, childEvents int) {
	t.Helper()
	// Not parallel: observes the package's relay functions and pending wait.
	const rootID, childID = "read-progress-root", "read-progress-child"
	const rootRef, childRef = "local:" + rootID, "local:" + childID
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)

	var relays hubRelayFunctions
	var pendingWait atomic.Bool
	previousFunctions, previousWait := observeHubRelayFunctions, observeHubRelayWait
	observeHubRelayFunctions = func(f hubRelayFunctions) { relays = f }
	observeHubRelayWait = func() { pendingWait.Store(true) }
	t.Cleanup(func() {
		observeHubRelayFunctions, observeHubRelayWait = previousFunctions, previousWait
	})

	daemon := daemonserver.NewServer(daemonserver.ServerConfig{})
	t.Cleanup(daemon.Close)
	prepared, err := daemonserver.PrepareAppIdentity("local", rootID, "")
	if err != nil {
		t.Fatal(err)
	}
	daemon.ReplaceAppIdentity(prepared.WithBootGeneration("1"), nil)
	daemon.SetStatus(daemonserver.StatusInfo{SessionID: rootID, State: "idle"})
	daemon.RecordDescendantAppEvent(rootID, events.SessionEvent{
		Kind: events.EventSessionStart, SessionID: childID, Data: events.SessionStartData{},
	})

	var gateArmed atomic.Bool
	gateEntered, gateRelease := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseGate := func() { releaseOnce.Do(func() { close(gateRelease) }) }
	daemon.AppServer().SetBeforeSubscriptionGate(func() {
		if gateArmed.CompareAndSwap(true, false) {
			close(gateEntered)
			select {
			case <-gateRelease:
			case <-ctx.Done():
			}
		}
	})
	t.Cleanup(releaseGate)
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.AppServer().ServeWebSocket))
	t.Cleanup(daemonHTTP.Close)
	entry := rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: daemonHTTP.URL, SourceID: "local", ThreadID: rootID, SessionID: rootID, WorkspaceRef: rootRef}
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{
			{Entry: entry, SessionID: rootID},
			{Entry: entry, SessionID: childID, OwnerSessionID: rootID, ReadOnlyAlias: true},
		}
	}, daemonHTTP.Client())
	sources := appsource.NewRegistry()
	sources.Add(source)
	idleDeleted := make(chan string, 4)
	hubServer := newHubAppServer(hubcore.WebConfig{
		HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex(""),
		RelayHooks: hubcore.RelayLifecycleHooks{AfterIdleDelete: func(id string) { idleDeleted <- id }},
	}, sources)
	hubHTTP := httptest.NewServer(http.HandlerFunc(hubServer.ServeWebSocket))
	t.Cleanup(hubHTTP.Close)
	t.Cleanup(func() { relays.stopCanonicalRelay(appwire.Ref{SourceID: "local", ThreadID: rootID}) })
	open := func() *appwire.Client {
		client := dialHubRPC(t, hubHTTP)
		t.Cleanup(func() { _ = client.Close() })
		if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
			t.Fatal(err)
		}
		return client
	}
	root, child := open(), open()
	for _, target := range []struct {
		client *appwire.Client
		ref    string
	}{{root, rootRef}, {child, childRef}} {
		if _, err := target.client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: target.ref, Subscribe: true, ReplaceSubscription: false}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := child.ThreadUnsubscribe(ctx, appwire.ThreadUnsubscribeParams{Ref: childRef}); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-idleDeleted:
		if id != rootID || relays.relayPublished(childRef) || !relays.relayPublished(rootRef) {
			t.Fatalf("unexpected retirement: canonical=%s child=%v root=%v", id, relays.relayPublished(childRef), relays.relayPublished(rootRef))
		}
	case <-ctx.Done():
		t.Fatal("child route did not retire")
	}
	if got := daemon.AppServer().SubscriberCount(childID); got != 1 {
		t.Fatalf("retained upstream child subscriptions = %d, want 1", got)
	}

	gateArmed.Store(true)
	type readOutcome struct {
		response appwire.ThreadReadResponse
		err      error
	}
	readDone := make(chan readOutcome, 1)
	go func() {
		response, err := root.ThreadRead(ctx, appwire.ThreadReadParams{Ref: rootRef, Subscribe: true})
		readDone <- readOutcome{response, err}
	}()
	select {
	case <-gateEntered:
	case <-ctx.Done():
		t.Fatal("root read did not reach the daemon subscription gate")
	}
	queuedDone := make(chan readOutcome, 1)
	if queuedRead {
		second := open()
		go func() {
			response, err := second.ThreadRead(ctx, appwire.ThreadReadParams{Ref: rootRef, Subscribe: true})
			queuedDone <- readOutcome{response, err}
		}()
		// No admission hook exists here. Observe ownership, not elapsed time,
		// before releasing the source command ahead of this one.
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for relays.relayCommandCount(rootRef) != 2 {
			select {
			case <-ticker.C:
			case <-ctx.Done():
				t.Fatal("second read did not acquire its hub owner")
			}
		}
	}
	// CommitProjection sends the real child notification before the gated read
	// can capture its response. The source publishes it at that response's cut.
	for range childEvents {
		daemon.RecordDescendantAppEvent(rootID, events.SessionEvent{
			Kind: events.EventExecutionStarted, SessionID: childID,
			Data: events.ExecutionStartedData{TurnID: "child-execution"},
		})
	}
	releaseGate()
	select {
	case result := <-readDone:
		if result.err != nil || result.response.Thread.ID != rootID {
			t.Fatalf("root read: thread=%s err=%v", result.response.Thread.ID, result.err)
		}
	case <-time.After(time.Second):
		var stacks bytes.Buffer
		_ = pprof.Lookup("goroutine").WriteTo(&stacks, 1)
		t.Fatalf("root read blocked after the response cut: pendingDelivery=%v commands=%d\n%s", pendingWait.Load(), relays.relayCommandCount(rootRef), stacks.String())
	}
	if queuedRead {
		select {
		case result := <-queuedDone:
			if result.err != nil || result.response.Thread.ID != rootID {
				t.Fatalf("queued root read: thread=%s err=%v", result.response.Thread.ID, result.err)
			}
		case <-ctx.Done():
			t.Fatal("queued root read did not finish")
		}
	}
	if _, err := root.ThreadRead(ctx, appwire.ThreadReadParams{Ref: rootRef, Subscribe: true}); err != nil {
		t.Fatalf("subsequent root read: %v", err)
	}

	// A following root note is the stream fence for both duplicate and
	// cross-target leakage checks. These events traverse the real projector.
	daemon.RecordAppEvent(events.SessionEvent{Kind: events.EventNotesUpdated, SessionID: rootID, Data: events.NotesUpdatedData{HumanNote: "root-sentinel"}})
	daemon.RecordAppEvent(events.SessionEvent{Kind: events.EventNotesUpdated, SessionID: rootID, Data: events.NotesUpdatedData{HumanNote: "root-fence"}})
	sentinels := 0
	for {
		select {
		case note, ok := <-root.Notifications():
			if !ok {
				t.Fatal("root notification stream closed")
			}
			if note.Method == appwire.NotifyThreadStatusChanged {
				var params appwire.ThreadStatusChangedParams
				if err := json.Unmarshal(note.Params, &params); err != nil {
					t.Fatal(err)
				}
				if params.ThreadID != rootID || params.Ref != rootRef {
					t.Fatalf("foreign status reached root: %+v", params)
				}
			}
			if note.Method != appwire.NotifyEvenerNotesUpdated {
				continue
			}
			var params appwire.NotesUpdatedParams
			if err := json.Unmarshal(note.Params, &params); err != nil {
				t.Fatal(err)
			}
			if params.ThreadID != rootID || params.Ref != rootRef {
				t.Fatalf("foreign note reached root: %+v", params)
			}
			if params.HumanNote == "root-sentinel" {
				sentinels++
			}
			if params.HumanNote == "root-fence" {
				if sentinels != 1 {
					t.Fatalf("root sentinel count = %d, want 1", sentinels)
				}
				return
			}
		case <-ctx.Done():
			t.Fatal("root live note did not arrive after the reads")
		}
	}
}
