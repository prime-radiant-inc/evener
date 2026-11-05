package hub

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/appwire/appwiretest"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

type resumeAfterSubscribeUnavailableSource struct {
	relayBroadcastSource
	mu          sync.Mutex
	calls       int
	startPrompt string
}

func (s *resumeAfterSubscribeUnavailableSource) ID() string { return "local" }

func (s *resumeAfterSubscribeUnavailableSource) SubscribeThread(ctx context.Context, params appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	s.mu.Lock()
	s.calls++
	calls := s.calls
	s.mu.Unlock()
	if calls == 1 {
		return nil, appwire.SessionUnavailable("stale relay subscription")
	}
	return s.relayBroadcastSource.SubscribeThread(ctx, params)
}

func (s *resumeAfterSubscribeUnavailableSource) StartTurn(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	s.mu.Lock()
	s.startPrompt = inputTextForTest(params.Input)
	s.mu.Unlock()
	return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_resumed"}}, nil
}

func (s *resumeAfterSubscribeUnavailableSource) subscribeCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *resumeAfterSubscribeUnavailableSource) lastStartPrompt() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startPrompt
}

func makeResumeSession(t *testing.T, root, sessionID, profileID, model string) (string, *hubcore.PastIndex) {
	t.Helper()
	stateDir := filepath.Join(root, "projects", "project-resume-0000000000")
	workingDir := t.TempDir()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID:        sessionID,
		ProfileID: profileID,
		Model:     model,
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: workingDir},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))
	return stateDir, past
}

