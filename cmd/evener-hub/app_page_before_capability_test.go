package hub

import (
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
