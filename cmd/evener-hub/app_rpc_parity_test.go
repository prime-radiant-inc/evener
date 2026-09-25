package hub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
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

// TestHubRPCThreadClearResumesPastThread proves clear can act on a cold local
// session through the same managed resume path as the other session actions.
func TestHubRPCThreadClearResumesPastThread(t *testing.T) {
	var sessionID string
	clearCalled := false
	cfg, sid, resumeCalls := parityResumeFixture(t, func(daemon *appserver.Server) {
		appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID:        sessionID,
				SessionID: sessionID,
				Source:    "local",
				Evener: appwire.EvenerThread{
					Ref:          params.Ref,
					InstanceID:   sessionID,
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
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	response, err := client.ThreadClear(context.Background(), appwire.ThreadClearParams{Ref: "local:" + sessionID, ClientMutationID: "clear-past", ExpectedInstanceID: sessionID})
	if err != nil {
		t.Fatalf("ThreadClear: %v", err)
	}
	if response.Ref != "local:"+sessionID {
		t.Fatalf("ThreadClear ref=%q, want local:%s", response.Ref, sessionID)
	}
	if *resumeCalls != 1 {
		t.Fatalf("resume calls=%d, want 1", *resumeCalls)
	}
	if !clearCalled {
		t.Fatal("clear was not routed to the resumed daemon")
	}
}

// TestHubRPCThreadNameSetEditsPastThreadWithoutResume proves renaming an ended
// local session updates its metadata without launching a daemon.
func TestHubRPCThreadNameSetEditsPastThreadWithoutResume(t *testing.T) {
	var sessionID string
	renamedTo := ""
	cfg, sid, resumeCalls := parityResumeFixture(t, func(daemon *appserver.Server) {
		appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID:        sessionID,
				SessionID: sessionID,
				Source:    "local",
				Evener: appwire.EvenerThread{
					Ref:          params.Ref,
					Capabilities: appwire.ThreadCapabilities{Rename: true},
				},
			}}, nil
		})
		appserver.HandleTyped(daemon.Router(), appwire.MethodEvenerThreadNameSet, func(_ context.Context, params appwire.ThreadNameSetParams) (appwire.EmptyResponse, error) {
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
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := client.ThreadNameSet(context.Background(), appwire.ThreadNameSetParams{Ref: "local:" + sessionID, Name: "renamed"}); err != nil {
		t.Fatalf("ThreadNameSet: %v", err)
	}
	if *resumeCalls != 0 {
		t.Fatalf("resume calls=%d, want 0", *resumeCalls)
	}
	if renamedTo != "" {
		t.Fatalf("daemon rename=%q, want no daemon call", renamedTo)
	}
	entry, ok := cfg.Past.Find(sessionID)
	if !ok {
		t.Fatalf("past session %q not found", sessionID)
	}
	meta, err := schema.LoadSessionMeta(entry.StateDir, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Name != "renamed" || meta.NameSource != "user" {
		t.Fatalf("renamed meta=%+v", meta)
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
				Evener: appwire.EvenerThread{
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
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
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

// TestHubRPCNotesHumanSetResumesPastThread proves setting a human note on an
// exited session resumes the daemon and retries, mirroring
// TestHubRPCGoalSetResumesPastThread (notes/human/set shares the same
// withSessionResume path as goal/set).
func TestHubRPCNotesHumanSetResumesPastThread(t *testing.T) {
	var sessionID string
	noteText := ""
	cfg, sid, resumeCalls := parityResumeFixture(t, func(daemon *appserver.Server) {
		appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID:        sessionID,
				SessionID: sessionID,
				Source:    "local",
				Evener: appwire.EvenerThread{
					Ref:          params.Ref,
					Capabilities: appwire.ThreadCapabilities{SharedNotes: true},
				},
			}}, nil
		})
		appserver.HandleTyped(daemon.Router(), appwire.MethodNotesHumanSet, func(_ context.Context, params appwire.NotesHumanSetParams) (appwire.NotesHumanSetResponse, error) {
			if params.Ref != "local:"+sessionID {
				t.Fatalf("notes ref=%q", params.Ref)
			}
			noteText = params.Note
			return appwire.NotesHumanSetResponse{Note: params.Note}, nil
		})
	})
	sessionID = sid

	hub := newHubRPCTestServer(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.NotesHumanSet(context.Background(), appwire.NotesHumanSetParams{Ref: "local:" + sessionID, ClientMutationID: "note-past", ExpectedInstanceID: sessionID, Note: "whiteboard note"})
	if err != nil {
		t.Fatalf("NotesHumanSet: %v", err)
	}
	if resp.Note != "whiteboard note" {
		t.Fatalf("NotesHumanSet note=%q, want %q", resp.Note, "whiteboard note")
	}
	if *resumeCalls != 1 {
		t.Fatalf("resume calls=%d, want 1", *resumeCalls)
	}
	if noteText != "whiteboard note" {
		t.Fatalf("noteText=%q, want %q", noteText, "whiteboard note")
	}
}

// TestHubRPCUrlsRemoveResumesPastThread proves removing a URL on an exited
// session resumes the daemon and retries, mirroring
// TestHubRPCGoalSetResumesPastThread (urls/remove shares the same
// withSessionResume path as goal/set).
func TestHubRPCUrlsRemoveResumesPastThread(t *testing.T) {
	var sessionID string
	removedID := ""
	cfg, sid, resumeCalls := parityResumeFixture(t, func(daemon *appserver.Server) {
		appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID:        sessionID,
				SessionID: sessionID,
				Source:    "local",
				Evener: appwire.EvenerThread{
					Ref:          params.Ref,
					Capabilities: appwire.ThreadCapabilities{SharedNotes: true},
				},
			}}, nil
		})
		appserver.HandleTyped(daemon.Router(), appwire.MethodUrlsRemove, func(_ context.Context, params appwire.UrlsRemoveParams) (appwire.UrlsRemoveResponse, error) {
			if params.Ref != "local:"+sessionID {
				t.Fatalf("urls ref=%q", params.Ref)
			}
			removedID = params.ID
			return appwire.UrlsRemoveResponse{}, nil
		})
	})
	sessionID = sid

	hub := newHubRPCTestServer(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.UrlsRemove(context.Background(), appwire.UrlsRemoveParams{Ref: "local:" + sessionID, ClientMutationID: "url-rm-past", ExpectedInstanceID: sessionID, ID: "u1"}); err != nil {
		t.Fatalf("UrlsRemove: %v", err)
	}
	if *resumeCalls != 1 {
		t.Fatalf("resume calls=%d, want 1", *resumeCalls)
	}
	if removedID != "u1" {
		t.Fatalf("removedID=%q, want %q", removedID, "u1")
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
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
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
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadFork(context.Background(), appwire.ThreadForkParams{
		Ref:           "local:" + sessionID,
		SourceItemKey: "apptranscript-item-v2:t_1:0:0",
		EditedInput:   "edited",
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

// TestHubRPCTurnControlsDoNotResumeExitedSession locks in the qp94 exception
// classification: the controls that act on a turn already in flight (steer,
// interrupt, drainAsSteer, promoteQueuedAsSteer, cancelQueued) gate on that
// turn, which a cold exited session cannot have. They must fail at the gate
// rather than resurrect the daemon — resuming to a fresh idle session would
// give the control nothing to act on.
//
// Queue is deliberately NOT in this set any more: a queued message needs no
// turn to act on (the daemon runs it as the next one), the web composer routes
// a message there for a finished session, and a queue write against an exited
// session used to be refused outright — dropping the message. It carries the
// same resume-and-retry contract as send; the positive half is
// TestHubRPCTurnQueueResumesExitedSession.
func TestHubRPCTurnControlsDoNotResumeExitedSession(t *testing.T) {
	sessionID := "02wMz5Txv1C3Hut0M8GCeB"
	ref := "local:" + sessionID
	ctx := context.Background()

	cases := []struct {
		name string
		call func(*appwire.Client) error
	}{
		{"steer", func(c *appwire.Client) error {
			return c.TurnSteer(ctx, appwire.TurnSteerParams{ClientMutationID: "test-mutation", ExpectedInstanceID: sessionID, Ref: ref, Input: []appwire.InputItem{{Type: "text", Text: "x"}}})
		}},
		{"interrupt", func(c *appwire.Client) error {
			return c.TurnInterrupt(ctx, appwire.TurnInterruptParams{ClientMutationID: "test-mutation", ExpectedInstanceID: sessionID, Ref: ref})
		}},
		{"drainAsSteer", func(c *appwire.Client) error {
			return c.TurnDrainAsSteer(ctx, appwire.TurnDrainAsSteerParams{ClientMutationID: "test-mutation", ExpectedInstanceID: sessionID, ExpectedQueueRevision: 0, Ref: ref, Input: []appwire.InputItem{{Type: "text", Text: "x"}}})
		}},
		{"promoteQueuedAsSteer", func(c *appwire.Client) error {
			return c.TurnPromoteQueuedAsSteer(ctx, appwire.TurnPromoteQueuedAsSteerParams{ClientMutationID: "test-mutation", ExpectedInstanceID: sessionID, ExpectedEntryID: "test-entry", Ref: ref, Index: 0})
		}},
		{"cancelQueued", func(c *appwire.Client) error {
			_, err := c.TurnCancelQueued(ctx, appwire.TurnCancelQueuedParams{ClientMutationID: "test-mutation", ExpectedInstanceID: sessionID, ExpectedEntryID: "test-entry", Ref: ref, Index: 0})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, resumeCalled := newExitedSessionHub(t)
			if err := tc.call(client); err == nil {
				t.Fatalf("%s on exited session succeeded; want gate error", tc.name)
			}
			if *resumeCalled {
				t.Fatalf("%s resurrected an exited session; turn controls must stay live-only", tc.name)
			}
		})
	}
}

// newExitedSessionHub serves one local thread known only to the past index —
// no daemon behind it — and reports whether anything resumed it. It is the
// fixture for the turn controls' resume classification, both halves.
func newExitedSessionHub(t *testing.T) (*appwire.Client, *bool) {
	t.Helper()
	root := t.TempDir()
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
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
	t.Cleanup(hub.Close)
	client := dialHubRPC(t, hub)
	t.Cleanup(func() { _ = client.Close() })
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return client, &resumeCalled
}

// TestHubRPCTurnQueueResumesExitedSession is the positive half of the queue
// classification above: a queued message is the user speaking, so a queue write
// against an exited session resumes the daemon the same way send does instead
// of being refused. The write itself is not asserted here (this fixture's
// spawner publishes no daemon, so there is nothing to queue against); the
// delivery end to end is cmd/evener-hub/e2e_queue_prompt_exited_test.go.
func TestHubRPCTurnQueueResumesExitedSession(t *testing.T) {
	const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
	client, resumeCalled := newExitedSessionHub(t)
	err := client.TurnQueue(context.Background(), appwire.TurnQueueParams{
		ClientMutationID:   "test-mutation",
		ExpectedInstanceID: sessionID,
		Ref:                "local:" + sessionID,
		Input:              []appwire.InputItem{{Type: "text", Text: "x"}},
	})
	if err == nil {
		t.Fatal("turn/queue against an exited session succeeded with no daemon to queue against")
	}
	if !*resumeCalled {
		t.Fatalf("turn/queue did not resume the exited session: %v", err)
	}
}

// TestHubRPCConcurrentMutationsResumeExitedSessionOnce proves the RPC
// auto-resume path serializes concurrent resume attempts for one exited
// session behind the per-session lock, exactly as the REST send path does
// (kata sm1a). Two mutations racing on a cold session must resume the daemon
// once, not twice.
func TestHubRPCConcurrentMutationsResumeExitedSessionOnce(t *testing.T) {
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
			ID: sessionID, SessionID: sessionID, Source: "local",
			Evener: appwire.EvenerThread{Ref: params.Ref, Capabilities: appwire.ThreadCapabilities{Compact: true}},
		}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadCompactStart, func(context.Context, appwire.ThreadCompactStartParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	roster := hubcore.NewRoster(runDir, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusIdle})
	var resumeCalls atomic.Int32
	spawner := &fakeRPCSpawner{
		resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
			n := resumeCalls.Add(1)
			// Hold the resume in flight so a second concurrent caller is
			// guaranteed to be waiting on the per-session lock while this one
			// registers the daemon.
			time.Sleep(150 * time.Millisecond)
			entry := rendezvous.Entry{
				PID: 106 + int(n), Protocol: appwire.ProtocolVersion,
				Endpoint: "ws" + daemonHTTP.URL[len("http"):], SourceID: "local",
				ThreadID: sessionID, SessionID: sessionID, WorkingDir: workingDir,
			}
			// Write directly (not the writeRendezvous helper) so a losing
			// concurrent resume can't t.Fatalf from this goroutine. A unique PID
			// per call keeps two racing resumes from colliding on one file, so
			// the failure surfaces cleanly as a resume-count mismatch.
			if _, err := rendezvous.Write(runDir, entry); err != nil {
				return rendezvous.Entry{}, err
			}
			roster.Refresh()
			return entry, nil
		},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner, Past: past})
	defer hub.Close()

	clientA := dialHubRPC(t, hub)
	defer clientA.Close()
	clientB := dialHubRPC(t, hub)
	defer clientB.Close()
	for _, c := range []*appwire.Client{clientA, clientB} {
		if _, err := c.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	errs := make([]error, 2)
	go func() {
		defer wg.Done()
		errs[0] = clientA.ThreadCompactStart(context.Background(), appwire.ThreadCompactStartParams{Ref: "local:" + sessionID})
	}()
	go func() {
		defer wg.Done()
		errs[1] = clientB.ThreadCompactStart(context.Background(), appwire.ThreadCompactStartParams{Ref: "local:" + sessionID})
	}()
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent mutation %d failed: %v", i, err)
		}
	}
	if got := resumeCalls.Load(); got != 1 {
		t.Fatalf("resume calls=%d, want 1 (concurrent resumes must serialize behind the per-session lock)", got)
	}
}

// TestSessionResumeRetryCorrelation pins what withSessionResume reports when
// the retry its own resume made possible fails. Every session mutation that
// shares withSessionResume (notes/human/set, urls/remove, clear, goal/set,
// turn/queue) must correlate that failure the same way turn/start's retry does.
//
// An uncorrelated failure names no clientMutationId, so the caller's mutation
// dispatcher cannot classify it; it is wrapped as a blocked unknown outcome so
// the input is retained. A resolution failure proves nothing was dispatched at
// all -- the attempt could not even resolve the owning source -- but its
// outcome is reported not-accepted only when the WHOLE operation is proven
// pre-dispatch: the original attempt AND the retry both failed before reaching a
// source. A retry that fails source resolution while the original attempt
// already reached a source proves nothing about what that earlier attempt did
// (a source call that loses its response does not turn an applied mutation into
// a known rejection), so it stays blocked-unknown. Every outcome carries the
// caller's clientMutationId so the browser's outbox, the TUI's draft restore and
// every other surface can settle the record instead of leaving it submitting
// forever.
//
// The whole-operation not-accepted case is constructed directly here, not
// reached through a caller: no real caller can satisfy it, because reaching the
// retry requires a session-unavailable first failure and source resolution never
// produces one (see preDispatchRefusalError and firstAttemptPreDispatch in
// app_session_resume.go). The case pins the rule's shape -- it keeps the rule
// honest if a source resolution that reports session-unavailable ever appears --
// while TestHubRPCResumeRetrySourceResolutionFailureIsNotAccepted pins the
// blocked-unknown outcome every real caller gets today.
//
// The once closure is withSessionResume's own seam: it stands in for the
// relayWithResume / setGoalWithResume / clearThreadWithResume shapes that wrap
// their sourceForThread failure in preDispatchRefusalError. firstErr is the
// original attempt's failure -- bare when it stood in for a closure that reached
// a source, wrapped when the closure failed to resolve one.
func TestSessionResumeRetryCorrelation(t *testing.T) {
	const mutationID = "mutation-session-resume-retry"

	cases := []struct {
		name            string
		firstErr        error
		retryErr        error
		wantOutcome     appwire.MutationOutcome
		wantDisposition appwire.RetryDisposition
	}{
		{
			name:            "uncorrelated retry failure is blocked-unknown",
			firstErr:        appwire.SessionUnavailable("session has exited"),
			retryErr:        appwire.InternalError("resumed session refused the retry without naming the mutation"),
			wantOutcome:     appwire.MutationOutcomeUnknown,
			wantDisposition: appwire.RetryDispositionBlocked,
		},
		{
			// Forward-compatibility shape, not a production outcome: both attempts
			// are handed the pre-dispatch signal directly (see the doc comment).
			name:            "forward-compat: a both-pre-dispatch operation is not-accepted",
			firstErr:        preDispatchRefusalError{appwire.SessionUnavailable("session has exited")},
			retryErr:        preDispatchRefusalError{errors.New("source registry unavailable")},
			wantOutcome:     appwire.MutationOutcomeNotAccepted,
			wantDisposition: appwire.RetryDispositionNone,
		},
		{
			name:            "first attempt reached the source is blocked-unknown",
			firstErr:        appwire.SessionUnavailable("session has exited"),
			retryErr:        preDispatchRefusalError{errors.New("source registry unavailable")},
			wantOutcome:     appwire.MutationOutcomeUnknown,
			wantDisposition: appwire.RetryDispositionBlocked,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sessionID string
			cfg, sid, resumeCalls := parityResumeFixture(t, func(daemon *appserver.Server) {
				appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
					return appwire.ThreadReadResponse{Thread: appwire.Thread{
						ID:        sessionID,
						SessionID: sessionID,
						Source:    "local",
						Evener:    appwire.EvenerThread{Ref: params.Ref},
					}}, nil
				})
			})
			sessionID = sid
			// The hub's own source registry is what withSessionResume's resume
			// reads the freshly-resumed thread back through.
			server, web := newHubRPCTestServerWithWeb(t, cfg)
			defer server.Close()
			ref := "local:" + sessionID

			attempts := 0
			_, err := withSessionResume(context.Background(), cfg, web.sources, ref, mutationID, func() (appwire.EmptyResponse, error) {
				attempts++
				if attempts == 1 {
					return appwire.EmptyResponse{}, tc.firstErr
				}
				return appwire.EmptyResponse{}, tc.retryErr
			})
			if err == nil {
				t.Fatal("withSessionResume reported success although the retry failed")
			}
			if attempts != 2 {
				t.Fatalf("attempts=%d, want 2 (the original and the post-resume retry)", attempts)
			}
			if *resumeCalls != 1 {
				t.Fatalf("resume calls=%d, want 1", *resumeCalls)
			}

			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatalf("withSessionResume error %T=%v, want a WireError", err, err)
			}
			data, ok := wire.Data.(appwire.ErrorData)
			if !ok {
				t.Fatalf("wire data %#v is not appwire.ErrorData", wire.Data)
			}
			if data.ClientMutationID != mutationID {
				t.Fatalf("the retry failure names clientMutationId %q, want %q: the caller cannot correlate it (wire=%+v)",
					data.ClientMutationID, mutationID, wire)
			}
			if data.MutationOutcome != tc.wantOutcome || data.RetryDisposition != tc.wantDisposition {
				t.Fatalf("mutationOutcome=%q retryDisposition=%q, want %q/%q (wire=%+v)",
					data.MutationOutcome, data.RetryDisposition, tc.wantOutcome, tc.wantDisposition, wire)
			}
		})
	}
}