func TestResumeRequestForConfigPassesThroughOpenAIProfileID(t *testing.T) {
	root := t.TempDir()
	sessionID := "02wMz5Txv1C3Hut0M8GCeB"
	stateDir, past := makeResumeSession(t, root, sessionID, "openai", "gpt-4o")

	req, err := resumeRequestForConfig(hubcore.WebConfig{Past: past}, sessionID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Provider != "openai" {
		t.Fatalf("provider=%q, want %q", req.Provider, "openai")
	}
	if req.Resolved.Effective.Model != "openai/gpt-4o" {
		t.Fatalf("model=%q, want %q", req.Resolved.Effective.Model, "openai/gpt-4o")
	}
	if req.WorkingDir == "" || req.StateDir != stateDir {
		t.Fatalf("resume request=%+v", req)
	}
}

func TestResumeRequestForConfigPassesThroughCustomProfileID(t *testing.T) {
	root := t.TempDir()
	sessionID := "02wMz5Txv2enqVTitaig6F"
	_, past := makeResumeSession(t, root, sessionID, "work", "gpt-4o")

	req, err := resumeRequestForConfig(hubcore.WebConfig{Past: past}, sessionID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Provider != "work" {
		t.Fatalf("provider=%q, want %q", req.Provider, "work")
	}
	if req.Resolved.Effective.Model != "work/gpt-4o" {
		t.Fatalf("model=%q, want %q", req.Resolved.Effective.Model, "work/gpt-4o")
	}
}

func TestResumeRequestForConfigErrorsOnEmptyProfileID(t *testing.T) {
	root := t.TempDir()
	sessionID := "02wMz5Txv47YP64RR3B9YJ"
	_, past := makeResumeSession(t, root, sessionID, "", "gpt-4o")

	_, err := resumeRequestForConfig(hubcore.WebConfig{Past: past}, sessionID)
	if err == nil {
		t.Fatal("expected error for empty profile id, got nil")
	}
}

// TestResumeRequestForConfigCarriesLaunchAPILog proves the resume path
// consults the session's launch layers for api_log: buildResumeArgs passes
// the value through to the daemon, so without this carry an explicit
// api_log choice would be silently dropped and the hub floor (or the
// daemon's own default) would fill the gap instead. An unset layer must
// stay nil so the hub floor still applies, and a broken launch.toml must
// not block the resume.
func TestResumeRequestForConfigCarriesLaunchAPILog(t *testing.T) {
	for _, tc := range []struct {
		name       string
		launchTOML string // global launch.toml content; "" writes nothing
		wantNil    bool
		wantOn     bool
	}{
		{name: "layer true is carried", launchTOML: "api_log = true\n", wantOn: true},
		{name: "layer false is carried, not dropped", launchTOML: "api_log = false\n", wantOn: false},
		{name: "unset stays nil for the hub floor", launchTOML: "", wantNil: true},
		{name: "broken launch.toml resumes with api_log unset", launchTOML: "not = [toml", wantNil: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			sessionID := hubtest.SessionID(t)
			_, past := makeResumeSession(t, root, sessionID, "openai", "gpt-4o")
			launchRoot := filepath.Join(root, "launchroot")
			if tc.launchTOML != "" {
				if err := os.MkdirAll(launchRoot, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(launchRoot, "launch.toml"), []byte(tc.launchTOML), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			req, err := resumeRequestForConfig(hubcore.WebConfig{Past: past, LaunchConfigRoot: launchRoot}, sessionID)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantNil {
				if req.Resolved.Effective.APILog != nil {
					t.Fatalf("APILog = %v, want nil", *req.Resolved.Effective.APILog)
				}
				return
			}
			if got := req.Resolved.Effective.APILog; got == nil || *got != tc.wantOn {
				t.Fatalf("APILog = %v, want %v", got, tc.wantOn)
			}
			if got := req.Resolved.Effective.Model; got != "openai/gpt-4o" {
				t.Fatalf("Model = %q, want openai/gpt-4o (api_log carry must not clobber it)", got)
			}
		})
	}
}

// TestResumeRequestForConfigUsesRestoreRootWhenWorktreeActive proves the
// native worktree tools spec §7 "Hub consumers" migration: a session
// actively inside a worktree must resume with `--dir` set to its restore
// root, not the worktree path — Task 18's resume re-entry logic takes the
// session back into the worktree itself; hub-driven `--dir` must not launch
// straight into it (or a deleted corpse of it), bypassing the lock and
// validation rules.
func TestResumeRequestForConfigUsesRestoreRootWhenWorktreeActive(t *testing.T) {
	root := t.TempDir()
	sessionID := "02wMz5Txv5aIxgf9yVdd0N"
	stateDir := filepath.Join(root, "projects", "project-resume-0000000000")
	restoreRoot := t.TempDir()
	worktreePath := t.TempDir()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID:                  sessionID,
		ProfileID:           "openai",
		Model:               "gpt-4o",
		EnvInfo:             schema.EnvironmentInfo{WorkingDir: worktreePath},
		WorktreePath:        worktreePath,
		WorktreeManaged:     true,
		WorktreeRestoreRoot: restoreRoot,
		CreatedAt:           time.Now().UTC(),
		UpdatedAt:           time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))

	req, err := resumeRequestForConfig(hubcore.WebConfig{Past: past}, sessionID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.WorkingDir != restoreRoot {
		t.Fatalf("resume dir=%q, want restore root %q (not the worktree path)", req.WorkingDir, restoreRoot)
	}
}

func TestHubRPCThreadResumeSpawnsAndReadsDaemon(t *testing.T) {
	// The stand-in answers the hub's resume read and, with no roster in this
	// hub, the rendezvous directory's identity probe (thread/list and a bare
	// thread/read naming the root) that lists it in the first place. Like a
	// real daemon it carries one id as both thread and session id: the probe
	// lists it by session id, and the resume reads it by thread id.
	resumed := appwire.Thread{ID: "th_resumed", SessionID: "th_resumed", Evener: appwire.EvenerThread{Ref: "local:th_resumed"}}
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadList, func(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		return appwire.ThreadListResponse{Data: []appwire.Thread{resumed}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if params.Ref != "" && params.Ref != "local:th_resumed" {
			t.Fatalf("ref=%q", params.Ref)
		}
		return appwire.ThreadReadResponse{Thread: resumed}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	spawner := &fakeRPCSpawner{
		resume: func(ctx context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
			if req.SessionID != "sess_old" {
				t.Fatalf("resume session=%q", req.SessionID)
			}
			entry := rendezvous.Entry{
				// The probe, not this PID, decides whether the entry is
				// listed; it still has to be a live process.
				PID:       os.Getpid(),
				Protocol:  appwire.ProtocolVersion,
				Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
				SourceID:  "local",
				ThreadID:  "th_resumed",
				SessionID: "th_resumed",
			}
			writeRendezvous(t, runDir, entry)
			return entry, nil
		},
	}

	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Spawner: spawner, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	resp, err := appwiretest.ThreadResume(context.Background(), client, appwire.ThreadResumeParams{Session: "sess_old"})
	if err != nil {
		t.Fatalf("ThreadResume: %v", err)
	}
	if resp.Thread.ID != "th_resumed" || resp.Thread.Evener.Ref != "local:th_resumed" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
}

func TestHubRPCThreadResumeConfirmsSpawnAfterDiscoveryFailure(t *testing.T) {
	for _, fault := range []string{"status", "listing", "confirmed", "read", "identity"} {
		t.Run(fault, func(t *testing.T) {
			const sessionID = "resumed-owner"
			daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
			appserver.HandleTyped(daemon.Router(), appwire.MethodThreadList, func(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
				if fault == "confirmed" {
					return appwire.ThreadListResponse{Data: []appwire.Thread{{ID: sessionID, SessionID: sessionID, Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}}}}, nil
				}
				return appwire.ThreadListResponse{}, appwire.Unavailable("inventory unavailable")
			})
			appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
				if fault == "read" {
					return appwire.ThreadReadResponse{}, appwire.Unavailable("read unavailable")
				}
				id := sessionID
				if fault == "identity" {
					id = "different-owner"
				}
				return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: id, SessionID: id, Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}}}, nil
			})
			appserver.HandleTyped(daemon.Router(), appwire.MethodTurnStart, func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
				return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "delivered"}}, nil
			})
			peer := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
			defer peer.Close()
			runDir := t.TempDir()
			roster := hubcore.NewRoster(runDir, &hubcore.StatusProber{})
			entry := rendezvous.Entry{PID: os.Getpid(), Protocol: appwire.ProtocolVersion, Endpoint: "ws" + strings.TrimPrefix(peer.URL, "http"), SourceID: "local", ThreadID: sessionID, SessionID: sessionID}
			spawns := 0
			spawner := &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
				spawns++
				writeRendezvous(t, runDir, entry)
				if fault == "confirmed" {
					roster.Refresh()
				}
				if fault == "listing" || fault == "confirmed" {
					if err := os.WriteFile(filepath.Join(runDir, "1.json"), []byte("{"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				return entry, nil
			}}
			hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner, ResumeLocks: hubcore.NewResumeLocks()})
			defer hub.Close()
			client := dialHubRPC(t, hub)
			defer client.Close()
			if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
				t.Fatal(err)
			}
			response, err := appwiretest.ThreadResume(context.Background(), client, appwire.ThreadResumeParams{Session: sessionID})
			if spawns != 1 {
				t.Fatalf("spawns=%d", spawns)
			}
			if fault == "read" || fault == "identity" {
				if err == nil || roster.HasConfirmedEntry(entry) {
					t.Fatalf("unverified owner admitted: error=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if response.Thread.ID != sessionID || !roster.HasConfirmedEntry(entry) {
				t.Fatalf("resume did not publish owner: %+v", response.Thread)
			}
			if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID}); err != nil {
				t.Fatal(err)
			}
			turn, err := client.TurnStart(context.Background(), appwire.TurnStartParams{Ref: "local:" + sessionID, ClientMutationID: "resume-followup", ExpectedInstanceID: sessionID, Input: []appwire.InputItem{{Type: "text", Text: "continue"}}})
			if err != nil || turn.Turn.ID != "delivered" {
				t.Fatalf("followup=%+v error=%v", turn, err)
			}
		})
	}
}

