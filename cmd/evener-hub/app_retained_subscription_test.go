package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

// Roster-only retained reads materialize their response through ListThreads.
// The barrier lets a second connection establish the live relay during that read.
type retainedHydrationSource struct {
	relayBroadcastSource
	live    atomic.Bool
	reading chan struct{}
	release chan struct{}
	exited  chan struct{}
	readErr error
}

func (s *retainedHydrationSource) ReadThread(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	if s.live.Load() {
		return s.relayBroadcastSource.ReadThread(ctx, params)
	}
	return appwire.ThreadReadResponse{}, appwire.SessionUnavailable("source is retained")
}

func (s *retainedHydrationSource) ListThreads(ctx context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	close(s.reading)
	defer close(s.exited)
	select {
	case <-s.release:
	case <-ctx.Done():
		return appwire.ThreadListResponse{}, ctx.Err()
	}
	if s.readErr != nil {
		return appwire.ThreadListResponse{}, s.readErr
	}
	thread := s.thread
	thread.Status.Type = appwire.ThreadStatusRestartRequired
	return appwire.ThreadListResponse{Data: []appwire.Thread{thread}}, nil
}

func TestHubRPCRetainedHydrationLifecycle(t *testing.T) {
	for _, boundary := range []string{"resume", "unsubscribe", "read-error", "close"} {
		t.Run(boundary, func(t *testing.T) {
			const id = "retained_session"
			source := &retainedHydrationSource{
				reading: make(chan struct{}), release: make(chan struct{}), exited: make(chan struct{}),
			}
			source.relayBroadcastSource = relayBroadcastSource{
				id:            "local",
				thread:        appwire.Thread{ID: id, SessionID: id, Source: "local", Evener: appwire.EvenerThread{Ref: "local:" + id}},
				notifications: make(chan appwire.Notification, 4), canceled: make(chan struct{}, 2),
			}
			if boundary == "read-error" {
				source.readErr = errors.New("saved metadata is unreadable")
			}
			runDir := t.TempDir()
			writeRendezvous(t, runDir, rendezvous.Entry{PID: 104, Protocol: appwire.ProtocolVersion, SourceID: "local", ThreadID: id, SessionID: id})
			roster := liveClaimRoster(runDir, fakeProber{sessionID: id, status: appwire.ThreadStatusRestartRequired})
			roster.Refresh()
			registry := appsource.NewRegistry()
			registry.Add(source)
			server := newHubAppServer(hubcore.WebConfig{Roster: roster, HubStateRoot: t.TempDir()}, registry)
			hub := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
			defer hub.Close()
			reader := dialHubRPC(t, hub)
			defer reader.Close()
			sender := dialHubRPC(t, hub)
			defer sender.Close()
			for _, client := range []*appwire.Client{reader, sender} {
				if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
					t.Fatal(err)
				}
			}
			readDone := make(chan error, 1)
			go func() {
				_, err := reader.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + id, Subscribe: true})
				readDone <- err
			}()
			select {
			case <-source.reading:
			case err := <-readDone:
				t.Fatalf("retained read ended before metadata projection: %v", err)
			case <-time.After(time.Second):
				t.Fatal("retained hydration did not begin")
			}
			source.live.Store(true)
			if _, err := sender.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + id, Subscribe: true}); err != nil {
				t.Fatal(err)
			}
			source.notifications <- *appwire.NotificationMessage(appwire.NotifyOverlayDelta, appwire.OverlayDeltaParams{ThreadID: id, Ref: "local:" + id, Delta: "during retained read"}).Notification
			expectRelayDelta(t, sender.Notifications(), "during retained read")
			select {
			case notification := <-reader.Notifications():
				t.Fatalf("event preceded saved response: %+v", notification)
			default:
			}
			switch boundary {
			case "unsubscribe":
				if _, err := reader.ThreadUnsubscribe(context.Background(), appwire.ThreadUnsubscribeParams{Ref: "local:" + id}); err != nil {
					t.Fatal(err)
				}
			case "close":
				reader.Close()
				select {
				case <-source.exited:
				case <-time.After(time.Second):
					t.Fatal("connection closure did not cancel retained materialization")
				}
			}
			close(source.release)
			select {
			case err := <-readDone:
				if (boundary == "read-error" || boundary == "close") != (err != nil) {
					t.Fatalf("read error=%v boundary=%s", err, boundary)
				}
			case <-time.After(time.Second):
				t.Fatal("read did not finish")
			}
			select {
			case <-source.exited:
			case <-time.After(time.Second):
				t.Fatal("retained source read leaked")
			}
			if boundary == "resume" {
				expectRelayDelta(t, reader.Notifications(), "during retained read")
			} else {
				// The sender remains subscribed; all withdrawn reader ownership is gone.
				deadline := time.Now().Add(time.Second)
				for server.SubscriberCount("local:"+id) != 1 && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if count := server.SubscriberCount("local:" + id); count != 1 {
					t.Fatalf("subscriber count=%d, want only live sender", count)
				}
				if boundary != "close" {
					select {
					case got := <-reader.Notifications():
						t.Fatalf("withdrawn hydration released event: %+v", got)
					default:
					}
				}
			}
		})
	}
}

