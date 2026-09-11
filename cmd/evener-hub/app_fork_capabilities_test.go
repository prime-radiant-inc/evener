package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/daemonprocess"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
	daemonserver "primeradiant.com/evener/server"
)

func TestHubForkCapabilityReadAndStatusMatchHubOwnership(t *testing.T) {
	const sessionID = "hub-fork-capability"
	const ref = "local:" + sessionID
	daemon := daemonserver.NewServer(daemonserver.ServerConfig{HubToken: "fork-test-token"})
	daemon.SetAppIdentity("local", sessionID)
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.AppServer().ServeWebSocket))
	t.Cleanup(daemonHTTP.Close)
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		Protocol: appwire.ProtocolVersion, Endpoint: "ws" + daemonHTTP.URL[len("http"):], SourceID: "local",
		ThreadID: sessionID, SessionID: sessionID, WorkspaceRef: ref, InstanceID: "instance-1", HubToken: "fork-test-token",
	})
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, StateDir: runDir, Past: hubcore.NewPastIndex("")})
	t.Cleanup(hub.Close)
	client := dialHubRPC(t, hub)
	t.Cleanup(func() { _ = client.Close() })
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	for _, turns := range []bool{false, true} {
		read, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: ref, IncludeTurns: turns, Subscribe: true, ItemLimit: 40})
		if err != nil {
			t.Fatal(err)
		}
		if !read.Thread.Evener.Capabilities.ForkFromTurn {
			t.Errorf("includeTurns=%v: hub read did not advertise its fork operation", turns)
		}
	}
	daemon.RecordAppEvent(events.SessionEvent{
		Kind: events.EventUserInput, SessionID: sessionID, Data: events.UserInputData{Text: "fork status fixture"},
	})
	deadline := time.After(2 * time.Second)
	for {
		select {
		case notification := <-client.Notifications():
			if notification.Method != appwire.NotifyThreadStatusChanged {
				continue
			}
			var status appwire.ThreadStatusChangedParams
			if err := json.Unmarshal(notification.Params, &status); err != nil {
				t.Fatal(err)
			}
			if status.Status.Type != appwire.ThreadStatusActive {
				continue
			}
			if status.Capabilities == nil || !status.Capabilities.ForkFromTurn || status.Capabilities.Send || status.Capabilities.Interrupt {
				t.Fatalf("hub relayed active capabilities=%+v, want fork enabled and daemon send/interrupt restrictions preserved", status.Capabilities)
			}
			return
		case <-deadline:
			t.Fatal("hub did not relay the status notification")
		}
	}
}

func TestHubForkAdmissionLoadsOwnershipWhenPastIndexIsUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		past   *hubcore.PastIndex
		child  bool
		wantOK bool
	}{
		{name: "past nil root", wantOK: true},
		{name: "past miss root", past: hubcore.NewPastIndex(""), wantOK: true},
		{name: "past nil persisted subagent", child: true, wantOK: true},
		{name: "past miss persisted subagent", past: hubcore.NewPastIndex(""), child: true, wantOK: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := t.TempDir()
			parentID := buildRPCParentSession(t, stateDir)
			targetID := parentID
			if tc.child {
				var err error
				targetID, err = agent.ForkSession(stateDir, parentID, 1, "child", "")
				if err != nil {
					t.Fatal(err)
				}
				meta, err := schema.LoadSessionMeta(stateDir, targetID)
				if err != nil {
					t.Fatal(err)
				}
				meta.IsSubagent = true
				if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
					t.Fatal(err)
				}
			}
			cfg := hubcore.WebConfig{StateDir: stateDir, Past: tc.past}
			_, err := hubThreadFork(t.Context(), cfg, nil, appwire.ThreadForkParams{
				Ref: "local:" + targetID, SourceTurnID: "turn_1", EditedInput: "forked input",
			})
			if tc.wantOK {
				if err != nil {
					t.Fatalf("root fork rejected: %v", err)
				}
			} else if err == nil {
				t.Fatal("fork failed unexpectedly")
			}
		})
	}
}

type liveSubagentProber struct {
	sessionID            string
	runningSubagentIDs   []string
	runningSubagentState map[string]string
}

func (p liveSubagentProber) Probe(rendezvous.Entry) hubcore.ProbeResult {
	return hubcore.ProbeResult{
		SessionID:             p.sessionID,
		Status:                appwire.ThreadStatusIdle,
		RunningSubagentIDs:    p.runningSubagentIDs,
		RunningSubagentStates: p.runningSubagentState,
		OK:                    true,
	}
}

func TestHubForkAdmissionRejectsLiveSubagentAliasFromRoster(t *testing.T) {
	for _, tc := range []struct {
		name string
		past *hubcore.PastIndex
	}{
		{name: "past nil"},
		{name: "past miss", past: hubcore.NewPastIndex("")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := t.TempDir()
			parentID := buildRPCParentSession(t, stateDir)
			childID, err := agent.ForkSession(stateDir, parentID, 1, "live child", "")
			if err != nil {
				t.Fatal(err)
			}
			childMeta, err := schema.LoadSessionMeta(stateDir, childID)
			if err != nil {
				t.Fatal(err)
			}
			childMeta.IsSubagent = true
			if err := schema.SaveSessionMeta(stateDir, childMeta); err != nil {
				t.Fatal(err)
			}

			runDir := t.TempDir()
			writeRendezvous(t, runDir, rendezvous.Entry{
				PID: os.Getpid(), SourceID: "local", ThreadID: parentID, SessionID: parentID,
				StateDir: stateDir,
			})
			roster := hubcore.NewRoster(runDir, liveSubagentProber{
				sessionID: parentID, runningSubagentIDs: []string{childID},
				runningSubagentState: map[string]string{childID: appwire.ThreadStatusActive},
			})
			roster.Refresh()
			if !roster.IsSubagentActive(childID) {
				t.Fatal("scripted live roster did not admit the child")
			}
			cfg := hubcore.WebConfig{StateDir: stateDir, Past: tc.past, Roster: roster}

			for _, sessionID := range []string{"", "wrong-session-id"} {
				thread := appwire.Thread{SessionID: sessionID, Evener: appwire.EvenerThread{
					Ref: "local:" + childID, Kind: "subagent",
					Capabilities: appwire.ThreadCapabilities{ForkFromTurn: true},
				}}
				if got := applyHubForkCapability(cfg, thread); got.Evener.Capabilities.ForkFromTurn {
					t.Fatalf("session_id=%q: live subagent alias retained fork capability", sessionID)
				}
			}

			_, err = hubThreadFork(t.Context(), cfg, nil, appwire.ThreadForkParams{
				Ref: "local:" + childID, SourceTurnID: "turn_1", EditedInput: "forked input",
			})
			if err == nil {
				t.Fatal("live subagent alias fork succeeded")
			}
			wire, ok := errors.AsType[appwire.WireError](err)
			if !ok || wire.Code != appwire.CodeUnavailable {
				t.Fatalf("live subagent alias fork error=%v, want structured unavailable", err)
			}
		})
	}
}

func TestHubForkCapabilityProjectionFencesRecoveryAndSubagents(t *testing.T) {
	for _, tc := range []struct {
		name     string
		thread   appwire.Thread
		wantFork bool
	}{
		{
			name:     "owned idle local session",
			thread:   appwire.Thread{Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}, Evener: appwire.EvenerThread{Ref: "local:root", Capabilities: appwire.ThreadCapabilities{ForkFromTurn: true}}},
			wantFork: true,
		},
		{
			name:     "resume required",
			thread:   appwire.Thread{Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}, Evener: appwire.EvenerThread{Ref: "local:root", ResumeRequired: true, Capabilities: appwire.ThreadCapabilities{ForkFromTurn: true}}},
			wantFork: false,
		},
		{
			name:     "restart required fallback",
			thread:   appwire.Thread{Status: appwire.ThreadStatus{Type: appwire.ThreadStatusRestartRequired}, Evener: appwire.EvenerThread{Ref: "local:root", Capabilities: appwire.ThreadCapabilities{ForkFromTurn: true}}},
			wantFork: false,
		},
		{
			name:     "resume required active flag",
			thread:   appwire.Thread{Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle, ActiveFlags: []string{"resumeRequired"}}, Evener: appwire.EvenerThread{Ref: "local:root", Capabilities: appwire.ThreadCapabilities{ForkFromTurn: true}}},
			wantFork: false,
		},
		{
			name:     "persisted subagent",
			thread:   appwire.Thread{Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}, Evener: appwire.EvenerThread{Ref: "local:child", Kind: "subagent", Capabilities: appwire.ThreadCapabilities{ForkFromTurn: true}}},
			wantFork: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := applyHubForkCapability(hubcore.WebConfig{StateDir: t.TempDir()}, tc.thread)
			if got.Evener.Capabilities.ForkFromTurn != tc.wantFork {
				t.Fatalf("forkFromTurn=%v, want %v (thread=%+v)", got.Evener.Capabilities.ForkFromTurn, tc.wantFork, got)
			}
		})
	}
	locks := hubcore.NewResumeLocks()
	finish := locks.BeginForceStop([]string{"root"})
	if err := locks.PersistForceStop([]string{"root"}, "root"); err != nil {
		t.Fatal(err)
	}
	finish(false)
	thread := appwire.Thread{Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}, Evener: appwire.EvenerThread{Ref: "local:root"}}
	if got := applyHubForkCapability(hubcore.WebConfig{StateDir: t.TempDir(), ResumeLocks: locks}, thread); got.Evener.Capabilities.ForkFromTurn {
		t.Fatal("current ResumeLocks recovery state re-advertised fork")
	}
	for _, want := range []bool{false, true} {
		thread := appwire.Thread{Evener: appwire.EvenerThread{Ref: "remote:thread", Capabilities: appwire.ThreadCapabilities{ForkFromTurn: want}}}
		if got := applyHubForkCapability(hubcore.WebConfig{StateDir: t.TempDir()}, thread); got.Evener.Capabilities.ForkFromTurn != want {
			t.Fatalf("remote capability=%v, want source declaration %v", got.Evener.Capabilities.ForkFromTurn, want)
		}
	}
	local := appwire.Thread{Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}, Evener: appwire.EvenerThread{Ref: "local:no-storage"}}
	if got := applyHubForkCapability(hubcore.WebConfig{}, local); got.Evener.Capabilities.ForkFromTurn {
		t.Fatal("local thread without a configured or persisted state directory advertised fork")
	}
}

