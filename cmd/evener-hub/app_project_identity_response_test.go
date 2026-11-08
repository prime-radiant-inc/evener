package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

// liveProjectThread is the shape a daemon sends before the hub projects its
// canonical project identity: a real CWD, no ProjectID or ProjectPath.
func liveProjectThread(id, workingDir string) appwire.Thread {
	return appwire.Thread{
		ID:        id,
		SessionID: id,
		CWD:       workingDir,
		Source:    appwire.LocalSourceID,
		Evener:    appwire.EvenerThread{Ref: appwire.Ref{SourceID: appwire.LocalSourceID, ThreadID: id}.String()},
	}
}

func wantProject(t *testing.T, workingDir string) identifier.Project {
	t.Helper()
	project, err := identifier.ResolveProject(workingDir)
	if err != nil {
		t.Fatalf("ResolveProject(%q): %v", workingDir, err)
	}
	return project
}

func assertThreadProject(t *testing.T, thread appwire.Thread, want identifier.Project) {
	t.Helper()
	if thread.ProjectID != want.ID || thread.ProjectPath != want.CanonicalPath {
		t.Fatalf("project identity = (%q, %q), want (%q, %q)",
			thread.ProjectID, thread.ProjectPath, want.ID, want.CanonicalPath)
	}
}

func newProjectReadOperations(t *testing.T, cfg hubcore.WebConfig, source appsource.Source) *sessionOperations {
	t.Helper()
	sources := appsource.NewRegistry()
	if source != nil {
		sources.Add(source)
	}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", Version: "test", SourceID: appwire.LocalSourceID})
	return &sessionOperations{
		cfg:     cfg,
		sources: sources,
		relays:  newHubRelay(server, cfg, sources),
		server:  server,
	}
}

// TestLiveThreadReadReturnsProjectIdentity covers the three thread/read
// response shapes that used to annotate a copy of the thread and discard the
// result: the lean read, the live item-page read, and the saved item-page
// merge (#4712).
func TestLiveThreadReadReturnsProjectIdentity(t *testing.T) {
	t.Run("lean read", func(t *testing.T) {
		workingDir := t.TempDir()
		want := wantProject(t, workingDir)
		operations := newProjectReadOperations(t, hubcore.WebConfig{Past: hubcore.NewPastIndex("")},
			&scriptedAppSource{id: appwire.LocalSourceID, thread: liveProjectThread("th_project_lean", workingDir)})

		response, err := operations.read(context.Background(), appwire.ThreadReadParams{Ref: "local:th_project_lean"}, false)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		assertThreadProject(t, response.Thread, want)
	})

	t.Run("live item page", func(t *testing.T) {
		workingDir := t.TempDir()
		want := wantProject(t, workingDir)
		operations := newProjectReadOperations(t, hubcore.WebConfig{Past: hubcore.NewPastIndex("")},
			&scriptedAppSource{id: appwire.LocalSourceID, thread: liveProjectThread("th_project_items", workingDir)})

		response, err := operations.read(context.Background(), appwire.ThreadReadParams{
			Ref: "local:th_project_items", IncludeTurns: true,
		}, false)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		assertThreadProject(t, response.Thread, want)
	})

	t.Run("remote thread keeps its identity", func(t *testing.T) {
		workingDir := t.TempDir()
		thread := liveProjectThread("th_remote_project", workingDir)
		thread.Source = "host"
		thread.Evener.Ref = "host:th_remote_project"
		thread.ProjectID = "remote-project-id"
		thread.ProjectPath = "/remote/repo"
		operations := newProjectReadOperations(t, hubcore.WebConfig{Past: hubcore.NewPastIndex("")},
			&scriptedAppSource{id: "host", thread: thread})

		response, err := operations.read(context.Background(), appwire.ThreadReadParams{Ref: "host:th_remote_project"}, false)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if response.Thread.ProjectID != "remote-project-id" || response.Thread.ProjectPath != "/remote/repo" {
			t.Fatalf("remote project identity = (%q, %q), want the remote hub's own identity",
				response.Thread.ProjectID, response.Thread.ProjectPath)
		}
	})

	t.Run("local identity authority replaces populated fields", func(t *testing.T) {
		workingDir := t.TempDir()
		want := wantProject(t, workingDir)
		thread := liveProjectThread("th_project_stale", workingDir)
		thread.ProjectID = "stale-project-id"
		thread.ProjectPath = "/stale/project"
		operations := newProjectReadOperations(t, hubcore.WebConfig{Past: hubcore.NewPastIndex("")},
			&scriptedAppSource{id: appwire.LocalSourceID, thread: thread})

		response, err := operations.read(context.Background(), appwire.ThreadReadParams{Ref: "local:th_project_stale"}, false)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		assertThreadProject(t, response.Thread, want)
	})

	t.Run("saved item page merge", func(t *testing.T) {
		root := t.TempDir()
		workingDir := filepath.Join(root, "project")
		if err := os.MkdirAll(workingDir, 0o755); err != nil {
			t.Fatal(err)
		}
		stateDir := filepath.Join(root, "state", "projects", "project-4712-saved-0000000000")
		sessionID := "02wMz5Txv5aIxgf9yVdd0N"
		if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
			t.Fatal(err)
		}
		now := time.Unix(1700000000, 0).UTC()
		if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
			ID:        sessionID,
			ProfileID: "openai",
			Model:     "gpt-5",
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: workingDir},
			CreatedAt: now,
			UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		writer, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl"), transcript.Header{
			SessionID: sessionID, CreatedAt: now, ProfileID: "openai", Model: "gpt-5",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		indexPastTranscriptForTest(t, stateDir, sessionID)
		cfg := hubcore.WebConfig{Past: newRebuiltPastIndex(t, filepath.Join(root, "state", "projects", "*"))}
		operations := newProjectReadOperations(t, cfg,
			&scriptedAppSource{id: appwire.LocalSourceID, thread: liveProjectThread(sessionID, workingDir)})

		response, err := operations.read(context.Background(), appwire.ThreadReadParams{
			Ref: "local:" + sessionID, IncludeTurns: true,
		}, false)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		assertThreadProject(t, response.Thread, wantProject(t, workingDir))
	})
}

