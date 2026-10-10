package hubcore

import (
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

// Live rows are roots. A live subagent is never a Live row, and its parent's
// row carries no child for it.
func TestBuildTreeLiveHasNoRowForSubagent(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "parent", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "child", CreatedAt: now, UpdatedAt: now, ParentSessionID: "parent", IsSubagent: true, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	}
	live := []LiveEntry{
		{PID: 1, SessionID: "parent", Status: appwire.ThreadStatusIdle, RunningSubagentIDs: []string{"child"}},
		{PID: 2, SessionID: "child", Status: appwire.ThreadStatusActive},
	}

	tree := BuildTreeAt(metas, live, nil, now)

	if len(tree.Live) != 1 || tree.Live[0].ID != "parent" || len(tree.Live[0].Children) != 0 {
		t.Fatalf("Live = %+v, want the parent alone with no children", tree.Live)
	}
}

func TestBuildTreeCarriesSessionJobsForNavigation(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{{ID: "parent", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}}}
	live := []LiveEntry{{
		PID: 1, SessionID: "parent", Status: appwire.ThreadStatusIdle,
		RunningJobs:   []appwire.EvenerJobInfo{{JobID: "job-running", JobType: "shell", Status: "running"}},
		CompletedJobs: []appwire.EvenerJobInfo{{JobID: "job-completed", JobType: "shell", Status: "completed"}},
	}}

	tree := BuildTreeAt(metas, live, nil, now)
	if len(tree.Live) != 1 {
		t.Fatalf("Live tier has %d rows, want one parent", len(tree.Live))
	}
	parent := tree.Live[0]
	if len(parent.RunningJobs) != 1 || parent.RunningJobs[0].JobID != "job-running" {
		t.Fatalf("running jobs = %+v", parent.RunningJobs)
	}
	if len(parent.CompletedJobs) != 1 || parent.CompletedJobs[0].JobID != "job-completed" {
		t.Fatalf("completed jobs = %+v", parent.CompletedJobs)
	}
	if len(tree.Projects) != 1 || !tree.Projects[0].Expanded || tree.Projects[0].RollupLive != 1 {
		t.Fatalf("project job rollup = %+v, want expanded with one live task", tree.Projects)
	}
}

// The daemon's watch inventory rides the live entry onto the tree node, per
// session. Each session's row carries only its own daemon's rows, so a
// receiver watch that both sessions can see is never copied across and a
// rollup cannot double count it.
func TestBuildTreeCarriesSessionWatchesForNavigation(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "parent", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "sibling", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "child", CreatedAt: now, UpdatedAt: now, ParentSessionID: "parent", IsSubagent: true, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	}
	live := []LiveEntry{
		{PID: 1, SessionID: "parent", Status: appwire.ThreadStatusIdle,
			Watches:            []appwire.EvenerWatchInfo{{ID: "watch-parent", Source: "timer", Note: "owner"}},
			RunningSubagentIDs: []string{"child"},
			// The child has no LiveEntry of its own; its watches ride the parent's
			// ChildWatches, the way the prober now reports them.
			ChildWatches: map[string][]appwire.EvenerWatchInfo{
				"child": {{ID: "watch-child", Source: "self", Note: "child-own"}},
			}},
		{PID: 2, SessionID: "sibling", Status: appwire.ThreadStatusIdle,
			Watches: []appwire.EvenerWatchInfo{{ID: "watch-sibling", Source: "output", Note: "receiver"}}},
	}

	tree := BuildTreeAt(metas, live, nil, now)
	parentRow, inLive, parentProject, inProject := liveAndProjectRowsFor(tree, "parent")
	if !inLive || !inProject {
		t.Fatalf("parent missing: live=%v project=%v", inLive, inProject)
	}
	if len(parentRow.Watches) != 1 || parentRow.Watches[0].ID != "watch-parent" {
		t.Fatalf("parent live watches = %+v, want only watch-parent", parentRow.Watches)
	}
	if len(parentProject.Watches) != 1 || parentProject.Watches[0].ID != "watch-parent" {
		t.Fatalf("parent project watches = %+v, want only watch-parent", parentProject.Watches)
	}
	siblingRow, inLive, _, _ := liveAndProjectRowsFor(tree, "sibling")
	if !inLive {
		t.Fatal("sibling missing from the Live tier")
	}
	if len(siblingRow.Watches) != 1 || siblingRow.Watches[0].ID != "watch-sibling" {
		t.Fatalf("sibling watches = %+v, want only watch-sibling", siblingRow.Watches)
	}
	for _, watch := range siblingRow.Watches {
		if watch.ID == "watch-parent" {
			t.Fatalf("sibling carries the parent's watch: %+v", siblingRow.Watches)
		}
	}
}

