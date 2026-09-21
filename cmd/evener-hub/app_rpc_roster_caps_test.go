package hub

import (
	"testing"

	"primeradiant.com/evener/appwire"
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
	root, child := &entries[0], &entries[1]
	if root.ReadOnlyAlias || !child.ReadOnlyAlias {
		t.Fatalf("entries = %+v, want the root first and its read-only alias second", entries)
	}
	if !root.CapabilitiesKnown || root.Capabilities != caps {
		t.Fatalf("root entry = %+v, want the probe's capabilities %+v (known)", root, caps)
	}
	// The child inherits the pair through the same struct copy that carries
	// its other root fields; threadFromEntry's alias branch zeroes the
	// rendered set (pinned in appsource), so this hand-off must not be where
	// the set goes missing even though the child's own row never renders it.
	if !child.CapabilitiesKnown || child.Capabilities != caps {
		t.Fatalf("child entry = %+v, want the inherited capabilities %+v (known)", child, caps)
	}
}