// TestHubRPCThreadReadReturnsProjectIdentity proves the same projection over
// the real appwire socket path, not just the handler.
func TestHubRPCThreadReadReturnsProjectIdentity(t *testing.T) {
	workingDir := t.TempDir()
	want := wantProject(t, workingDir)
	srv := newWebSourceTestServer(t, &scriptedAppSource{
		id:     appwire.LocalSourceID,
		thread: liveProjectThread("th_rpc_project", workingDir),
	})
	client := dialHubRPCInitialized(t, srv)
	response, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:th_rpc_project"})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	assertThreadProject(t, response.Thread, want)
}

// TestThreadStartReturnsProjectIdentity covers both local thread/start
// responses that used to annotate a copy: the thread read back from the
// spawned source and the synthesized thread kept when the source cannot be
// resolved (#4712).
func TestThreadStartReturnsProjectIdentity(t *testing.T) {
	newSpawner := func() *fakeRPCSpawner {
		return &fakeRPCSpawner{spawn: func(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
			return rendezvous.Entry{
				PID:        201,
				Protocol:   appwire.ProtocolVersion,
				SourceID:   appwire.LocalSourceID,
				ThreadID:   "th_start_project",
				SessionID:  "th_start_project",
				WorkingDir: req.WorkingDir,
			}, nil
		}}
	}
	t.Run("read back from source", func(t *testing.T) {
		workingDir := t.TempDir()
		want := wantProject(t, workingDir)
		sources := appsource.NewRegistry()
		sources.Add(&scriptedAppSource{id: appwire.LocalSourceID, thread: liveProjectThread("th_start_project", workingDir)})
		cfg := hubcore.WebConfig{
			HubStateRoot:     t.TempDir(),
			LaunchConfigRoot: t.TempDir(),
			Spawner:          newSpawner(),
		}

		response, err := hubThreadStart(context.Background(), cfg, sources, appwire.ThreadStartParams{
			Model: "openai/gpt-5", CWD: workingDir,
		})
		if err != nil {
			t.Fatalf("hubThreadStart: %v", err)
		}
		assertThreadProject(t, response.Thread, want)
	})

	t.Run("synthesized after source resolution failure", func(t *testing.T) {
		workingDir := t.TempDir()
		want := wantProject(t, workingDir)
		cfg := hubcore.WebConfig{
			HubStateRoot:     t.TempDir(),
			LaunchConfigRoot: t.TempDir(),
			Spawner:          newSpawner(),
		}

		response, err := hubThreadStart(context.Background(), cfg, appsource.NewRegistry(), appwire.ThreadStartParams{
			Model: "openai/gpt-5", CWD: workingDir,
		})
		if err != nil {
			t.Fatalf("hubThreadStart: %v", err)
		}
		assertThreadProject(t, response.Thread, want)
	})
}

// TestResumedThreadResponseReturnsProjectIdentity covers the shared resume
// tail that reads the resumed thread back and used to annotate a copy of it
// (#4712).
func TestResumedThreadResponseReturnsProjectIdentity(t *testing.T) {
	workingDir := t.TempDir()
	want := wantProject(t, workingDir)
	sources := appsource.NewRegistry()
	sources.Add(&scriptedAppSource{id: appwire.LocalSourceID, thread: liveProjectThread("th_resumed_project", workingDir)})

	response, err := hubResumedThreadResponse(context.Background(), hubcore.WebConfig{}, sources, "th_resumed_project", "th_resumed_project")
	if err != nil {
		t.Fatalf("hubResumedThreadResponse: %v", err)
	}
	assertThreadProject(t, response.Thread, want)
}

// TestResumeRosterDoubleCheckReturnsProjectIdentity covers the resume path that
// confirms a freshly spawned daemon through its read when discovery left the
// entry unconfirmed (#4712).
func TestResumeRosterDoubleCheckReturnsProjectIdentity(t *testing.T) {
	workingDir := t.TempDir()
	want := wantProject(t, workingDir)
	sessionID := "02wMz5Txv5aIxgf9yVdd0N"
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: appwire.LocalSourceID})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: liveProjectThread(sessionID, workingDir)}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	entry := rendezvous.Entry{
		PID:        106,
		Protocol:   appwire.ProtocolVersion,
		Endpoint:   "ws" + strings.TrimPrefix(daemonHTTP.URL, "http"),
		SourceID:   appwire.LocalSourceID,
		ThreadID:   sessionID,
		SessionID:  sessionID,
		WorkingDir: workingDir,
	}
	spawner := &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
		writeRendezvous(t, runDir, entry)
		return entry, nil
	}}
	// A failed probe leaves discovery unable to confirm the spawned entry, so
	// resume must confirm it through the daemon's own read.
	roster := hubcore.NewRoster(runDir, failedRPCProber{})
	cfg := hubcore.WebConfig{
		RunDir:  runDir,
		Roster:  roster,
		Spawner: spawner,
		Past:    hubcore.NewPastIndex(""),
	}
	response, err := resumeThreadLocked(context.Background(), cfg, newHubSourceRegistry(cfg), appwire.ThreadResumeParams{Session: sessionID})
	if err != nil {
		t.Fatalf("resumeThreadLocked: %v", err)
	}
	assertThreadProject(t, response.Thread, want)
}
