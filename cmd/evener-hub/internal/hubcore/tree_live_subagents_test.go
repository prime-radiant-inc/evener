package hubcore

import (
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

// The Live tier used to build flat, parentless TreeNode values: a live
// subagent appeared as its own top-level row instead of nesting under the
// parent that spawned it, and its active state never colored the row the way
// every other section's rows do. These tests pin the fix: the Live tier now
// builds via buildNode (the same path the Projects tier uses), so subagent
// children nest under their live parent with the same foldout and the same
// active-status color as every other section.

func TestBuildTreeLiveNestsActiveSubagentUnderParent(t *testing.T) {
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

	// The parent is the one top-level Live row; the subagent nests under it.
	if len(tree.Live) != 1 {
		t.Fatalf("Live tier has %d rows, want 1 (the parent)", len(tree.Live))
	}
	parent := tree.Live[0]
	if parent.ID != "parent" {
		t.Fatalf("Live[0].ID = %q, want parent", parent.ID)
	}
	if len(parent.Children) != 1 {
		t.Fatalf("parent has %d children, want 1 (the live subagent)", len(parent.Children))
	}
	child := parent.Children[0]
	if child.ID != "child" {
		t.Fatalf("child.ID = %q, want child", child.ID)
	}
	if child.Kind != "subagent" {
		t.Errorf("child.Kind = %q, want subagent", child.Kind)
	}
	// The active subagent carries its own daemon-reported state, which the
	// frontend's cadenceStateFor maps to the working/active color family —
	// the same color every other section uses for an active subagent.
	if child.State != "active" {
		t.Errorf("child.State = %q, want active (the working/active color)", child.State)
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

func TestBuildTreeLiveKeepsOrphanedSubagentTopLevel(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "parent", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "orphan", CreatedAt: now, UpdatedAt: now, ParentSessionID: "parent", IsSubagent: true, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	}
	// Only the subagent is live; the parent is not. The subagent has no live
	// row to nest under, so it keeps its own top-level Live row rather than
	// vanishing from the rail.
	live := []LiveEntry{
		{PID: 1, SessionID: "orphan", Status: appwire.ThreadStatusActive},
	}

	tree := BuildTreeAt(metas, live, nil, now)

	if len(tree.Live) != 1 {
		t.Fatalf("Live tier has %d rows, want 1 (the orphaned subagent)", len(tree.Live))
	}
	if tree.Live[0].ID != "orphan" {
		t.Fatalf("Live[0].ID = %q, want orphan", tree.Live[0].ID)
	}
}

func TestBuildTreeLivePrunesNonLiveChildren(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "parent", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "livechild", CreatedAt: now, UpdatedAt: now, ParentSessionID: "parent", IsSubagent: true, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "deadchild", CreatedAt: now, UpdatedAt: now, ParentSessionID: "parent", IsSubagent: true, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	}
	// Only livechild and the parent are live. deadchild has no live entry and
	// is not listed in RunningSubagentIDs, so it is neither running nor
	// resumable — it must be pruned from the Live subtree.
	live := []LiveEntry{
		{PID: 1, SessionID: "parent", Status: appwire.ThreadStatusIdle, RunningSubagentIDs: []string{"livechild"}},
		{PID: 2, SessionID: "livechild", Status: appwire.ThreadStatusActive},
	}

	tree := BuildTreeAt(metas, live, nil, now)

	if len(tree.Live) != 1 {
		t.Fatalf("Live tier has %d rows, want 1 (the parent)", len(tree.Live))
	}
	parent := tree.Live[0]
	if len(parent.Children) != 1 {
		t.Fatalf("parent has %d children, want 1 (only the live subagent)", len(parent.Children))
	}
	if parent.Children[0].ID != "livechild" {
		t.Fatalf("child = %q, want livechild", parent.Children[0].ID)
	}
	// deadchild is not live and must not appear anywhere in the Live subtree.
	for _, child := range parent.Children {
		if child.ID == "deadchild" {
			t.Error("non-live subagent deadchild appeared in the Live tier subtree")
		}
	}
}

func TestBuildTreeLiveSubagentStateMatchesProjectRow(t *testing.T) {
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

	// The Live-tier subagent child and the Projects-tier subagent child must
	// report the same state — both come from the same stateFor closure, so
	// the active color cannot disagree between sections.
	liveRow, inLive, projectRow, inProject := liveAndProjectRowsFor(tree, "parent")
	if !inLive || !inProject {
		t.Fatalf("parent missing: live=%v project=%v", inLive, inProject)
	}
	if len(liveRow.Children) != 1 || len(projectRow.Children) != 1 {
		t.Fatalf("children: live=%d project=%d, want 1 each", len(liveRow.Children), len(projectRow.Children))
	}
	liveChild := liveRow.Children[0]
	projectChild := projectRow.Children[0]
	if liveChild.State != projectChild.State {
		t.Errorf("subagent state disagrees: live=%q project=%q", liveChild.State, projectChild.State)
	}
	if liveChild.State != "active" {
		t.Errorf("subagent state = %q, want active", liveChild.State)
	}
}

// A crashed daemon stays in the roster for the crash-retention window carrying
// the in-process children it last reported, and Roster.List hands those records
// to the tree unfiltered. Nothing is running those delegates, so the sidebar
// must say so — the same answer Roster.SubagentState now gives the thread read
// and workspace projections. The parent's own crash marker is untouched.
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
	r.procAlive = func(int) bool { return true }
	r.Refresh()

	metas := []schema.SessionMeta{
		{ID: "01PARENT", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "01CHILD", CreatedAt: now, UpdatedAt: now, ParentSessionID: "01PARENT", IsSubagent: true, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	}
	if got := treeNodeState(t, BuildTreeAt(metas, r.List(), nil, now), "01CHILD"); got != "active" {
		t.Fatalf("child state while the parent daemon is alive = %q, want active", got)
	}

	// kill -9 the parent: its probe fails and the process is confirmed gone.
	prober.result = ProbeResult{}
	r.procAlive = func(int) bool { return false }
	r.Refresh()
	parent, ok := r.Find("01PARENT")
	if !ok || !parent.Crashed || !slices.Contains(parent.RunningSubagentIDs, "01CHILD") {
		t.Fatalf("parent entry=%+v ok=%v, want a retained crashed record still listing the child", parent, ok)
	}

	tree := BuildTreeAt(metas, r.List(), nil, now)
	if got := treeNodeState(t, tree, "01CHILD"); got != "ended" {
		t.Fatalf("child state after the parent crashed = %q, want ended", got)
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
