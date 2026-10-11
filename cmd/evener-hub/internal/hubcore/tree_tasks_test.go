package hubcore

import (
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// Task progress is a fact about one live session, and it rides every row that
// session gets: the Live and project rows buildNode makes, the Live tier's leaf
// for a session the past index has not caught up with, and the NeedsYou row. A
// subagent row carries none, because an in-process child has no live entry of
// its own. Each row owns its copy, and so does a tree snapshot.
func fuzzScenarioBuildTree_CarriesTaskProgressOnEveryRow(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	progress := func(description string) *appwire.TaskAggregate {
		return taskProgress(7, 3, 0, &appwire.TaskSummary{ID: 4, Description: description})
	}
	workingDir := schema.EnvironmentInfo{WorkingDir: "/projects/evener"}
	metas := []schema.SessionMeta{
		{ID: "01PLANNED", CreatedAt: now, UpdatedAt: now, EnvInfo: workingDir},
		{ID: "01HELPER", ParentSessionID: "01PLANNED", IsSubagent: true, CreatedAt: now, UpdatedAt: now, EnvInfo: workingDir},
		{ID: "01UNPLANNED", CreatedAt: now, UpdatedAt: now, EnvInfo: workingDir},
	}
	live := []LiveEntry{
		{PID: 1, SessionID: "01PLANNED", Status: appwire.ThreadStatusAwaiting, RunningSubagentIDs: []string{"01HELPER"}, Tasks: progress("Fix the settle/drain race")},
		{PID: 2, SessionID: "01UNINDEXED", Status: appwire.ThreadStatusActive, Tasks: progress("Land the fix")},
		{PID: 3, SessionID: "01UNPLANNED", Status: appwire.ThreadStatusActive},
	}
	tree := BuildTreeAt(metas, live, map[ArchiveKey]bool{}, now)

	liveRow, inLive, projectRow, inProject := liveAndProjectRowsFor(tree, "01PLANNED")
	if !inLive || !inProject || len(tree.NeedsYou) != 1 || tree.NeedsYou[0].ID != "01PLANNED" {
		t.Fatalf("rows of the planned session: live=%v project=%v needsYou=%+v", inLive, inProject, tree.NeedsYou)
	}
	want := progress("Fix the settle/drain race")
	for name, row := range map[string]TreeNode{"Live": liveRow, "project": projectRow, "NeedsYou": tree.NeedsYou[0]} {
		if !reflect.DeepEqual(row.Tasks, want) {
			t.Fatalf("%s row tasks = %+v, want %+v", name, row.Tasks, want)
		}
	}
	unindexed, found, _, _ := liveAndProjectRowsFor(tree, "01UNINDEXED")
	if !found || !reflect.DeepEqual(unindexed.Tasks, progress("Land the fix")) {
		t.Errorf("Live leaf of the unindexed session = %+v (found %v), want its own task progress", unindexed, found)
	}
	if unplannedLive, _, unplannedProject, _ := liveAndProjectRowsFor(tree, "01UNPLANNED"); unplannedLive.Tasks != nil || unplannedProject.Tasks != nil {
		t.Errorf("rows of a session without a task list carry %+v and %+v, want none", unplannedLive.Tasks, unplannedProject.Tasks)
	}

	snapshotRow, _, _, _ := liveAndProjectRowsFor(tree.Snapshot(), "01PLANNED")
	snapshotRow.Tasks.Current.Description = "changed in a snapshot"
	if liveRow.Tasks.Current.Description != "Fix the settle/drain race" {
		t.Fatalf("a snapshot's row shares the tree's task progress: %+v", liveRow.Tasks.Current)
	}
	liveRow.Tasks.Current.Description = "changed on the Live row"
	for name, current := range map[string]*appwire.TaskSummary{
		"project row":  projectRow.Tasks.Current,
		"NeedsYou row": tree.NeedsYou[0].Tasks.Current,
		"live entry":   live[0].Tasks.Current,
	} {
		if current.Description != "Fix the settle/drain race" {
			t.Errorf("the %s shares the Live row's task progress: %+v", name, current)
		}
	}
}