func TestHubRPCSessionActionsRefreshReplacedDaemonOwnership(t *testing.T) {
	for _, method := range []string{appwire.MethodThreadShutdown, appwire.MethodThreadModelSet, appwire.MethodThreadVisionModelSet, appwire.MethodThreadCompactStart, appwire.MethodGoalSet} {
		t.Run(method, func(t *testing.T) { testHubSubscribedReadReplacedOwner(t, true, method, serveProtocolMismatch) })
	}
}

func TestHubRPCTurnStartRefreshesReplacedDaemonBeforeRelay(t *testing.T) {
	testHubSubscribedReadReplacedOwner(t, true, appwire.MethodTurnStart, serveProtocolMismatch)
}

func TestHubRPCCachedRouteRefreshesTypedProtocolMismatch(t *testing.T) {
	for _, method := range []string{appwire.MethodThreadRead, appwire.MethodTurnStart, appwire.MethodThreadModelSet} {
		t.Run(method, func(t *testing.T) { testHubSubscribedReadReplacedOwner(t, true, method, serveTypedProtocolMismatch) })
	}
}

func TestHubRPCThreadResumeVerifiesExistingOwnerWhenDiscoveryFails(t *testing.T) {
	for _, state := range []string{"healthy", "unreachable", "absent"} {
		t.Run(state, func(t *testing.T) {
			const sessionID = "confirmed-owner"
			daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
			thread := appwire.Thread{ID: sessionID, SessionID: sessionID, Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}}
			appserver.HandleTyped(daemon.Router(), appwire.MethodThreadList, func(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
				return appwire.ThreadListResponse{Data: []appwire.Thread{thread}}, nil
			})
			appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
				return appwire.ThreadReadResponse{Thread: thread}, nil
			})
			peer := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
			defer peer.Close()
			runDir := t.TempDir()
			roster := hubcore.NewRoster(runDir, &hubcore.StatusProber{})
			if state != "absent" {
				writeRendezvous(t, runDir, rendezvous.Entry{PID: os.Getpid(), Protocol: appwire.ProtocolVersion, Endpoint: "ws" + strings.TrimPrefix(peer.URL, "http"), SourceID: "local", ThreadID: sessionID, SessionID: sessionID})
				roster.Refresh()
				if _, ok := roster.Find(sessionID); !ok {
					t.Fatal("owner was not confirmed")
				}
			}
			if state == "unreachable" {
				peer.Close()
			}
			if err := os.WriteFile(filepath.Join(runDir, "1.json"), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
			spawned := false
			hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, ResumeLocks: hubcore.NewResumeLocks(), Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
				spawned = true
				return rendezvous.Entry{}, errors.New("unexpected replacement")
			}}})
			defer hub.Close()
			client := dialHubRPC(t, hub)
			defer client.Close()
			if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
				t.Fatal(err)
			}
			response, err := appwiretest.ThreadResume(context.Background(), client, appwire.ThreadResumeParams{Session: sessionID})
			if spawned {
				t.Fatal("spawned despite incomplete ownership discovery")
			}
			if state == "healthy" {
				if err != nil {
					t.Fatal(err)
				}
				if response.Thread.ID != sessionID {
					t.Fatalf("thread=%+v", response.Thread)
				}
			} else if err == nil {
				t.Fatal("resume succeeded without a verified owner")
			}
			if roster.OwnershipError() == nil {
				t.Fatal("partial confirmation cleared the discovery failure")
			}
		})
	}
}

