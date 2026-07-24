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
