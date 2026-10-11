package hub

import (
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/rendezvous"
)

// The hub's own list rows carry a root's tally and never lend it to the root's
// in-process subagent aliases.
func TestLocalDaemonEntriesFromRosterCarryTheTallyOnlyOnTheRoot(t *testing.T) {
	tally := appwire.SubagentTally{Running: 1, Done: 2}
	entries := localDaemonEntriesFromRoster([]hubcore.LiveEntry{{
		Entry:     rendezvous.Entry{ThreadID: "sess_root", SessionID: "sess_root"},
		SessionID: "sess_root", Status: appwire.ThreadStatusActive,
		RunningSubagentIDs: []string{"sess_child"}, Subagents: tally,
	}})
	if len(entries) != 2 || entries[0].Subagents != tally || entries[1].Subagents != (appwire.SubagentTally{}) {
		t.Fatalf("entries = %+v, want the root's tally %+v and none on its alias", entries, tally)
	}
}

// A controller reads a remote host's tally off its list row, so the remote
// live row carries subagents exactly like a local one (S3), and a remote row
// whose tree has no subagent carries no key.
func TestNavigationRemoteLiveRowCarriesItsSubagentTally(t *testing.T) {
	projection := remoteNavigationProjection(t, []appwire.Thread{
		{ID: "coordinating", Source: "host-a", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}, Evener: appwire.EvenerThread{Subagents: &appwire.SubagentTally{Running: 3, Failed: 1}}},
		{ID: "alone", Source: "host-a", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}},
	})
	row := navigationProjectedSummary(t, projection, "host-a:coordinating")
	if want := (hubapi.NavigationSubagentTally{Running: 3, Failed: 1}); row.Subagents == nil || *row.Subagents != want {
		t.Fatalf("remote row tally = %+v, want %+v", row.Subagents, want)
	}
	if _, carried := navigationSummaryJSONFields(t, navigationProjectedSummary(t, projection, "host-a:alone"))["subagents"]; carried {
		t.Fatal("a remote row with no subagent carries the subagents key")
	}
}