func TestHubRPCThreadResumeReplacesIncompatibleRosterDaemon(t *testing.T) {
	const sessionID = "sess_old"

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Evener:    appwire.EvenerThread{Ref: params.Ref},
		}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       104,
		Protocol:  "evener-appwire-v1",
		Endpoint:  "ws://127.0.0.1:1/rpc",
		SourceID:  "local",
		ThreadID:  sessionID,
		SessionID: sessionID,
	})
	roster := hubcore.NewRoster(runDir, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusIdle})
	roster.Refresh()
	if stale, ok := roster.Find(sessionID); !ok || stale.Status == "errored" {
		t.Fatalf("stale roster entry = %+v, %v; want non-errored incompatible daemon", stale, ok)
	}

	resumeCalls := 0
	spawner := &fakeRPCSpawner{
		resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
			if req.SessionID != sessionID {
				t.Fatalf("resume session=%q", req.SessionID)
			}
			resumeCalls++
			entry := rendezvous.Entry{
				PID:       105,
				Protocol:  appwire.ProtocolVersion,
				Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
				SourceID:  "local",
				ThreadID:  sessionID,
				SessionID: sessionID,
			}
			writeRendezvous(t, runDir, entry)
			roster.Refresh()
			return entry, nil
		},
	}

	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir:      runDir,
		Roster:      roster,
		Spawner:     spawner,
		Past:        hubcore.NewPastIndex(""),
		ResumeLocks: hubcore.NewResumeLocks(),
	})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	resp, err := appwiretest.ThreadResume(context.Background(), client, appwire.ThreadResumeParams{Session: sessionID})
	if err != nil {
		t.Fatalf("ThreadResume: %v", err)
	}
	if resumeCalls != 1 {
		t.Fatalf("resume calls=%d, want 1", resumeCalls)
	}
	if resp.Thread.ID != sessionID || resp.Thread.Evener.Ref != "local:"+sessionID {
		t.Fatalf("thread=%+v", resp.Thread)
	}
}

