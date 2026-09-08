package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

// TestHubRPCThreadStartPublishesOneThreadStartedAfterRelayAdmission uses a
// real appserver daemon behind LocalDaemonSource. The daemon deliberately
// emits no startup notification: this is the production ordering gap where
// the Hub's relay is attached after thread/start has already read the daemon.
// The status frame is an event-driven barrier proving the relay remains live
// while the count is checked, without sleeping to prove absence.
func TestHubRPCThreadStartPublishesOneThreadStartedAfterRelayAdmission(t *testing.T) {
	const sessionID = "034LSdkThreadStarted"
	const instanceID = "instance-sdk-thread-started"
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		appserver.Subscribe(ctx, sessionID)
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID: sessionID, SessionID: sessionID, Source: "local",
			Evener: appwire.EvenerThread{Ref: params.Ref, InstanceID: instanceID},
		}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	entry := rendezvous.Entry{
		PID: os.Getpid(), Protocol: appwire.ProtocolVersion,
		Endpoint: "ws" + daemonHTTP.URL[len("http"):], SourceID: "local",
		ThreadID: sessionID, SessionID: sessionID, InstanceID: instanceID,
		WorkspaceRef: "local:" + sessionID,
	}
	spawner := &fakeRPCSpawner{spawn: func(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
		writeRendezvous(t, runDir, entry)
		return entry, nil
	}}
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	response, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{Model: "openai/gpt-5", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	if response.Thread.ID != sessionID || response.Thread.Evener.Ref != entry.WorkspaceRef {
		t.Fatalf("thread/start response thread=%+v", response.Thread)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var startedCount int
	var started appwire.Notification
	for startedCount == 0 {
		select {
		case notification := <-client.Notifications():
			if notification.Method == appwire.NotifyThreadStarted {
				startedCount++
				started = notification
			}
		case <-waitCtx.Done():
			t.Fatal("thread/start did not deliver thread/started")
		}
	}
	var params appwire.ThreadStartedParams
	if err := json.Unmarshal(started.Params, &params); err != nil {
		t.Fatalf("decode thread/started: %v", err)
	}
	if params.Ref != entry.WorkspaceRef || params.ThreadID != sessionID || params.Thread.Evener.InstanceID != instanceID {
		t.Fatalf("thread/started params=%+v", params)
	}

	read, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: entry.WorkspaceRef, Subscribe: true, IncludeTurns: false})
	if err != nil {
		t.Fatalf("subscribed thread/read: %v", err)
	}
	if read.Thread.ID != response.Thread.ID || read.Thread.Evener.Ref != entry.WorkspaceRef || read.Thread.Evener.InstanceID != instanceID {
		t.Fatalf("subscribed thread/read thread=%+v", read.Thread)
	}

	// A same-instance original released after the fallback decision must not
	// create a second public startup event.
	daemon.Broadcast(sessionID, appwire.NotifyThreadStarted, appwire.ThreadStartedParams{
		ThreadID: sessionID, Ref: entry.WorkspaceRef,
		Thread: appwire.Thread{ID: sessionID, SessionID: sessionID, Source: "local", Evener: appwire.EvenerThread{Ref: entry.WorkspaceRef, InstanceID: instanceID}},
	})
	daemon.Broadcast(sessionID, appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{ThreadID: sessionID, Ref: entry.WorkspaceRef, Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}})
	for {
		select {
		case notification := <-client.Notifications():
			if notification.Method == appwire.NotifyThreadStarted {
				startedCount++
			}
			if notification.Method == appwire.NotifyThreadStatusChanged {
				if startedCount != 1 {
					t.Fatalf("thread/started count before status barrier=%d, want 1", startedCount)
				}
				goto differentInstance
			}
		case <-waitCtx.Done():
			t.Fatal("same-instance status barrier was not relayed")
		}
	}

differentInstance:
	// A later instance must remain visible; suppression is instance-scoped.
	daemon.Broadcast(sessionID, appwire.NotifyThreadStarted, appwire.ThreadStartedParams{
		ThreadID: sessionID, Ref: entry.WorkspaceRef,
		Thread: appwire.Thread{ID: sessionID, SessionID: sessionID, Source: "local", Evener: appwire.EvenerThread{Ref: entry.WorkspaceRef, InstanceID: "instance-sdk-thread-started-next"}},
	})
	daemon.Broadcast(sessionID, appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{ThreadID: sessionID, Ref: entry.WorkspaceRef, Status: appwire.ThreadStatus{Type: appwire.ThreadStatusAwaiting}})
	for {
		select {
		case notification := <-client.Notifications():
			if notification.Method == appwire.NotifyThreadStarted {
				startedCount++
				var next appwire.ThreadStartedParams
				if err := json.Unmarshal(notification.Params, &next); err != nil {
					t.Fatalf("decode different-instance thread/started: %v", err)
				}
				if next.Thread.Evener.InstanceID != "instance-sdk-thread-started-next" {
					t.Fatalf("different-instance event instance=%q", next.Thread.Evener.InstanceID)
				}
			}
			if notification.Method == appwire.NotifyThreadStatusChanged {
				if startedCount != 2 {
					t.Fatalf("different-instance event count=%d, want 2", startedCount)
				}
				return
			}
		case <-waitCtx.Done():
			t.Fatal("different-instance status barrier was not relayed")
		}
	}
}