func TestHubRPCPersistedSubagentCanForkAfterStop(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-subagent-0000000000")
	sessionID := buildRPCParentSession(t, stateDir)
	meta, err := schema.LoadSessionMeta(stateDir, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	meta.IsSubagent = true
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	list, err := client.ThreadList(t.Context(), appwire.ThreadListParams{IncludeSubagents: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Data) != 1 || !list.Data[0].Evener.Capabilities.ForkFromTurn {
		t.Fatalf("persisted subagent list capability=%+v, want fork enabled", list.Data)
	}
	read, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: "local:" + sessionID, Subscribe: true})
	if err != nil {
		t.Fatal(err)
	}
	if !read.Thread.Evener.Capabilities.ForkFromTurn {
		t.Fatalf("persisted subagent read did not advertise fork: %+v", read.Thread.Evener.Capabilities)
	}
	before := len(past.Search("", 100, 0))
	_, err = client.ThreadFork(t.Context(), appwire.ThreadForkParams{Ref: "local:" + sessionID, SourceTurnID: "turn_1", EditedInput: "fork"})
	if err != nil {
		t.Fatalf("persisted subagent fork: %v", err)
	}
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if after := len(past.Search("", 100, 0)); after != before+1 {
		t.Fatalf("accepted subagent fork changed persisted index count from %d to %d", before, after)
	}
	if _, ok := past.Find(sessionID); !ok {
		t.Fatal("parent subagent disappeared after accepted fork")
	}
}

func TestHubForkCapabilityKeepsDaemonPermissionsAndUnknownFields(t *testing.T) {
	original := appwire.Notification{Method: appwire.NotifyThreadStatusChanged, Params: testRawJSON(t, map[string]any{
		"ref": "local:root", "status": map[string]any{"type": "active"}, "future_field": 17,
		"capabilities": map[string]bool{"send": false, "steer": true, "forkFromTurn": false, "futureAction": true},
	})}
	got := stampForkCapability(original, true)
	var result struct {
		FutureField  int             `json:"future_field"`
		Capabilities map[string]bool `json:"capabilities"`
	}
	if err := json.Unmarshal(got.Params, &result); err != nil {
		t.Fatal(err)
	}
	if result.FutureField != 17 || result.Capabilities["send"] || !result.Capabilities["steer"] || !result.Capabilities["futureAction"] || !result.Capabilities["forkFromTurn"] {
		t.Fatalf("capability overlay changed unrelated data: %+v", result)
	}
	for _, raw := range []string{`{}`, `{"capabilities":null}`, `{"capabilities":false}`} {
		n := stampForkCapability(appwire.Notification{Method: appwire.NotifyThreadStatusChanged, Params: json.RawMessage(raw)}, true)
		if string(n.Params) != raw {
			t.Fatalf("missing capability set was fabricated: %s", n.Params)
		}
		for _, status := range []appwire.ThreadStatus{
			{Type: appwire.ThreadStatusRestartRequired},
			{Type: appwire.ThreadStatusIdle, ActiveFlags: []string{"resumeRequired"}},
		} {
			original := appwire.Notification{Method: appwire.NotifyThreadStatusChanged, Params: testRawJSON(t, appwire.ThreadStatusChangedParams{
				Status: status, Capabilities: &appwire.ThreadCapabilities{ForkFromTurn: true},
			})}
			got := stampForkCapability(original, true)
			var params appwire.ThreadStatusChangedParams
			if err := json.Unmarshal(got.Params, &params); err != nil {
				t.Fatal(err)
			}
			if params.Capabilities == nil || params.Capabilities.ForkFromTurn {
				t.Fatalf("status %q with recovery fence advertised fork: %+v", status.Type, params.Capabilities)
			}
		}
	}
}

func TestHubForkCapabilityFencesOnlyUnconfirmedTarget(t *testing.T) {
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: os.Getpid(), SessionID: "uncertain", ThreadID: "uncertain"})
	roster := hubcore.NewRoster(runDir, &fakeProber{shouldFail: true})
	roster.Refresh()
	if roster.OwnershipError() != nil || len(roster.UnconfirmedEntries()) != 1 {
		t.Fatalf("expected target-specific uncertainty: error=%v entries=%v", roster.OwnershipError(), roster.UnconfirmedEntries())
	}
	cfg := hubcore.WebConfig{StateDir: t.TempDir(), Roster: roster}
	for _, id := range []string{"uncertain", "unrelated"} {
		thread := appwire.Thread{Evener: appwire.EvenerThread{Ref: "local:" + id, Capabilities: appwire.ThreadCapabilities{ForkFromTurn: true}}}
		got := applyHubForkCapability(cfg, thread).Evener.Capabilities.ForkFromTurn
		if want := id == "unrelated"; got != want {
			t.Fatalf("thread %s fork=%v, want %v", id, got, want)
		}
	}
}

// A production hub is configured with the parent of `projects`, so a session
// admitted by the project scan lives one directory deeper than cfg.StateDir.
// Both fork modes must branch the child where its parent is stored.
func TestHubForkBranchesInAdmittedEntryStateDir(t *testing.T) {
	for _, mode := range []struct {
		name   string
		params appwire.ThreadForkParams
	}{
		{name: "aside", params: appwire.ThreadForkParams{Aside: true}},
		{name: "fork from turn", params: appwire.ThreadForkParams{SourceTurnID: "turn_1", EditedInput: "forked input"}},
	} {
		for _, index := range []struct {
			name string
			past *hubcore.PastIndex
		}{
			{name: "past nil"},
			{name: "past miss", past: hubcore.NewPastIndex("")},
		} {
			t.Run(mode.name+" "+index.name, func(t *testing.T) {
				root := t.TempDir()
				stateDir := filepath.Join(root, "projects", "project-fork-0000000000")
				parentID := buildRPCParentSession(t, stateDir)
				if index.past != nil {
					if _, ok := index.past.Find(parentID); ok {
						t.Fatal("past index answered for the parent: the project scan is not exercised")
					}
				}
				params := mode.params
				params.Ref = "local:" + parentID
				resp, err := hubThreadFork(t.Context(), hubcore.WebConfig{StateDir: root, Past: index.past}, nil, params)
				if err != nil {
					t.Fatalf("fork: %v", err)
				}
				if resp.Thread.ID == "" || resp.Thread.ID == parentID {
					t.Fatalf("thread=%+v", resp.Thread)
				}
				if _, err := schema.LoadSessionMeta(stateDir, resp.Thread.ID); err != nil {
					t.Fatalf("child was not branched beside its parent: %v", err)
				}
			})
		}
	}
}

// An explicit thread/resume clears the session's recovery fence as it returns.
// Its own response must carry the fork capability a read issued straight after
// it reports, or a client is told fork is unavailable on a session it can fork.
func TestHubExplicitResumeResponseAdvertisesClearedForkFence(t *testing.T) {
	var sessionID string
	cfg, id, resumeCalls := parityResumeFixture(t, func(daemon *appserver.Server) {
		thread := func(ref string) appwire.Thread {
			return appwire.Thread{
				ID: sessionID, SessionID: sessionID, Source: "local",
				Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
				Evener: appwire.EvenerThread{Ref: ref, InstanceID: sessionID, Capabilities: appwire.ThreadCapabilities{Send: true}},
			}
		}
		appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return appwire.ThreadReadResponse{Thread: thread(params.Ref)}, nil
		})
		appserver.HandleTyped(daemon.Router(), appwire.MethodThreadList, func(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
			return appwire.ThreadListResponse{Data: []appwire.Thread{thread(localAppRef(sessionID))}}, nil
		})
	})
	sessionID = id
	locks := hubcore.NewResumeLocks()
	finish := locks.BeginForceStop([]string{sessionID})
	if err := locks.PersistForceStop([]string{sessionID}, sessionID); err != nil {
		t.Fatal(err)
	}
	finish(true)
	cfg.ResumeLocks = locks

	hub := newHubRPCTestServer(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	resumed, err := client.ThreadResume(t.Context(), appwire.ThreadResumeParams{Ref: "local:" + sessionID})
	if err != nil {
		t.Fatal(err)
	}
	read, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: "local:" + sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if !read.Thread.Evener.Capabilities.ForkFromTurn {
		t.Fatalf("read after explicit resume did not advertise fork: %+v", read.Thread.Evener.Capabilities)
	}
	if !resumed.Thread.Evener.Capabilities.ForkFromTurn {
		t.Fatalf("resume response fork=%v, want the capability the following read reports", resumed.Thread.Evener.Capabilities.ForkFromTurn)
	}
	if *resumeCalls != 1 {
		t.Fatalf("resume launches=%d", *resumeCalls)
	}
}

// A delegate a live parent daemon is running in process is daemon-owned, and
// the capability projection and the fork RPC must say so from the same signal:
// neither the persisted IsSubagent flag nor the projected wire kind is reliably
// present on every live alias, so either alone lets the two answers diverge.
func TestHubForkFencesLiveDelegateFromOneSignal(t *testing.T) {
	for _, persisted := range []bool{false, true} {
		for _, kind := range []string{"", "subagent"} {
			name := map[bool]string{false: "meta is a fork", true: "meta is a subagent"}[persisted] +
				map[string]string{"": " untyped thread", "subagent": " subagent thread"}[kind]
			t.Run(name, func(t *testing.T) {
				stateDir := t.TempDir()
				parentID := buildRPCParentSession(t, stateDir)
				childID, err := agent.ForkSession(stateDir, parentID, 1, "live delegate", "")
				if err != nil {
					t.Fatal(err)
				}
				if persisted {
					meta, err := schema.LoadSessionMeta(stateDir, childID)
					if err != nil {
						t.Fatal(err)
					}
					meta.IsSubagent = true
					if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
						t.Fatal(err)
					}
				}
				runDir := t.TempDir()
				writeRendezvous(t, runDir, rendezvous.Entry{
					PID: os.Getpid(), SourceID: "local", ThreadID: parentID, SessionID: parentID, StateDir: stateDir,
				})
				roster := hubcore.NewRoster(runDir, liveSubagentProber{
					sessionID: parentID, runningSubagentIDs: []string{childID},
					runningSubagentState: map[string]string{childID: appwire.ThreadStatusActive},
				})
				roster.Refresh()
				if !roster.IsSubagentActive(childID) {
					t.Fatal("scripted live roster did not admit the delegate")
				}
				cfg := hubcore.WebConfig{StateDir: stateDir, Roster: roster}

				thread := appwire.Thread{Evener: appwire.EvenerThread{
					Ref: "local:" + childID, Kind: kind,
					Capabilities: appwire.ThreadCapabilities{ForkFromTurn: true},
				}}
				if applyHubForkCapability(cfg, thread).Evener.Capabilities.ForkFromTurn {
					t.Error("live delegate was advertised as forkable")
				}
				_, err = hubThreadFork(t.Context(), cfg, nil, appwire.ThreadForkParams{
					Ref: "local:" + childID, SourceTurnID: "turn_1", EditedInput: "forked input",
				})
				if err == nil {
					t.Fatal("live delegate fork succeeded")
				}
				wire, ok := errors.AsType[appwire.WireError](err)
				if !ok || wire.Code != appwire.CodeUnavailable {
					t.Fatalf("live delegate fork error=%v, want structured unavailable", err)
				}
			})
		}
	}
}

