package hubcore

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/rendezvous"
)

func TestBuildTree_GroupsByProjectWithSubagentsAndForks(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{
		// Active branch — newer, holds the original session's name. Top-level.
		{ID: "01ACTIVE", UpdatedAt: now, OriginalPrompt: "fix replay bug",
			EnvInfo:         schema.EnvironmentInfo{WorkingDir: "/projects/serf-hub"},
			ParentSessionID: "01OLDORIG", DivergenceTurn: 7},
		// Subagent of the active branch.
		{ID: "01SUB1", UpdatedAt: now.Add(-time.Minute), OriginalPrompt: "verify",
			EnvInfo:         schema.EnvironmentInfo{WorkingDir: "/projects/serf-hub"},
			ParentSessionID: "01ACTIVE", IsSubagent: true},
		// Snapshotted original — older transcript preserved. Has ForkLabel.
		// Becomes a dim child of 01ACTIVE (the active branch references it).
		{ID: "01OLDORIG", UpdatedAt: now.Add(-2 * time.Hour), OriginalPrompt: "fix replay bug",
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/projects/serf-hub"},
			ForkLabel: "before TDD"},
		// Unrelated session in same project.
		{ID: "01OTHER", UpdatedAt: now.Add(-15 * time.Minute), OriginalPrompt: "htmx swap",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf-hub"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01ACTIVE", Status: appwire.ThreadStatusActive},
		{Entry: rendezvous.Entry{PID: 2}, SessionID: "01SUB1", Status: appwire.ThreadStatusActive},
	}

	tree := buildTree(metas, live)

	// One project
	if len(tree.Projects) != 1 {
		t.Fatalf("projects: %d", len(tree.Projects))
	}
	proj := tree.Projects[0]
	if proj.Name != "serf-hub" {
		t.Errorf("name: %q", proj.Name)
	}

	// Top-level sessions: 01ACTIVE then 01OTHER (by recency).
	// 01OLDORIG is NOT top-level — it's the snapshotted original under 01ACTIVE.
	sessions := allSessions(proj)
	if len(sessions) != 2 {
		t.Fatalf("sessions: %d", len(sessions))
	}
	if sessions[0].ID != "01ACTIVE" {
		t.Errorf("[0]: %q", sessions[0].ID)
	}
	if sessions[1].ID != "01OTHER" {
		t.Errorf("[1]: %q", sessions[1].ID)
	}

	// Children of 01ACTIVE: subagent first, then the snapshotted original (fork).
	children := sessions[0].Children
	if len(children) != 2 {
		t.Fatalf("children: %d", len(children))
	}
	if children[0].ID != "01SUB1" || children[0].Kind != "subagent" {
		t.Errorf("[0]: %s/%s", children[0].ID, children[0].Kind)
	}
	if children[1].ID != "01OLDORIG" || children[1].Kind != "fork" {
		t.Errorf("[1]: %s/%s", children[1].ID, children[1].Kind)
	}
	// Fork title includes the label.
	if !strings.Contains(children[1].Title, "before TDD") {
		t.Errorf("fork title missing label: %q", children[1].Title)
	}

	// 01OTHER has no children
	if len(sessions[1].Children) != 0 {
		t.Errorf("01OTHER should have no children, got %d", len(sessions[1].Children))
	}

	// Live: 2 entries, both active.
	if len(tree.Live) != 2 {
		t.Fatalf("live: %d", len(tree.Live))
	}

	// Rollup state for the project: active (the most-attention live state).
	if proj.RollupState != "active" {
		t.Errorf("rollup: %q", proj.RollupState)
	}
}

func TestBuildTree_UsesGeneratedNameForSessionTitle(t *testing.T) {
	tree := buildTree([]schema.SessionMeta{{
		ID:             "01NAMED",
		Name:           "Launch Config Cheap Model",
		OriginalPrompt: "unrelated original prompt",
		UpdatedAt:      time.Now(),
	}}, nil)
	if len(tree.Projects) != 1 || len(allSessions(tree.Projects[0])) != 1 {
		t.Fatalf("unexpected tree shape: %#v", tree)
	}
	if got := allSessions(tree.Projects[0])[0].Title; got != "Launch Config Cheap Model" {
		t.Fatalf("session title = %q, want generated name", got)
	}
}