// TestSessionResumeResumeDeletionFailureKeepsDeletionOutcome pins what
// withSessionResume reports when the auto-resume its own retry needed fails
// because the target was deleted.
//
// The target can be deleted between the first attempt's session-unavailable
// failure and the auto-resume that failure triggered; the resume then fails on
// the deletion fence with an error that names NO mutation (the hub's resume
// fences carry no caller id -- see resumeThreadLockedLaunch). That deletion is
// this caller's to reconcile, so it must come back keeping its own outcome
// (targetDeleted / none) and carrying the caller's own, verbatim id, rather than
// hidden behind the blocked-unknown envelope: the caller's mutation hit a
// deleted target, and its record is reconciled as orphaned instead of retained.
func TestSessionResumeResumeDeletionFailureKeepsDeletionOutcome(t *testing.T) {
	const verbatim = " mutation-padded "

	var sessionID string
	cfg, sid, resumeCalls := parityResumeFixture(t, func(daemon *appserver.Server) {
		appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID:        sessionID,
				SessionID: sessionID,
				Source:    "local",
				Evener:    appwire.EvenerThread{Ref: params.Ref},
			}}, nil
		})
	})
	sessionID = sid
	server, web := newHubRPCTestServerWithWeb(t, cfg)
	defer server.Close()
	ref := "local:" + sessionID

	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// The store is wired empty: the first attempt's own deletion fence must pass so
	// the attempt runs. The target is deleted from inside that attempt -- after its
	// fence check and before the auto-resume it triggers reads it -- which is the
	// window the finding describes.
	cfg.DeletionStore = store

	attempts := 0
	_, err = withSessionResume(context.Background(), cfg, web.sources, ref, verbatim, func() (appwire.EmptyResponse, error) {
		attempts++
		if _, err := store.Begin("project-fence-0123456789", []hubcore.DeletionTarget{{
			Ref:      ref,
			ThreadID: sessionID,
		}}); err != nil {
			t.Fatalf("record the deletion: %v", err)
		}
		// The first attempt finds the exited session and triggers the resume.
		return appwire.EmptyResponse{}, appwire.SessionUnavailable("session has exited")
	})
	if err == nil {
		t.Fatal("withSessionResume reported success although the resume failed")
	}
	if attempts != 1 {
		t.Fatalf("attempts=%d, want 1 (the retry must not run after a failed resume)", attempts)
	}
	if *resumeCalls != 0 {
		t.Fatalf("spawner resume calls=%d, want 0 (the deletion fence refuses before any launch)", *resumeCalls)
	}

	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("withSessionResume error %T=%v, want a WireError", err, err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok {
		t.Fatalf("wire data %#v is not appwire.ErrorData", wire.Data)
	}
	if data.MutationOutcome != appwire.MutationOutcomeTargetDeleted || data.RetryDisposition != appwire.RetryDispositionNone {
		t.Fatalf("outcome=%q retryDisposition=%q, want targetDeleted/none: a deleted target is reconcilable, not an unknown outcome (wire=%+v)",
			data.MutationOutcome, data.RetryDisposition, wire)
	}
	if data.ClientMutationID != verbatim {
		t.Fatalf("the deletion names clientMutationId %q, want the caller's verbatim %q: an unnamed deletion must be stamped, not left uncorrelated (wire=%+v)",
			data.ClientMutationID, verbatim, wire)
	}
}