func TestHubRPCSubscribedPastReadReceivesIndependentResume(t *testing.T) {
	root := t.TempDir()
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		appserver.Subscribe(ctx, sessionID)
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: sessionID, SessionID: sessionID, Source: "local", Evener: appwire.EvenerThread{Ref: params.Ref, Capabilities: appwire.ThreadCapabilities{Send: true}}}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnStart, func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_4"}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	spawner := &fakeRPCSpawner{
		resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
			if req.WorkingDir != workingDir {
				t.Fatalf("resume request=%+v", req)
			}
			entry := rendezvous.Entry{
				PID:        107,
				Protocol:   appwire.ProtocolVersion,
				Endpoint:   "ws" + daemonHTTP.URL[len("http"):],
				SourceID:   "local",
				ThreadID:   sessionID,
				SessionID:  sessionID,
				WorkingDir: workingDir,
			}
			writeRendezvous(t, runDir, entry)
			return entry, nil
		},
	}
	roster := hubcore.NewRoster(runDir, nil)
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner, Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	reader := dialHubRPC(t, hub)
	defer reader.Close()
	if _, err := reader.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	saved, err := reader.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true, Subscribe: true})
	if err != nil || len(saved.Thread.Turns) != 2 || saved.BootGeneration != appwire.DaemonlessBootGeneration {
		t.Fatalf("saved history=%+v err=%v", saved, err)
	}
	if len(roster.List()) != 0 {
		t.Fatal("reading saved history launched a daemon")
	}
	if _, err := client.TurnStart(context.Background(), appwire.TurnStartParams{ClientMutationID: "test-mutation", ExpectedInstanceID: sessionID, Ref: "local:" + sessionID, Input: []appwire.InputItem{{Type: "text", Text: "resume work"}}}); err != nil {
		t.Fatalf("TurnStart: %v", err)
	}

	daemon.Broadcast(sessionID, appwire.NotifyHistoryUpdated, appwire.HistoryUpdatedParams{
		ThreadID: sessionID, Ref: "local:" + sessionID,
		Items: []appwire.ThreadItem{{Type: "agentMessage", ID: "resumed-item", Text: "live update"}},
	})

	select {
	case got := <-reader.Notifications():
		if got.Method != appwire.NotifyHistoryUpdated {
			t.Fatalf("method=%q", got.Method)
		}
		var history appwire.HistoryUpdatedParams
		if err := json.Unmarshal(got.Params, &history); err != nil || len(history.Items) != 1 || history.Items[0].ID != "resumed-item" || history.Items[0].Text != "live update" {
			t.Fatalf("resumed history=%+v err=%v", history, err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for resumed turn notification")
	}
	daemon.Broadcast(sessionID, appwire.NotifyEvenerThreadActivityChanged, appwire.SessionActivityChangedParams{
		ThreadID: sessionID, Ref: "local:" + sessionID, SessionID: sessionID,
		Resources: []appwire.SessionActivityResource{appwire.SessionActivityResourceJobs, appwire.SessionActivityResourceWatches},
	})
	select {
	case got := <-reader.Notifications():
		if got.Method != appwire.NotifyEvenerThreadActivityChanged {
			t.Fatalf("activity notification=%q", got.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("saved-history subscriber missed activity invalidation after independent resume")
	}
}

func TestHubRPCRetainedReadKeepsCapturedCandidate(t *testing.T) {
	root := t.TempDir()
	oldWorkingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-a-0000000000")
	id := buildRPCParentSessionWithWorkingDir(t, stateDir, oldWorkingDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	hub, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: past})
	defer hub.Close()
	newWorkingDir := t.TempDir()
	web.appRPC.SetBeforeSubscriptionGate(func() {
		// A rebuild can select a different project copy while hydration is admitted.
		// The response must project the same retained candidate that chose membership.
		if got := buildRPCParentSessionWithWorkingDir(t, filepath.Join(root, "projects", "project-z-0000000000"), newWorkingDir); got != id {
			t.Fatalf("copied session ID=%q", got)
		}
		if _, err := past.Rebuild(); err != nil {
			t.Fatal(err)
		}
	})
	reader := dialHubRPC(t, hub)
	defer reader.Close()
	if _, err := reader.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	response, err := reader.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: "local:" + id, Subscribe: true, IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	if current, ok := past.Find(id); !ok || current.Meta.EnvInfo.WorkingDir != newWorkingDir {
		t.Fatalf("rebuild did not replace indexed candidate: %+v", current)
	}
	if response.Thread.CWD != oldWorkingDir || len(response.Thread.Turns) != 2 {
		t.Fatalf("response switched retained candidates: cwd=%q turns=%d", response.Thread.CWD, len(response.Thread.Turns))
	}
}

func TestHubRPCUnknownRetainedTargetDoesNotSubscribe(t *testing.T) {
	registry := appsource.NewRegistry()
	registry.Add(&pastFallbackRelaySource{readErr: appwire.SessionUnavailable("missing source session")})
	server := newHubAppServer(hubcore.WebConfig{HubStateRoot: t.TempDir()}, registry)
	hub := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	_, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: "local:missing", Subscribe: true})
	if err == nil || !strings.Contains(err.Error(), "missing source session") {
		t.Fatalf("unknown target error=%v", err)
	}
	if count := server.SubscriberCount("local:missing"); count != 0 {
		t.Fatalf("unknown target acquired %d subscriptions", count)
	}
}
