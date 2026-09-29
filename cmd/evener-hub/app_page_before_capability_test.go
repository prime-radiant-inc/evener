package hub

import (
	"encoding/json"
	"os"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

// pageBefore is the hub's own answer, like fork: it pages its local and saved
// threads from a before position itself. A thread on another host stays off
// until #3176, and so does a live delegate, a read-only alias its parent
// daemon owns.
func TestHubCapabilitiesAdvertisePageBeforeForTheHubsOwnThreads(t *testing.T) {
	stateDir := t.TempDir()
	parentID := buildRPCParentSession(t, stateDir)
	childID, err := agent.ForkSession(stateDir, parentID, 1, "live delegate", "")
	if err != nil {
		t.Fatal(err)
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
	for ref, want := range map[string]bool{
		"local:" + parentID: true,
		"local:" + childID:  false,
		"host:" + parentID:  false,
		"not a ref":         false,
	} {
		// A stale source value never survives: the hub's answer replaces it.
		thread := appwire.Thread{Evener: appwire.EvenerThread{Ref: ref, Capabilities: appwire.ThreadCapabilities{PageBefore: !want}}}
		if got := applyHubCapabilities(cfg, thread).Evener.Capabilities.PageBefore; got != want {
			t.Errorf("%s: pageBefore = %v, want %v", ref, got, want)
		}
	}
}

// A session reads the same from thread/list as from thread/read (#1840).
func TestHubRPCLocalThreadListRowAdvertisesPageBefore(t *testing.T) {
	client, ref := realDaemonBehindHub(t, "turns-list-page-before-list", 3)
	listed, err := client.ThreadList(t.Context(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, thread := range listed.Data {
		if thread.Evener.Ref == ref {
			if !thread.Evener.Capabilities.PageBefore {
				t.Fatalf("local list row capabilities = %+v, want pageBefore", thread.Evener.Capabilities)
			}
			return
		}
	}
	t.Fatalf("list = %+v, want a row for %s", listed.Data, ref)
}

// A status frame carries the daemon's capability set, which never names
// pageBefore; the relay stamps the hub's answer on it, so the thread keeps
// reading the same after its first status change.
func TestRelayedStatusFrameCarriesThePageBeforeAnswer(t *testing.T) {
	frame := func(capabilities string) appwire.Notification {
		return appwire.Notification{
			Method: appwire.NotifyThreadStatusChanged,
			Params: json.RawMessage(`{"threadId":"t1","status":{"type":"idle"},"capabilities":` + capabilities + `}`),
		}
	}
	decode := func(n appwire.Notification) appwire.ThreadCapabilities {
		var params struct {
			Capabilities appwire.ThreadCapabilities `json:"capabilities"`
		}
		if err := json.Unmarshal(n.Params, &params); err != nil {
			t.Fatal(err)
		}
		return params.Capabilities
	}
	if caps := decode(stampPageBeforeCapability(frame(`{"send":true}`), true)); !caps.PageBefore || !caps.Send {
		t.Fatalf("stamped capabilities = %+v, want pageBefore beside the daemon's send", caps)
	}
	if caps := decode(stampPageBeforeCapability(frame(`{"send":true,"pageBefore":true}`), false)); caps.PageBefore {
		t.Fatalf("stamped capabilities = %+v, want a stale pageBefore cleared", caps)
	}
	other := appwire.Notification{Method: appwire.NotifyThreadNameChanged, Params: json.RawMessage(`{"threadId":"t1"}`)}
	if got := stampPageBeforeCapability(other, true); string(got.Params) != string(other.Params) {
		t.Fatalf("a non-status frame changed: %s", got.Params)
	}
}
