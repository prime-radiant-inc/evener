package hubcore

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/identifier"
)

func TestBuildTree_RecursiveSubagentActivityRollsUpToItsRoot(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "parent", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "child", ParentSessionID: "parent", IsSubagent: true, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "grandchild", ParentSessionID: "child", IsSubagent: true, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	}
	live := []LiveEntry{
		{PID: 1, SessionID: "parent", Status: appwire.ThreadStatusIdle, RunningSubagentIDs: []string{"child"}},
		{PID: 2, SessionID: "child", Status: appwire.ThreadStatusActive},
		{PID: 3, SessionID: "child", Status: appwire.ThreadStatusActive, RunningSubagentIDs: []string{"grandchild"}},
	}

	tree := BuildTreeAt(metas, live, nil, now)
	if len(tree.Projects) != 1 || len(tree.Projects[0].Current) != 1 {
		t.Fatalf("tree = %#v, want one current parent", tree)
	}
	if parent := tree.Projects[0].Current[0]; parent.State != "idle" || len(parent.Children) != 0 {
		t.Fatalf("parent = %#v, want an idle row with no subagent rows", parent)
	}
	if tree.Projects[0].RollupState != "active" || !tree.Projects[0].Expanded {
		t.Fatalf("project rollup = %q expanded=%v, want active/true", tree.Projects[0].RollupState, tree.Projects[0].Expanded)
	}
}

func TestBuildTree_ProjectsRunningInProcessSubagent(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "parent", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "child", ParentSessionID: "parent", IsSubagent: true, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	}
	parent := LiveEntry{PID: 1, SessionID: "parent", Status: appwire.ThreadStatusIdle, RunningSubagentIDs: []string{"child"}}
	tree := BuildTreeAt(metas, []LiveEntry{parent}, nil, now)
	if tree.Projects[0].RollupState != "idle" || tree.Projects[0].Expanded {
		t.Fatalf("project rollup = %q expanded=%v, want idle/false", tree.Projects[0].RollupState, tree.Projects[0].Expanded)
	}
}

// TestBuildTree_ChildOwnLiveEntryBeatsParentProjection: a child with its own
// live entry reporting "active" keeps the project working even when its
// parent's daemon lists it in RunningSubagentIDs with no RunningSubagentStates
// (which alone would fall back to idle).
func TestBuildTree_ChildOwnLiveEntryBeatsParentProjection(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "parent", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "child", ParentSessionID: "parent", IsSubagent: true, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	}
	live := []LiveEntry{
		// Parent lists child as a running subagent but carries no state for it.
		{PID: 1, SessionID: "parent", Status: appwire.ThreadStatusIdle, RunningSubagentIDs: []string{"child"}},
		// Child has its own live entry reporting active — its own daemon truth.
		{PID: 2, SessionID: "child", Status: appwire.ThreadStatusActive},
	}
	tree := BuildTreeAt(metas, live, nil, now)
	if tree.Projects[0].RollupState != "active" || !tree.Projects[0].Expanded {
		t.Fatalf("project rollup = %q expanded=%v, want active/true", tree.Projects[0].RollupState, tree.Projects[0].Expanded)
	}
}

func TestBuildTree_CrossEffectiveDirectorySubagentHasNoRowOrProject(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "parent", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "isolated-child", ParentSessionID: "parent", IsSubagent: true, UpdatedAt: now,
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/worktrees/isolated-child"}},
		{ID: "nested-isolated-child", ParentSessionID: "isolated-child", IsSubagent: true, UpdatedAt: now,
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/worktrees/nested-isolated-child"}},
	}
	tree := BuildTreeAt(metas, []LiveEntry{{PID: 1, SessionID: "parent", Status: appwire.ThreadStatusIdle}}, nil, now)
	if len(tree.Projects) != 1 || len(tree.Projects[0].Current) != 1 {
		t.Fatalf("projects = %#v, want one parent project with one current session", tree.Projects)
	}
	if children := tree.Projects[0].Current[0].Children; len(children) != 0 {
		t.Fatalf("parent children = %#v, want none: subagents have no rows", children)
	}
	if len(tree.ArchivedProjects) != 0 {
		t.Fatalf("archived projects = %#v: a subagent's directory is not a project", tree.ArchivedProjects)
	}
}

func TestBuildProjectTreeAt_LazyLookupRollsUpCrossDirectorySubagent(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	projectDir := filepath.Join(root, "projects", "evener")
	isolationDir := filepath.Join(root, "worktrees", "isolated-child")
	for _, dir := range []string{projectDir, isolationDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	projectID, err := identifier.ProjectID(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	metas := []schema.SessionMeta{
		{ID: "parent", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: projectDir}},
		{ID: "isolated-child", ParentSessionID: "parent", IsSubagent: true, UpdatedAt: now,
			EnvInfo: schema.EnvironmentInfo{WorkingDir: isolationDir}},
	}
	live := []LiveEntry{
		{PID: 1, SessionID: "parent", Status: appwire.ThreadStatusIdle, RunningSubagentIDs: []string{"isolated-child"}},
		{PID: 2, SessionID: "isolated-child", Status: appwire.ThreadStatusActive},
	}
	project, ok := BuildProjectTreeAt(metas, live, nil, now, projectID)
	if !ok || len(project.Current) != 1 || len(project.Current[0].Children) != 0 {
		t.Fatalf("lazy project = %#v, found=%v; want the parent alone", project, ok)
	}
	if project.RollupState != "active" || project.RollupLive != 1 {
		t.Fatalf("lazy project rollup = %q live=%d, want the cross-directory child to keep it working", project.RollupState, project.RollupLive)
	}
}

func TestNormalizeState_UnknownAndNotLoadedRemainNeutralCurrent(t *testing.T) {
	for _, status := range []string{appwire.ThreadStatusNotLoaded, "future-live-status"} {
		if got := NormalizeState(status); got != "notLoaded" {
			t.Errorf("NormalizeState(%q) = %q, want notLoaded", status, got)
		}
	}
	for _, tc := range []struct {
		status string
		want   string
	}{
		{appwire.ThreadStatusClosed, "ended"},
		{"ended", "ended"},
		{"errored", "errored"},
	} {
		if got := NormalizeState(tc.status); got != tc.want {
			t.Errorf("NormalizeState(%q) = %q, want %q", tc.status, got, tc.want)
		}
	}
}
