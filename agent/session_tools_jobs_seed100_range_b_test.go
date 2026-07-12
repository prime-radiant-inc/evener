//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"regexp"
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
	valid := true
	grep := "needle"
	matches := []jobOutputMatch{{ByteOffset: 3, Line: "needle"}}
	for _, out := range []*jobReadOutputResult{
		{JobID: "job_a", Status: "completed", Content: "body", ExitCode: &exit, OutputStatus: "complete", TotalBytes: 2048, DroppedBytes: 3},
		{JobID: "job_b", Status: "running", Content: "body\n", StructuredResult: map[string]any{"value": "ok"}, StructuredResultValid: &valid},
		{JobID: "job_c", Status: "running", StructuredResult: func() {}},
		{JobID: "job_d", Status: "running", Grep: &grep, Matches: &matches},
		{JobID: "job_e", Status: "running", Matches: &[]jobOutputMatch{}},
	} {
		_ = formatJobReadOutput(out, "--- range ---", 4096)
	}
	tooLarge := &jobReadOutputResult{JobID: "job_f", Status: "done", StructuredResult: map[string]string{"large": "abcdef"}, StructuredResultValid: &valid}
	_ = formatJobReadOutput(tooLarge, "", 1)
	_ = derefString(nil)
	_ = derefString(&grep)

	seed100RangeBSnapshotFaults(t, s, jm)
	_, _, _ = s.jobReadClosedStoreFallback(jm, errors.New("ordinary"))
	_, _, _ = s.jobReadClosedStoreFallback(jm, jobstore.ErrStoreClosed)
	_, _, _ = (*Session)(nil).jobReadClosedStoreFallback(jm, jobstore.ErrStoreClosed)

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

func seed100RangeBSnapshotFaults(t *testing.T, s *Session, jm *jobManager) {
	t.Helper()
	originalFind := findJobRecordForSnapshot
	originalRead := readJobWindowForSnapshot
	originalGrep := grepJobOutputForSnapshot
	originalFallback := jobReadFallbackForSnapshot
	t.Cleanup(func() {
		findJobRecordForSnapshot = originalFind
		readJobWindowForSnapshot = originalRead
		grepJobOutputForSnapshot = originalGrep
		jobReadFallbackForSnapshot = originalFallback
	})

	want := errors.New("range-b snapshot fault")
	jobReadFallbackForSnapshot = func(*Session, *jobManager, error) (*jobManager, bool, error) { return nil, false, want }
	findJobRecordForSnapshot = func(*jobManager, string) (*jobstore.JobRecord, error) { return nil, want }
	_, _ = s.readJobOutputSnapshot(jm, s, "job", 1, false, nil)

	findJobRecordForSnapshot = func(*jobManager, string) (*jobstore.JobRecord, error) { return &jobstore.JobRecord{JobID: "job"}, nil }
	readJobWindowForSnapshot = func(*jobManager, string, int, bool) (string, int64, int64, bool, error) { return "", 0, 0, false, want }
	_, _ = s.readJobOutputSnapshot(jm, s, "job", 1, false, nil)

	readJobWindowForSnapshot = func(*jobManager, string, int, bool) (string, int64, int64, bool, error) { return "x", 1, 0, false, nil }
	calls := 0
	findJobRecordForSnapshot = func(*jobManager, string) (*jobstore.JobRecord, error) {
		calls++
		if calls == 2 {
			return nil, want
		}
		return &jobstore.JobRecord{JobID: "job"}, nil
	}
	_, _ = s.readJobOutputSnapshot(jm, s, "job", 1, false, nil)

	findJobRecordForSnapshot = func(*jobManager, string) (*jobstore.JobRecord, error) { return &jobstore.JobRecord{JobID: "job"}, nil }
	grepJobOutputForSnapshot = func(*jobManager, string, *regexp.Regexp) ([]jobstore.Match, error) { return nil, want }
	_, _ = s.readJobOutputSnapshot(jm, s, "job", 1, false, regexp.MustCompile("x"))

	grepJobOutputForSnapshot = func(*jobManager, string, *regexp.Regexp) ([]jobstore.Match, error) {
		return []jobstore.Match{{ByteOffset: 0, Line: "x"}}, nil
	}
	calls = 0
	findJobRecordForSnapshot = func(*jobManager, string) (*jobstore.JobRecord, error) {
		calls++
		if calls == 2 {
			return nil, want
		}
		return &jobstore.JobRecord{JobID: "job"}, nil
	}
	_, _ = s.readJobOutputSnapshot(jm, s, "job", 1, false, regexp.MustCompile("x"))

	findJobRecordForSnapshot = originalFind
	readJobWindowForSnapshot = originalRead
	grepJobOutputForSnapshot = originalGrep
	jobReadFallbackForSnapshot = originalFallback
}
