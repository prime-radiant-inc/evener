package hubcore

import (
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// Navigation lists top-level sessions only. Subagents never get a row in any
// section, whether a persisted meta, a live in-process child or an orphan.
func TestBuildTreeRootsOnlyHasNoSubagentRows(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	dir := schema.EnvironmentInfo{WorkingDir: "/projects/evener"}
	metas := []schema.SessionMeta{
		{ID: "root", CreatedAt: now, UpdatedAt: now, EnvInfo: dir},
		{ID: "child", ParentSessionID: "root", IsSubagent: true, CreatedAt: now, UpdatedAt: now, EnvInfo: dir},
		{ID: "grandchild", ParentSessionID: "child", IsSubagent: true, CreatedAt: now, UpdatedAt: now, EnvInfo: dir},
		{ID: "orphan", ParentSessionID: "gone", IsSubagent: true, CreatedAt: now, UpdatedAt: now, EnvInfo: dir},
		{ID: "parentless", IsSubagent: true, CreatedAt: now, UpdatedAt: now, EnvInfo: dir},
	}
	live := []LiveEntry{
		{PID: 1, SessionID: "root", Status: appwire.ThreadStatusIdle, RunningSubagentIDs: []string{"child", "unindexed"}},
		{PID: 2, SessionID: "child", Status: appwire.ThreadStatusAwaiting, RunningSubagentIDs: []string{"grandchild"}},
		{PID: 3, SessionID: "orphan", Status: appwire.ThreadStatusAwaiting},
		{PID: 4, SessionID: "parentless", Status: appwire.ThreadStatusSystemError},
		// A live child the past index has not caught up with: no meta at all,
		// named only by its parent's running list.
		{PID: 5, SessionID: "unindexed", Status: appwire.ThreadStatusAwaiting, WorkingDir: "/projects/evener"},
	}
	subagents := []string{"child", "grandchild", "orphan", "parentless", "unindexed"}

	tree := BuildTreeAt(metas, live, nil, now)

	var ids []string
	var walk func(rows []TreeNode)
	walk = func(rows []TreeNode) {
		for _, row := range rows {
			ids = append(ids, row.ID)
			if row.Kind == "subagent" {
				t.Errorf("row %s has Kind subagent", row.ID)
			}
			walk(row.Children)
		}
	}
	walk(tree.NeedsYou)
	walk(tree.Live)
	for _, project := range slices.Concat(tree.Projects, tree.ArchivedProjects) {
		walk(project.Current)
		walk(project.Recent)
		walk(project.Archived)
	}
	for _, id := range subagents {
		if slices.Contains(ids, id) {
			t.Errorf("subagent %s has a navigation row", id)
		}
	}
	if !slices.Contains(ids, "root") {
		t.Fatalf("root has no row; rows = %v", ids)
	}
	if len(tree.NeedsYou) != 0 {
		t.Errorf("needs-you = %+v, want none: only a top-level session can need the user", tree.NeedsYou)
	}
	if len(tree.Live) != 1 || tree.Live[0].ID != "root" {
		t.Errorf("live = %+v, want only root", tree.Live)
	}
	for _, project := range tree.Projects {
		for _, row := range project.Current {
			if len(row.Children) != 0 {
				t.Errorf("row %s has children %+v, want fork originals only", row.ID, row.Children)
			}
		}
	}
}

// A subagent whose root is gone or unknown vanishes: it neither gets its own
// Live row nor a project.
func TestBuildTreeOrphanSubagentVanishes(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "orphan", ParentSessionID: "gone", IsSubagent: true, CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	}
	live := []LiveEntry{{PID: 1, SessionID: "orphan", Status: appwire.ThreadStatusActive}}

	tree := BuildTreeAt(metas, live, nil, now)

	if len(tree.Live) != 0 || len(tree.NeedsYou) != 0 || len(tree.Projects) != 0 || len(tree.ArchivedProjects) != 0 {
		t.Fatalf("orphan left rows behind: live=%+v needs=%+v projects=%+v archived=%+v", tree.Live, tree.NeedsYou, tree.Projects, tree.ArchivedProjects)
	}
}

// Fork originals still nest under their continuation, and stay the only
// children a row can carry.
func TestBuildTreeRootsOnlyKeepsForkOriginalsAsChildren(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	dir := schema.EnvironmentInfo{WorkingDir: "/projects/evener"}
	metas := []schema.SessionMeta{
		{ID: "orig", ForkLabel: "before edit", CreatedAt: now, UpdatedAt: now, EnvInfo: dir},
		{ID: "cont", ParentSessionID: "orig", CreatedAt: now, UpdatedAt: now, EnvInfo: dir},
		{ID: "child", ParentSessionID: "orig", IsSubagent: true, CreatedAt: now, UpdatedAt: now, EnvInfo: dir},
	}

	tree := BuildTreeAt(metas, nil, nil, now)

	if len(tree.Projects) != 1 || len(tree.Projects[0].Current) != 1 {
		t.Fatalf("tree = %+v, want one current row", tree.Projects)
	}
	cont := tree.Projects[0].Current[0]
	if cont.ID != "cont" || len(cont.Children) != 1 || cont.Children[0].ID != "orig" || cont.Children[0].Kind != "fork" {
		t.Fatalf("continuation = %+v, want the fork original as its only child", cont)
	}
	if len(cont.Children[0].Children) != 0 {
		t.Fatalf("fork original children = %+v, want none", cont.Children[0].Children)
	}
}

// A subagent's live attention never reaches the needs-you tier or the
// attention summary, even when no meta marks it a subagent.
func TestBuildTreeNeedsYouExcludesLiveSubagent(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{{ID: "root", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}}}
	live := []LiveEntry{
		{PID: 1, SessionID: "root", Status: appwire.ThreadStatusIdle, RunningSubagentIDs: []string{"child"}},
		{PID: 2, SessionID: "child", Status: appwire.ThreadStatusAwaiting, PendingAsk: true},
	}

	tree := BuildTreeAt(metas, live, nil, now)
	if len(tree.NeedsYou) != 0 {
		t.Fatalf("needs-you = %+v, want none", tree.NeedsYou)
	}
	if _, summary := DeriveAttention(metas, live, nil); summary.NeedsYou != 0 {
		t.Fatalf("attention summary = %+v, want no needs-you", summary)
	}
}