// A replacement spawn cannot win the session's exclusive API-log reservation
// while the daemon it is replacing is still running, so the operator's only
// way forward is to stop that process. The hub holds its pid and address in
// the roster at the moment it refuses to reuse it; the failure must hand both
// over instead of leaving the operator to find the holder with lsof (kata
// ew86, demonstrated live against a real v1 daemon).
func TestHubRPCThreadResumeNamesLiveIncompatibleDaemonWhenReplacementFails(t *testing.T) {
	const (
		sessionID   = "sess_old"
		blockerPID  = 104
		blockerHTTP = "127.0.0.1:61535"
		// Verbatim shape of the failure a real replacement daemon dies with.
		spawnFailure = "resume failed: process exited before rendezvous: exit status 1: " +
			"evener serve: session sess_old is already running; send work to the live session or fork it: " +
			"API log target is already running: /state/sessions/sess_old.api.jsonl"
	)

	tests := []struct {
		name        string
		rosterEntry *rendezvous.Entry
		wantContain []string
		wantAbsent  []string
	}{
		{
			name: "live incompatible daemon still owns the session",
			rosterEntry: &rendezvous.Entry{
				PID:       blockerPID,
				Protocol:  "evener-appwire-v1",
				Address:   blockerHTTP,
				Endpoint:  "ws://" + blockerHTTP + "/rpc",
				SourceID:  "local",
				ThreadID:  sessionID,
				SessionID: sessionID,
			},
			wantContain: []string{
				// the holder
				"pid 104",
				// why the hub will not just talk to it
				"evener-appwire-v1",
				appwire.ProtocolVersion,
				// the remedy the operator can actually run
				"kill 104",
				// the underlying cause, preserved
				spawnFailure,
			},
		},
		{
			name:        "no live daemon to blame",
			rosterEntry: nil,
			wantContain: []string{spawnFailure},
			// Nothing died holding the session, so the hub must not invent a
			// holder or a pid.
			wantAbsent: []string{"pid ", "/shutdown"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runDir := t.TempDir()
			var roster *hubcore.Roster
			if tc.rosterEntry != nil {
				writeRendezvous(t, runDir, *tc.rosterEntry)
				roster = hubcore.NewRoster(runDir, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusIdle})
			} else {
				roster = hubcore.NewRoster(runDir, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusIdle})
			}
			roster.Refresh()

			spawner := &fakeRPCSpawner{
				resume: func(_ context.Context, _ hubcore.ResumeRequest) (rendezvous.Entry, error) {
					return rendezvous.Entry{}, errors.New(spawnFailure)
				},
			}

			hub := newHubRPCTestServer(t, hubcore.WebConfig{
				RunDir:      runDir,
				Roster:      roster,
				Spawner:     spawner,
				Past:        hubcore.NewPastIndex(""),
				ResumeLocks: hubcore.NewResumeLocks(),
			})
			defer hub.Close()
			client := dialHubRPC(t, hub)
			defer client.Close()

			if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
				t.Fatalf("Initialize: %v", err)
			}
			_, err := appwiretest.ThreadResume(context.Background(), client, appwire.ThreadResumeParams{Session: sessionID})
			if err == nil {
				t.Fatal("ThreadResume succeeded, want the replacement spawn to fail")
			}
			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatalf("ThreadResume error %T=%v, want WireError", err, err)
			}
			for _, want := range tc.wantContain {
				if !strings.Contains(wire.Message, want) {
					t.Fatalf("resume error is missing %q:\n%s", want, wire.Message)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(wire.Message, absent) {
					t.Fatalf("resume error should not contain %q:\n%s", absent, wire.Message)
				}
			}
		})
	}
}

func TestHubRPCThreadResumeRelaysReturnedSourceThread(t *testing.T) {
	source := &startResumeRelaySource{
		id: "codex",
		thread: appwire.Thread{
			ID:        "th_resume_relay",
			SessionID: "th_resume_relay",
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:th_resume_relay", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	srv := newWebSourceTestServer(t, source)

	client := dialHubRPCInitialized(t, srv)
	resp, err := appwiretest.ThreadResume(context.Background(), client, appwire.ThreadResumeParams{Ref: "codex:th_resume_relay"})
	if err != nil {
		t.Fatalf("ThreadResume: %v", err)
	}
	if resp.Thread.Evener.Ref != "codex:th_resume_relay" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
	expectRelaySubscription(t, source.subscribed)

	source.notifications <- appwire.Notification{
		Method: appwire.NotifyOverlayDelta,
		Params: testRawJSON(t, appwire.OverlayDeltaParams{
			ThreadID: "th_resume_relay",
			Ref:      "codex:th_resume_relay",
			Delta:    "after resume",
		}),
	}
	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyOverlayDelta {
			t.Fatalf("method=%q", got.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for resume relay notification")
	}
}

func TestHubRPCTurnStartBlocksUnknownMutationWhenAutoResumeFails(t *testing.T) {
	oldResolve, oldResume := resolveTurnStartSource, resumeTurnStartThread
	t.Cleanup(func() {
		resolveTurnStartSource, resumeTurnStartThread = oldResolve, oldResume
	})

	const (
		mutationID    = "mutation-resume-failed"
		resumeMessage = "restore session: incompatible mutation snapshot"
	)
	tests := []struct {
		name           string
		configure      func(*int)
		wantStartCalls int
	}{
		{
			name: "initial source resolution",
			configure: func(_ *int) {
				resolveTurnStartSource = func(*appsource.Registry, string, string) (appsource.Source, error) {
					return nil, errors.New("source unavailable")
				}
			},
		},
		{
			name: "session unavailable while starting turn",
			configure: func(startCalls *int) {
				source := &scriptedAppSource{
					id: "local",
					startTurn: func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
						(*startCalls)++
						return appwire.TurnStartResponse{}, appwire.SessionUnavailable("daemon went away")
					},
				}
				resolveTurnStartSource = func(*appsource.Registry, string, string) (appsource.Source, error) {
					return source, nil
				}
			},
			wantStartCalls: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			startCalls := 0
			resumeCalls := 0
			tc.configure(&startCalls)
			resumeTurnStartThread = func(context.Context, hubcore.WebConfig, *appsource.Registry, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
				resumeCalls++
				return appwire.ThreadResumeResponse{}, appwire.HubLaunchError(resumeMessage)
			}

			server := newHubAppServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, appsource.NewRegistry())
			_, err := exactDispatch(context.Background(), t, server, appwire.MethodTurnStart, appwire.TurnStartParams{
				ClientMutationID: mutationID,
			})

			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatalf("TurnStart error %T=%v, want WireError", err, err)
			}
			data, ok := wire.Data.(appwire.ErrorData)
			if !ok ||
				wire.Code != appwire.CodeInternalError ||
				wire.Message != resumeMessage ||
				data.EvenerErrorInfo != appwire.ErrorMutationOutcomeUnknown ||
				data.ClientMutationID != mutationID ||
				data.MutationOutcome != appwire.MutationOutcomeUnknown ||
				data.RetryDisposition != appwire.RetryDispositionBlocked ||
				data.Cause != "persistenceUnavailable" {
				t.Fatalf("wire code=%d message=%q data=%#v", wire.Code, wire.Message, wire.Data)
			}
			if resumeCalls != 1 {
				t.Fatalf("resume calls=%d, want 1", resumeCalls)
			}
			if startCalls != tc.wantStartCalls {
				t.Fatalf("start calls=%d, want %d", startCalls, tc.wantStartCalls)
			}
		})
	}
}