// Every recovery signal the hub's read projection fences fork on must also stop
// the fork RPC. The projection reads the signals off a resolved thread; the RPC
// re-derives them from the recovery locks and the roster, so this pins the two
// derivations to the same answer for each signal that reaches a local thread.
// The unfenced row keeps the fenced rows falsifiable: the same fixture without
// a fence both advertises fork and branches a child.
func TestHubForkAdmissionRefusesEveryProjectedRecoveryFence(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fence func(t *testing.T, cfg *hubcore.WebConfig, runDir, sessionID string)
		// projected overrides the thread the capability projection is asked
		// about. The past projection has no way to express a live daemon's own
		// status flags, so the row that fences on them supplies the shape
		// applyHubForkCapability sees on a thread/read of that daemon instead.
		projected   func(sessionID string) appwire.Thread
		wantRefusal func(error) bool
	}{
		{name: "unfenced"},
		{
			name: "explicit resume required",
			fence: func(t *testing.T, cfg *hubcore.WebConfig, _, sessionID string) {
				finish := cfg.ResumeLocks.BeginForceStop([]string{sessionID})
				if err := cfg.ResumeLocks.PersistForceStop([]string{sessionID}, sessionID); err != nil {
					t.Fatal(err)
				}
				finish(true)
			},
			wantRefusal: isSessionRecoveryAdmissionError,
		},
		{
			name: "force stop in flight",
			fence: func(t *testing.T, cfg *hubcore.WebConfig, _, sessionID string) {
				finish := cfg.ResumeLocks.BeginForceStop([]string{sessionID})
				t.Cleanup(func() { finish(false) })
			},
			wantRefusal: isSessionRecoveryAdmissionError,
		},
		{
			name: "incompatible daemon restart required",
			fence: func(t *testing.T, cfg *hubcore.WebConfig, runDir, sessionID string) {
				writeRendezvous(t, runDir, rendezvous.Entry{
					PID: 1001, Protocol: "evener-appwire-v3", ThreadID: sessionID, SessionID: sessionID,
					Endpoint: protocolMismatchPeer(t),
				})
				cfg.Roster.Refresh()
			},
			wantRefusal: isDaemonRestartRequiredError,
		},
		{
			name: "daemon status carries the resumeRequired flag",
			fence: func(t *testing.T, cfg *hubcore.WebConfig, runDir, sessionID string) {
				writeRendezvous(t, runDir, rendezvous.Entry{
					PID: os.Getpid(), SourceID: "local", ThreadID: sessionID, SessionID: sessionID,
					Protocol: appwire.ProtocolVersion, StartedAt: time.Now().UTC(),
				})
				cfg.Roster = hubcore.NewRoster(runDir, recoveryFlagProber{sessionID: sessionID, flags: []string{"resumeRequired"}})
				cfg.Roster.Refresh()
				owner, ok := cfg.Roster.Find(sessionID)
				if !ok || !slices.Contains(owner.ActiveFlags, "resumeRequired") {
					t.Fatalf("roster owner=%+v ok=%v, want the daemon's recovery flag carried", owner, ok)
				}
			},
			projected: func(sessionID string) appwire.Thread {
				return appwire.Thread{
					Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle, ActiveFlags: []string{"resumeRequired"}},
					Evener: appwire.EvenerThread{Ref: "local:" + sessionID, Capabilities: appwire.ThreadCapabilities{ForkFromTurn: true}},
				}
			},
			wantRefusal: isSessionRecoveryAdmissionError,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			stateDir := filepath.Join(root, "projects", "project-fence-0000000000")
			sessionID := buildRPCParentSession(t, stateDir)
			past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
			if _, err := past.Rebuild(); err != nil {
				t.Fatal(err)
			}
			entry, ok := past.Find(sessionID)
			if !ok {
				t.Fatal("session is not indexed")
			}
			runDir := t.TempDir()
			cfg := hubcore.WebConfig{
				StateDir: root, Past: past, RunDir: runDir,
				Roster: hubcore.NewRoster(runDir, &hubcore.StatusProber{}), ResumeLocks: hubcore.NewResumeLocks(),
			}
			if tc.fence != nil {
				tc.fence(t, &cfg, runDir, sessionID)
			}

			thread := appwire.Thread{}
			if tc.projected != nil {
				thread = applyHubForkCapability(cfg, tc.projected(sessionID))
			} else {
				var err error
				if thread, err = pastEntryThreadForList(t.Context(), cfg, entry); err != nil {
					t.Fatalf("project thread: %v", err)
				}
			}
			if got := thread.Evener.Capabilities.ForkFromTurn; got != (tc.fence == nil) {
				t.Errorf("projected forkFromTurn=%v, want %v", got, tc.fence == nil)
			}
			_, err := hubThreadFork(t.Context(), cfg, nil, appwire.ThreadForkParams{
				Ref: "local:" + sessionID, SourceTurnID: "turn_1", EditedInput: "forked input",
			})
			wantMetas := 1
			if tc.fence == nil {
				if err != nil {
					t.Fatalf("unfenced fork: %v", err)
				}
				wantMetas++
			} else if !tc.wantRefusal(err) {
				t.Fatalf("fenced session fork error=%v, want the fence that hid the capability", err)
			}
			metas, err := schema.ListSessionMetas(stateDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(metas) != wantMetas {
				t.Fatalf("session metadata count=%d, want %d", len(metas), wantMetas)
			}
		})
	}
}

// The advertised fork capability is deliberately ahead of the fork RPC's
// ownership resolution: the projection runs per thread on every list and per
// relayed status notification, so it does not repeat ownershipEntry's scan of
// every project directory. A session whose ownership cannot be resolved is
// therefore offered and then refused, and the refusal is structured.
func TestHubForkCapabilityAdvertisesAheadOfOwnershipResolution(t *testing.T) {
	root := t.TempDir()
	sessionID := buildRPCParentSession(t, filepath.Join(root, "projects", "project-one-0000000000"))
	meta, err := schema.LoadSessionMeta(filepath.Join(root, "projects", "project-one-0000000000"), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(filepath.Join(root, "projects", "project-two-0000000000"), meta); err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{StateDir: root}
	thread := appwire.Thread{Evener: appwire.EvenerThread{Ref: "local:" + sessionID}}
	if !applyHubForkCapability(cfg, thread).Evener.Capabilities.ForkFromTurn {
		t.Fatal("projection resolved ownership; update the note on applyHubForkCapability")
	}
	_, err = hubThreadFork(t.Context(), cfg, nil, appwire.ThreadForkParams{
		Ref: "local:" + sessionID, SourceTurnID: "turn_1", EditedInput: "forked input",
	})
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("ambiguous ownership fork error=%v, want structured unavailable", err)
	}
}

// Every producer of thread/status/changed builds appwire.ThreadStatusChangedParams
// (internal/appprojector's threadStatus, then server's failure-count and
// capability stamps), so what that type declares is the whole of what a relayed
// status notification can carry. The recovery signal it carries lives inside
// status, which is where stampForkCapability reads the fork fence from; the
// wire's other recovery flag, resumeRequired, is a field of EvenerThread on a
// thread snapshot and is written only by the hub's own read projection. A
// top-level recovery flag on the notification would have to be declared here
// first, and this fails when any recovery-shaped one is, so the relay fence
// cannot silently start missing a signal the protocol began carrying.
func TestThreadStatusChangedParamsDeclareNoTopLevelRecoveryFlag(t *testing.T) {
	params := reflect.TypeFor[appwire.ThreadStatusChangedParams]()
	for field := range params.Fields() {
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if slices.ContainsFunc([]string{"resume", "restart", "recovery"}, func(word string) bool {
			return strings.Contains(strings.ToLower(name), word)
		}) {
			t.Errorf("%s declares top-level field %q; stampForkCapability reads the fork fence only from status", params.Name(), name)
		}
	}
}

// A relayed status notification reports the fork authority the hub holds when
// it publishes, not the one it held when the client subscribed. The fence and
// the clear are both observed through one subscription: nothing between them
// re-reads or resubscribes, so a relay that answered from its subscription-time
// snapshot would keep publishing the fenced answer after recovery cleared.
func TestHubRelayedForkCapabilityFollowsLiveRecovery(t *testing.T) {
	const sessionID = "hub-fork-relay-recovery"
	const ref = "local:" + sessionID
	daemon := daemonserver.NewServer(daemonserver.ServerConfig{HubToken: "fork-relay-token"})
	daemon.SetAppIdentity("local", sessionID)
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.AppServer().ServeWebSocket))
	t.Cleanup(daemonHTTP.Close)
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		Protocol: appwire.ProtocolVersion, Endpoint: "ws" + daemonHTTP.URL[len("http"):], SourceID: "local",
		ThreadID: sessionID, SessionID: sessionID, WorkspaceRef: ref, InstanceID: "instance-1", HubToken: "fork-relay-token",
	})
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()
	locks := hubcore.NewResumeLocks()
	finishForceStop := locks.BeginForceStop([]string{sessionID})
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir: runDir, Roster: roster, StateDir: runDir, Past: hubcore.NewPastIndex(""), ResumeLocks: locks,
	})
	t.Cleanup(hub.Close)
	client := dialHubRPC(t, hub)
	t.Cleanup(func() { _ = client.Close() })
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	read, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: ref, Subscribe: true, ItemLimit: 40})
	if err != nil {
		t.Fatal(err)
	}
	if read.Thread.Evener.Capabilities.ForkFromTurn {
		t.Fatal("subscribed read advertised fork while an in-flight force stop fenced the session")
	}
	relayedForkStamp := func(text string) bool {
		t.Helper()
		daemon.RecordAppEvent(events.SessionEvent{
			Kind: events.EventUserInput, SessionID: sessionID, Data: events.UserInputData{Text: text},
		})
		deadline := time.After(2 * time.Second)
		for {
			select {
			case notification := <-client.Notifications():
				if notification.Method != appwire.NotifyThreadStatusChanged {
					continue
				}
				var status appwire.ThreadStatusChangedParams
				if err := json.Unmarshal(notification.Params, &status); err != nil {
					t.Fatal(err)
				}
				if status.Status.Type != appwire.ThreadStatusActive {
					continue
				}
				if status.Capabilities == nil {
					t.Fatalf("relayed status carried no capability set: %s", notification.Params)
				}
				return status.Capabilities.ForkFromTurn
			case <-deadline:
				t.Fatal("hub did not relay the status notification")
			}
		}
	}
	if relayedForkStamp("fenced relay fixture") {
		t.Fatal("relayed status advertised fork while an in-flight force stop fenced the session")
	}
	// The force stop failed: the session keeps running and is forkable again.
	finishForceStop(false)
	if state := locks.RecoveryState(sessionID); state.ResumeRequired || state.Stopping != 0 {
		t.Fatalf("recovery state after an abandoned force stop = %+v, want cleared", state)
	}
	if !relayedForkStamp("cleared relay fixture") {
		t.Fatal("relayed status still refused fork on the same subscription after recovery cleared")
	}
}

