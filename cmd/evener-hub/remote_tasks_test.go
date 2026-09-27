package hub

import (
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/rendezvous"
)

// The hub's own list rows carry a root's task progress and never lend it to
// the root's in-process subagent aliases: the task list is the root's.
func TestLocalDaemonEntriesFromRosterCarryTasksOnlyOnTheRoot(t *testing.T) {
	tasks := &appwire.TaskAggregate{Total: 2, Done: 1, Remaining: 1}
	entries := localDaemonEntriesFromRoster([]hubcore.LiveEntry{{
		Entry:     rendezvous.Entry{ThreadID: "sess_root", SessionID: "sess_root"},
		SessionID: "sess_root", Status: appwire.ThreadStatusActive,
		RunningSubagentIDs: []string{"sess_child"}, Tasks: tasks,
	}})
	if len(entries) != 2 || !reflect.DeepEqual(entries[0].Tasks, tasks) || entries[1].Tasks != nil {
		t.Fatalf("entries = %+v, want the root's tasks %+v and none on its alias", entries, tasks)
	}
}

// A controller reads a remote host's task progress off its list row, so the
// remote live row carries its task line like a local one (S13b).
func TestNavigationRemoteLiveRowCarriesItsTaskProgress(t *testing.T) {
	cache := &hubcore.RemoteThreadCache{}
	cache.Store([]appwire.Thread{{
		ID: "planned", Source: "host-a", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
		Evener: appwire.EvenerThread{Tasks: &appwire.TaskAggregate{Total: 7, Done: 3, Remaining: 4, Current: &appwire.TaskSummary{ID: 4, Description: "Fix the settle/drain race"}}},
	}})
	web := NewWebServer(hubcore.WebConfig{RemoteThreadCache: cache})

	captured, err := (webNavigationSource{web: web}).Capture(t.Context(), "generation", time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	projection, err := buildNavigationProjection(captured.Inputs)
	if err != nil {
		t.Fatalf("buildNavigationProjection: %v", err)
	}
	want := &hubapi.NavigationTaskProgress{Total: 7, Done: 3, CurrentID: 4, Current: "Fix the settle/drain race"}
	if row := navigationProjectedSummary(t, projection, "host-a:planned"); !reflect.DeepEqual(row.Tasks, want) {
		t.Fatalf("remote row tasks = %+v, want %+v", row.Tasks, want)
	}
}
