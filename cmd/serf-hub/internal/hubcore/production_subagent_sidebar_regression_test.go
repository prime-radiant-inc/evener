package hubcore

import (
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
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