// crashingSubagentProber reports a parent daemon running one in-process child
// until it is stopped, after which its probe fails the way a dead daemon's does.
type crashingSubagentProber struct {
	sessionID string
	childID   string
	stopped   atomic.Bool
}

func (p *crashingSubagentProber) Probe(rendezvous.Entry) hubcore.ProbeResult {
	if p.stopped.Load() {
		return hubcore.ProbeResult{}
	}
	return hubcore.ProbeResult{
		SessionID:             p.sessionID,
		Status:                appwire.ThreadStatusIdle,
		RunningSubagentIDs:    []string{p.childID},
		RunningSubagentStates: map[string]string{p.childID: appwire.ThreadStatusActive},
		OK:                    true,
	}
}

// A crashed daemon stays in the roster for the crash-retention window carrying
// the in-process children it last reported. Those delegates are not running
// anywhere, so a stopped persisted one is hub-owned from the moment its parent
// dies: the capability advertises the fork and the RPC branches it, rather than
// both waiting out retention.
func TestHubForkAdmitsPersistedDelegateOfCrashedParent(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-crashfork-0000000000")
	parentID := buildRPCParentSession(t, stateDir)
	childID, err := agent.ForkSession(stateDir, parentID, 1, "stopped delegate", "")
	if err != nil {
		t.Fatal(err)
	}
	meta, err := schema.LoadSessionMeta(stateDir, childID)
	if err != nil {
		t.Fatal(err)
	}
	meta.IsSubagent = true
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID: os.Getpid(), SourceID: "local", ThreadID: parentID, SessionID: parentID, StateDir: stateDir,
		StartedAt: time.Now().UTC(), // fresh: within the crash-retention window
	})
	prober := &crashingSubagentProber{sessionID: parentID, childID: childID}
	roster := hubcore.NewRoster(runDir, prober).SetProcessAlive(func(int) bool { return !prober.stopped.Load() })
	roster.Refresh()
	if !roster.IsSubagentActive(childID) {
		t.Fatal("scripted live roster did not admit the delegate")
	}

	// kill -9 the parent: its probe fails and the process is confirmed gone.
	prober.stopped.Store(true)
	roster.Refresh()
	parent, ok := roster.Find(parentID)
	if !ok || !parent.Crashed || !slices.Contains(parent.RunningSubagentIDs, childID) {
		t.Fatalf("parent entry=%+v ok=%v, want a retained crashed record still listing the delegate", parent, ok)
	}

	cfg := hubcore.WebConfig{StateDir: root, Roster: roster}
	thread := appwire.Thread{Evener: appwire.EvenerThread{Ref: "local:" + childID, Kind: "subagent"}}
	if !applyHubForkCapability(cfg, thread).Evener.Capabilities.ForkFromTurn {
		t.Error("stopped delegate of a crashed parent was not advertised as forkable")
	}
	resp, err := hubThreadFork(t.Context(), cfg, nil, appwire.ThreadForkParams{
		Ref: "local:" + childID, SourceTurnID: "turn_1", EditedInput: "forked input",
	})
	if err != nil {
		t.Fatalf("stopped delegate of a crashed parent could not be forked: %v", err)
	}
	if _, err := schema.LoadSessionMeta(stateDir, resp.Thread.ID); err != nil {
		t.Fatalf("child was not branched beside its parent: %v", err)
	}
}

// delegateArrivalProber reports a live parent daemon that begins running one
// in-process child only once it is armed, so a roster refreshed before that
// arrival is stale about the child while the daemon itself stays healthy.
type delegateArrivalProber struct {
	sessionID string
	childID   string
	running   atomic.Bool
}

func (p *delegateArrivalProber) Probe(rendezvous.Entry) hubcore.ProbeResult {
	result := hubcore.ProbeResult{SessionID: p.sessionID, Status: appwire.ThreadStatusIdle, OK: true}
	if p.running.Load() {
		result.RunningSubagentIDs = []string{p.childID}
		result.RunningSubagentStates = map[string]string{p.childID: appwire.ThreadStatusActive}
	}
	return result
}

// recoveryFlagProber reports a healthy daemon whose own thread status carries a
// recovery flag, the signal applyHubForkCapability fences fork on.
type recoveryFlagProber struct {
	sessionID string
	flags     []string
}

func (p recoveryFlagProber) Probe(rendezvous.Entry) hubcore.ProbeResult {
	return hubcore.ProbeResult{
		SessionID:   p.sessionID,
		Status:      appwire.ThreadStatusIdle,
		ActiveFlags: p.flags,
		OK:          true,
	}
}

// A delegate its parent daemon picked up after the hub's last roster scan is
// daemon-owned by the time the fork arrives. Admission has to decide on a
// roster it refreshed itself, or the fence answers from a snapshot that is
// already out of date and the hub branches a transcript a daemon is writing.
func TestHubForkRefusesDelegateThatWentLiveBeforeAdmission(t *testing.T) {
	stateDir := t.TempDir()
	parentID := buildRPCParentSession(t, stateDir)
	childID, err := agent.ForkSession(stateDir, parentID, 1, "delegate", "")
	if err != nil {
		t.Fatal(err)
	}
	meta, err := schema.LoadSessionMeta(stateDir, childID)
	if err != nil {
		t.Fatal(err)
	}
	meta.IsSubagent = true
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID: os.Getpid(), SourceID: "local", ThreadID: parentID, SessionID: parentID, StateDir: stateDir,
		Protocol: appwire.ProtocolVersion, StartedAt: time.Now().UTC(),
	})
	prober := &delegateArrivalProber{sessionID: parentID, childID: childID}
	roster := hubcore.NewRoster(runDir, prober)
	roster.Refresh()
	if roster.IsSubagentActive(childID) {
		t.Fatal("the roster already lists the delegate; this fixture is not stale")
	}

	// The parent daemon starts running the delegate after that scan.
	prober.running.Store(true)
	before, err := schema.ListSessionMetas(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{StateDir: stateDir, Roster: roster}
	_, err = hubThreadFork(t.Context(), cfg, nil, appwire.ThreadForkParams{
		Ref: "local:" + childID, SourceTurnID: "turn_1", EditedInput: "forked input",
	})
	if err == nil {
		t.Fatal("fork of a delegate that went live before admission succeeded")
	}
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("fork error=%v, want structured unavailable", err)
	}
	after, err := schema.ListSessionMetas(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("refused fork still branched a child: %d metadata records became %d", len(before), len(after))
	}
}

// thread/clear mints a replacement session behind the daemon's STABLE workspace
// ref: cmd/evener/serve.go's clear hook moves the rendezvous ThreadID/SessionID
// to the new session (rvreg.UpdateSessionID) and leaves WorkspaceRef alone, so a
// client keeps holding local:<first session>. A read of that ref returns the
// current session's transcript and the hub advertises fork on it, so a fork by
// the same ref has to branch the session the client was reading — not the
// retired one the ref happens to be named after.
//
// The rows are the two shapes a live entry takes: a probe that names the
// session, and one that does not. liveEntryFromProbe copies the probe verbatim,
// so the second leaves LiveEntry.SessionID empty while the rendezvous entry it
// carries still names the current session — the resolution has to fall back to
// that entry exactly as the read path and the roster's sibling helpers do.
func TestHubForkByStableRefBranchesTheCurrentSession(t *testing.T) {
	for _, tc := range []struct {
		name        string
		probeNoName bool
	}{
		{name: "probe names the session"},
		{name: "probe reports no session id", probeNoName: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := t.TempDir()
			retiredID := buildRPCParentSession(t, stateDir)
			currentID := buildRPCSessionWithWorkingDir(t, stateDir, "02xNz6Uyw2D4Ivu1N9HDfC", t.TempDir())
			runDir := t.TempDir()
			writeRendezvous(t, runDir, rendezvous.Entry{
				PID: os.Getpid(), SourceID: "local", ThreadID: currentID, SessionID: currentID, InstanceID: currentID,
				WorkspaceRef: "local:" + retiredID, StateDir: stateDir,
				Protocol: appwire.ProtocolVersion, StartedAt: time.Now().UTC(),
			})
			probed := currentID
			if tc.probeNoName {
				probed = ""
			}
			roster := hubcore.NewRoster(runDir, fakeProber{sessionID: probed, status: appwire.ThreadStatusIdle})
			roster.Refresh()
			listed := roster.List()
			if len(listed) != 1 || listed[0].SessionID != probed || listed[0].Entry.SessionID != currentID {
				t.Fatalf("roster listed %+v, want one entry with the probe's session id over the rendezvous entry's", listed)
			}
			if listed[0].WorkspaceRef != "local:"+retiredID {
				t.Fatalf("roster owner workspace ref = %q, want the retired session's stable ref", listed[0].WorkspaceRef)
			}

			cfg := hubcore.WebConfig{StateDir: stateDir, RunDir: runDir, Roster: roster}
			thread := appwire.Thread{ID: currentID, SessionID: currentID, Evener: appwire.EvenerThread{Ref: "local:" + retiredID}}
			if !applyHubForkCapability(cfg, thread).Evener.Capabilities.ForkFromTurn {
				t.Fatal("the stable ref was not advertised as forkable; this fixture cannot reach the handler")
			}
			resp, err := hubThreadFork(t.Context(), cfg, nil, appwire.ThreadForkParams{
				Ref: "local:" + retiredID, SourceTurnID: "turn_1", EditedInput: "forked input",
			})
			if err != nil {
				t.Fatalf("fork by the daemon's stable workspace ref: %v", err)
			}
			meta, err := schema.LoadSessionMeta(stateDir, resp.Thread.ID)
			if err != nil {
				t.Fatal(err)
			}
			if meta.ParentSessionID != currentID {
				t.Fatalf("fork branched %q, want the daemon's current session %q", meta.ParentSessionID, currentID)
			}
		})
	}
}

