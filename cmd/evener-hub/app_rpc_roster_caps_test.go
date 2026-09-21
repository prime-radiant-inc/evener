package hub

import (
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

// TestLocalDaemonEntriesFromRosterCarriesProbeCapabilities pins the last hop
// of the probe plumbing: a LiveEntry whose probe captured the daemon's own
// capabilities must carry them into the LocalDaemonEntry the hub's list rows
// render from. In-process children inherit the copy too; threadFromEntry's
// alias branch zeroes the rendered set, so inheritance is harmless there,
// but this hand-off must not be where the set goes missing.
func TestLocalDaemonEntriesFromRosterCarriesProbeCapabilities(t *testing.T) {
	caps := appwire.ThreadCapabilities{Send: true, ChangeVisionModel: true}
	live := hubcore.LiveEntry{
		Entry:              rendezvous.Entry{ThreadID: "sess_caps", SessionID: "sess_caps"},
		SessionID:          "sess_caps",
		Status:             "idle",
		RunningSubagentIDs: []string{"sess_child"},
		Capabilities:       caps,
		CapabilitiesKnown:  true,
	}

	entries := localDaemonEntriesFromRoster([]hubcore.LiveEntry{live})
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want the root and its in-process child", entries)
	}
	var root, child *appsource.LocalDaemonEntry
	for i := range entries {
		if entries[i].ReadOnlyAlias {
			child = &entries[i]
		} else {
			root = &entries[i]
		}
	}
	if root == nil || child == nil {
		t.Fatalf("entries = %+v, want one root and one alias", entries)
	}
	if !root.CapabilitiesKnown || root.Capabilities != caps {
		t.Fatalf("root entry = %+v, want the probe's capabilities %+v (known)", root, caps)
	}
	if !child.CapabilitiesKnown || child.Capabilities != caps {
		t.Fatalf("child entry = %+v, want the inherited capabilities %+v (known)", child, caps)
	}
}
