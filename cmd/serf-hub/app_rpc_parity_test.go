package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/rendezvous"
)

// parityResumeDaemon builds a fake daemon that answers ThreadRead with the
// given capabilities and registers the supplied mutation handler, plus a
// spawner whose Resume writes a rendezvous entry pointing at that daemon. It
// returns a WebConfig wired to a rebuilt past index for sessionID so
// hubKnowsRef(ref) is true and auto-resume is eligible. resumeCalls is
// incremented on every Resume.
func parityResumeFixture(t *testing.T, register func(daemon *appserver.Server)) (cfg hubcore.WebConfig, sessionID string, resumeCalls *int) {
	t.Helper()
	root := t.TempDir()
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID = buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	register(daemon)
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	t.Cleanup(daemonHTTP.Close)

	runDir := t.TempDir()
	calls := 0
	spawner := &fakeRPCSpawner{
		resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
			if req.SessionID != sessionID || req.StateDir != stateDir || req.WorkingDir != workingDir {
				t.Fatalf("resume request=%+v", req)
			}
			calls++
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
	cfg = hubcore.WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner, Past: past}
	return cfg, sessionID, &calls
}

// TestHubRPCThreadClearResumesPastThread proves ThreadClear on an exited
// session resumes the daemon and retries, matching compact/model (kata qp94).
func TestHubRPCThreadClearResumesPastThread(t *testing.T) {
	var sessionID string
	clearCalled := false
	cfg, sid, resumeCalls := parityResumeFixture(t, func(daemon *appserver.Server) {
		appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID:        sessionID,
				SessionID: sessionID,
				Source:    "local",
				Serf: appwire.SerfThread{
					Ref:          params.Ref,
					Capabilities: appwire.ThreadCapabilities{Clear: true},
				},
			}}, nil
		})
		appserver.HandleTyped(daemon.Router(), appwire.MethodThreadClear, func(_ context.Context, params appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
			if params.Ref != "local:"+sessionID {
				t.Fatalf("clear ref=%q", params.Ref)
			}
			clearCalled = true
			return appwire.ThreadClearResponse{Ref: params.Ref}, nil
		})
	})
	sessionID = sid

	hub := newHubRPCTestServer(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadClear(context.Background(), appwire.ThreadClearParams{Ref: "local:" + sessionID}); err != nil {
		t.Fatalf("ThreadClear: %v", err)
	}
	if *resumeCalls != 1 {
		t.Fatalf("resume calls=%d, want 1", *resumeCalls)
	}
	if !clearCalled {
		t.Fatal("clear was not routed after resume")
	}
}

// TestHubRPCThreadNameSetResumesPastThread proves renaming an exited session
// resumes the daemon and retries (kata qp94).
func TestHubRPCThreadNameSetResumesPastThread(t *testing.T) {
	var sessionID string
	renamedTo := ""
	cfg, sid, resumeCalls := parityResumeFixture(t, func(daemon *appserver.Server) {
		appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID:        sessionID,
				SessionID: sessionID,
				Source:    "local",
				Serf: appwire.SerfThread{
					Ref:          params.Ref,
					Capabilities: appwire.ThreadCapabilities{Rename: true},
				},
			}}, nil
		})
		appserver.HandleTyped(daemon.Router(), appwire.MethodSerfThreadNameSet, func(_ context.Context, params appwire.ThreadNameSetParams) (appwire.EmptyResponse, error) {
			if params.Ref != "local:"+sessionID {
				t.Fatalf("rename ref=%q", params.Ref)
			}
			renamedTo = params.Name
			return appwire.EmptyResponse{}, nil
		})
	})
	sessionID = sid

	hub := newHubRPCTestServer(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := client.ThreadNameSet(context.Background(), appwire.ThreadNameSetParams{Ref: "local:" + sessionID, Name: "renamed"}); err != nil {
		t.Fatalf("ThreadNameSet: %v", err)
	}
	if *resumeCalls != 1 {
		t.Fatalf("resume calls=%d, want 1", *resumeCalls)
	}
	if renamedTo != "renamed" {
		t.Fatalf("renamedTo=%q, want %q", renamedTo, "renamed")
	}
}

