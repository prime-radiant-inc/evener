package hubcore

import (
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

	tree := BuildTree(metas, live)

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
	if len(proj.Sessions) != 2 {
		t.Fatalf("sessions: %d", len(proj.Sessions))
	}
	if proj.Sessions[0].ID != "01ACTIVE" {
		t.Errorf("[0]: %q", proj.Sessions[0].ID)
	}
	if proj.Sessions[1].ID != "01OTHER" {
		t.Errorf("[1]: %q", proj.Sessions[1].ID)
	}

	// Children of 01ACTIVE: subagent first, then the snapshotted original (fork).
	children := proj.Sessions[0].Children
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
	if len(proj.Sessions[1].Children) != 0 {
		t.Errorf("01OTHER should have no children, got %d", len(proj.Sessions[1].Children))
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
	tree := BuildTree([]schema.SessionMeta{{
		ID:             "01NAMED",
		Name:           "Launch Config Cheap Model",
		OriginalPrompt: "unrelated original prompt",
		UpdatedAt:      time.Now(),
	}}, nil)
	if len(tree.Projects) != 1 || len(tree.Projects[0].Sessions) != 1 {
		t.Fatalf("unexpected tree shape: %#v", tree)
	}
	if got := tree.Projects[0].Sessions[0].Title; got != "Launch Config Cheap Model" {
		t.Fatalf("session title = %q, want generated name", got)
	}
}

func TestBuildTree_UsesGeneratedNameForForkBaseTitle(t *testing.T) {
	tree := BuildTree([]schema.SessionMeta{{
		ID:             "01FORK",
		Name:           "Generated Base",
		OriginalPrompt: "original base",
		ForkLabel:      "before TDD",
		UpdatedAt:      time.Now(),
	}}, nil)
	if len(tree.Projects) != 1 || len(tree.Projects[0].Sessions) != 1 {
		t.Fatalf("unexpected tree shape: %#v", tree)
	}
	if got := tree.Projects[0].Sessions[0].Title; got != "Generated Base · before TDD" {
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

	tree := BuildTree(metas, live)

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

	tree := BuildTree(metas, nil)
	if len(tree.Projects) != 1 {
		t.Fatalf("projects=%d", len(tree.Projects))
	}
	got := make([]string, 0, len(tree.Projects[0].Sessions))
	for _, node := range tree.Projects[0].Sessions {
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

	tree := BuildTree(nil, live)
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

	tree := BuildTree(metas, live)
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

	tree := BuildTree(metas, live)

	if len(tree.Projects) != 1 {
		t.Fatalf("projects: %d", len(tree.Projects))
	}
	if tree.Projects[0].Name != "(no project)" {
		t.Errorf("project name: %q", tree.Projects[0].Name)
	}
	if len(tree.Projects[0].Sessions) != 1 {
		t.Fatalf("sessions: %d", len(tree.Projects[0].Sessions))
	}
	if tree.Projects[0].Sessions[0].ID != "01NOPROJ" {
		t.Errorf("session id: %q", tree.Projects[0].Sessions[0].ID)
	}
	// Not live => state = "ended"
	if tree.Projects[0].Sessions[0].State != "ended" {
		t.Errorf("state: %q", tree.Projects[0].Sessions[0].State)
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
