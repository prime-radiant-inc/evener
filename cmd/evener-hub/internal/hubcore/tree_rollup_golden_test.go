package hubcore

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// The project rollup and working dot used to come from walking a root's child
// rows. Subagents no longer have rows, so the builder derives the same answer
// from live data. These goldens were captured from the builder that still
// nested subagent rows, on these same fixtures, and must not change.
func TestProjectRollupGolden(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	meta := func(id, parent string, subagent bool) schema.SessionMeta {
		return schema.SessionMeta{ID: id, ParentSessionID: parent, IsSubagent: subagent, CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}}
	}
	fork := func(id string) schema.SessionMeta {
		m := meta(id, "", false)
		m.ForkLabel = "before edit"
		return m
	}
	job := []appwire.EvenerJobInfo{{JobID: "j", JobType: "shell", Status: "running"}}
	active := map[string]string{"child": appwire.ThreadStatusActive}

	type rollup struct {
		state    string
		live     int
		attn     int
		expanded bool
	}
	cases := []struct {
		name  string
		metas []schema.SessionMeta
		live  []LiveEntry
		want  rollup
	}{
		{
			name:  "idle root with an active in-process child is working",
			metas: []schema.SessionMeta{meta("root", "", false), meta("child", "root", true)},
			live:  []LiveEntry{{PID: 1, SessionID: "root", Status: appwire.ThreadStatusIdle, RunningSubagentIDs: []string{"child"}, RunningSubagentStates: active}},
			want:  rollup{"active", 1, 0, true},
		},
		{
			name:  "idle root with an idle in-process child is not working",
			metas: []schema.SessionMeta{meta("root", "", false), meta("child", "root", true)},
			live:  []LiveEntry{{PID: 1, SessionID: "root", Status: appwire.ThreadStatusIdle, RunningSubagentIDs: []string{"child"}}},
			want:  rollup{"idle", 0, 0, false},
		},
		{
			name:  "idle child with a running job keeps the project working",
			metas: []schema.SessionMeta{meta("root", "", false), meta("child", "root", true)},
			live: []LiveEntry{
				{PID: 1, SessionID: "root", Status: appwire.ThreadStatusIdle, RunningSubagentIDs: []string{"child"}},
				{PID: 2, SessionID: "child", Status: appwire.ThreadStatusIdle, RunningJobs: job},
			},
			want: rollup{"active", 1, 0, true},
		},
		{
			name:  "crashed root entry with stale children is not working",
			metas: []schema.SessionMeta{meta("root", "", false), meta("child", "root", true)},
			live: []LiveEntry{{PID: 1, SessionID: "root", Status: appwire.ThreadStatusIdle, Crashed: true,
				RunningSubagentIDs: []string{"child"}, RunningSubagentStates: active,
				Subagents: appwire.SubagentTally{Running: 1}}},
			want: rollup{"idle", 0, 0, false},
		},
		{
			name:  "ended root with an active child and no jobs is not working",
			metas: []schema.SessionMeta{meta("root", "", false), meta("child", "root", true)},
			live:  []LiveEntry{{PID: 2, SessionID: "child", Status: appwire.ThreadStatusActive}},
			want:  rollup{"", 0, 0, false},
		},
		{
			name:  "ended root with an active child running a job is working",
			metas: []schema.SessionMeta{meta("root", "", false), meta("child", "root", true)},
			live:  []LiveEntry{{PID: 2, SessionID: "child", Status: appwire.ThreadStatusActive, RunningJobs: job}},
			want:  rollup{"active", 1, 0, true},
		},
		{
			name:  "an active grandchild makes the root working",
			metas: []schema.SessionMeta{meta("root", "", false), meta("child", "root", true), meta("grandchild", "child", true)},
			live: []LiveEntry{
				{PID: 1, SessionID: "root", Status: appwire.ThreadStatusIdle, RunningSubagentIDs: []string{"child"}},
				{PID: 2, SessionID: "child", Status: appwire.ThreadStatusIdle, RunningSubagentIDs: []string{"grandchild"},
					RunningSubagentStates: map[string]string{"grandchild": appwire.ThreadStatusActive}},
			},
			want: rollup{"active", 1, 0, true},
		},
		{
			name: "a subagent of a nested fork original raises the continuation",
			metas: []schema.SessionMeta{
				fork("orig"), meta("cont", "orig", false), meta("child", "orig", true),
			},
			live: []LiveEntry{
				{PID: 1, SessionID: "cont", Status: appwire.ThreadStatusIdle},
				{PID: 2, SessionID: "orig", Status: appwire.ThreadStatusIdle, RunningSubagentIDs: []string{"child"}, RunningSubagentStates: active},
			},
			want: rollup{"active", 1, 0, true},
		},
		{
			name:  "an awaiting child changes neither root nor project",
			metas: []schema.SessionMeta{meta("root", "", false), meta("child", "root", true)},
			live: []LiveEntry{
				{PID: 1, SessionID: "root", Status: appwire.ThreadStatusIdle, RunningSubagentIDs: []string{"child"}},
				{PID: 2, SessionID: "child", Status: appwire.ThreadStatusAwaiting},
			},
			want: rollup{"idle", 0, 0, false},
		},
		{
			name:  "an errored child changes neither root nor project",
			metas: []schema.SessionMeta{meta("root", "", false), meta("child", "root", true)},
			live: []LiveEntry{
				{PID: 1, SessionID: "root", Status: appwire.ThreadStatusIdle, RunningSubagentIDs: []string{"child"}},
				{PID: 2, SessionID: "child", Status: appwire.ThreadStatusSystemError},
			},
			want: rollup{"idle", 0, 0, false},
		},
		{
			name:  "an awaiting root stays awaiting beside an active child",
			metas: []schema.SessionMeta{meta("root", "", false), meta("child", "root", true)},
			live: []LiveEntry{
				{PID: 1, SessionID: "root", Status: appwire.ThreadStatusAwaiting, RunningSubagentIDs: []string{"child"}, RunningSubagentStates: active},
			},
			want: rollup{"awaiting", 0, 1, true},
		},
		{
			name:  "an orphan live subagent affects nothing",
			metas: []schema.SessionMeta{meta("root", "", false), meta("orphan", "gone", true)},
			live:  []LiveEntry{{PID: 2, SessionID: "orphan", Status: appwire.ThreadStatusActive, RunningJobs: job}},
			want:  rollup{"", 0, 0, false},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tree := BuildTreeAt(tc.metas, tc.live, nil, now)
			projects := append(append([]TreeProject(nil), tree.Projects...), tree.ArchivedProjects...)
			if len(projects) != 1 {
				t.Fatalf("got %d projects, want 1", len(projects))
			}
			p := projects[0]
			got := rollup{p.RollupState, p.RollupLive, p.RollupAttn, p.Expanded}
			if got != tc.want {
				t.Fatalf("rollup = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// A live child its parent lists as running, whose meta the past index has not
// caught up with, still keeps its root's project working: the builder
// synthesizes a meta for it, but the parent's running list marks it a
// subagent, so it has no row and attributes its activity to the reporter.
func TestProjectRollupCountsUnindexedRunningChild(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	metas := []schema.SessionMeta{{ID: "root", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: dir}}}
	live := []LiveEntry{
		{PID: 1, SessionID: "root", Status: appwire.ThreadStatusIdle, RunningSubagentIDs: []string{"child"}},
		{PID: 2, SessionID: "child", Status: appwire.ThreadStatusActive},
	}
	live[0].WorkingDir, live[1].WorkingDir = dir, dir

	tree := BuildTreeAt(metas, live, nil, now)

	if len(tree.Projects) != 1 || tree.Projects[0].RollupState != "active" || tree.Projects[0].RollupLive != 1 {
		t.Fatalf("projects = %+v, want one working project", tree.Projects)
	}
	for _, row := range tree.Live {
		if row.ID == "child" {
			t.Fatal("the unindexed child has a Live row")
		}
	}
}