func TestBuildTree_UsesGeneratedNameForForkBaseTitle(t *testing.T) {
	tree := buildTree([]schema.SessionMeta{{
		ID:             "01FORK",
		Name:           "Generated Base",
		OriginalPrompt: "original base",
		ForkLabel:      "before TDD",
		UpdatedAt:      time.Now(),
	}}, nil)
	if len(tree.Projects) != 1 || len(allSessions(tree.Projects[0])) != 1 {
		t.Fatalf("unexpected tree shape: %#v", tree)
	}
	if got := allSessions(tree.Projects[0])[0].Title; got != "Generated Base · before TDD" {
		t.Fatalf("fork title = %q, want generated name plus label", got)
	}
}

func TestBuildTree_AttentionSortsLive(t *testing.T) {
	// Three live sessions: idle, awaiting, processing.
	// Live should sort: awaiting, processing, idle.
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01IDLE", UpdatedAt: now.Add(-3 * time.Minute), OriginalPrompt: "idle task",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
		{ID: "01AWAIT", UpdatedAt: now.Add(-2 * time.Minute), OriginalPrompt: "awaiting task",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
		{ID: "01PROC", UpdatedAt: now.Add(-1 * time.Minute), OriginalPrompt: "processing task",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01IDLE", Status: "idle"},
		{Entry: rendezvous.Entry{PID: 2}, SessionID: "01AWAIT", Status: "awaiting"},
		{Entry: rendezvous.Entry{PID: 3}, SessionID: "01PROC", Status: appwire.ThreadStatusActive},
	}

	tree := buildTree(metas, live)

	if len(tree.Live) != 3 {
		t.Fatalf("live count: %d", len(tree.Live))
	}
	if tree.Live[0].ID != "01AWAIT" {
		t.Errorf("live[0] should be awaiting, got %q (state=%q)", tree.Live[0].ID, tree.Live[0].State)
	}
	if tree.Live[1].ID != "01PROC" {
		t.Errorf("live[1] should be processing, got %q (state=%q)", tree.Live[1].ID, tree.Live[1].State)
	}
	if tree.Live[2].ID != "01IDLE" {
		t.Errorf("live[2] should be idle, got %q (state=%q)", tree.Live[2].ID, tree.Live[2].State)
	}

	// All live nodes should have Kind="session"
	for _, node := range tree.Live {
		if node.Kind != "session" {
			t.Errorf("live node %q has kind %q, want session", node.ID, node.Kind)
		}
	}
}

func TestBuildTree_OrdersProjectSessionsByUpdatedCreatedTitleAndID(t *testing.T) {
	updated := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "02OLD", CreatedAt: updated.Add(-2 * time.Hour), UpdatedAt: updated, OriginalPrompt: "beta task",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
		{ID: "01NEW", CreatedAt: updated.Add(-time.Hour), UpdatedAt: updated, OriginalPrompt: "alpha task",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
		{ID: "03TITLEB", CreatedAt: updated.Add(-3 * time.Hour), UpdatedAt: updated.Add(-time.Hour), OriginalPrompt: "bravo task",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
		{ID: "04TITLEA", CreatedAt: updated.Add(-3 * time.Hour), UpdatedAt: updated.Add(-time.Hour), OriginalPrompt: "alpha task",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
	}

	// Classify against a clock right after the sessions so all four land in the
	// same (Current) tier; the test is about within-project ordering, not tiering.
	tree := BuildTreeAt(metas, nil, map[ArchiveKey]bool{}, updated.Add(time.Hour))
	if len(tree.Projects) != 1 {
		t.Fatalf("projects=%d", len(tree.Projects))
	}
	sessions := allSessions(tree.Projects[0])
	got := make([]string, 0, len(sessions))
	for _, node := range sessions {
		got = append(got, node.ID)
	}
	want := []string{"01NEW", "02OLD", "04TITLEA", "03TITLEB"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order=%v, want %v", got, want)
	}
}

func TestBuildTree_OrdersLiveRowsWithoutMetasByStartedAtAndID(t *testing.T) {
	base := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1, StartedAt: base.Add(-time.Hour)}, SessionID: "02OLD", Status: appwire.ThreadStatusIdle},
		{Entry: rendezvous.Entry{PID: 2, StartedAt: base}, SessionID: "01NEW", Status: appwire.ThreadStatusIdle},
		{Entry: rendezvous.Entry{PID: 3, StartedAt: base.Add(-2 * time.Hour)}, SessionID: "04TIEA", Status: appwire.ThreadStatusIdle},
		{Entry: rendezvous.Entry{PID: 4, StartedAt: base.Add(-2 * time.Hour)}, SessionID: "03TIEB", Status: appwire.ThreadStatusIdle},
	}

	tree := buildTree(nil, live)
	got := make([]string, 0, len(tree.Live))
	for _, node := range tree.Live {
		got = append(got, node.ID)
	}
	want := []string{"01NEW", "02OLD", "03TIEB", "04TIEA"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("live order=%v, want %v", got, want)
	}
}

