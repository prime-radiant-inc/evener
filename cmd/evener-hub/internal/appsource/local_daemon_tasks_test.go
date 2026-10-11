package appsource

import (
	"context"
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

// A hub listing its own sessions to a controller carries each root's task-list
// progress on its row, as its own copy, and no key on a row whose daemon
// cannot read its tasks (S13b).
func TestLocalDaemonSourceListCarriesTheRootsTasks(t *testing.T) {
	tasks := &appwire.TaskAggregate{Total: 7, Done: 3, Remaining: 4, Current: &appwire.TaskSummary{ID: 4, Description: "Fix the settle/drain race"}}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/planned", ThreadID: "th_planned", SessionID: "sess_planned"}, Status: appwire.ThreadStatusActive, Tasks: tasks},
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/unknown", ThreadID: "th_unknown", SessionID: "sess_unknown"}, Status: appwire.ThreadStatusIdle},
		}
	}, nil)
	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	byID := map[string]*appwire.TaskAggregate{}
	for _, thread := range resp.Data {
		byID[thread.ID] = thread.Evener.Tasks
	}
	if !reflect.DeepEqual(byID["th_planned"], tasks) || byID["th_unknown"] != nil {
		t.Fatalf("tasks = %+v, want th_planned's %+v and none on th_unknown", byID, tasks)
	}
	byID["th_planned"].Current.Description = "changed"
	if tasks.Current.Description != "Fix the settle/drain race" {
		t.Fatal("a listed row aliases the roster's task progress")
	}
}
