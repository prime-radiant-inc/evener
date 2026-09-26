package hub

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
)

// liveTaskRows projects rows onto the Live section and returns them by session
// ID, after proving the section passes the hub's own schema.
func liveTaskRows(t *testing.T, rows []hubcore.TreeNode) map[string]hubapi.NavigationSessionSummary {
	t.Helper()
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Revision: 1, Tree: hubcore.Tree{Live: rows}})
	if err != nil {
		t.Fatal(err)
	}
	key := navigationResourceKey{Kind: navigationResourceLive, Limit: 50}
	section := projection.LivePage(0, 50)
	if _, err := normalizeNavigationResource(key, section); err != nil {
		t.Fatalf("live section rejected by the hub schema: %v", err)
	}
	out := make(map[string]hubapi.NavigationSessionSummary, len(section.Sessions))
	for _, row := range section.Sessions {
		out[row.SessionID] = row
	}
	return out
}

// A live session's task-list progress reaches its row as the facts the task
// line shows (S13a, "Task 4 of 7 · Fix the settle/drain race"): the counts,
// and the first task in progress cut to the label bound. A session with an
// empty list, or whose daemon cannot read its task state, carries no tasks
// record at all.
func TestNavigationProjectionCarriesTaskProgress(t *testing.T) {
	long := strings.Repeat("é", maxNavigationLabelRunes+10)
	rows := liveTaskRows(t, []hubcore.TreeNode{
		{ID: "session-planned", Title: "planned", Kind: "session", State: "active", Tasks: &appwire.TaskAggregate{
			Total: 7, Done: 2, Cancelled: 1, Remaining: 4, Current: &appwire.TaskSummary{ID: 4, Description: "Fix the settle/drain race"},
		}},
		{ID: "session-between", Title: "between tasks", Kind: "session", State: "idle", Tasks: &appwire.TaskAggregate{Total: 2, Done: 1, Remaining: 1}},
		{ID: "session-long", Title: "long task", Kind: "session", State: "active", Tasks: &appwire.TaskAggregate{
			Total: 1, Remaining: 1, Current: &appwire.TaskSummary{ID: 1, Description: long},
		}},
		{ID: "session-empty", Title: "empty list", Kind: "session", State: "idle", Tasks: &appwire.TaskAggregate{}},
		{ID: "session-unknown", Title: "unknown", Kind: "session", State: "idle"},
	})

	want := map[string]*hubapi.NavigationTaskProgress{
		"session-planned": {Total: 7, Done: 2, Cancelled: 1, CurrentID: 4, Current: "Fix the settle/drain race"},
		"session-between": {Total: 2, Done: 1},
		"session-empty":   nil,
		"session-unknown": nil,
	}
	for id, tasks := range want {
		if got := rows[id].Tasks; !reflect.DeepEqual(got, tasks) {
			t.Errorf("%s tasks = %+v, want %+v", id, got, tasks)
		}
	}
	cut := rows["session-long"].Tasks
	if cut == nil || cut.CurrentID != 1 || utf8.RuneCountInString(cut.Current) != maxNavigationLabelRunes || !strings.HasSuffix(cut.Current, "…") {
		t.Fatalf("long current task = %+v, want it cut to %d runes", cut, maxNavigationLabelRunes)
	}

	planned := navigationSummaryJSONFields(t, rows["session-planned"])
	if got, want := string(planned["tasks"]), `{"total":7,"done":2,"cancelled":1,"current_id":4,"current":"Fix the settle/drain race"}`; got != want {
		t.Errorf("planned row tasks on the wire = %s, want %s", got, want)
	}
	for _, id := range []string{"session-empty", "session-unknown"} {
		if tasks, ok := navigationSummaryJSONFields(t, rows[id])["tasks"]; ok {
			t.Errorf("%s row carries tasks %s on the wire, want no tasks key", id, tasks)
		}
	}
}

// A daemon aggregate the schema would refuse is dropped from its row rather
// than carried: one invalid summary would make the whole resource, every other
// row in it included, unreadable. The row itself stays listed.
func TestNavigationProjectionDropsTaskProgressTheSchemaRefuses(t *testing.T) {
	rows := liveTaskRows(t, []hubcore.TreeNode{
		{ID: "session-oversettled", Title: "oversettled", Kind: "session", State: "active", Tasks: &appwire.TaskAggregate{Total: 2, Done: 2, Cancelled: 1}},
		{ID: "session-negative", Title: "negative", Kind: "session", State: "active", Tasks: &appwire.TaskAggregate{Total: 3, Done: -1}},
		{ID: "session-valid", Title: "valid", Kind: "session", State: "active", Tasks: &appwire.TaskAggregate{Total: 3, Done: 1, Remaining: 2}},
	})
	for _, id := range []string{"session-oversettled", "session-negative"} {
		row, listed := rows[id]
		if !listed || row.Tasks != nil {
			t.Errorf("%s = %+v (listed %v), want the row listed without its task progress", id, row.Tasks, listed)
		}
	}
	if got, want := rows["session-valid"].Tasks, (&hubapi.NavigationTaskProgress{Total: 3, Done: 1}); !reflect.DeepEqual(got, want) {
		t.Errorf("valid row tasks = %+v, want %+v", got, want)
	}
}

// The byte-budget fitter and the location index work on clones of a summary,
// so a clone must own its task progress.
func TestCloneNavigationSummaryOwnsTaskProgress(t *testing.T) {
	original := hubapi.NavigationSessionSummary{Tasks: &hubapi.NavigationTaskProgress{Total: 2, Done: 1, CurrentID: 2, Current: "second"}}
	clone := cloneNavigationSummary(original)
	original.Tasks.Current = "mutated"
	original.Tasks.Done = 2
	if clone.Tasks == nil || clone.Tasks.Current != "second" || clone.Tasks.Done != 1 {
		t.Fatalf("clone task progress = %+v, want its own copy", clone.Tasks)
	}
}
