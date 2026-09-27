package hub

import (
	"slices"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

// TestLocalDaemonEntriesFromRosterCarriesProbeCapabilitiesAndApprovalFlags
// pins the last hop of the probe plumbing: a LiveEntry whose probe captured
// the daemon's own capabilities must carry them into the LocalDaemonEntry
// the hub's list rows render from, and the approval flags must survive the
// same hand-off — the fallback folds them out of Clear, so a roster entry
// with pending approval work must reach the row builder flagged, or prod
// rows overstate Clear. In-process children inherit the capabilities through
// the same copy; threadFromEntry's alias branch zeroes the rendered set, so
// inheritance is harmless there, but this hand-off must not be where the state
// goes missing. The approval flags are the root's alone, never inherited
// (TestLocalDaemonEntriesFromRosterAliasesInheritNoAskOrApproval).
func TestLocalDaemonEntriesFromRosterCarriesProbeCapabilitiesAndApprovalFlags(t *testing.T) {
	caps := appwire.ThreadCapabilities{Send: true, ChangeVisionModel: true}
	live := hubcore.LiveEntry{
		Entry:              rendezvous.Entry{ThreadID: "sess_caps", SessionID: "sess_caps"},
		SessionID:          "sess_caps",
		Status:             "idle",
		RunningSubagentIDs: []string{"sess_child"},
		Capabilities:       caps,
		CapabilitiesKnown:  true,
		PendingAsk:         true,
		PendingEscalation:  true,
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
	if !root.PendingAsk || !root.PendingEscalation {
		t.Fatalf("root entry = %+v, want the roster's ask and escalation flags carried", root)
	}
	// The child inherits the capabilities through the same struct copy that
	// carries its other root fields; threadFromEntry's alias branch zeroes the
	// rendered set (pinned in appsource), so this hand-off must not be where
	// the set goes missing even though the child's own row never renders it.
	if !child.CapabilitiesKnown || child.Capabilities != caps {
		t.Fatalf("child entry = %+v, want the inherited capabilities %+v (known)", child, caps)
	}
}

// TestLocalDaemonEntriesFromRosterAliasesInheritNoAskOrApproval pins who owns
// a pending question and a pending approval: the root session. ask_user and
// sandbox escalation are both root-only (agent/session_escalation.go's
// escalationAllowed mirrors ask_user's gate), yet an in-process child's entry is
// a copy of its root's. A copied flag would put the root's question on the
// child's row, and a controller hub reading this hub's list would show it, and
// the approval, on the remote subagent too. The root's row carries the cards
// themselves.
func TestLocalDaemonEntriesFromRosterAliasesInheritNoAskOrApproval(t *testing.T) {
	card := appwire.SandboxEscalationRequested{ThreadID: "sess_root", Ref: "local:sess_root", EscalationID: "esc_1", Tool: "write_file", Kind: "file_tool", DeniedPath: "/srv/docs/a.md"}
	live := hubcore.LiveEntry{
		Entry:              rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1:50001/rpc", ThreadID: "sess_root", SessionID: "sess_root"},
		SessionID:          "sess_root",
		Status:             appwire.ThreadStatusActive,
		RunningSubagentIDs: []string{"sess_child"},
		PendingAsk:         true,
		PendingEscalation:  true,
		PendingEscalations: []appwire.SandboxEscalationRequested{card},
	}

	entries := localDaemonEntriesFromRoster([]hubcore.LiveEntry{live})
	if len(entries) != 2 || entries[0].ReadOnlyAlias || !entries[1].ReadOnlyAlias {
		t.Fatalf("entries = %+v, want the root and then its read-only alias", entries)
	}
	root, child := entries[0], entries[1]
	if !root.PendingAsk || !root.PendingEscalation || !slices.Equal(root.PendingEscalations, []appwire.SandboxEscalationRequested{card}) {
		t.Fatalf("root entry = %+v, want its ask, its escalation flag and its card", root)
	}
	if child.PendingAsk || child.PendingEscalation || len(child.PendingEscalations) != 0 {
		t.Fatalf("child entry = %+v, want no question, no escalation flag and no card", child)
	}

	// The rows this hub's thread/list serves: what a controller hub reads.
	listed := listRowsFromLocalDaemonSource(t, func() []appsource.LocalDaemonEntry { return entries })
	rows := make(map[string]appwire.Thread, len(listed))
	for _, row := range listed {
		rows[row.ID] = row
	}
	rootRow, childRow := rows["sess_root"], rows["sess_child"]
	if !rootRow.Evener.AskPending || !slices.Equal(rootRow.Evener.PendingEscalations, []appwire.SandboxEscalationRequested{card}) {
		t.Fatalf("root row = %+v, want its question and its card", rootRow.Evener)
	}
	if childRow.Evener.Kind != "subagent" || childRow.Evener.AskPending || len(childRow.Evener.PendingEscalations) != 0 {
		t.Fatalf("child row = %+v, want a subagent row with no question and no card", childRow.Evener)
	}
}
