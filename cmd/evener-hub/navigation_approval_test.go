package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/rendezvous"
)

// TestNavigationRowsCarryApprovalPending pins the approval's wire contract: a
// live session blocked on a sandbox approval carries approval_pending on its
// Live and NeedsYou rows, keeps reporting its real state ("active"), and names
// what the oldest card asks for: approval_tool, the tool that was denied, and
// approval_target, the full path it was denied. A row without an approval
// omits all three keys.
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
		hubcore.LiveEntry{Entry: rendezvous.Entry{PID: 1, SessionID: "01APPROVAL", WorkingDir: project.CanonicalPath}, SessionID: "01APPROVAL", Status: appwire.ThreadStatusActive, PendingEscalation: true, PendingEscalations: []appwire.SandboxEscalationRequested{
			{EscalationID: "esc_1", Tool: "write_file", Kind: "file_tool", DeniedPath: "/home/me/sites/docs/index.md"},
			{EscalationID: "esc_2", Tool: "edit_file", Kind: "file_tool", DeniedPath: "/etc/hosts"},
		}},
		hubcore.LiveEntry{Entry: rendezvous.Entry{PID: 2, SessionID: "01WORKING", WorkingDir: project.CanonicalPath}, SessionID: "01WORKING", Status: appwire.ThreadStatusActive},
	)
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), Roster: roster})
	snapshot, err := (webNavigationSource{web: web}).Capture(t.Context(), "generation", time.Now())
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	projection, err := buildNavigationProjection(snapshot.Inputs)
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
	if needsYou[0].ApprovalTool != "write_file" || needsYou[0].ApprovalTarget != "/home/me/sites/docs/index.md" {
		t.Fatalf("needs-you row = %#v, want the first card's write_file and its full path", needsYou[0])
	}
	liveRows := projection.LivePage(0, 50).Sessions
	if len(liveRows) != 2 {
		t.Fatalf("live rows = %#v, want both sessions", liveRows)
	}
	for _, row := range liveRows {
		assertNavigationApprovalJSON(t, row, row.SessionID == "01APPROVAL")
	}
}

// assertNavigationApprovalJSON checks a row's approval keys on the wire: the
// row blocked on the write_file card carries all three with that card's
// values, and every other row omits them.
func assertNavigationApprovalJSON(t *testing.T, row hubapi.NavigationSessionSummary, approving bool) {
	t.Helper()
	want := map[string]string{"approval_pending": "true", "approval_tool": `"write_file"`, "approval_target": `"/home/me/sites/docs/index.md"`}
	fields := navigationSummaryJSONFields(t, row)
	for key, value := range want {
		raw, carried := fields[key]
		if approving && string(raw) != value {
			t.Fatalf("approval row %s JSON %s = %s, want %s (row = %#v)", row.Ref, key, raw, value, row)
		}
		if !approving && carried {
			t.Fatalf("row %s carries %s; want the key absent: %#v", row.Ref, key, row)
		}
	}
}

// A target past the label bound arrives cut to it, and a tool past the identity
// bound arrives cut to that, so one long path cannot make the hub's schema, and
// then the client's codec, refuse the whole section and every other row in it.
func TestNavigationProjectionTruncatesTheApprovalDetail(t *testing.T) {
	rows := liveTaskRows(t, []hubcore.TreeNode{{
		ID: "session-approval", Title: "approval", Kind: "session", State: "active", ApprovalPending: true,
		ApprovalTool:   strings.Repeat("t", maxNavigationIdentityBytes+10),
		ApprovalTarget: strings.Repeat("é", 600),
	}})
	row := rows["session-approval"]
	if utf8.RuneCountInString(row.ApprovalTarget) != maxNavigationLabelRunes || !strings.HasSuffix(row.ApprovalTarget, "…") {
		t.Fatalf("target = %d runes, want it cut to %d ending in an ellipsis", utf8.RuneCountInString(row.ApprovalTarget), maxNavigationLabelRunes)
	}
	if len(row.ApprovalTool) > maxNavigationIdentityBytes || !strings.HasSuffix(row.ApprovalTool, "…") {
		t.Fatalf("tool = %d bytes, want it cut to at most %d ending in an ellipsis", len(row.ApprovalTool), maxNavigationIdentityBytes)
	}
}
