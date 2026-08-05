package hubcore

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/rendezvous"
)

func TestBuildTree_PreservesRecursiveSubagentParentage(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "parent", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
		{ID: "child", ParentSessionID: "parent", IsSubagent: true, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
		{ID: "grandchild", ParentSessionID: "child", IsSubagent: true, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "parent", Status: appwire.ThreadStatusIdle, RunningSubagentIDs: []string{"child"}},
		{Entry: rendezvous.Entry{PID: 2}, SessionID: "child", Status: appwire.ThreadStatusActive},
		{Entry: rendezvous.Entry{PID: 3}, SessionID: "child", Status: appwire.ThreadStatusActive, RunningSubagentIDs: []string{"grandchild"}},
	}

	tree := BuildTreeAt(metas, live, nil, now)
	if len(tree.Projects) != 1 || len(tree.Projects[0].Current) != 1 {
		t.Fatalf("tree = %#v, want one current parent", tree)
	}
	children := tree.Projects[0].Current[0].Children
	if len(children) != 1 || children[0].ID != "child" {
		t.Fatalf("parent children = %#v, want direct child", children)
	}
	if len(children[0].Children) != 1 || children[0].Children[0].ID != "grandchild" {
		t.Fatalf("child children = %#v, want recursively preserved grandchild", children[0].Children)
	}
	if parent := tree.Projects[0].Current[0]; parent.State != "idle" {
		t.Fatalf("parent state = %q, want idle", parent.State)
	}
	if children[0].State != "active" {
		t.Fatalf("child state = %q, want active", children[0].State)
	}
	if tree.Projects[0].RollupState != "active" || !tree.Projects[0].Expanded {
		t.Fatalf("project rollup = %q expanded=%v, want active/true", tree.Projects[0].RollupState, tree.Projects[0].Expanded)
	}
}

func TestBuildTree_ProjectsRunningInProcessSubagent(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "parent", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
		{ID: "child", ParentSessionID: "parent", IsSubagent: true, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
	}
	parent := LiveEntry{Entry: rendezvous.Entry{PID: 1}, SessionID: "parent", Status: appwire.ThreadStatusIdle, RunningSubagentIDs: []string{"child"}}
	tree := BuildTreeAt(metas, []LiveEntry{parent}, nil, now)
	child := tree.Projects[0].Current[0].Children[0]
	if child.State != "active" {
		t.Fatalf("running in-process child state = %q, want active", child.State)
	}
	if tree.Projects[0].RollupState != "active" || !tree.Projects[0].Expanded {
		t.Fatalf("project rollup = %q expanded=%v, want active/true", tree.Projects[0].RollupState, tree.Projects[0].Expanded)
	}
}

func TestBuildTree_AttachesCrossEffectiveDirectorySubagentToParentProject(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "parent", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
		{ID: "isolated-child", ParentSessionID: "parent", IsSubagent: true, UpdatedAt: now,
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/worktrees/isolated-child"}},
		{ID: "nested-isolated-child", ParentSessionID: "isolated-child", IsSubagent: true, UpdatedAt: now,
			EnvInfo: schema.EnvironmentInfo{WorkingDir: "/worktrees/nested-isolated-child"}},
	}
	tree := BuildTreeAt(metas, []LiveEntry{{Entry: rendezvous.Entry{PID: 1}, SessionID: "parent", Status: appwire.ThreadStatusIdle}}, nil, now)
	if len(tree.Projects) != 1 || len(tree.Projects[0].Current) != 1 {
		t.Fatalf("projects = %#v, want one parent project with one current session", tree.Projects)
	}
	children := tree.Projects[0].Current[0].Children
	if len(children) != 1 || children[0].ID != "isolated-child" {
		t.Fatalf("parent children = %#v, want cross-directory child attached once", children)
	}
	if len(children[0].Children) != 1 || children[0].Children[0].ID != "nested-isolated-child" {
		t.Fatalf("cross-directory child children = %#v, want recursively attached grandchild", children[0].Children)
	}
}

func TestBuildProjectTreeAt_LazyLookupKeepsCrossDirectorySubagent(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	projectDir := filepath.Join(root, "projects", "serf")
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
	project, ok := BuildProjectTreeAt(metas, nil, nil, now, projectID)
	if !ok || len(project.Current) != 1 || len(project.Current[0].Children) != 1 || project.Current[0].Children[0].ID != "isolated-child" {
		t.Fatalf("lazy project = %#v, found=%v; want parent and cross-directory child", project, ok)
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