// A malformed fork request is refused on its own terms, before the handler
// fences the target or goes looking for its transcript. The fixture makes that
// observable three ways at once: the session is under a recovery fence that
// would otherwise answer first, <stateDir>/projects is a regular file so
// ownershipEntry's scan of every project directory cannot run without failing,
// and the roster refresh is counted. An InvalidParams answer with no refresh
// proves the request never got past validation.
func TestHubForkValidatesParamsBeforeFencingOrDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params appwire.ThreadForkParams
	}{
		{name: "aside with a source turn", params: appwire.ThreadForkParams{Aside: true, SourceTurnID: "turn_1"}},
		{name: "aside with edited input", params: appwire.ThreadForkParams{Aside: true, EditedInput: "forked input"}},
		{name: "aside with a label", params: appwire.ThreadForkParams{Aside: true, Label: "side"}},
		{name: "aside deferring input", params: appwire.ThreadForkParams{Aside: true, DeferInput: true}},
		{name: "no source turn", params: appwire.ThreadForkParams{EditedInput: "forked input"}},
		{name: "unparseable source turn", params: appwire.ThreadForkParams{SourceTurnID: "turn_zero", EditedInput: "forked input"}},
		{name: "no edited input", params: appwire.ThreadForkParams{SourceTurnID: "turn_1"}},
		{name: "edited input with deferred input", params: appwire.ThreadForkParams{SourceTurnID: "turn_1", EditedInput: "forked input", DeferInput: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const sessionID = "02fencedForkTarget0000"
			stateDir := t.TempDir()
			// Not a directory: ownershipEntry's os.ReadDir fails here rather
			// than reporting absence, so reaching the scan cannot look like
			// reaching nothing.
			if err := os.WriteFile(filepath.Join(stateDir, "projects"), []byte("not a directory"), 0o600); err != nil {
				t.Fatal(err)
			}
			locks := hubcore.NewResumeLocks()
			finish := locks.BeginForceStop([]string{sessionID})
			if err := locks.PersistForceStop([]string{sessionID}, sessionID); err != nil {
				t.Fatal(err)
			}
			finish(true)
			runDir := t.TempDir()
			cfg := hubcore.WebConfig{
				StateDir: stateDir, RunDir: runDir, ResumeLocks: locks,
				Roster: hubcore.NewRoster(runDir, &hubcore.StatusProber{}),
			}
			refreshes := 0
			previousRefresh := hubRosterRefresh
			hubRosterRefresh = func(ctx context.Context, r *hubcore.Roster) error {
				refreshes++
				return previousRefresh(ctx, r)
			}
			t.Cleanup(func() { hubRosterRefresh = previousRefresh })

			params := tc.params
			params.Ref = "local:" + sessionID
			_, err := hubThreadFork(t.Context(), cfg, nil, params)
			wire, ok := errors.AsType[appwire.WireError](err)
			if !ok || wire.Code != appwire.CodeInvalidParams {
				t.Fatalf("fork error=%v, want structured invalid params ahead of every fence", err)
			}
			if refreshes != 0 {
				t.Fatalf("malformed fork refreshed daemon ownership %d times, want none", refreshes)
			}
		})
	}
}

// The recovery flags a daemon reports reach the hub only through the roster:
// the local source's list projection builds its threads from roster entries
// that carry no flags, and a live thread/read is answered by the daemon itself,
// whose response has never carried them either. So the capability projection
// asks the roster the same question fork admission asks it, and both read and
// list stop advertising a fork the RPC would refuse.
func TestHubForkCapabilityHonoursDaemonReportedRecoveryFlags(t *testing.T) {
	const sessionID = "hub-fork-active-flags"
	const ref = "local:" + sessionID
	daemon := daemonserver.NewServer(daemonserver.ServerConfig{HubToken: "flags-test-token"})
	daemon.SetAppIdentity("local", sessionID)
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.AppServer().ServeWebSocket))
	t.Cleanup(daemonHTTP.Close)
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		Protocol: appwire.ProtocolVersion, Endpoint: "ws" + daemonHTTP.URL[len("http"):], SourceID: "local",
		ThreadID: sessionID, SessionID: sessionID, WorkspaceRef: ref, InstanceID: "instance-1", HubToken: "flags-test-token",
	})
	for _, tc := range []struct {
		name     string
		flags    []string
		wantFork bool
	}{
		{name: "no flags", wantFork: true},
		{name: "resume required", flags: []string{"resumeRequired"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			roster := hubcore.NewRoster(runDir, recoveryFlagProber{sessionID: sessionID, flags: tc.flags})
			roster.Refresh()
			owner, ok := roster.Find(sessionID)
			if !ok || !slices.Equal(owner.ActiveFlags, tc.flags) {
				t.Fatalf("roster owner=%+v ok=%v, want the daemon's reported flags %v", owner, ok, tc.flags)
			}
			hub := newHubRPCTestServer(t, hubcore.WebConfig{
				RunDir: runDir, Roster: roster, StateDir: runDir, Past: hubcore.NewPastIndex(""),
			})
			t.Cleanup(hub.Close)
			client := dialHubRPC(t, hub)
			t.Cleanup(func() { _ = client.Close() })
			if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
				t.Fatal(err)
			}
			list, err := client.ThreadList(t.Context(), appwire.ThreadListParams{})
			if err != nil {
				t.Fatal(err)
			}
			listed := false
			for _, thread := range list.Data {
				if thread.Evener.Ref != ref {
					continue
				}
				listed = true
				if got := thread.Evener.Capabilities.ForkFromTurn; got != tc.wantFork {
					t.Errorf("listed forkFromTurn=%v, want %v", got, tc.wantFork)
				}
			}
			if !listed {
				t.Fatalf("the live thread is not in the list: %+v", list.Data)
			}
			read, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: ref})
			if err != nil {
				t.Fatal(err)
			}
			if got := read.Thread.Evener.Capabilities.ForkFromTurn; got != tc.wantFork {
				t.Errorf("read forkFromTurn=%v, want %v", got, tc.wantFork)
			}
		})
	}
}

// A stable workspace ref and the session it currently names are two identities
// for one fork, and both carry fences the hub must honour: the request reserves
// the alias the client asked about, and the branch reads the resolved session's
// transcript. Fencing only the alias lets a fork of a resume-required or
// deleted current session through, which is the mirror of the dual
// live-delegate check the same admission already performs.
func TestHubForkFencesBothTheRequestedAliasAndTheResolvedSession(t *testing.T) {
	for _, tc := range []struct {
		name string
		// namesRef is set for a fence whose refusal carries an identity. It
		// must be the ref the client asked about: the resolved session id is
		// the hub's own answer to that ref, and naming it would report an
		// identity the request never mentioned.
		namesRef bool
		fence    func(t *testing.T, cfg *hubcore.WebConfig, fencedID string)
	}{
		{
			name: "resolved session needs an explicit resume",
			fence: func(t *testing.T, cfg *hubcore.WebConfig, fencedID string) {
				finish := cfg.ResumeLocks.BeginForceStop([]string{fencedID})
				if err := cfg.ResumeLocks.PersistForceStop([]string{fencedID}, fencedID); err != nil {
					t.Fatal(err)
				}
				finish(true)
			},
		},
		{
			name:     "resolved session is deleted",
			namesRef: true,
			fence: func(t *testing.T, cfg *hubcore.WebConfig, fencedID string) {
				store, err := hubcore.NewDeletionStore(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.Begin("project-fence-0123456789", []hubcore.DeletionTarget{{
					Ref: localAppRef(fencedID), ThreadID: fencedID,
				}}); err != nil {
					t.Fatal(err)
				}
				cfg.DeletionStore = store
			},
		},
	} {
		for _, fenceCurrent := range []bool{false, true} {
			name := tc.name + map[bool]string{false: " (control: the retired alias is fenced instead)", true: ""}[fenceCurrent]
			t.Run(name, func(t *testing.T) {
				stateDir := t.TempDir()
				retiredID := buildRPCParentSession(t, stateDir)
				replacementID, err := identifier.NewSessionID()
				if err != nil {
					t.Fatal(err)
				}
				currentID := buildRPCSessionWithWorkingDir(t, stateDir, replacementID, t.TempDir())
				runDir := t.TempDir()
				writeRendezvous(t, runDir, rendezvous.Entry{
					PID: os.Getpid(), SourceID: "local", ThreadID: currentID, SessionID: currentID, InstanceID: currentID,
					WorkspaceRef: "local:" + retiredID, StateDir: stateDir,
					Protocol: appwire.ProtocolVersion, StartedAt: time.Now().UTC(),
				})
				roster := hubcore.NewRoster(runDir, fakeProber{sessionID: currentID, status: appwire.ThreadStatusIdle})
				roster.Refresh()
				cfg := hubcore.WebConfig{StateDir: stateDir, RunDir: runDir, Roster: roster, ResumeLocks: hubcore.NewResumeLocks()}
				fencedID := retiredID
				if fenceCurrent {
					fencedID = currentID
				}
				tc.fence(t, &cfg, fencedID)

				before, listErr := schema.ListSessionMetas(stateDir)
				if listErr != nil {
					t.Fatal(listErr)
				}
				_, err = hubThreadFork(t.Context(), cfg, nil, appwire.ThreadForkParams{
					Ref: "local:" + retiredID, SourceTurnID: "turn_1", EditedInput: "forked input",
				})
				if err == nil {
					t.Fatalf("fork by the stable ref succeeded while %s was fenced", fencedID)
				}
				wire, ok := errors.AsType[appwire.WireError](err)
				if !ok || wire.Code != appwire.CodeUnavailable {
					t.Fatalf("fork error=%v, want structured unavailable", err)
				}
				if tc.namesRef {
					if !strings.Contains(wire.Message, "local:"+retiredID) {
						t.Errorf("refusal %q does not name the ref the client asked about (local:%s)", wire.Message, retiredID)
					}
					if fenceCurrent && strings.Contains(wire.Message, currentID) {
						t.Errorf("refusal %q names the session the hub resolved to, which the client never mentioned", wire.Message)
					}
				}
				after, err := schema.ListSessionMetas(stateDir)
				if err != nil {
					t.Fatal(err)
				}
				if len(after) != len(before) {
					t.Fatalf("refused fork still branched a child: %d metadata records became %d", len(before), len(after))
				}
			})
		}
	}
}

// A deleted target and a recovery-fenced one are refused differently — deletion
// is terminal and carries MutationOutcomeTargetDeleted, a recovery fence is
// retryable once the session is resumed — so which one a client is told about
// must not depend on how two session ids happen to sort. Both orders are
// exercised: the alias is deleted and the session the fork resolves to is
// recovery-fenced, whichever of the two sorts first.
func TestHubForkReportsDeletionBeforeRecoveryWhicheverIdentitySortsFirst(t *testing.T) {
	// Sorted, not assumed sorted: session ids are UUIDv7-based, so two taken in
	// the same millisecond order by random low bits rather than by generation.
	// The rows are about which of the two identities the fence loop reaches
	// first, which the sorted pair still puts both ways round.
	ids := make([]string, 2)
	for i := range ids {
		id, err := identifier.NewSessionID()
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
	}
	if ids[0] == ids[1] {
		t.Fatalf("session ids are not distinct: %q", ids[0])
	}
	slices.Sort(ids)
	lower, higher := ids[0], ids[1]
	for _, tc := range []struct {
		name            string
		aliasID, liveID string
	}{
		{name: "deleted alias sorts first", aliasID: lower, liveID: higher},
		{name: "deleted alias sorts second", aliasID: higher, liveID: lower},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := t.TempDir()
			buildRPCSessionWithWorkingDir(t, stateDir, tc.aliasID, t.TempDir())
			buildRPCSessionWithWorkingDir(t, stateDir, tc.liveID, t.TempDir())
			runDir := t.TempDir()
			writeRendezvous(t, runDir, rendezvous.Entry{
				PID: os.Getpid(), SourceID: "local", ThreadID: tc.liveID, SessionID: tc.liveID, InstanceID: tc.liveID,
				WorkspaceRef: "local:" + tc.aliasID, StateDir: stateDir,
				Protocol: appwire.ProtocolVersion, StartedAt: time.Now().UTC(),
			})
			roster := hubcore.NewRoster(runDir, fakeProber{sessionID: tc.liveID, status: appwire.ThreadStatusIdle})
			roster.Refresh()
			store, err := hubcore.NewDeletionStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Begin("project-order-0123456789", []hubcore.DeletionTarget{{
				Ref: localAppRef(tc.aliasID), ThreadID: tc.aliasID,
			}}); err != nil {
				t.Fatal(err)
			}
			locks := hubcore.NewResumeLocks()
			finish := locks.BeginForceStop([]string{tc.liveID})
			if err := locks.PersistForceStop([]string{tc.liveID}, tc.liveID); err != nil {
				t.Fatal(err)
			}
			finish(true)
			cfg := hubcore.WebConfig{
				StateDir: stateDir, RunDir: runDir, Roster: roster,
				ResumeLocks: locks, DeletionStore: store,
			}

			_, err = hubThreadFork(t.Context(), cfg, nil, appwire.ThreadForkParams{
				Ref: "local:" + tc.aliasID, SourceTurnID: "turn_1", EditedInput: "forked input",
			})
			if !isTargetDeletedError(err) {
				t.Fatalf("fork error=%v, want the deleted target reported ahead of the recovery fence", err)
			}
		})
	}
}