func TestBuildTree_OrdersMixedLiveRowsByMergedMetadata(t *testing.T) {
	base := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{{
		ID:             "01META",
		UpdatedAt:      base.Add(time.Hour),
		CreatedAt:      base.Add(-time.Hour),
		OriginalPrompt: "meta-backed live row",
	}}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1, StartedAt: base.Add(-2 * time.Hour)}, SessionID: "01META", Status: appwire.ThreadStatusIdle},
		{Entry: rendezvous.Entry{PID: 2, StartedAt: base}, SessionID: "02FRESH", Status: appwire.ThreadStatusIdle},
	}

	tree := buildTree(metas, live)
	got := make([]string, 0, len(tree.Live))
	for _, node := range tree.Live {
		got = append(got, node.ID)
	}
	want := []string{"01META", "02FRESH"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("live order=%v, want %v", got, want)
	}
}

func TestBuildTree_NoProjectFallback(t *testing.T) {
	// A meta with empty WorkingDir — project name "(no project)".
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01NOPROJ", UpdatedAt: now, OriginalPrompt: "orphan task",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: ""}},
	}
	live := []LiveEntry{}

	tree := buildTree(metas, live)

	if len(tree.Projects) != 1 {
		t.Fatalf("projects: %d", len(tree.Projects))
	}
	if tree.Projects[0].Name != "(no project)" {
		t.Errorf("project name: %q", tree.Projects[0].Name)
	}
	sessions := allSessions(tree.Projects[0])
	if len(sessions) != 1 {
		t.Fatalf("sessions: %d", len(sessions))
	}
	if sessions[0].ID != "01NOPROJ" {
		t.Errorf("session id: %q", sessions[0].ID)
	}
	// Not live => state = "ended"
	if sessions[0].State != "ended" {
		t.Errorf("state: %q", sessions[0].State)
	}
	// No live sessions => RollupState = ""
	if tree.Projects[0].RollupState != "" {
		t.Errorf("rollup should be empty, got %q", tree.Projects[0].RollupState)
	}
	// Live slice is empty
	if len(tree.Live) != 0 {
		t.Fatalf("live: %d", len(tree.Live))
	}
}

// buildTree is the test convenience wrapper around BuildTree for the cases that
// don't exercise archive decisions: it passes an empty decision map.
func buildTree(metas []schema.SessionMeta, live []LiveEntry) Tree {
	return BuildTree(metas, live, map[ArchiveKey]bool{})
}

// allSessions returns a project's top-level session rows across all tiers,
// Current then Recent then Archived — the flat list most session-shape
// assertions want now that sessions are tier-split.
func allSessions(p TreeProject) []TreeNode {
	out := make([]TreeNode, 0, len(p.Current)+len(p.Recent)+len(p.Archived))
	out = append(out, p.Current...)
	out = append(out, p.Recent...)
	out = append(out, p.Archived...)
	return out
}

// projectByName finds the TreeProject with the given name in the active list, or
// fails.
func projectByName(t *testing.T, tree Tree, name string) TreeProject {
	t.Helper()
	for _, p := range tree.Projects {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("project %q not found in %v", name, projectNames(tree.Projects))
	return TreeProject{}
}

func projectNames(ps []TreeProject) []string {
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		names = append(names, p.Name)
	}
	return names
}