func TestHubRPCTurnStartResumesPastThread(t *testing.T) {
	root := t.TempDir()
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: sessionID, SessionID: sessionID, Source: "local", Evener: appwire.EvenerThread{Ref: params.Ref, Capabilities: appwire.ThreadCapabilities{Send: true}}}}, nil
	})
	var gotPrompt string
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnStart, func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		gotPrompt = inputTextForTest(params.Input)
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
	client := dialHubRPCInitialized(t, hub)
	if _, err := client.TurnStart(context.Background(), appwire.TurnStartParams{ClientMutationID: "test-mutation", ExpectedInstanceID: sessionID, Ref: "local:" + sessionID, Input: []appwire.InputItem{{Type: "text", Text: "resume work"}}}); err != nil {
		t.Fatalf("TurnStart: %v", err)
	}
	if gotPrompt != "resume work" {
		t.Fatalf("prompt=%q", gotPrompt)
	}
}

func TestHubRPCTurnStartResumesPastThreadAfterRelaySubscribeUnavailable(t *testing.T) {
	root := t.TempDir()
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))

	source := &resumeAfterSubscribeUnavailableSource{
		thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Source:    "local",
			Evener:    appwire.EvenerThread{Ref: "local:" + sessionID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 1),
	}
	spawner := &fakeRPCSpawner{
		resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
			if req.WorkingDir != workingDir {
				t.Fatalf("resume request=%+v", req)
			}
			return rendezvous.Entry{
				Protocol:   appwire.ProtocolVersion,
				SourceID:   "local",
				ThreadID:   sessionID,
				SessionID:  sessionID,
				WorkingDir: workingDir,
			}, nil
		},
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Spawner: spawner, Past: past})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()
	client := dialHubRPCInitialized(t, srv)
	if _, err := client.TurnStart(context.Background(), appwire.TurnStartParams{ClientMutationID: "test-mutation", ExpectedInstanceID: sessionID, Ref: "local:" + sessionID, Input: []appwire.InputItem{{Type: "text", Text: "resume after relay"}}}); err != nil {
		t.Fatalf("TurnStart: %v", err)
	}
	if prompt := source.lastStartPrompt(); prompt != "resume after relay" {
		t.Fatalf("start prompt=%q", prompt)
	}
	if calls := source.subscribeCalls(); calls != 2 {
		t.Fatalf("subscribe calls=%d, want 2", calls)
	}
}

func TestHubRPCTurnStartDoesNotResumePastThreadOnLiveStartError(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSession(t, stateDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		appserver.SubscribeWithAdmission(ctx, sessionID, false)
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: sessionID, SessionID: sessionID, Source: "local", Evener: appwire.EvenerThread{Ref: params.Ref, Capabilities: appwire.ThreadCapabilities{Send: true}}}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnStart, func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		return appwire.TurnStartResponse{}, appwire.Unavailable("session is processing")
	})
	_, roster, runDir := startDaemonRoster(t, daemon, 107, sessionID, sessionID)
	resumeCalled := false
	spawner := &fakeRPCSpawner{
		resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			resumeCalled = true
			return rendezvous.Entry{}, errors.New("resume should not be called")
		},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner, Past: past})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	_, err := client.TurnStart(context.Background(), appwire.TurnStartParams{ClientMutationID: "test-mutation", ExpectedInstanceID: sessionID, Ref: "local:" + sessionID, Input: []appwire.InputItem{{Type: "text", Text: "resume work"}}})
	if err == nil || !strings.Contains(err.Error(), "session is processing") {
		t.Fatalf("TurnStart err=%v, want live start error", err)
	}
	if resumeCalled {
		t.Fatal("resume was called for a non-stale live StartTurn error")
	}
}

