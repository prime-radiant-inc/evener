package hubcore

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
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
