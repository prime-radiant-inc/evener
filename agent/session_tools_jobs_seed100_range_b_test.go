//go:build serffuzz

package agent

import (
	"context"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// seed100ToolsRangeB covers job-tool status, rendering, snapshot, list, and
// stop branches in the middle of session_tools_jobs.go.
func seed100ToolsRangeB(t *testing.T) {
	t.Helper()

	s := newSession(t)
	jm := s.jobManager
	freezeClock(jm)
	started := frozenTestTime
	if err := jm.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               started,
		JobID:            "job_range_b",
		Type:             jobstore.JobShell,
		OwnerSessionID:   s.ID(),
		VisibleToSession: s.ID(),
		StartedAt:        &started,
		Command:          "printf range-b",
	}); err != nil {
		t.Fatal(err)
	}
	_, _ = jobStatusTool(s, map[string]any{"job_id": "job_range_b"}, 4096)
	_, _ = jobStatusTool(s, map[string]any{"job_id": "job_range_b"}, 1)
	_, _ = jobStatusTool(s, map[string]any{"job_id": ""}, 4096)
	_, _ = jobStatusTool(s, map[string]any{"job_id": "dlg_range_b"}, 4096)
	_, _ = jobStatusTool(s, map[string]any{"job_id": "missing"}, 4096)

	exit := 7

	_ = jobListDelegatesForJobs(s, nil, nil)
	_ = jobListDelegatesForJobs(s, map[string]*jobstore.DelegateRecord{"dlg": {}}, []jobListEntry{{JobID: "job"}})
	_ = jobListDelegatesForJobs(s, map[string]*jobstore.DelegateRecord{
		"missing":   nil,
		"foreign":   {DelegateID: "foreign", OwnerSessionID: "other", CurrentJobID: "job"},
		"unrelated": {DelegateID: "unrelated", OwnerSessionID: s.ID(), CurrentJobID: "elsewhere"},
		"dlg":       {DelegateID: "dlg", OwnerSessionID: s.ID(), CurrentJobID: "job", LatestJobID: "old"},
	}, []jobListEntry{{JobID: "job", DelegateID: "dlg"}, {JobID: "second", DelegateID: "missing"}, {JobID: "third", DelegateID: "foreign"}, {JobID: "fourth", DelegateID: "unrelated"}})

	command, reason, resumable := "cmd", "reason", true
	_ = formatJobList(jobListResult{
		Count: 1, DelegationAllowance: 3,
		Jobs: []jobListEntry{{JobID: "job", Type: "shell", Status: "done", Depth: 2, Command: &command, Reason: &reason, ExitCode: &exit, DelegateID: "dlg", TotalBytes: 4, Resumable: &resumable}},
		Delegates: []delegateListEntry{
			{DelegateID: "a", Status: "running", CurrentJobID: "job", LatestJobID: "old", TranscriptRef: "tx", Resumable: true, ParentDelegateID: "parent"},
			{DelegateID: "b", Status: "stopped", NotResumableWhy: "expired"},
		},
		Watches:       []watchListEntry{{ID: "w", Source: "job", Condition: "done"}},
		RecentWatches: []recentWatchEntry{{ID: "rw", Source: "job", EndReason: "fired", Deliveries: 2}},
	})
	_ = formatJobList(jobListResult{})
	_ = shortTimestamp("")
	_ = shortTimestamp("invalid")
	_ = shortTimestamp(frozenTestTime.Format("2006-01-02T15:04:05.999999999Z07:00"))

	_, _ = jobStopTool(context.Background(), nil, nil, 0)
	_, _ = jobStopTool(context.Background(), s, map[string]any{}, 0)
	_, _ = jobStopTool(context.Background(), s, map[string]any{"job_id": "dlg_x"}, 0)
	_, _ = jobStopTool(context.Background(), s, map[string]any{"job_id": "job_range_b", "max_wait_ms": -1}, 0)
	_, _ = jobStopTool(context.Background(), s, map[string]any{"job_id": "missing", "include_children": true}, 0)
}