// A thread/clear that lands while a fork waits on the per-session locks moves
// the session the requested ref names. The target is resolved before those
// locks are taken — it has to be, so both identities can be locked in one
// sorted pass — so the fork re-resolves once it holds them and refuses rather
// than branch a transcript the ref stopped naming, the same recheck
// resumeThread performs after acquiring the same mutexes.
//
// The roster answers the stable ref through its workspace-ref scan (Find misses:
// the entry is keyed by the daemon's current session id), so scripting
// hubRosterList — the package's existing roster seam, the one
// cov_exact_lifecycle_tree_fuzz_test.go already overrides — is what makes the
// change land between the two resolutions. The call count is asserted so a
// future change to how admission consults the roster fails this test loudly
// instead of quietly flipping the answer at the wrong moment.
func TestHubForkRefusesWhenItsTargetMovesUnderTheLocks(t *testing.T) {
	for _, tc := range []struct {
		name    string
		moved   bool
		wantErr bool
	}{
		{name: "target holds still", moved: false},
		{name: "target moves under the locks", moved: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := t.TempDir()
			retiredID := buildRPCParentSession(t, stateDir)
			firstID, err := identifier.NewSessionID()
			if err != nil {
				t.Fatal(err)
			}
			secondID, err := identifier.NewSessionID()
			if err != nil {
				t.Fatal(err)
			}
			buildRPCSessionWithWorkingDir(t, stateDir, firstID, t.TempDir())
			buildRPCSessionWithWorkingDir(t, stateDir, secondID, t.TempDir())
			runDir := t.TempDir()
			writeRendezvous(t, runDir, rendezvous.Entry{
				PID: os.Getpid(), SourceID: "local", ThreadID: firstID, SessionID: firstID, InstanceID: firstID,
				WorkspaceRef: "local:" + retiredID, StateDir: stateDir,
				Protocol: appwire.ProtocolVersion, StartedAt: time.Now().UTC(),
			})
			roster := hubcore.NewRoster(runDir, fakeProber{sessionID: firstID, status: appwire.ThreadStatusIdle})
			roster.Refresh()
			if _, found := roster.Find(retiredID); found {
				t.Fatal("the roster answers the stable ref directly; this fixture does not exercise the workspace-ref scan")
			}

			entryFor := func(sessionID string) []hubcore.LiveEntry {
				return []hubcore.LiveEntry{{
					Entry: rendezvous.Entry{
						PID: os.Getpid(), SourceID: "local", ThreadID: sessionID, SessionID: sessionID,
						WorkspaceRef: "local:" + retiredID, StateDir: stateDir,
						Protocol: appwire.ProtocolVersion,
					},
					SessionID: sessionID,
					Status:    appwire.ThreadStatusIdle,
				}}
			}
			// The clear lands after the fork resolved its target and before it
			// holds the locks: calls one and two are the admission refresh's
			// owner lookup and that resolution, every later call is the fork
			// re-resolving under the locks.
			const resolutionCall = 2
			calls := 0
			previousList := hubRosterList
			hubRosterList = func(r *hubcore.Roster) []hubcore.LiveEntry {
				calls++
				if tc.moved && calls > resolutionCall {
					return entryFor(secondID)
				}
				return entryFor(firstID)
			}
			t.Cleanup(func() { hubRosterList = previousList })

			cfg := hubcore.WebConfig{StateDir: stateDir, RunDir: runDir, Roster: roster}
			before, listErr := schema.ListSessionMetas(stateDir)
			if listErr != nil {
				t.Fatal(listErr)
			}
			_, err = hubThreadFork(t.Context(), cfg, nil, appwire.ThreadForkParams{
				Ref: "local:" + retiredID, SourceTurnID: "turn_1", EditedInput: "forked input",
			})
			after, listErr := schema.ListSessionMetas(stateDir)
			if listErr != nil {
				t.Fatal(listErr)
			}
			if calls <= resolutionCall {
				t.Fatalf("admission consulted the roster %d times, so nothing ran after the resolution this test moves", calls)
			}
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("fork whose target held still: %v", err)
				}
				if len(after) != len(before)+1 {
					t.Fatalf("admitted fork branched %d children, want 1", len(after)-len(before))
				}
				return
			}
			if err == nil {
				t.Fatal("fork branched a session the requested ref stopped naming")
			}
			wire, ok := errors.AsType[appwire.WireError](err)
			if !ok || wire.Code != appwire.CodeUnavailable || isTargetDeletedError(err) {
				t.Fatalf("fork error=%v, want a retryable structured unavailable", err)
			}
			if len(after) != len(before) {
				t.Fatalf("refused fork still branched a child: %d metadata records became %d", len(before), len(after))
			}
		})
	}
}

// The roster is an asynchronous snapshot: a daemon rewrites its rendezvous
// entry as it swaps sessions (cmd/evener/serve.go's clear hook, through
// rvreg.UpdateSessionID), and the roster only learns of it when its watcher
// next re-lists. A recheck that asks the roster therefore compares one stale
// reading to the same stale reading and passes, so the post-lock recheck reads
// the rendezvous directly — the source resumeOwnershipStep reads, and the
// reason resumeThread's own recheck is not fooled.
//
// The fixture is that delayed watcher: hubRosterList keeps answering with the
// pre-clear session for every call, while the real rendezvous entry in the run
// dir moves to a new session id at the moment the pre-lock resolution happens.
func TestHubForkRechecksItsTargetAgainstTheRendezvousNotTheRoster(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cleared bool
	}{
		{name: "rendezvous still names the resolved session"},
		{name: "rendezvous moved while the roster stayed stale", cleared: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := t.TempDir()
			retiredID := buildRPCParentSession(t, stateDir)
			firstID, err := identifier.NewSessionID()
			if err != nil {
				t.Fatal(err)
			}
			secondID, err := identifier.NewSessionID()
			if err != nil {
				t.Fatal(err)
			}
			buildRPCSessionWithWorkingDir(t, stateDir, firstID, t.TempDir())
			buildRPCSessionWithWorkingDir(t, stateDir, secondID, t.TempDir())
			runDir := t.TempDir()
			daemonEntry := func(sessionID string) rendezvous.Entry {
				return rendezvous.Entry{
					PID: os.Getpid(), SourceID: "local", ThreadID: sessionID, SessionID: sessionID, InstanceID: sessionID,
					WorkspaceRef: "local:" + retiredID, StateDir: stateDir,
					Protocol: appwire.ProtocolVersion, StartedAt: time.Now().UTC(),
				}
			}
			writeRendezvous(t, runDir, daemonEntry(firstID))
			roster := hubcore.NewRoster(runDir, fakeProber{sessionID: firstID, status: appwire.ThreadStatusIdle})
			roster.Refresh()
			if _, found := roster.Find(retiredID); found {
				t.Fatal("the roster answers the stable ref directly; this fixture does not exercise the workspace-ref scan")
			}

			// The watcher never runs: every roster answer stays pre-clear.
			const resolutionCall = 2
			calls := 0
			previousList := hubRosterList
			hubRosterList = func(*hubcore.Roster) []hubcore.LiveEntry {
				calls++
				if tc.cleared && calls == resolutionCall {
					// thread/clear lands here, between the pre-lock resolution
					// and the locks: the daemon's rendezvous entry moves to its
					// replacement session while the roster still says otherwise.
					writeRendezvous(t, runDir, daemonEntry(secondID))
				}
				return []hubcore.LiveEntry{{
					Entry: daemonEntry(firstID), SessionID: firstID, Status: appwire.ThreadStatusIdle,
				}}
			}
			t.Cleanup(func() { hubRosterList = previousList })

			cfg := hubcore.WebConfig{StateDir: stateDir, RunDir: runDir, Roster: roster}
			before, listErr := schema.ListSessionMetas(stateDir)
			if listErr != nil {
				t.Fatal(listErr)
			}
			_, err = hubThreadFork(t.Context(), cfg, nil, appwire.ThreadForkParams{
				Ref: "local:" + retiredID, SourceTurnID: "turn_1", EditedInput: "forked input",
			})
			after, listErr := schema.ListSessionMetas(stateDir)
			if listErr != nil {
				t.Fatal(listErr)
			}
			if calls < resolutionCall {
				t.Fatalf("admission consulted the roster %d times, so the clear this test stages never landed", calls)
			}
			if !tc.cleared {
				if err != nil {
					t.Fatalf("fork whose rendezvous still names the resolved session: %v", err)
				}
				if len(after) != len(before)+1 {
					t.Fatalf("admitted fork branched %d children, want 1", len(after)-len(before))
				}
				return
			}
			if err == nil {
				t.Fatal("fork branched a session the rendezvous had already stopped naming")
			}
			wire, ok := errors.AsType[appwire.WireError](err)
			if !ok || wire.Code != appwire.CodeUnavailable || isTargetDeletedError(err) {
				t.Fatalf("fork error=%v, want a retryable structured unavailable", err)
			}
			if len(after) != len(before) {
				t.Fatalf("refused fork still branched a child: %d metadata records became %d", len(before), len(after))
			}
		})
	}
}