// An older daemon omits Watches entirely; the diagnostics helper must treat
// that (and a nil probe) as an empty list rather than an error.
func TestDiagnosticsWatchesAbsentYieldsEmptyList(t *testing.T) {
	if got := diagnosticsWatches(nil); len(got) != 0 {
		t.Fatalf("diagnosticsWatches(nil) = %+v, want empty", got)
	}
	if got := diagnosticsWatches(&appwire.EvenerDiagnostics{}); len(got) != 0 {
		t.Fatalf("diagnosticsWatches(without Watches) = %+v, want empty", got)
	}
}

func TestBuildTreeNeedsYouCarriesSessionJobs(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{{ID: "parent", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}}}
	tree := BuildTreeAt(metas, []LiveEntry{{
		PID: 1, SessionID: "parent", Status: appwire.ThreadStatusAwaiting,
		RunningJobs: []appwire.EvenerJobInfo{{JobID: "job-running", JobType: "shell", Status: "running"}},
	}}, nil, now)
	if len(tree.NeedsYou) != 1 || len(tree.NeedsYou[0].RunningJobs) != 1 || tree.NeedsYou[0].RunningJobs[0].JobID != "job-running" {
		t.Fatalf("needs-you job projection = %+v, want active job", tree.NeedsYou)
	}
}

func TestBuildTreeLiveExcludesSubagentWhoseParentIsLive(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "parent", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "child", CreatedAt: now, UpdatedAt: now, ParentSessionID: "parent", IsSubagent: true, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	}
	live := []LiveEntry{
		{PID: 1, SessionID: "parent", Status: appwire.ThreadStatusActive, RunningSubagentIDs: []string{"child"}},
		{PID: 2, SessionID: "child", Status: appwire.ThreadStatusActive},
	}

	tree := BuildTreeAt(metas, live, nil, now)

	// The subagent must NOT appear as a separate top-level Live row — it
	// nests under its live parent. Only the parent is top-level.
	ids := make(map[string]bool, len(tree.Live))
	for _, node := range tree.Live {
		ids[node.ID] = true
	}
	if ids["child"] {
		t.Error("live subagent child appeared as a top-level Live row; it must nest under its parent")
	}
	if !ids["parent"] {
		t.Error("live parent missing from the Live tier")
	}
}

func TestBuildTreeLivePrunesNonLiveForkOriginals(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	dir := schema.EnvironmentInfo{WorkingDir: "/projects/evener"}
	metas := []schema.SessionMeta{
		{ID: "liveorig", ForkLabel: "before edit", CreatedAt: now, UpdatedAt: now, EnvInfo: dir},
		{ID: "livecont", ParentSessionID: "liveorig", CreatedAt: now, UpdatedAt: now, EnvInfo: dir},
		{ID: "deadorig", ForkLabel: "before edit", CreatedAt: now, UpdatedAt: now, EnvInfo: dir},
		{ID: "deadcont", ParentSessionID: "deadorig", CreatedAt: now, UpdatedAt: now, EnvInfo: dir},
	}
	// Only livecont and its original are live, so the original stays under the
	// Live row. deadcont is live with a non-live original, which the Live tier
	// prunes; the project row keeps it.
	live := []LiveEntry{
		{PID: 1, SessionID: "livecont", Status: appwire.ThreadStatusIdle},
		{PID: 2, SessionID: "liveorig", Status: appwire.ThreadStatusIdle},
		{PID: 3, SessionID: "deadcont", Status: appwire.ThreadStatusIdle},
	}

	tree := BuildTreeAt(metas, live, nil, now)

	rows := map[string]TreeNode{}
	for _, row := range tree.Live {
		rows[row.ID] = row
	}
	if len(rows) != 2 {
		t.Fatalf("Live rows = %v, want the two continuations", rows)
	}
	if children := rows["livecont"].Children; len(children) != 1 || children[0].ID != "liveorig" {
		t.Fatalf("livecont children = %+v, want its live original", children)
	}
	if children := rows["deadcont"].Children; len(children) != 0 {
		t.Fatalf("deadcont children = %+v, want the non-live original pruned", children)
	}
}