// TestHubRPCGoalSetResumesPastThread proves setting a goal on an exited
// session resumes the daemon and retries. This was the live user-reachable
// bug: the capability was advertised true on a past thread but no resume was
// wired, so the UI offered /goal and it failed (kata qp94/xr4x).
func TestHubRPCGoalSetResumesPastThread(t *testing.T) {
	var sessionID string
	goalObjective := ""
	cfg, sid, resumeCalls := parityResumeFixture(t, func(daemon *appserver.Server) {
		appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID:        sessionID,
				SessionID: sessionID,
				Source:    "local",
				Serf: appwire.SerfThread{
					Ref:          params.Ref,
					Capabilities: appwire.ThreadCapabilities{Goal: true},
				},
			}}, nil
		})
		appserver.HandleTyped(daemon.Router(), appwire.MethodGoalSet, func(_ context.Context, params appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
			if params.Ref != "local:"+sessionID {
				t.Fatalf("goal ref=%q", params.Ref)
			}
			goalObjective = params.Objective
			return appwire.GoalSetResponse{Started: true}, nil
		})
	})
	sessionID = sid

	hub := newHubRPCTestServer(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.GoalSet(context.Background(), appwire.GoalSetParams{Ref: "local:" + sessionID, Objective: "ship it"})
	if err != nil {
		t.Fatalf("GoalSet: %v", err)
	}
	if !resp.Started {
		t.Fatal("GoalSet response Started=false, want true")
	}
	if *resumeCalls != 1 {
		t.Fatalf("resume calls=%d, want 1", *resumeCalls)
	}
	if goalObjective != "ship it" {
		t.Fatalf("goalObjective=%q, want %q", goalObjective, "ship it")
	}
}

// TestHubRPCThreadShutdownExitedSessionIsNoOpSuccess proves that shutting down
// an already-exited session succeeds as a no-op WITHOUT resuming it — we must
// never resurrect a daemon just to kill it (kata qp94 carve-out).
func TestHubRPCThreadShutdownExitedSessionIsNoOpSuccess(t *testing.T) {
	root := t.TempDir()
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	resumeCalled := false
	spawner := &fakeRPCSpawner{
		resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			resumeCalled = true
			return rendezvous.Entry{}, nil
		},
	}
	roster := hubcore.NewRoster(runDir, nil)
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner, Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := client.ThreadShutdown(context.Background(), appwire.ThreadShutdownParams{Ref: "local:" + sessionID}); err != nil {
		t.Fatalf("ThreadShutdown on exited session: %v, want no-op success", err)
	}
	if resumeCalled {
		t.Fatal("shutdown resurrected an exited session; must be a no-op")
	}
}

// TestHubRPCThreadForkExitedSessionSucceeds proves forking a local session
// that has no live daemon works without a resume: the local fork reads the
// parent's transcript from the state dir directly, so an exited session forks
// identically to a live one (kata qp94 fork parity).
func TestHubRPCThreadForkExitedSessionSucceeds(t *testing.T) {
	root := t.TempDir()
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	resumeCalled := false
	spawner := &fakeRPCSpawner{
		resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			resumeCalled = true
			return rendezvous.Entry{}, nil
		},
	}
	roster := hubcore.NewRoster(runDir, nil)
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner, Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadFork(context.Background(), appwire.ThreadForkParams{
		Ref:          "local:" + sessionID,
		SourceTurnID: "turn_1",
		EditedInput:  "edited",
	})
	if err != nil {
		t.Fatalf("ThreadFork on exited session: %v", err)
	}
	if resp.Thread.ID == "" || resp.Thread.ID == sessionID {
		t.Fatalf("fork child id=%q, want a fresh child id", resp.Thread.ID)
	}
	if resumeCalled {
		t.Fatal("local fork resurrected the parent daemon; it should read the state dir directly")
	}
}