func TestHubRPCTurnStartDoesNotResumePastThreadOnGenericSubstringError(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSession(t, stateDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		appserver.SubscribeWithAdmission(ctx, sessionID, false)
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: sessionID, SessionID: sessionID, Source: "local", Evener: appwire.EvenerThread{Ref: params.Ref, Capabilities: appwire.ThreadCapabilities{Send: true}}}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnStart, func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		return appwire.TurnStartResponse{}, appwire.InternalError("tool output included connection refused")
	})
	_, roster, runDir := startDaemonRoster(t, daemon, 108, sessionID, sessionID)
	resumeCalled := false
	spawner := &fakeRPCSpawner{
		resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			resumeCalled = true
			return rendezvous.Entry{}, errors.New("resume should not be called")
		},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner, Past: past})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	_, err := client.TurnStart(context.Background(), appwire.TurnStartParams{ClientMutationID: "test-mutation", ExpectedInstanceID: sessionID, Ref: "local:" + sessionID, Input: []appwire.InputItem{{Type: "text", Text: "resume work"}}})
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("TurnStart err=%v, want live start error", err)
	}
	if resumeCalled {
		t.Fatal("resume was called for a generic live StartTurn error")
	}
}

func TestHubRPCTurnStartResumesPastThreadAndRelaysNotifications(t *testing.T) {
	root := t.TempDir()
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		appserver.SubscribeWithAdmission(ctx, sessionID, false)
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
	client := dialHubRPCInitialized(t, hub)
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if _, err := client.TurnStart(context.Background(), appwire.TurnStartParams{ClientMutationID: "test-mutation", ExpectedInstanceID: sessionID, Ref: "local:" + sessionID, Input: []appwire.InputItem{{Type: "text", Text: "resume work"}}}); err != nil {
		t.Fatalf("TurnStart: %v", err)
	}

	daemon.Broadcast(sessionID, appwire.NotifyOverlayDelta, appwire.OverlayDeltaParams{
		ThreadID: sessionID,
		Ref:      "local:" + sessionID,
		Delta:    "live update",
	})

	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyOverlayDelta {
			t.Fatalf("method=%q", got.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for resumed turn notification")
	}
}

func TestHubRPCTurnStartResumesPastThreadAfterLocalTransportError(t *testing.T) {
	for _, refreshFailure := range []bool{false, true} {
		t.Run(fmt.Sprint("refresh failure=", refreshFailure), func(t *testing.T) {
			root := t.TempDir()
			workingDir := t.TempDir()
			stateDir := filepath.Join(root, "projects", "project-past-0000000000")
			sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
			past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
			if _, err := past.Rebuild(); err != nil {
				t.Fatal(err)
			}

			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			staleAddress := ln.Addr().String()
			staleEndpoint := "ws://" + ln.Addr().String() + "/rpc"
			if err := ln.Close(); err != nil {
				t.Fatal(err)
			}

			daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
			appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
				appserver.SubscribeWithAdmission(ctx, sessionID, false)
				return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: sessionID, SessionID: sessionID, Source: "local", Evener: appwire.EvenerThread{Ref: params.Ref, Capabilities: appwire.ThreadCapabilities{Send: true}}}}, nil
			})
			appserver.HandleTyped(daemon.Router(), appwire.MethodTurnStart, func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
				return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_recovered"}}, nil
			})
			daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
			defer daemonHTTP.Close()

			runDir := t.TempDir()
			writeRendezvous(t, runDir, rendezvous.Entry{
				PID:       -1,
				Address:   staleAddress,
				Protocol:  appwire.ProtocolVersion,
				Endpoint:  staleEndpoint,
				SourceID:  "local",
				ThreadID:  sessionID,
				SessionID: sessionID,
				StartedAt: time.Now().UTC(), // fresh crash: within the roster's crash-retention window
			})
			prober := perAddrProber{byAddr: map[string]struct{ SessionID, Status string }{}}
			roster := hubcore.NewRoster(runDir, prober)
			roster.Refresh()
			if stale, ok := roster.Find(sessionID); !ok || stale.Status != "errored" || !stale.Crashed {
				t.Fatalf("stale roster entry = %+v, %v; want retained crash marker", stale, ok)
			}
			resumeCalled := false
			spawner := &fakeRPCSpawner{
				resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
					if req.WorkingDir != workingDir {
						t.Fatalf("resume request=%+v", req)
					}
					resumeCalled = true
					entry := rendezvous.Entry{
						PID:        110,
						Address:    daemonHTTP.Listener.Addr().String(),
						Protocol:   appwire.ProtocolVersion,
						Endpoint:   "ws" + daemonHTTP.URL[len("http"):],
						SourceID:   "local",
						ThreadID:   sessionID,
						SessionID:  sessionID,
						WorkingDir: workingDir,
						StartedAt:  time.Now().UTC(), // a real spawn stamps StartedAt; it must outrank the stale crashed entry
					}
					prober.byAddr[entry.Address] = struct{ SessionID, Status string }{SessionID: sessionID, Status: "idle"}
					writeRendezvous(t, runDir, entry)
					if refreshFailure {
						if err := os.WriteFile(filepath.Join(runDir, "1.json"), []byte("{"), 0600); err != nil {
							t.Fatal(err)
						}
					}

					roster.Refresh()
					return entry, nil
				},
			}
			hub := newHubRPCTestServer(t, hubcore.WebConfig{
				RunDir:      runDir,
				Roster:      roster,
				Spawner:     spawner,
				Past:        past,
				ResumeLocks: hubcore.NewResumeLocks(),
			})
			defer hub.Close()
			client := dialHubRPC(t, hub)
			defer client.Close()

			if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
				t.Fatalf("Initialize: %v", err)
			}
			resp, err := client.TurnStart(context.Background(), appwire.TurnStartParams{ClientMutationID: "test-mutation", ExpectedInstanceID: sessionID, Ref: "local:" + sessionID, Input: []appwire.InputItem{{Type: "text", Text: "resume work"}}})
			if err != nil {
				t.Fatalf("TurnStart: %v", err)
			}
			if !resumeCalled {
				t.Fatal("resume was not called after local transport error")
			}
			if resp.Turn.ID != "turn_recovered" {
				t.Fatalf("turn=%+v", resp.Turn)
			}

		})
	}
}