// TestResumeSiblingAliasDeletionIsNotTheCallersToClaim pins the alias scope of
// that rule, in two halves that share one production resume.
//
// A resume fences the requested target and then every alias of its ownership
// group (resumeThreadLockedLaunch / deletionFenceErrorForGroup), and it reports
// the first alias it finds deleted, so a resume failure can name a SIBLING alias
// rather than the target this mutation addressed. First this test drives the real
// auto-resume with only the sibling deleted and asserts the refusal it produces
// names the sibling -- the evidence that the shape is reachable, not hypothetical.
// Then it feeds that real refusal to the mutation boundary
// (mutationResumeFailureError) and asserts the mutation keeps the blocked-unknown
// envelope: a sibling's deletion is not the caller's to reconcile, so the record
// is retained rather than settled as orphaned on a target that may still exist.
//
// The requested-target half of the contrast is covered end-to-end by
// TestSessionResumeResumeDeletionFailureKeepsDeletionOutcome.
func TestResumeSiblingAliasDeletionIsNotTheCallersToClaim(t *testing.T) {
	const verbatim = " mutation-padded "

	var sessionID string
	cfg, sid, _ := parityResumeFixture(t, func(daemon *appserver.Server) {
		appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID:        sessionID,
				SessionID: sessionID,
				Source:    "local",
				Evener:    appwire.EvenerThread{Ref: params.Ref},
			}}, nil
		})
	})
	sessionID = sid
	ref := "local:" + sessionID
	sibling := identifier.MustNewSessionID()

	// One recovery group holds the requested session and a sibling alias of it, so
	// the resume's group fence covers both.
	cfg.ResumeLocks = hubcore.NewResumeLocks()
	cfg.ResumeLocks.PersistForceStop([]string{sibling, sessionID}, sessionID)

	server, web := newHubRPCTestServerWithWeb(t, cfg)
	defer server.Close()

	// Only the SIBLING alias is deleted; the requested target is not. The
	// requested target's own fence therefore passes and the group fence is what
	// fails the resume.
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Begin("project-fence-0123456789", []hubcore.DeletionTarget{{
		Ref:      "local:" + sibling,
		ThreadID: sibling,
	}}); err != nil {
		t.Fatal(err)
	}
	cfg.DeletionStore = store

	// The production auto-resume, addressed to the requested target, fails on the
	// sibling alias's deletion.
	_, err = hubThreadAutoResume(context.Background(), cfg, web.sources, appwire.ThreadResumeParams{Ref: ref})
	if err == nil {
		t.Fatal("the auto-resume reported success although the sibling alias is deleted")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("auto-resume error %T=%v, want a WireError", err, err)
	}
	// The resume really failed on the SIBLING's deletion, not the requested
	// target's: the refusal names the sibling alias.
	if want := "target has been deleted: local:" + sibling; wire.Message != want {
		t.Fatalf("auto-resume refusal message = %q, want %q -- the resume's group fence must be the one that failed (wire=%+v)", wire.Message, want, wire)
	}

	// The mutation boundary must not claim that sibling deletion for the caller.
	got := mutationResumeFailureError(cfg, ref, "", verbatim, err)
	var gotWire appwire.WireError
	if !errors.As(got, &gotWire) {
		t.Fatalf("mutation failure %T=%v, want a WireError", got, got)
	}
	data, ok := gotWire.Data.(appwire.ErrorData)
	if !ok {
		t.Fatalf("wire data %#v is not appwire.ErrorData", gotWire.Data)
	}
	if data.MutationOutcome != appwire.MutationOutcomeUnknown || data.RetryDisposition != appwire.RetryDispositionBlocked {
		t.Fatalf("a sibling-alias deletion must not settle the caller's record as its own deleted target: outcome=%q retryDisposition=%q, want unknown/blocked (wire=%+v)",
			data.MutationOutcome, data.RetryDisposition, gotWire)
	}
	if data.ClientMutationID != verbatim {
		t.Fatalf("the blocked outcome names clientMutationId %q, want the caller's verbatim %q (wire=%+v)", data.ClientMutationID, verbatim, gotWire)
	}
}