func TestBuildTreeSessionTiersAndProjectOrder(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	mk := func(id, proj string, createdAgo, updatedAgo time.Duration) schema.SessionMeta {
		return schema.SessionMeta{ID: id, CreatedAt: now.Add(-createdAgo), UpdatedAt: now.Add(-updatedAgo),
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/" + proj}}
	}
	metas := []schema.SessionMeta{
		mk("a-cur", "alpha", 2*time.Hour, 1*time.Hour),         // alpha: current
		mk("a-old", "alpha", 40*24*time.Hour, 30*24*time.Hour), // alpha: auto-archived
		mk("b-rec", "beta", 50*time.Hour, 48*time.Hour),        // beta: recent only
	}
	tree := BuildTreeAt(metas, nil, map[ArchiveKey]bool{}, now)

	// alpha has a current session and a >14d session -> alpha is an active project
	// with one Current and one Archived session.
	var alpha *TreeProject
	for i := range tree.Projects {
		if tree.Projects[i].Name == "alpha" {
			alpha = &tree.Projects[i]
		}
	}
	if alpha == nil {
		t.Fatalf("alpha should be an active project")
	}
	if len(alpha.Current) != 1 || len(alpha.Archived) != 1 || len(alpha.Recent) != 0 {
		t.Fatalf("alpha tiers: current=%d recent=%d archived=%d", len(alpha.Current), len(alpha.Recent), len(alpha.Archived))
	}
	// Projects ordered by most-recent session START desc: alpha's newest start is 2h ago,
	// beta's is 50h ago -> alpha first.
	if len(tree.Projects) < 2 || tree.Projects[0].Name != "alpha" {
		t.Fatalf("project order wrong: %+v", projectNames(tree.Projects))
	}
}

func TestBuildTreeArchivedProjectPlacement(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	mk := func(id, proj string, updatedAgo time.Duration) schema.SessionMeta {
		return schema.SessionMeta{ID: id, CreatedAt: now.Add(-updatedAgo), UpdatedAt: now.Add(-updatedAgo),
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/" + proj}}
	}
	// gamma: every session auto-archived -> gamma goes to ArchivedProjects.
	metas := []schema.SessionMeta{mk("g1", "gamma", 30*24*time.Hour)}
	tree := BuildTreeAt(metas, nil, map[ArchiveKey]bool{}, now)
	if len(tree.Projects) != 0 {
		t.Fatalf("gamma should not be active, got %v", projectNames(tree.Projects))
	}
	if len(tree.ArchivedProjects) != 1 || tree.ArchivedProjects[0].Name != "gamma" {
		t.Fatalf("gamma should be an archived project")
	}
	if !tree.ArchivedProjects[0].IsArchived {
		t.Fatalf("gamma should be flagged IsArchived")
	}

	// manual project archive forces delta to ArchivedProjects even though it's fresh.
	metas2 := []schema.SessionMeta{mk("d1", "delta", 1*time.Hour)}
	tree2 := BuildTreeAt(metas2, nil, map[ArchiveKey]bool{{Kind: "project", ID: "delta"}: true}, now)
	if len(tree2.Projects) != 0 || len(tree2.ArchivedProjects) != 1 {
		t.Fatalf("delta manual-archived should be in ArchivedProjects")
	}
}

func TestBuildTreeManualSessionArchiveAndUnarchive(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	mk := func(id string, updatedAgo time.Duration) schema.SessionMeta {
		return schema.SessionMeta{ID: id, CreatedAt: now.Add(-updatedAgo), UpdatedAt: now.Add(-updatedAgo),
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}}
	}
	metas := []schema.SessionMeta{
		mk("fresh", 1*time.Hour),     // current by age
		mk("stale", 30*24*time.Hour), // auto-archived by age
	}
	// Manual archive overrides the fresh session; manual unarchive rescues the stale one.
	decisions := map[ArchiveKey]bool{
		{Kind: "session", ID: "fresh"}: true,
		{Kind: "session", ID: "stale"}: false,
	}
	proj := projectByName(t, BuildTreeAt(metas, nil, decisions, now), "serf")
	if len(proj.Archived) != 1 || proj.Archived[0].ID != "fresh" {
		t.Fatalf("manual-archived fresh session should be Archived; archived=%v", proj.Archived)
	}
	// An unarchived stale session is visible (Recent), not Current — it is old.
	if len(proj.Recent) != 1 || proj.Recent[0].ID != "stale" {
		t.Fatalf("manual-unarchived stale session should be Recent; recent=%v", proj.Recent)
	}
}

func TestBuildTreeE2eProjectsClassifyByRecency(t *testing.T) {
	// The serf-e2e-* prefix bucket is gone: a fresh e2e project flows through the
	// normal model (active, ordered by recency), and a stale one auto-archives.
	now := time.Unix(1_700_000_000, 0)
	mk := func(id, proj string, updatedAgo time.Duration) schema.SessionMeta {
		return schema.SessionMeta{ID: id, CreatedAt: now.Add(-updatedAgo), UpdatedAt: now.Add(-updatedAgo),
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/tmp/" + proj}}
	}
	metas := []schema.SessionMeta{
		mk("e-fresh", "serf-e2e-fresh", 1*time.Hour),
		mk("e-stale", "serf-e2e-stale", 30*24*time.Hour),
	}
	tree := BuildTreeAt(metas, nil, map[ArchiveKey]bool{}, now)
	if got := projectNames(tree.Projects); len(got) != 1 || got[0] != "serf-e2e-fresh" {
		t.Fatalf("fresh e2e project should be active, got %v", got)
	}
	if got := projectNames(tree.ArchivedProjects); len(got) != 1 || got[0] != "serf-e2e-stale" {
		t.Fatalf("stale e2e project should be archived, got %v", got)
	}
}

func TestBuildTree_RollupMagnitudeCountsLiveAndAttention(t *testing.T) {
	// mockup #10 spine: a project header carries a *magnitude* rollup
	// (how many are live vs. how many need you), not a single ambiguous dot.
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01WORK1", UpdatedAt: now, OriginalPrompt: "work a",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
		{ID: "01WORK2", UpdatedAt: now.Add(-time.Minute), OriginalPrompt: "work b",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
		{ID: "01ASK", UpdatedAt: now.Add(-2 * time.Minute), OriginalPrompt: "blocked",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
		{ID: "01ZZZ", UpdatedAt: now.Add(-3 * time.Minute), OriginalPrompt: "idle one",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01WORK1", Status: appwire.ThreadStatusActive},
		{Entry: rendezvous.Entry{PID: 2}, SessionID: "01WORK2", Status: appwire.ThreadStatusActive},
		{Entry: rendezvous.Entry{PID: 3}, SessionID: "01ASK", Status: appwire.ThreadStatusAwaiting},
		{Entry: rendezvous.Entry{PID: 4}, SessionID: "01ZZZ", Status: appwire.ThreadStatusIdle},
	}
	proj := projectByName(t, buildTree(metas, live), "serf")
	// 2 working (active) sessions, 1 awaiting (needs-you). Idle does not count
	// toward either magnitude — it's the settled state.
	if proj.RollupLive != 2 {
		t.Errorf("RollupLive = %d, want 2", proj.RollupLive)
	}
	if proj.RollupAttn != 1 {
		t.Errorf("RollupAttn = %d, want 1", proj.RollupAttn)
	}
}

func TestBuildTree_NeedsYouAggregatesAwaitingAcrossProjects(t *testing.T) {
	// mockup #11 rec: a single "Needs you" tier that aggregates every awaiting
	// session across all projects, oldest-first, hidden when nothing awaits.
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01A_NEW", UpdatedAt: now.Add(-time.Minute), OriginalPrompt: "newer ask",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
		{ID: "01A_OLD", UpdatedAt: now.Add(-5 * time.Minute), OriginalPrompt: "older ask",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/prime-radiant"}},
		{ID: "01LIVE", UpdatedAt: now, OriginalPrompt: "just working",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01A_NEW", Status: appwire.ThreadStatusAwaiting},
		{Entry: rendezvous.Entry{PID: 2}, SessionID: "01A_OLD", Status: appwire.ThreadStatusAwaiting},
		{Entry: rendezvous.Entry{PID: 3}, SessionID: "01LIVE", Status: appwire.ThreadStatusActive},
	}
	tree := buildTree(metas, live)
	if len(tree.NeedsYou) != 2 {
		t.Fatalf("NeedsYou count = %d, want 2 (only awaiting sessions)", len(tree.NeedsYou))
	}
	// Oldest-blocked first so the triage queue works top-down.
	if tree.NeedsYou[0].ID != "01A_OLD" {
		t.Errorf("NeedsYou[0] = %q, want 01A_OLD (oldest awaiting first)", tree.NeedsYou[0].ID)
	}
	// Each needs-you node carries its project for the cross-project meta line.
	if tree.NeedsYou[0].Project != "prime-radiant" {
		t.Errorf("NeedsYou[0].Project = %q, want prime-radiant", tree.NeedsYou[0].Project)
	}
	// A working (non-awaiting) session never appears in the triage tier.
	for _, n := range tree.NeedsYou {
		if n.ID == "01LIVE" {
			t.Errorf("working session leaked into NeedsYou: %q", n.ID)
		}
	}
}

func TestBuildTree_NeedsYouEmptyWhenNothingAwaits(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01LIVE", UpdatedAt: now, OriginalPrompt: "working",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01LIVE", Status: appwire.ThreadStatusActive},
	}
	if got := len(buildTree(metas, live).NeedsYou); got != 0 {
		t.Errorf("NeedsYou should be empty when nothing awaits, got %d", got)
	}
}

func TestBuildTree_ClustersRepeatedIdleTitles(t *testing.T) {
	// mockup #10/#C rec: a run of same-titled idle sessions collapses to one
	// cluster row; live/needs-you sessions stay un-clustered so signal isn't
	// hidden behind a fold.
	now := time.Now()
	metas := []schema.SessionMeta{}
	for i := 0; i < 5; i++ {
		metas = append(metas, schema.SessionMeta{
			ID:        "01IMG" + string(rune('A'+i)),
			Name:      "describe this image",
			UpdatedAt: now.Add(-time.Duration(i) * time.Hour),
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/projects/serf-docs"},
		})
	}
	// A distinct singleton in the same project must not be swept into a cluster.
	metas = append(metas, schema.SessionMeta{
		ID: "01HAIKU", Name: "write a haiku", UpdatedAt: now.Add(-30 * time.Minute),
		EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf-docs"}})

	proj := projectByName(t, buildTree(metas, nil), "serf-docs")
	var clusters, singles int
	for _, s := range allSessions(proj) {
		if s.Kind == "cluster" {
			clusters++
			if s.ClusterCount != 5 {
				t.Errorf("cluster count = %d, want 5", s.ClusterCount)
			}
			if len(s.Children) != 5 {
				t.Errorf("cluster should hold its 5 members as children, got %d", len(s.Children))
			}
			if !strings.Contains(s.Title, "describe this image") {
				t.Errorf("cluster title = %q, want the shared title", s.Title)
			}
		} else {
			singles++
		}
	}
	if clusters != 1 {
		t.Errorf("clusters = %d, want 1", clusters)
	}
	if singles != 1 {
		t.Errorf("singleton sessions = %d, want 1 (the haiku)", singles)
	}
}

func TestBuildTree_DoesNotClusterLiveRepeatedTitles(t *testing.T) {
	// A live/needs-you member must keep all repeated-title sessions un-clustered
	// so live signal is never hidden behind a fold.
	now := time.Now()
	metas := []schema.SessionMeta{}
	for i := 0; i < 4; i++ {
		metas = append(metas, schema.SessionMeta{
			ID:        "01DUP" + string(rune('A'+i)),
			Name:      "describe this image",
			UpdatedAt: now.Add(-time.Duration(i) * time.Minute),
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/projects/serf-docs"},
		})
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01DUPA", Status: appwire.ThreadStatusActive},
	}
	proj := projectByName(t, buildTree(metas, live), "serf-docs")
	sessions := allSessions(proj)
	for _, s := range sessions {
		if s.Kind == "cluster" {
			t.Fatalf("repeated titles must NOT cluster when a member is live: got cluster %q", s.Title)
		}
	}
	if len(sessions) != 4 {
		t.Errorf("expected 4 un-clustered sessions, got %d", len(sessions))
	}
}

func TestBuildTree_ClampsSubagentsOfDeadParent(t *testing.T) {
	// A subagent that still reports "active" in the live map but whose parent
	// session has ended must not keep spinning ⟳ forever — its state is clamped
	// to "ended" so the dead session's children read as terminal.
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01DEADP", UpdatedAt: now.Add(-2 * time.Hour), OriginalPrompt: "parent",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
		{ID: "01STALESUB", UpdatedAt: now.Add(-2 * time.Hour), OriginalPrompt: "sub",
			IsSubagent: true, ParentSessionID: "01DEADP",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
	}
	// Parent is NOT live (ended); the subagent lingers as "active" in the map.
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 9}, SessionID: "01STALESUB", Status: appwire.ThreadStatusActive},
	}
	proj := projectByName(t, buildTree(metas, live), "serf")
	sessions := allSessions(proj)
	if len(sessions) != 1 || len(sessions[0].Children) != 1 {
		t.Fatalf("unexpected shape: %#v", sessions)
	}
	if got := sessions[0].Children[0].State; got != "ended" {
		t.Errorf("stale subagent state = %q, want ended (parent is dead)", got)
	}
}

func TestBuildTree_KeepsSubagentStateWhenParentLive(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01LIVEP", UpdatedAt: now, OriginalPrompt: "parent",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
		{ID: "01RUNSUB", UpdatedAt: now, OriginalPrompt: "sub",
			IsSubagent: true, ParentSessionID: "01LIVEP",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01LIVEP", Status: appwire.ThreadStatusActive},
		{Entry: rendezvous.Entry{PID: 2}, SessionID: "01RUNSUB", Status: appwire.ThreadStatusActive},
	}
	proj := projectByName(t, buildTree(metas, live), "serf")
	if got := allSessions(proj)[0].Children[0].State; got != "active" {
		t.Errorf("live subagent state = %q, want active (parent is live)", got)
	}
}

func TestBuildTree_OrdersProjectsByMostRecentStart(t *testing.T) {
	// Active projects are emitted newest-first by their most-recent session START
	// (max CreatedAt across the project's sessions).
	now := time.Unix(1_700_000_000, 0)
	mk := func(id, proj string, startedAgo time.Duration) schema.SessionMeta {
		return schema.SessionMeta{ID: id, CreatedAt: now.Add(-startedAgo), UpdatedAt: now.Add(-startedAgo),
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/" + proj}}
	}
	metas := []schema.SessionMeta{
		mk("01R2", "started-older", 2*time.Hour),
		mk("01R1", "started-newer", 10*time.Minute),
		// started-older also has an even-newer session, which should lift it above
		// started-newer because most-recent START wins.
		mk("01R3", "started-older", 1*time.Minute),
	}
	tree := BuildTreeAt(metas, nil, map[ArchiveKey]bool{}, now)
	got := projectNames(tree.Projects)
	want := []string{"started-older", "started-newer"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("project order = %v, want %v", got, want)
	}
}

func TestBuildTree_ExpandedOnlyForLiveProjects(t *testing.T) {
	// A project auto-expands only when it has a live (working) or awaiting
	// (needs-you) session — RollupLive > 0 || RollupAttn > 0. Idle-only and
	// archived projects stay collapsed.
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01LIVE", UpdatedAt: now, OriginalPrompt: "working",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/live-proj"}},
		{ID: "01IDLE", UpdatedAt: now, OriginalPrompt: "idle",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/idle-proj"}},
		{ID: "01OLD", UpdatedAt: now.Add(-30 * 24 * time.Hour), OriginalPrompt: "stale",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/archived-proj"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01LIVE", Status: appwire.ThreadStatusActive},
		{Entry: rendezvous.Entry{PID: 2}, SessionID: "01IDLE", Status: appwire.ThreadStatusIdle},
	}
	tree := buildTree(metas, live)

	liveProj := projectByName(t, tree, "live-proj")
	if !liveProj.Expanded {
		t.Errorf("live-proj should be Expanded (RollupLive=%d RollupAttn=%d)", liveProj.RollupLive, liveProj.RollupAttn)
	}
	idleProj := projectByName(t, tree, "idle-proj")
	if idleProj.Expanded {
		t.Errorf("idle-proj should NOT be Expanded (RollupLive=%d RollupAttn=%d)", idleProj.RollupLive, idleProj.RollupAttn)
	}
	var archivedProj *TreeProject
	for i := range tree.ArchivedProjects {
		if tree.ArchivedProjects[i].Name == "archived-proj" {
			archivedProj = &tree.ArchivedProjects[i]
		}
	}
	if archivedProj == nil {
		t.Fatalf("archived-proj should be an archived project")
	}
	if archivedProj.Expanded {
		t.Errorf("archived-proj should NOT be Expanded")
	}
}

func TestBuildTree_ExpandedForAwaitingProject(t *testing.T) {
	// An awaiting (needs-you) session counts toward RollupAttn, which auto-expands.
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01ASK", UpdatedAt: now, OriginalPrompt: "blocked",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/asking"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01ASK", Status: appwire.ThreadStatusAwaiting},
	}
	proj := projectByName(t, buildTree(metas, live), "asking")
	if !proj.Expanded {
		t.Errorf("awaiting project should be Expanded (RollupAttn=%d)", proj.RollupAttn)
	}
}