// sessionUnavailableOnceSource returns SessionUnavailable on the first
// StartTurn and tracks ResumeThread calls.
type sessionUnavailableOnceSource struct {
	relayLifecycleSource
	id          string
	mu          sync.Mutex
	startCalls  int
	resumeCalls int
	thread      appwire.Thread
}

func (s *sessionUnavailableOnceSource) ID() string { return s.id }

func (s *sessionUnavailableOnceSource) ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	return appwire.ThreadReadResponse{Thread: s.thread}, nil
}

func (s *sessionUnavailableOnceSource) ResumeThread(context.Context, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	s.mu.Lock()
	s.resumeCalls++
	s.mu.Unlock()
	return appwire.ThreadResumeResponse{Thread: s.thread}, nil
}

func (s *sessionUnavailableOnceSource) StartTurn(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	s.mu.Lock()
	s.startCalls++
	calls := s.startCalls
	s.mu.Unlock()
	if calls == 1 {
		return appwire.TurnStartResponse{}, appwire.SessionUnavailable("daemon went away")
	}
	return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_recovered"}}, nil
}

func (s *sessionUnavailableOnceSource) counts() (start, resume int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startCalls, s.resumeCalls
}

// TestHubRPCTurnStartDoesNotResumeUnknownNonLocalRef confirms the local
// past-index retry gate refuses non-local refs. The hub should bubble up the
// original SessionUnavailable error without attempting a resume.
func TestHubRPCTurnStartDoesNotResumeUnknownNonLocalRef(t *testing.T) {
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	fake := &sessionUnavailableOnceSource{
		canceled: make(chan struct{}, 1),
		id:       "remote",
		thread: appwire.Thread{
			ID:        "th_unknown",
			SessionID: "th_unknown",
			Source:    "remote",
			Evener: appwire.EvenerThread{
				Ref:          "remote:th_unknown",
				Capabilities: appwire.ThreadCapabilities{Send: true},
			},
		},
	}
	web.sources.Add(fake)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPCInitialized(t, srv)
	_, err := client.TurnStart(context.Background(), appwire.TurnStartParams{ClientMutationID: "test-mutation",
		ExpectedInstanceID: "th_unknown",
		Ref:                "remote:th_unknown",
		Input:              []appwire.InputItem{{Type: "text", Text: "should not resume"}},
	})
	if err == nil {
		t.Fatal("TurnStart succeeded, want SessionUnavailable error")
	}
	if !strings.Contains(err.Error(), "daemon went away") {
		t.Fatalf("err=%v, want daemon went away", err)
	}
	starts, resumes := fake.counts()
	if starts != 1 {
		t.Fatalf("StartTurn calls=%d, want 1 (no retry for unknown ref)", starts)
	}
	if resumes != 0 {
		t.Fatalf("ResumeThread calls=%d, want 0 (gate must reject unknown non-local ref)", resumes)
	}
}
