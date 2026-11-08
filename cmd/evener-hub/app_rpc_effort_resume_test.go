package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

// TestHubRPCThreadReasoningEffortSetNotifiesColdSubscriber pins the delivery
// half of the cold pick: a pane subscribed while the session was exited must
// learn the new effort the same way a live one does — thread/reasoning-effort/
// changed through a relay attached to the resumed daemon — because the web
// footer only re-renders from that frame. A frame emitted before the relay
// attaches is lost, leaving the control showing the old level.
func TestHubRPCThreadReasoningEffortSetNotifiesColdSubscriber(t *testing.T) {
	root := t.TempDir()
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	var currentEffort atomic.Value
	currentEffort.Store("")
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Source:    "local",
			Evener: appwire.EvenerThread{
				Ref:             params.Ref,
				ReasoningEffort: currentEffort.Load().(string),
			},
		}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadReasoningEffortSet, func(_ context.Context, params appwire.ThreadReasoningEffortSetParams) (appwire.EmptyResponse, error) {
		currentEffort.Store(params.ReasoningEffort)
		// The live daemon emits this from Session.SetReasoningEffort; the hub's
		// relay listens on its own subscription connection, so the fake fans the
		// frame out the way the daemon does instead of notifying only the
		// request socket.
		daemon.BroadcastAll(appwire.NotifyThreadReasoningEffortChanged, appwire.ThreadReasoningEffortChangedParams{
			ThreadID:        sessionID,
			Ref:             "local:" + sessionID,
			ReasoningEffort: params.ReasoningEffort,
		})
		return appwire.EmptyResponse{}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	spawner := &fakeRPCSpawner{
		resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
			entry := rendezvous.Entry{
				PID:        106,
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
	// The pane is open on the exited session, so it subscribed while cold.
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID, Subscribe: true}); err != nil {
		t.Fatalf("thread/read subscribe on the cold session: %v", err)
	}
	// One pump per client, started before the mutation: notifications arrive on
	// a single channel, and a reader that starts after the set would race the
	// arrival.
	notifications := make(chan appwire.Notification, 32)
	go func() {
		for notification := range client.Notifications() {
			notifications <- notification
		}
	}()

	if err := client.ThreadReasoningEffortSet(context.Background(), appwire.ThreadReasoningEffortSetParams{
		Ref:             "local:" + sessionID,
		ReasoningEffort: "high",
	}); err != nil {
		t.Fatalf("ThreadReasoningEffortSet: %v", err)
	}

	// The scripted daemon fans the frame out to every subscription connection,
	// so the subscribed client must receive the change itself — not merely a
	// gap resync — and the authoritative read must agree.
	deadline := time.After(10 * time.Second)
	converged := false
	for !converged {
		select {
		case notification := <-notifications:
			if notification.Method != appwire.NotifyThreadReasoningEffortChanged {
				continue
			}
			var params appwire.ThreadReasoningEffortChangedParams
			if err := json.Unmarshal(notification.Params, &params); err != nil {
				t.Fatalf("decode thread/reasoning-effort/changed: %v", err)
			}
			if params.ReasoningEffort != "high" {
				t.Fatalf("notification effort = %q, want high", params.ReasoningEffort)
			}
			converged = true
		case <-deadline:
			t.Fatal("thread/reasoning-effort/changed never reached the subscriber that attached while the session was cold")
		}
	}

	read, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID})
	if err != nil {
		t.Fatalf("thread/read after the cold effort pick: %v", err)
	}
	if read.Thread.Evener.ReasoningEffort != "high" {
		t.Fatalf("thread/read effort = %q, want the picked high", read.Thread.Evener.ReasoningEffort)
	}
}