// Two local daemons can both claim one stable workspace alias — that is the
// shape resumeClaimTarget's conflict check exists for. Taking whichever of them
// the rendezvous directory happened to list first would branch a transcript
// chosen by filename order, and nothing downstream catches it: ownershipEntry
// refuses a session id found in two project directories, never a second daemon
// claiming the same alias. Both orders are exercised, and the single-claim
// control shows the fixture forks when the alias is unambiguous.
func TestHubForkRefusesAnAliasTwoDaemonsClaim(t *testing.T) {
	for _, tc := range []struct {
		name          string
		firstSession  string // the entry written as 1001.json, listed first
		secondSession string // 1002.json
		wantFork      bool
	}{
		{name: "resolved session listed first", firstSession: "a", secondSession: "b"},
		{name: "resolved session listed second", firstSession: "b", secondSession: "a"},
		{name: "single claim", firstSession: "a", wantFork: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := t.TempDir()
			retiredID := buildRPCParentSession(t, stateDir)
			ids := map[string]string{}
			for _, key := range []string{"a", "b"} {
				id, err := identifier.NewSessionID()
				if err != nil {
					t.Fatal(err)
				}
				ids[key] = id
				buildRPCSessionWithWorkingDir(t, stateDir, id, t.TempDir())
			}
			runDir := t.TempDir()
			claim := func(pid int, sessionID string) {
				writeRendezvous(t, runDir, rendezvous.Entry{
					PID: pid, SourceID: "local", ThreadID: sessionID, SessionID: sessionID, InstanceID: sessionID,
					WorkspaceRef: "local:" + retiredID, StateDir: stateDir,
					Protocol: appwire.ProtocolVersion, StartedAt: time.Now().UTC(),
				})
			}
			claim(1001, ids[tc.firstSession])
			if tc.secondSession != "" {
				claim(1002, ids[tc.secondSession])
			}

			// The roster answers the alias with one of the claims, so the
			// pre-lock resolution is stable and the recheck is deciding the
			// ambiguity rather than a disagreement between the two resolvers.
			previousList := hubRosterList
			hubRosterList = func(*hubcore.Roster) []hubcore.LiveEntry {
				return []hubcore.LiveEntry{{
					Entry: rendezvous.Entry{
						PID: 1001, SourceID: "local", ThreadID: ids["a"], SessionID: ids["a"],
						WorkspaceRef: "local:" + retiredID, StateDir: stateDir, Protocol: appwire.ProtocolVersion,
					},
					SessionID: ids["a"], Status: appwire.ThreadStatusIdle,
				}}
			}
			t.Cleanup(func() { hubRosterList = previousList })

			// Both claiming daemons are running: a marker whose process is
			// gone is not a claim at all (forkClaimIsLiveOwner), so the
			// ambiguity this test is about needs live ones.
			var probes []string
			cfg := hubcore.WebConfig{
				StateDir: stateDir, RunDir: runDir, Roster: hubcore.NewRosterWithEntries(),
				DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
					return &forceStopProcess{events: &probes}, nil
				}),
			}
			before, listErr := schema.ListSessionMetas(stateDir)
			if listErr != nil {
				t.Fatal(listErr)
			}
			resp, err := hubThreadFork(t.Context(), cfg, nil, appwire.ThreadForkParams{
				Ref: "local:" + retiredID, SourceTurnID: "turn_1", EditedInput: "forked input",
			})
			after, listErr := schema.ListSessionMetas(stateDir)
			if listErr != nil {
				t.Fatal(listErr)
			}
			if tc.wantFork {
				if err != nil {
					t.Fatalf("fork of an alias one daemon claims: %v", err)
				}
				meta, metaErr := schema.LoadSessionMeta(stateDir, resp.Thread.ID)
				if metaErr != nil {
					t.Fatal(metaErr)
				}
				if meta.ParentSessionID != ids["a"] {
					t.Fatalf("fork branched %q, want the one session claiming the alias %q", meta.ParentSessionID, ids["a"])
				}
				return
			}
			if err == nil {
				t.Fatal("fork branched a transcript chosen by rendezvous directory order")
			}
			wire, ok := errors.AsType[appwire.WireError](err)
			if !ok || wire.Code != appwire.CodeUnavailable || isTargetDeletedError(err) {
				t.Fatalf("fork error=%v, want a retryable structured unavailable", err)
			}
			if len(after) != len(before) {
				t.Fatalf("refused fork still branched a child: %d metadata records became %d", len(before), len(after))
			}
		})
	}
}

// After an explicit resume through a stable alias, the recovery locks keep
// mapping that alias to the session the resume settled on — ResolvedSessionID
// is recorded and never cleared. Once that daemon shuts down gracefully its
// rendezvous entry goes away, so a resolver that only asks the roster answers
// the alias itself while the one that reads the recovery state answers the
// session: two readings that disagree with nothing having changed, and every
// cold fork of that alias refused until the hub restarts. Both resolvers follow
// the same redirect, so the fork branches the session the resume named.
func TestHubForkFollowsTheRecoveryRedirectForAStoppedAlias(t *testing.T) {
	stateDir := t.TempDir()
	aliasID, err := identifier.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	currentID := buildRPCParentSession(t, stateDir)

	// The shape an explicit resume through the alias leaves behind: one
	// recovery group over both ids, resolved onto the current session, then
	// completed. No daemon remains — no roster entry and no rendezvous marker.
	locks := hubcore.NewResumeLocks()
	group := []string{aliasID, currentID}
	finish := locks.BeginForceStop(group)
	if err := locks.PersistForceStop(group, currentID); err != nil {
		t.Fatal(err)
	}
	finish(true)
	epoch := locks.RecoveryState(aliasID).Epoch
	if err := locks.ExplicitResumeCompleted(aliasID, epoch); err != nil {
		t.Fatal(err)
	}
	locks.RecordResolvedSession(aliasID, currentID, epoch)
	if got := locks.ResolvedSessionID(aliasID); got != currentID {
		t.Fatalf("resolved session for the alias = %q, want %q", got, currentID)
	}
	if state := locks.RecoveryState(aliasID); state.ResumeRequired || state.Stopping != 0 {
		t.Fatalf("recovery state after the explicit resume = %+v, want cleared", state)
	}

	cfg := hubcore.WebConfig{
		StateDir: stateDir, RunDir: t.TempDir(),
		Roster: hubcore.NewRosterWithEntries(), ResumeLocks: locks,
	}
	resp, err := hubThreadFork(t.Context(), cfg, nil, appwire.ThreadForkParams{
		Ref: "local:" + aliasID, SourceTurnID: "turn_1", EditedInput: "forked input",
	})
	if err != nil {
		t.Fatalf("fork through a resumed alias whose daemon has stopped: %v", err)
	}
	meta, err := schema.LoadSessionMeta(stateDir, resp.Thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ParentSessionID != currentID {
		t.Fatalf("fork branched %q, want the session the resume settled on %q", meta.ParentSessionID, currentID)
	}
}

// A deleted target is terminal: the client is told the target is gone and must
// not retry. Daemon discovery failing is transient and retryable. Refreshing
// ownership before reading the deletion state let the transient answer mask the
// terminal one — a deleted session whose refresh happened to fail came back as
// a generic unavailable, so the client kept retrying a fork that can never
// succeed. The durable deletion state is read first; a target that is not
// deleted still gets the refresh failure.
func TestHubForkReportsDeletionEvenWhenTheRefreshFails(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		name := map[bool]string{false: "live target, refresh fails", true: "deleted target, refresh fails"}[deleted]
		t.Run(name, func(t *testing.T) {
			stateDir := t.TempDir()
			sessionID := buildRPCParentSession(t, stateDir)
			store, err := hubcore.NewDeletionStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if deleted {
				if _, err := store.Begin("project-deleted-0123456789", []hubcore.DeletionTarget{{
					Ref: localAppRef(sessionID), ThreadID: sessionID,
				}}); err != nil {
					t.Fatal(err)
				}
			}
			runDir := t.TempDir()
			previousRefresh := hubRosterRefresh
			hubRosterRefresh = func(context.Context, *hubcore.Roster) error {
				return errors.New("daemon discovery is incomplete")
			}
			t.Cleanup(func() { hubRosterRefresh = previousRefresh })

			cfg := hubcore.WebConfig{
				StateDir: stateDir, RunDir: runDir, DeletionStore: store,
				Roster: hubcore.NewRoster(runDir, &hubcore.StatusProber{}), ResumeLocks: hubcore.NewResumeLocks(),
			}
			before, listErr := schema.ListSessionMetas(stateDir)
			if listErr != nil {
				t.Fatal(listErr)
			}
			_, err = hubThreadFork(t.Context(), cfg, nil, appwire.ThreadForkParams{
				Ref: "local:" + sessionID, SourceTurnID: "turn_1", EditedInput: "forked input",
			})
			if err == nil {
				t.Fatal("fork proceeded while daemon discovery was failing")
			}
			if got := isTargetDeletedError(err); got != deleted {
				t.Fatalf("fork error=%v reports a deleted target=%v, want %v", err, got, deleted)
			}
			after, listErr := schema.ListSessionMetas(stateDir)
			if listErr != nil {
				t.Fatal(listErr)
			}
			if len(after) != len(before) {
				t.Fatalf("refused fork still branched a child: %d metadata records became %d", len(before), len(after))
			}
		})
	}
}