// TestMutationResumeFailureErrorRecognizesResolvedOwnershipTarget pins that a
// deletion the resume reports for the caller's OWN resolved target is claimed for
// the caller, even though the caller addressed a stable alias whose own ref is not
// the deleted record -- while a SIBLING alias's deletion still is not claimed.
//
// A stable alias can resolve to a different current target (resumeOwnership walks
// the redirect chain a completed resume records), the resume fences that resolved
// ownership, and the resume discards the resolved target when it fails. Deciding
// by the alias alone would therefore throw away an outcome the hub already knew
// and report unknown/blocked instead.
func TestMutationResumeFailureErrorRecognizesResolvedOwnershipTarget(t *testing.T) {
	const verbatim = " mutation-padded "

	var sessionID string
	cfg, sid, _ := parityResumeFixture(t, func(daemon *appserver.Server) {
		appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID:        sessionID,
				SessionID: sessionID,
				Source:    "local",
				Evener:    appwire.EvenerThread{Ref: params.Ref},
			}}, nil
		})
	})
	sessionID = sid
	// alias is the stable id the caller addresses, target is the current session it
	// resolves to, and sibling is a third alias of the same recovery group.
	alias := sessionID
	target := identifier.MustNewSessionID()
	sibling := identifier.MustNewSessionID()
	ref := "local:" + alias

	cfg.ResumeLocks = hubcore.NewResumeLocks()
	cfg.ResumeLocks.PersistForceStop([]string{alias, target, sibling}, target)

	server, web := newHubRPCTestServerWithWeb(t, cfg)
	defer server.Close()

	cases := []struct {
		name            string
		deleted         string
		wantOutcome     appwire.MutationOutcome
		wantDisposition appwire.RetryDisposition
	}{
		{
			name:            "a deletion of the target the alias resolves to is the caller's own",
			deleted:         target,
			wantOutcome:     appwire.MutationOutcomeTargetDeleted,
			wantDisposition: appwire.RetryDispositionNone,
		},
		{
			name:            "a sibling alias's deletion still is not the caller's own",
			deleted:         sibling,
			wantOutcome:     appwire.MutationOutcomeUnknown,
			wantDisposition: appwire.RetryDispositionBlocked,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, err := hubcore.NewDeletionStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Begin("project-fence-0123456789", []hubcore.DeletionTarget{{
				Ref:      "local:" + tc.deleted,
				ThreadID: tc.deleted,
			}}); err != nil {
				t.Fatal(err)
			}
			cfg.DeletionStore = store

			// The production auto-resume, addressed to the ALIAS, fails on the
			// ownership group fence's report of the deleted alias.
			_, resumeErr := hubThreadAutoResume(context.Background(), cfg, web.sources, appwire.ThreadResumeParams{Ref: ref})
			if resumeErr == nil {
				t.Fatal("the auto-resume reported success although an alias of the ownership group is deleted")
			}
			var wire appwire.WireError
			if !errors.As(resumeErr, &wire) {
				t.Fatalf("auto-resume error %T=%v, want a WireError", resumeErr, resumeErr)
			}
			if want := "target has been deleted: local:" + tc.deleted; wire.Message != want {
				t.Fatalf("auto-resume refusal message = %q, want %q -- the group fence must be the one that failed (wire=%+v)", wire.Message, want, wire)
			}

			got := mutationResumeFailureError(cfg, ref, "", verbatim, resumeErr)
			var gotWire appwire.WireError
			if !errors.As(got, &gotWire) {
				t.Fatalf("mutation failure %T=%v, want a WireError", got, got)
			}
			data, ok := gotWire.Data.(appwire.ErrorData)
			if !ok {
				t.Fatalf("wire data %#v is not appwire.ErrorData", gotWire.Data)
			}
			if data.MutationOutcome != tc.wantOutcome || data.RetryDisposition != tc.wantDisposition {
				t.Fatalf("outcome=%q retryDisposition=%q, want %q/%q (wire=%+v)",
					data.MutationOutcome, data.RetryDisposition, tc.wantOutcome, tc.wantDisposition, gotWire)
			}
			if data.ClientMutationID != verbatim {
				t.Fatalf("the outcome names clientMutationId %q, want the caller's verbatim %q (wire=%+v)", data.ClientMutationID, verbatim, gotWire)
			}
		})
	}
}