// TestHubRPCThreadReasoningEffortSetReachesSubscriberAfterColdModelSet pins the
// sequence a user actually performs: open an exited session, pick a model (the
// model setter resumes it without attaching a relay), then pick an effort. The
// effort setter's first attempt then succeeds on the already-running session,
// so its after-resume hook does not run; the hub must still have a relay
// listening or the change frame is lost and the footer keeps the old level.
func TestHubRPCThreadReasoningEffortSetReachesSubscriberAfterColdModelSet(t *testing.T) {
	root := t.TempDir()
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	var currentEffort atomic.Value
	currentEffort.Store("")
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Source:    "local",
			Evener: appwire.EvenerThread{
				Ref:             params.Ref,
				Capabilities:    appwire.ThreadCapabilities{ChangeModel: true},
				ReasoningEffort: currentEffort.Load().(string),
			},
		}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadModelSet, func(_ context.Context, _ appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadReasoningEffortSet, func(_ context.Context, params appwire.ThreadReasoningEffortSetParams) (appwire.EmptyResponse, error) {
		currentEffort.Store(params.ReasoningEffort)
		daemon.BroadcastAll(appwire.NotifyThreadReasoningEffortChanged, appwire.ThreadReasoningEffortChangedParams{
			ThreadID:        sessionID,
			Ref:             "local:" + sessionID,
			ReasoningEffort: params.ReasoningEffort,
		})
		return appwire.EmptyResponse{}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	spawner := &fakeRPCSpawner{
		resume: func(_ context.Context, _ hubcore.ResumeRequest) (rendezvous.Entry, error) {
			entry := rendezvous.Entry{
				PID:        106,
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
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID, Subscribe: true}); err != nil {
		t.Fatalf("thread/read subscribe on the cold session: %v", err)
	}
	notifications := make(chan appwire.Notification, 32)
	go func() {
		for notification := range client.Notifications() {
			notifications <- notification
		}
	}()

	// The model pick resumes the session. Its own frame is not what this test
	// follows; the point is that it leaves a live daemon without a relay.
	if err := client.ThreadModelSet(context.Background(), appwire.ThreadModelSetParams{
		Ref:           "local:" + sessionID,
		ModelProvider: "openai",
		Model:         "gpt-5.6-sol",
	}); err != nil {
		t.Fatalf("ThreadModelSet on the cold session: %v", err)
	}
	if err := client.ThreadReasoningEffortSet(context.Background(), appwire.ThreadReasoningEffortSetParams{
		Ref:             "local:" + sessionID,
		ReasoningEffort: "high",
	}); err != nil {
		t.Fatalf("ThreadReasoningEffortSet after the cold model pick: %v", err)
	}

	deadline := time.After(10 * time.Second)
	for {
		select {
		case notification := <-notifications:
			if notification.Method != appwire.NotifyThreadReasoningEffortChanged {
				continue
			}
			var params appwire.ThreadReasoningEffortChangedParams
			if err := json.Unmarshal(notification.Params, &params); err != nil {
				t.Fatalf("decode thread/reasoning-effort/changed: %v", err)
			}
			if params.ReasoningEffort != "high" {
				t.Fatalf("notification effort = %q, want high", params.ReasoningEffort)
			}
			return
		case <-deadline:
			t.Fatal("thread/reasoning-effort/changed never reached the subscriber after a cold model pick resumed the session")
		}
	}
}

// TestHubRPCThreadModelSetCarriesReasoningProfileToColdSubscriber pins the
// profile half of a cold model switch: the resumed daemon's thread/model/changed
// carries ReasoningEffortLevels and SupportsReasoning, and the web footer needs
// that frame to show or hide the effort control for the new model. Without a
// relay on the resumed daemon the frame is lost, so switching from a
// non-reasoning model to a reasoning one leaves the control hidden.
func TestHubRPCThreadModelSetCarriesReasoningProfileToColdSubscriber(t *testing.T) {
	root := t.TempDir()
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	var supportsReasoning atomic.Bool
	supportsReasoning.Store(false)
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Source:    "local",
			Evener: appwire.EvenerThread{
				Ref:                   params.Ref,
				Capabilities:          appwire.ThreadCapabilities{ChangeModel: true},
				ReasoningEffortLevels: nil,
				SupportsReasoning:     supportsReasoning.Load(),
			},
		}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadModelSet, func(_ context.Context, params appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
		if params.Model != "gpt-6-luna" {
			return appwire.EmptyResponse{}, nil
		}
		supportsReasoning.Store(true)
		daemon.BroadcastAll(appwire.NotifyThreadModelChanged, appwire.ThreadModelChangedParams{
			ThreadID:              sessionID,
			Ref:                   "local:" + sessionID,
			ModelProvider:         "openai-codex",
			Model:                 params.Model,
			ReasoningEffortLevels: []string{"low", "medium", "high", "xhigh", "max"},
			SupportsReasoning:     true,
		})
		return appwire.EmptyResponse{}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	spawner := &fakeRPCSpawner{
		resume: func(_ context.Context, _ hubcore.ResumeRequest) (rendezvous.Entry, error) {
			entry := rendezvous.Entry{
				PID:        106,
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
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID, Subscribe: true}); err != nil {
		t.Fatalf("thread/read subscribe on the cold session: %v", err)
	}
	notifications := make(chan appwire.Notification, 32)
	go func() {
		for notification := range client.Notifications() {
			notifications <- notification
		}
	}()

	if err := client.ThreadModelSet(context.Background(), appwire.ThreadModelSetParams{
		Ref:           "local:" + sessionID,
		ModelProvider: "openai-codex",
		Model:         "gpt-6-luna",
	}); err != nil {
		t.Fatalf("ThreadModelSet on the cold session: %v", err)
	}

	deadline := time.After(10 * time.Second)
	for {
		select {
		case notification := <-notifications:
			if notification.Method != appwire.NotifyThreadModelChanged {
				continue
			}
			var params appwire.ThreadModelChangedParams
			if err := json.Unmarshal(notification.Params, &params); err != nil {
				t.Fatalf("decode thread/model/changed: %v", err)
			}
			if params.Model != "gpt-6-luna" {
				continue
			}
			if !params.SupportsReasoning || len(params.ReasoningEffortLevels) == 0 {
				t.Fatalf("model-changed profile = %+v, want the reasoning ladder", params)
			}
			return
		case <-deadline:
			t.Fatal("thread/model/changed never reached the subscriber that attached while the session was cold")
		}
	}
}

// TestHubRPCThreadReasoningEffortSetResumesPastThread proves the hub resumes
// a cold session behind thread/reasoning-effort/set, the same resume the
// model and vision-model switches already get (setThreadModelWithResume,
// setThreadVisionModelWithResume): a user picking the next effort level on
// an exited session must never have to know the session is cold.
func TestHubRPCThreadReasoningEffortSetResumesPastThread(t *testing.T) {
	root := t.TempDir()
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Source:    "local",
			Evener: appwire.EvenerThread{
				Ref: params.Ref,
			},
		}}, nil
	})
	effortCalled := ""
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadReasoningEffortSet, func(_ context.Context, params appwire.ThreadReasoningEffortSetParams) (appwire.EmptyResponse, error) {
		if params.Ref != "local:"+sessionID {
			t.Fatalf("effort ref=%q", params.Ref)
		}
		effortCalled = params.ReasoningEffort
		return appwire.EmptyResponse{}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	resumeCalls := 0
	spawner := &fakeRPCSpawner{
		resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
			if req.SessionID != sessionID || req.StateDir != stateDir || req.WorkingDir != workingDir {
				t.Fatalf("resume request=%+v", req)
			}
			resumeCalls++
			entry := rendezvous.Entry{
				PID:        106,
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
	if err := client.ThreadReasoningEffortSet(context.Background(), appwire.ThreadReasoningEffortSetParams{
		Ref:             "local:" + sessionID,
		ReasoningEffort: "high",
	}); err != nil {
		t.Fatalf("ThreadReasoningEffortSet: %v", err)
	}
	if resumeCalls != 1 {
		t.Fatalf("resume calls=%d, want 1", resumeCalls)
	}
	if effortCalled != "high" {
		t.Fatalf("effortCalled=%q", effortCalled)
	}
}