func TestBuildTree_CapsSessionsPerTierWithOverflowCounts(t *testing.T) {
	// Each tier is capped at maxSidebarSessionsPerTier; the overflow count is
	// recorded so the sidebar can show a "+N older" note instead of every row.
	now := time.Unix(1_700_000_000, 0)
	total := maxSidebarSessionsPerTier + 7
	metas := make([]schema.SessionMeta, 0, total)
	for i := 0; i < total; i++ {
		// Spread updated times so they don't cluster-fold (distinct titles too).
		metas = append(metas, schema.SessionMeta{
			ID:             fmt.Sprintf("01CUR%03d", i),
			OriginalPrompt: fmt.Sprintf("current task %d", i),
			CreatedAt:      now.Add(-time.Duration(i) * time.Minute),
			UpdatedAt:      now.Add(-time.Duration(i) * time.Minute),
			EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/projects/big"},
		})
	}
	proj := projectByName(t, BuildTreeAt(metas, nil, map[ArchiveKey]bool{}, now), "big")
	if len(proj.Current) != maxSidebarSessionsPerTier {
		t.Errorf("Current capped len = %d, want %d", len(proj.Current), maxSidebarSessionsPerTier)
	}
	if proj.MoreCurrent != 7 {
		t.Errorf("MoreCurrent = %d, want 7", proj.MoreCurrent)
	}
	// The kept rows are the most-recent N: the very first (newest) survives.
	if proj.Current[0].ID != "01CUR000" {
		t.Errorf("Current[0] = %q, want most-recent 01CUR000", proj.Current[0].ID)
	}
}

