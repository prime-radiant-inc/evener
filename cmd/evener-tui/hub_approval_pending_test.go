package tui

import (
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

// #2568: the TUI reads a session's pending question but not its escalation
// cards, so it passed false where the web passes the approval — an approval
// never showed a marker, never counted in the needs-you badge, colored and
// glossed as working, and a project summary called it Working. Since #2513 a
// thread/list row carries the escalation cards (EvenerThread.PendingEscalations),
// so the TUI can derive the approval like the web's displayState does.

func TestHubNodeFromThread_CarriesApprovalPending(t *testing.T) {
	thread := appwire.Thread{SessionID: "01A", Evener: appwire.EvenerThread{
		PendingEscalations: []appwire.SandboxEscalationRequested{{EscalationID: "esc_1", Tool: "write_file"}},
	}}
	node := hubNodeFromThread(thread)
	if !node.ApprovalPending {
		t.Fatal("expected hubTreeNode.ApprovalPending=true from thread.Evener.PendingEscalations")
	}
	if got, want := attentionState(node.State, node.ApprovalPending), "awaiting"; got != want {
		t.Fatalf("an approval node's attention state = %q, want %q (never reads as working)", got, want)
	}
}

func TestBuildDashboardRows_ApprovalPromotesAttention(t *testing.T) {
	tree := hubTreeResponse{Projects: []hubTreeProject{{
		Key: "proj", Name: "evener",
		Sessions: []hubTreeNode{{
			Ref: "local:th_1", State: appwire.ThreadStatusActive, Live: true,
			ApprovalPending: true,
		}},
	}}}
	rows := buildDashboardRows(tree)
	var session *hubRow
	for i := range rows {
		if rows[i].kind == hubRowSession {
			session = &rows[i]
			break
		}
	}
	if session == nil {
		t.Fatal("expected a session row")
	}
	if !session.approvalPending {
		t.Fatal("session row must carry the approval flag")
	}
	if got, want := attentionState(session.state, session.approvalPending), "awaiting"; got != want {
		t.Fatalf("session row attention state = %q, want %q", got, want)
	}
}

func TestDashboardRowLess_ApprovalBandsLikeAsk(t *testing.T) {
	working := hubRow{kind: hubRowSession, state: "active", updatedAt: 3000}
	approval := hubRow{kind: hubRowSession, state: "active", approvalPending: true, updatedAt: 1000}
	if !dashboardRowLess(approval, working) {
		t.Fatal("an approval row must sort above a working row even though it is older")
	}
	yourMove := hubRow{kind: hubRowSession, state: "awaiting", updatedAt: 3000}
	if !dashboardRowLess(approval, yourMove) {
		t.Fatal("an approval row must land in the blocked band above a your-move row")
	}
}

func TestNeedsYouCount_CountsApproval(t *testing.T) {
	rows := []hubRow{
		{kind: hubRowSession, state: "active", approvalPending: true, live: true},
		{kind: hubRowSession, state: "active", live: true},
	}
	if got := needsYouCount(rows); got != 1 {
		t.Fatalf("needsYouCount = %d, want 1 (the approval row)", got)
	}
}

func TestDashboardSessionRow_ApprovalMarkerAndState(t *testing.T) {
	row := hubRow{kind: hubRowSession, state: "active", approvalPending: true, title: "x"}
	rendered := renderDashboardSessionRow(row, false, 80, false, "")
	if !strings.Contains(rendered, "◆") {
		t.Fatalf("an approval row must show the ◆ needs-you marker, got %q", rendered)
	}
	if !strings.Contains(rendered, "awaiting") {
		t.Fatalf("an approval row must read as awaiting/needs-you, got %q", rendered)
	}
	if strings.Contains(rendered, "active") || strings.Contains(rendered, "working") {
		t.Fatalf("an approval row must never read as working, got %q", rendered)
	}
}

func TestProjectSummary_ApprovalReadsNeedsYou(t *testing.T) {
	project := hubRow{kind: hubRowProject, project: "evener", groupKey: "g"}
	rows := []hubRow{
		project,
		{kind: hubRowSession, state: "active", approvalPending: true, live: true, groupKey: "g"},
	}
	got := projectSummary(project, rows)
	if strings.Contains(got, "Working") {
		t.Fatalf("project summary must not call an approval Working, got %q", got)
	}
	if !strings.Contains(got, "Your move") {
		t.Fatalf("project summary should read Your move for a pending approval, got %q", got)
	}
}

func TestSessionHeader_ApprovalReadsNotWorking(t *testing.T) {
	withTestColorProfile(t)
	m := hubModel{}
	m.detail.State = appwire.ThreadStatusActive
	m.detail.Ref = "local:th_1"
	m.escalationsByRef = map[string][]*hubEscalation{
		"local:th_1": {{id: "esc_1", tool: "write_file", ref: "local:th_1"}},
	}
	header := strings.Join(m.sessionHeaderLines(), "\n")
	if strings.Contains(header, "Working") || strings.Contains(header, "WORKING") {
		t.Fatalf("session header must not call an approval Working, got %q", header)
	}
	if !strings.Contains(strings.ToUpper(header), "YOUR MOVE") {
		t.Fatalf("session header should read Your move for a pending approval, got %q", header)
	}
}

// The row's own details pane and the command palette's session entry are two
// more places that render a row's state; both must read an approval as
// awaiting too, or they contradict the row beside them (roborev #3129).
func TestDashboardSessionDetails_ApprovalReadsNeedsYou(t *testing.T) {
	row := hubRow{kind: hubRowSession, state: "active", approvalPending: true}
	got := dashboardSessionDetails(row)
	if !strings.Contains(got, "State:    awaiting") {
		t.Fatalf("details pane must show the approval as awaiting, got %q", got)
	}
	if strings.Contains(got, "State:    active") {
		t.Fatalf("details pane must not show the approval as active, got %q", got)
	}
}

func TestCommandPaletteSessionEntry_ApprovalReadsNeedsYou(t *testing.T) {
	rows := []hubRow{{
		kind: hubRowSession, state: "active", approvalPending: true, title: "s",
		ref: appwire.Ref{ThreadID: "t"},
	}}
	entries := commandPaletteEntriesForRows(hubModeDashboard, hubSessionCapabilities{}, rows)
	found := false
	for _, e := range entries {
		if e.Kind != commandPaletteSession {
			continue
		}
		found = true
		if !strings.Contains(e.Item.Detail, "awaiting") || strings.Contains(e.Item.Detail, "active") {
			t.Fatalf("palette session detail must read the approval as awaiting, got %q", e.Item.Detail)
		}
	}
	if !found {
		t.Fatal("expected a session entry in the palette rows")
	}
}

// The dashboard has no periodic refresh, so the row's approval flag must follow
// the live escalation path (raise/answer), not only the thread/list snapshot
// (roborev #3129 round 2).
func TestApplySandboxEscalation_KeepsDashboardRowLive(t *testing.T) {
	ref, err := appwire.ParseRef("local:th_1")
	if err != nil {
		t.Fatal(err)
	}
	m := hubModel{rows: []hubRow{{kind: hubRowSession, ref: ref, state: "active"}}}
	m.applySandboxEscalation(appwire.SandboxEscalationRequested{EscalationID: "esc_1"}, "local:th_1")
	if !m.rows[0].approvalPending {
		t.Fatal("a live escalation must mark the session's dashboard row as approval-pending")
	}
}

func TestHandleEscalationResolved_ClearsDashboardRowLive(t *testing.T) {
	ref, err := appwire.ParseRef("local:th_1")
	if err != nil {
		t.Fatal(err)
	}
	m := hubModel{rows: []hubRow{{kind: hubRowSession, ref: ref, state: "active", approvalPending: true}}}
	m.escalationsByRef = map[string][]*hubEscalation{"local:th_1": {{id: "esc_1", ref: "local:th_1"}}}
	m.handleEscalationResolved(hubEscalationResolvedMsg{ref: "local:th_1", id: "esc_1", approve: true})
	if m.rows[0].approvalPending {
		t.Fatal("answering the escalation must clear the row's approval flag")
	}
}

func TestMergeSnapshotEscalations_FlagsDashboardRow(t *testing.T) {
	ref, err := appwire.ParseRef("local:th_1")
	if err != nil {
		t.Fatal(err)
	}
	m := hubModel{rows: []hubRow{{kind: hubRowSession, ref: ref, state: "active"}}}
	m.mergeSnapshotEscalations(hubSessionDetail{
		Ref:                "local:th_1",
		PendingEscalations: []appwire.SandboxEscalationRequested{{EscalationID: "esc_1"}},
	})
	if !m.rows[0].approvalPending {
		t.Fatal("a snapshot/reconnect escalation must flag the session's dashboard row")
	}
}

// Clearing an approval must also recompute the project's rollup and re-sort the
// group, so the cached dashboard rows do not keep a stale "Your move" or a
// resolved session sorted above its working siblings (roborev #3129 round 3).
func TestSetDashboardRowApproval_RecomputesGroupAndOrder(t *testing.T) {
	tree := hubTreeResponse{Projects: []hubTreeProject{{
		Key: "proj", Name: "evener",
		Sessions: []hubTreeNode{
			{Ref: "local:s1", State: appwire.ThreadStatusActive, Live: true, ApprovalPending: true, UpdatedAt: 1000},
			{Ref: "local:s2", State: appwire.ThreadStatusActive, Live: true, UpdatedAt: 2000},
		},
	}}}
	m := hubModel{tree: tree, rows: buildDashboardRows(tree)}
	if got := projectRowState(m.rows); got != "awaiting" {
		t.Fatalf("before clearing, project state = %q, want awaiting", got)
	}
	if sessionRowIndex(m.rows, "local:s1") > sessionRowIndex(m.rows, "local:s2") {
		t.Fatal("the approval row should sort first before it is cleared")
	}

	m.setDashboardRowApproval("local:s1", false)

	if got := projectRowState(m.rows); got != "active" {
		t.Fatalf("after clearing, project state = %q, want active (summary must not stay Your move)", got)
	}
	if sessionRowIndex(m.rows, "local:s1") < sessionRowIndex(m.rows, "local:s2") {
		t.Fatal("after clearing, the more recent working row should sort first")
	}
}

func projectRowState(rows []hubRow) string {
	for _, row := range rows {
		if row.kind == hubRowProject {
			return row.state
		}
	}
	return "<none>"
}

func sessionRowIndex(rows []hubRow, ref string) int {
	for i, row := range rows {
		if row.kind == hubRowSession && row.ref.String() == ref {
			return i
		}
	}
	return -1
}
