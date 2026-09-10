package hub

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
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
		t.Fatal("parent subagent disappeared after rejected fork")
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