// TestHubRPCResumeRetrySourceResolutionFailureIsNotAccepted pins that a retry
// which fails source resolution is NOT, by itself, accepted as proof that the
// whole mutation was rejected. The pre-dispatch rule is decided by the CONCRETE
// caller wrappers -- clearThreadWithResume, relayWithResume, and turn/queue's
// own closure -- not only by the shared withSessionResume, and those wrappers
// are what wrap their own sourceForThread failure in preDispatchRefusalError; a
// test that injects the signal straight into withSessionResume keeps passing if
// a caller drops its wrap, so this drives the real RPC surface instead.
//
// The first attempt reaches the exited session's source and fails there with
// SessionUnavailable, the hub resumes it, and the retry's sourceForThread then
// fails because the owning source is gone from the registry. Because the ORIGINAL
// attempt reached a source -- a source call that loses its response does not turn
// a possibly-applied mutation into a known rejection -- the whole operation is
// not proven pre-dispatch, so the caller must be told the outcome is UNKNOWN and
// blocked, carrying its own clientMutationId, rather than being told the
// mutation was rejected (not-accepted) or left submitting because the retry
// failure names nothing the dispatcher can correlate. The proven-pre-dispatch
// not-accepted case is pinned by TestSessionResumeRetryCorrelation.
func TestHubRPCResumeRetrySourceResolutionFailureIsNotAccepted(t *testing.T) {
	cases := []struct {
		name     string
		dispatch func(t *testing.T, client *appwire.Client, ref, sessionID, mutationID string) error
	}{
		{
			name: "thread/clear via clearThreadWithResume",
			dispatch: func(t *testing.T, client *appwire.Client, ref, sessionID, mutationID string) error {
				t.Helper()
				_, err := client.ThreadClear(context.Background(), appwire.ThreadClearParams{
					Ref: ref, ClientMutationID: mutationID, ExpectedInstanceID: sessionID,
				})
				return err
			},
		},
		{
			name: "notes/human/set via relayWithResume",
			dispatch: func(t *testing.T, client *appwire.Client, ref, sessionID, mutationID string) error {
				t.Helper()
				_, err := client.NotesHumanSet(context.Background(), appwire.NotesHumanSetParams{
					Ref: ref, ClientMutationID: mutationID, ExpectedInstanceID: sessionID, Note: "whiteboard",
				})
				return err
			},
		},
		{
			name: "urls/remove via relayWithResume",
			dispatch: func(t *testing.T, client *appwire.Client, ref, sessionID, mutationID string) error {
				t.Helper()
				_, err := client.UrlsRemove(context.Background(), appwire.UrlsRemoveParams{
					Ref: ref, ClientMutationID: mutationID, ExpectedInstanceID: sessionID, ID: "u1",
				})
				return err
			},
		},
		{
			name: "turn/queue via withSessionResume",
			dispatch: func(t *testing.T, client *appwire.Client, ref, sessionID, mutationID string) error {
				t.Helper()
				return client.TurnQueue(context.Background(), appwire.TurnQueueParams{
					Ref: ref, ClientMutationID: mutationID, ExpectedInstanceID: sessionID,
					Input: []appwire.InputItem{{Type: "text", Text: "queue it"}},
				})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// hubSources is wired after the server exists; the daemon's thread
			// read runs later, during the resume, by which time it is set.
			var hubSources *appsource.Registry
			var sessionID string
			// resumeTimeReads counts the daemon's thread/read invocations and the
			// spawner resumes already recorded when each one ran. This fixture's
			// premise is that the owning source is removed by the RESUME's own read,
			// so the first attempt never reaches the daemon (it fails on the hub's
			// local source with a session-unavailable error, which is what makes the
			// resume fire at all, rather than a method-not-found from this daemon's
			// router -- which only registers thread/read).
			resumeTimeReads := 0
			resumesAtRead := -1
			var resumeCalls *int
			var cfg hubcore.WebConfig
			var sid string
			cfg, sid, resumeCalls = parityResumeFixture(t, func(daemon *appserver.Server) {
				appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
					// The resume's thread read is the last thing resume does.
					// Removing the owning source here makes the post-resume retry's
					// sourceForThread fail: the pre-dispatch failure under test.
					resumeTimeReads++
					resumesAtRead = *resumeCalls
					if hubSources != nil {
						hubSources.Remove("local")
					}
					return appwire.ThreadReadResponse{Thread: appwire.Thread{
						ID:        sessionID,
						SessionID: sessionID,
						Source:    "local",
						Evener: appwire.EvenerThread{
							Ref:          params.Ref,
							Capabilities: appwire.ThreadCapabilities{Clear: true, Goal: true, SharedNotes: true},
						},
					}}, nil
				})
			})
			sessionID = sid

			server, web := newHubRPCTestServerWithWeb(t, cfg)
			defer server.Close()
			hubSources = web.sources

			client := dialHubRPC(t, server)
			defer client.Close()
			if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
				t.Fatalf("Initialize: %v", err)
			}

			mutationID := "mutation-source-resolution-retry"
			ref := "local:" + sessionID
			err := tc.dispatch(t, client, ref, sessionID, mutationID)
			if err == nil {
				t.Fatal("the mutation reported success although the retry's source resolution failed")
			}
			if *resumeCalls != 1 {
				t.Fatalf("resume calls=%d, want 1", *resumeCalls)
			}
			// The premises the assertions rest on, pinned rather than assumed: the
			// daemon was read exactly once, and that read happened AFTER the resume
			// (so the source was removed at resume time, and the first attempt
			// failed against the hub's own local source -- a session-unavailable
			// failure, the only thing that triggers this resume).
			if resumeTimeReads != 1 {
				t.Fatalf("daemon thread/read calls=%d, want 1 (the resume's own read; the first attempt must not reach the daemon)", resumeTimeReads)
			}
			if resumesAtRead != 1 {
				t.Fatalf("the daemon's thread/read ran with %d spawner resumes recorded, want 1: the source must be removed by the RESUME's read, not the initial capability read", resumesAtRead)
			}

			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatalf("error %T=%v, want a WireError", err, err)
			}
			data, ok := wire.Data.(map[string]any)
			if !ok {
				t.Fatalf("wire data %#v is not map[string]any", wire.Data)
			}
			if data["clientMutationId"] != mutationID ||
				data["mutationOutcome"] != string(appwire.MutationOutcomeUnknown) ||
				data["retryDisposition"] != string(appwire.RetryDispositionBlocked) {
				t.Fatalf("an original attempt that reached a source is not proven pre-dispatch, so a retry that failed source resolution must be blocked-unknown for %q: wire=%+v data=%#v",
					mutationID, wire, data)
			}
		})
	}
}
