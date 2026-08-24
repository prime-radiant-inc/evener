package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
)

// TestContainsLegacyDelegateLifecycle covers all three legacy event kinds
// and the delegate-job-started path (lines 39-45).
func TestContainsLegacyDelegateLifecycle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		event jobstore.Event
		want  bool
	}{
		{"delegate_created", jobstore.Event{Kind: "delegate_created"}, true},
		{"delegate_stop_gate_closed", jobstore.Event{Kind: "delegate_stop_gate_closed"}, true},
		{"delegate_disposed", jobstore.Event{Kind: "delegate_disposed"}, true},
		{"delegate_job_started", jobstore.Event{Kind: jobstore.EventJobStarted, Type: "delegate"}, true},
		{"shell_job_started", jobstore.Event{Kind: jobstore.EventJobStarted, Type: jobstore.JobShell}, false},
		{"other_event", jobstore.Event{Kind: "other"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := containsLegacyDelegateLifecycle([]jobstore.Event{tc.event}); got != tc.want {
				t.Fatalf("containsLegacyDelegateLifecycle(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestLegacyDelegateJobIDs covers legacyDelegateJobIDs (lines 27-34).
func TestLegacyDelegateJobIDs(t *testing.T) {
	t.Parallel()
	events := []jobstore.Event{
		{Kind: jobstore.EventJobStarted, Type: "delegate", JobID: "job_d1"},
		{Kind: jobstore.EventJobStarted, Type: jobstore.JobShell, JobID: "job_s1"},
		{Kind: jobstore.EventJobStarted, Type: "delegate", JobID: "job_d2"},
	}
	ids := legacyDelegateJobIDs(events)
	if len(ids) != 2 {
		t.Fatalf("expected 2 legacy delegate job IDs, got %d", len(ids))
	}
	if _, ok := ids["job_d1"]; !ok {
		t.Error("expected job_d1 in legacy IDs")
	}
	if _, ok := ids["job_d2"]; !ok {
		t.Error("expected job_d2 in legacy IDs")
	}
	if _, ok := ids["job_s1"]; ok {
		t.Error("shell job should NOT be in legacy IDs")
	}
}

// TestFirstLegacyDelegateWatch_WatchSend covers the WatchSend path where
// the watchID comes from WatchSend.Key.WatchID (lines 59-69).
func TestFirstLegacyDelegateWatch_WatchSend(t *testing.T) {
	t.Parallel()
	delegateJobIDs := map[string]struct{}{"job_d1": {}}
	events := []jobstore.Event{
		{
			WatchID: "",
			WatchSend: &jobstore.WatchSendState{
				Key: jobstore.WatchSendKey{
					WatchID:                 "watch_abc",
					WatchTarget:             "job_d1",
					ResolvedWatchedIdentity: "job_d1",
				},
			},
		},
	}
	watchID, ok := firstLegacyDelegateWatch(events, delegateJobIDs)
	if !ok {
		t.Fatal("expected ok=true for legacy watch via WatchSend")
	}
	if watchID != "watch_abc" {
		t.Fatalf("watchID = %q, want watch_abc", watchID)
	}
}

// TestFirstLegacyDelegateWatch_WatchTarget covers the Watch.Target path
// (lines 54-57).
func TestFirstLegacyDelegateWatch_WatchTarget(t *testing.T) {
	t.Parallel()
	delegateJobIDs := map[string]struct{}{"job_d1": {}}
	events := []jobstore.Event{
		{
			WatchID: "watch_xyz",
			Watch: &jobstore.WatchEvent{
				Target: "job_d1",
				SendTo: "other",
			},
		},
	}
	watchID, ok := firstLegacyDelegateWatch(events, delegateJobIDs)
	if !ok {
		t.Fatal("expected ok=true for legacy watch via Watch.Target")
	}
	if watchID != "watch_xyz" {
		t.Fatalf("watchID = %q, want watch_xyz", watchID)
	}
}

// TestFirstLegacyDelegateWatch_NoLegacy covers the empty path (lines 73-74).
func TestFirstLegacyDelegateWatch_NoLegacy(t *testing.T) {
	t.Parallel()
	delegateJobIDs := map[string]struct{}{"job_d1": {}}
	events := []jobstore.Event{
		{WatchID: "watch_ok", Watch: &jobstore.WatchEvent{Target: "job_shell1"}},
	}
	_, ok := firstLegacyDelegateWatch(events, delegateJobIDs)
	if ok {
		t.Fatal("expected ok=false for non-legacy watch")
	}
}

// TestFirstLegacyDelegateWatch_SortedMultiple covers the sort path (line 76).
func TestFirstLegacyDelegateWatch_SortedMultiple(t *testing.T) {
	t.Parallel()
	delegateJobIDs := map[string]struct{}{"job_d1": {}}
	events := []jobstore.Event{
		{WatchID: "watch_zzz", Watch: &jobstore.WatchEvent{Target: "job_d1"}},
		{WatchID: "watch_aaa", Watch: &jobstore.WatchEvent{Target: "job_d1"}},
	}
	watchID, ok := firstLegacyDelegateWatch(events, delegateJobIDs)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if watchID != "watch_aaa" {
		t.Fatalf("watchID = %q, want watch_aaa (sorted first)", watchID)
	}
}
