package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
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
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Past: hubcore.NewPastIndex("")})
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

func TestHubForkCapabilityExcludesReadOnlyAndRemoteThreads(t *testing.T) {
	for _, tc := range []struct {
		ref, kind string
		want      bool
	}{
		{"local:root", "", true}, {"local:aside", "aside", true},
		{"local:child", "subagent", false}, {"remote:root", "", false}, {"", "", false},
	} {
		thread := appwire.Thread{Evener: appwire.EvenerThread{Ref: tc.ref, Kind: tc.kind}}
		if got := hubOwnsThreadFork(thread); got != tc.want {
			t.Errorf("ref=%q kind=%q: fork ownership=%v, want %v", tc.ref, tc.kind, got, tc.want)
		}
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
			name:     "persisted subagent",
			thread:   appwire.Thread{Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}, Evener: appwire.EvenerThread{Ref: "local:child", Kind: "subagent", Capabilities: appwire.ThreadCapabilities{ForkFromTurn: true}}},
			wantFork: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := applyHubForkCapability(tc.thread)
			if got.Evener.Capabilities.ForkFromTurn != tc.wantFork {
				t.Fatalf("forkFromTurn=%v, want %v (thread=%+v)", got.Evener.Capabilities.ForkFromTurn, tc.wantFork, got)
			}
		})
	}
}

func TestHubForkCapabilityKeepsDaemonPermissionsAndUnknownFields(t *testing.T) {
	original := appwire.Notification{Method: appwire.NotifyThreadStatusChanged, Params: testRawJSON(t, map[string]any{
		"ref": "local:root", "status": map[string]any{"type": "active"}, "future_field": 17,
		"capabilities": map[string]bool{"send": false, "steer": true, "forkFromTurn": false, "futureAction": true},
	})}
	got := stampForkCapability(original)
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
		n := stampForkCapability(appwire.Notification{Method: appwire.NotifyThreadStatusChanged, Params: json.RawMessage(raw)})
		if string(n.Params) != raw {
			t.Fatalf("missing capability set was fabricated: %s", n.Params)
		}
	}
}