// A crashed daemon stays in the roster for the crash-retention window carrying
// the in-process children it last reported, and Roster.List hands those records
// to the tree unfiltered. Nothing is running those delegates, so the project
// stops counting them as working: the same answer Roster.SubagentState gives
// the thread read and workspace projections. The parent's own crash marker is
// untouched.
func TestBuildTreeCrashedParentDoesNotShowItsSubagentRunning(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{
		PID:       1001,
		Address:   "127.0.0.1:50001",
		SessionID: "01PARENT",
		StartedAt: time.Now().UTC(), // fresh: within the crash-retention window
	})
	prober := &runningSubagentProber{result: ProbeResult{
		SessionID:             "01PARENT",
		Status:                appwire.ThreadStatusIdle,
		RunningSubagentIDs:    []string{"01CHILD"},
		RunningSubagentStates: map[string]string{"01CHILD": appwire.ThreadStatusActive},
		OK:                    true,
	}}
	r := NewRoster(dir, prober)
	r.SetProcessAlive(func(int) bool { return true })
	r.Refresh()

	metas := []schema.SessionMeta{
		{ID: "01PARENT", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "01CHILD", CreatedAt: now, UpdatedAt: now, ParentSessionID: "01PARENT", IsSubagent: true, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	}
	if project := BuildTreeAt(metas, r.List(), nil, now).Projects[0]; project.RollupLive != 1 {
		t.Fatalf("project working count while the parent daemon is alive = %d, want 1", project.RollupLive)
	}

	// kill -9 the parent: its probe fails and the process is confirmed gone.
	prober.result = ProbeResult{}
	r.SetProcessAlive(func(int) bool { return false })
	r.Refresh()
	parent, ok := r.Find("01PARENT")
	if !ok || !parent.Crashed || !slices.Contains(parent.RunningSubagentIDs, "01CHILD") {
		t.Fatalf("parent entry=%+v ok=%v, want a retained crashed record still listing the child", parent, ok)
	}

	tree := BuildTreeAt(metas, r.List(), nil, now)
	if project := tree.Projects[0]; project.RollupLive != 0 {
		t.Fatalf("project working count after the parent crashed = %d, want 0", project.RollupLive)
	}
	if got := treeNodeState(t, tree, "01PARENT"); got != "errored" {
		t.Fatalf("crashed parent state = %q, want errored", got)
	}
}

// treeNodeState finds one session's row anywhere in a tree and returns its
// display state. Which tier a row lands in is not what these tests are about.
func treeNodeState(t *testing.T, tree Tree, id string) string {
	t.Helper()
	var found *TreeNode
	var walk func(nodes []TreeNode)
	walk = func(nodes []TreeNode) {
		for i := range nodes {
			if nodes[i].ID == id && found == nil {
				found = &nodes[i]
			}
			walk(nodes[i].Children)
		}
	}
	walk(tree.NeedsYou)
	walk(tree.Live)
	for _, project := range slices.Concat(tree.Projects, tree.ArchivedProjects) {
		walk(project.Current)
		walk(project.Recent)
		walk(project.Archived)
	}
	if found == nil {
		t.Fatalf("session %s has no row anywhere in the tree", id)
	}
	return found.State
}
