package hub

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/rendezvous"
)

// TestNavigationRowsCarryApprovalPending pins S2's wire contract: a live
// session blocked on a sandbox approval carries approval_pending on its Live
// and NeedsYou rows and keeps reporting its real state ("active"). Every other
// row omits the key, so its shaping is byte-for-byte unchanged.
func TestNavigationRowsCarryApprovalPending(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "evener")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	roster := hubcore.NewRosterWithEntries(
		hubcore.LiveEntry{Entry: rendezvous.Entry{PID: 1, SessionID: "01APPROVAL", WorkingDir: project.CanonicalPath}, SessionID: "01APPROVAL", Status: appwire.ThreadStatusActive, PendingEscalation: true},
		hubcore.LiveEntry{Entry: rendezvous.Entry{PID: 2, SessionID: "01WORKING", WorkingDir: project.CanonicalPath}, SessionID: "01WORKING", Status: appwire.ThreadStatusActive},
	)
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), Roster: roster})
	_, live, projects := web.navigationTreeInputs(t.Context())
	tree := hubBuildNavigationTree(nil, live, map[hubcore.ArchiveKey]bool{}, projects)
	inputs := navigationBuildInputsFromTreeSnapshot("generation", 1, tree, web.apiTreeSources(), hubapi.AttentionSummary{}, live, nil, nil, nil, nil)
	projection, err := buildNavigationProjection(inputs)
	if err != nil {
		t.Fatalf("buildNavigationProjection: %v", err)
	}

	needsYou := projection.NeedsYouPage(0, 50).Sessions
	if len(needsYou) != 1 || needsYou[0].SessionID != "01APPROVAL" {
		t.Fatalf("needs-you rows = %#v, want only the session blocked on an approval", needsYou)
	}
	if !needsYou[0].ApprovalPending || needsYou[0].State != "active" {
		t.Fatalf("needs-you row = %#v, want approval_pending with its real state active", needsYou[0])
	}
	liveRows := projection.LivePage(0, 50).Sessions
	if len(liveRows) != 2 {
		t.Fatalf("live rows = %#v, want both sessions", liveRows)
	}
	for _, row := range liveRows {
		raw, carried := navigationSummaryJSONFields(t, row)["approval_pending"]
		if row.SessionID == "01APPROVAL" {
			if string(raw) != "true" {
				t.Fatalf("approval row JSON approval_pending = %q, want true (row = %#v)", raw, row)
			}
		} else if carried {
			t.Fatalf("row %s carries approval_pending; want the key absent: %#v", row.SessionID, row)
		}
	}
}