// A thread already fenced for deletion can never be forked: hubThreadFork reads
// that fence before it does anything else. The capability projection is the one
// place every surface funnels through, so it answers from the same unlocked
// read — for the ref the client holds and, when a live daemon has moved the
// session behind a stable ref, for the session the fork would actually branch.
func TestHubForkCapabilityHidesADeletionFencedThread(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fence string // "" leaves the fixture unfenced
		alias bool   // ask through the daemon's stable workspace ref
	}{
		{name: "unfenced"},
		{name: "requested session is fenced", fence: "session"},
		{name: "unfenced through a stable alias", alias: true},
		{name: "alias is fenced", fence: "alias", alias: true},
		{name: "the session the alias resolves to is fenced", fence: "session", alias: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := t.TempDir()
			aliasID := buildRPCParentSession(t, stateDir)
			sessionID, err := identifier.NewSessionID()
			if err != nil {
				t.Fatal(err)
			}
			buildRPCSessionWithWorkingDir(t, stateDir, sessionID, t.TempDir())
			runDir := t.TempDir()
			cfg := hubcore.WebConfig{StateDir: stateDir, RunDir: runDir}
			requestedID := sessionID
			if tc.alias {
				// The cleared-daemon shape: the client still holds the stable
				// workspace ref while the daemon runs its replacement session.
				requestedID = aliasID
				writeRendezvous(t, runDir, rendezvous.Entry{
					PID: os.Getpid(), SourceID: "local", ThreadID: sessionID, SessionID: sessionID, InstanceID: sessionID,
					WorkspaceRef: "local:" + aliasID, StateDir: stateDir,
					Protocol: appwire.ProtocolVersion, StartedAt: time.Now().UTC(),
				})
				roster := hubcore.NewRoster(runDir, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusIdle})
				roster.Refresh()
				cfg.Roster = roster
			}
			if tc.fence != "" {
				fencedID := map[string]string{"alias": aliasID, "session": sessionID}[tc.fence]
				store, err := hubcore.NewDeletionStore(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.Begin("project-deletion-0000000000", []hubcore.DeletionTarget{{
					Ref: localAppRef(fencedID), ThreadID: fencedID,
				}}); err != nil {
					t.Fatal(err)
				}
				cfg.DeletionStore = store
			}

			thread := appwire.Thread{
				ID: sessionID, SessionID: sessionID,
				Evener: appwire.EvenerThread{Ref: "local:" + requestedID, Capabilities: appwire.ThreadCapabilities{ForkFromTurn: true}},
			}
			wantFork := tc.fence == ""
			if got := applyHubForkCapability(cfg, thread).Evener.Capabilities.ForkFromTurn; got != wantFork {
				t.Fatalf("projected forkFromTurn=%v, want %v", got, wantFork)
			}
			// What the projection advertises and what the RPC does must agree.
			_, err = hubThreadFork(t.Context(), cfg, nil, appwire.ThreadForkParams{
				Ref: "local:" + requestedID, SourceTurnID: "turn_1", EditedInput: "forked input",
			})
			if wantFork {
				if err != nil {
					t.Fatalf("advertised fork was refused: %v", err)
				}
				return
			}
			if !isTargetDeletedError(err) {
				t.Fatalf("fork error=%v, want the deletion refusal the projection now hides", err)
			}
		})
	}
}

// A daemon that cleared to a new session and then crashed leaves its rendezvous
// marker on disk: the roster retains it as crashed and stops treating it as a
// live owner, so the pre-lock resolution answers the stable alias. A resolver
// that read the same marker as a live claim would answer the replacement
// session instead, and every fork through that alias would be refused as
// "session ownership changed" with nothing having changed. Both resolvers apply
// the roster's liveness rule, so the alias resolves to one session throughout
// and its saved transcript stays forkable.
func TestHubForkIgnoresACrashRetainedClaimOnTheAlias(t *testing.T) {
	stateDir := t.TempDir()
	aliasID := buildRPCParentSession(t, stateDir)
	currentID, err := identifier.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	buildRPCSessionWithWorkingDir(t, stateDir, currentID, t.TempDir())
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID: 1001, SourceID: "local", ThreadID: currentID, SessionID: currentID, InstanceID: currentID,
		WorkspaceRef: "local:" + aliasID, StateDir: stateDir,
		Protocol: appwire.ProtocolVersion, StartedAt: time.Now().UTC(),
	})
	// kill -9 after the clear: the probe fails and the process is confirmed
	// gone, so the roster retains the marker as crashed.
	roster := hubcore.NewRoster(runDir, fakeProber{shouldFail: true}).SetProcessAlive(func(int) bool { return false })
	roster.Refresh()
	marker, ok := roster.Find(currentID)
	if !ok || !marker.Crashed {
		t.Fatalf("roster entry=%+v ok=%v, want a retained crashed marker", marker, ok)
	}

	cfg := hubcore.WebConfig{
		StateDir: stateDir, RunDir: runDir, Roster: roster,
		DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
			return nil, daemonprocess.ErrExited
		}),
	}
	resp, err := hubThreadFork(t.Context(), cfg, nil, appwire.ThreadForkParams{
		Ref: "local:" + aliasID, SourceTurnID: "turn_1", EditedInput: "forked input",
	})
	if err != nil {
		t.Fatalf("fork through an alias whose only claim is a crash marker: %v", err)
	}
	meta, err := schema.LoadSessionMeta(stateDir, resp.Thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ParentSessionID != aliasID {
		t.Fatalf("fork branched %q, want the alias's own saved session %q", meta.ParentSessionID, aliasID)
	}
}

// A client holding a daemon's stable workspace ref is asking about whichever
// session that daemon is running now, and that is the transcript a fork would
// branch. hubThreadFork fences both identities; the capability has to answer for
// both too, or a stable-ref client is offered a fork of a session the RPC will
// refuse. The alias itself is clear in every fenced row, so only the resolved
// session's state can be hiding the action.
func TestHubForkCapabilityFencesTheSessionAStableRefResolvesTo(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fence func(t *testing.T, locks *hubcore.ResumeLocks, sessionID string)
	}{
		{name: "both identities clear"},
		{
			name: "resolved session needs an explicit resume",
			fence: func(t *testing.T, locks *hubcore.ResumeLocks, sessionID string) {
				finish := locks.BeginForceStop([]string{sessionID})
				if err := locks.PersistForceStop([]string{sessionID}, sessionID); err != nil {
					t.Fatal(err)
				}
				finish(true)
			},
		},
		{
			name: "resolved session is stopping",
			fence: func(t *testing.T, locks *hubcore.ResumeLocks, sessionID string) {
				finish := locks.BeginForceStop([]string{sessionID})
				t.Cleanup(func() { finish(false) })
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := t.TempDir()
			aliasID := buildRPCParentSession(t, stateDir)
			currentID, err := identifier.NewSessionID()
			if err != nil {
				t.Fatal(err)
			}
			buildRPCSessionWithWorkingDir(t, stateDir, currentID, t.TempDir())
			ref := "local:" + aliasID
			daemon := daemonserver.NewServer(daemonserver.ServerConfig{HubToken: "resolved-fence-token"})
			daemon.SetAppIdentity("local", aliasID)
			daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.AppServer().ServeWebSocket))
			t.Cleanup(daemonHTTP.Close)
			runDir := t.TempDir()
			writeRendezvous(t, runDir, rendezvous.Entry{
				Protocol: appwire.ProtocolVersion, Endpoint: "ws" + daemonHTTP.URL[len("http"):], SourceID: "local",
				ThreadID: currentID, SessionID: currentID, WorkspaceRef: ref, InstanceID: currentID,
				StateDir: stateDir, HubToken: "resolved-fence-token", StartedAt: time.Now().UTC(),
			})
			roster := hubcore.NewRoster(runDir, nil)
			roster.Refresh()
			locks := hubcore.NewResumeLocks()
			cfg := hubcore.WebConfig{StateDir: stateDir, RunDir: runDir, Roster: roster, ResumeLocks: locks}
			if got := forkTargetSessionID(cfg, aliasID); got != currentID {
				t.Fatalf("the alias resolves to %q, want the daemon's current session %q", got, currentID)
			}
			if tc.fence != nil {
				tc.fence(t, locks, currentID)
				if state := locks.RecoveryState(aliasID); state.ResumeRequired || state.Stopping != 0 {
					t.Fatalf("the alias itself is fenced (%+v); this row would not isolate the resolved session", state)
				}
			}
			wantFork := tc.fence == nil

			thread := appwire.Thread{ID: currentID, SessionID: currentID, Evener: appwire.EvenerThread{
				Ref: ref, Capabilities: appwire.ThreadCapabilities{ForkFromTurn: true},
			}}
			if got := applyHubForkCapability(cfg, thread).Evener.Capabilities.ForkFromTurn; got != wantFork {
				t.Errorf("projected forkFromTurn=%v, want %v", got, wantFork)
			}
			hub := newHubRPCTestServer(t, cfg)
			t.Cleanup(hub.Close)
			client := dialHubRPC(t, hub)
			t.Cleanup(func() { _ = client.Close() })
			if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
				t.Fatal(err)
			}
			list, err := client.ThreadList(t.Context(), appwire.ThreadListParams{})
			if err != nil {
				t.Fatal(err)
			}
			listed := false
			for _, item := range list.Data {
				if item.Evener.Ref != ref {
					continue
				}
				listed = true
				if got := item.Evener.Capabilities.ForkFromTurn; got != wantFork {
					t.Errorf("listed forkFromTurn=%v, want %v", got, wantFork)
				}
			}
			if !listed {
				t.Fatalf("the live thread is not in the list: %+v", list.Data)
			}
			read, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: ref})
			if err != nil {
				t.Fatal(err)
			}
			if got := read.Thread.Evener.Capabilities.ForkFromTurn; got != wantFork {
				t.Errorf("read forkFromTurn=%v, want %v", got, wantFork)
			}

			_, err = hubThreadFork(t.Context(), cfg, nil, appwire.ThreadForkParams{
				Ref: ref, SourceTurnID: "turn_1", EditedInput: "forked input",
			})
			if wantFork {
				if err != nil {
					t.Fatalf("advertised fork was refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("fork of a fenced resolved session succeeded")
			}
			wire, ok := errors.AsType[appwire.WireError](err)
			if !ok || wire.Code != appwire.CodeUnavailable {
				t.Fatalf("fork error=%v, want structured unavailable", err)
			}
		})
	}
}