func TestBuildTree_NoOverflowWhenUnderCap(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	metas := []schema.SessionMeta{
		{ID: "01A", OriginalPrompt: "a", CreatedAt: now, UpdatedAt: now,
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/small"}},
	}
	proj := projectByName(t, BuildTreeAt(metas, nil, map[ArchiveKey]bool{}, now), "small")
	if proj.MoreCurrent != 0 || proj.MoreRecent != 0 || proj.MoreArchived != 0 {
		t.Errorf("under-cap project should have zero overflow: more=%d/%d/%d",
			proj.MoreCurrent, proj.MoreRecent, proj.MoreArchived)
	}
}

func TestBuildProjectTree_ReturnsSingleProjectTiers(t *testing.T) {
	// The lazy-load helper rebuilds one named project's tiers from the full meta
	// set, ignoring sessions from other projects.
	now := time.Unix(1_700_000_000, 0)
	mk := func(id, proj string, updatedAgo time.Duration) schema.SessionMeta {
		return schema.SessionMeta{ID: id, OriginalPrompt: id,
			CreatedAt: now.Add(-updatedAgo), UpdatedAt: now.Add(-updatedAgo),
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/" + proj}}
	}
	metas := []schema.SessionMeta{
		mk("wanted-cur", "wanted", 1*time.Hour),
		mk("wanted-rec", "wanted", 48*time.Hour),
		mk("other-cur", "other", 1*time.Hour),
	}
	proj, ok := BuildProjectTreeAt(metas, nil, map[ArchiveKey]bool{}, now, "wanted")
	if !ok {
		t.Fatalf("BuildProjectTreeAt should find project 'wanted'")
	}
	if proj.Name != "wanted" {
		t.Fatalf("got project %q, want 'wanted'", proj.Name)
	}
	if len(proj.Current) != 1 || proj.Current[0].ID != "wanted-cur" {
		t.Errorf("wanted Current = %v, want [wanted-cur]", proj.Current)
	}
	if len(proj.Recent) != 1 || proj.Recent[0].ID != "wanted-rec" {
		t.Errorf("wanted Recent = %v, want [wanted-rec]", proj.Recent)
	}
	// No leakage from "other".
	for _, n := range append(append([]TreeNode{}, proj.Current...), proj.Recent...) {
		if n.Project == "other" {
			t.Errorf("project tree leaked a session from 'other': %+v", n)
		}
	}
}

func TestBuildProjectTree_FindsArchivedProject(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	metas := []schema.SessionMeta{
		{ID: "g1", OriginalPrompt: "g1", CreatedAt: now.Add(-30 * 24 * time.Hour),
			UpdatedAt: now.Add(-30 * 24 * time.Hour),
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/projects/gamma"}},
	}
	proj, ok := BuildProjectTreeAt(metas, nil, map[ArchiveKey]bool{}, now, "gamma")
	if !ok {
		t.Fatalf("BuildProjectTreeAt should find archived project 'gamma'")
	}
	if len(proj.Archived) != 1 || proj.Archived[0].ID != "g1" {
		t.Errorf("gamma Archived = %v, want [g1]", proj.Archived)
	}
}

func TestBuildProjectTree_UnknownProjectReturnsFalse(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	metas := []schema.SessionMeta{
		{ID: "a", OriginalPrompt: "a", CreatedAt: now, UpdatedAt: now,
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/known"}},
	}
	if _, ok := BuildProjectTreeAt(metas, nil, map[ArchiveKey]bool{}, now, "nope"); ok {
		t.Errorf("BuildProjectTreeAt should return ok=false for unknown project")
	}
}

func TestClassifySession(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	yes, no := true, false
	cases := []struct {
		name     string
		decision *bool
		age      time.Duration
		want     string
	}{
		{"fresh -> current", nil, 1 * time.Hour, "current"},
		{"yesterday -> recent", nil, 36 * time.Hour, "recent"},
		{"3 weeks -> archived (auto)", nil, 21 * 24 * time.Hour, "archived"},
		{"manual archive overrides fresh", &yes, 1 * time.Hour, "archived"},
		{"manual unarchive overrides old", &no, 30 * 24 * time.Hour, "recent"},
		{"boundary 24h -> current", nil, 24 * time.Hour, "current"},
		{"boundary 14d -> recent", nil, 14 * 24 * time.Hour, "recent"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifySession(c.decision, now.Add(-c.age), now)
			if got != c.want {
				t.Fatalf("classifySession=%q want %q", got, c.want)
			}
		})
	}
}
