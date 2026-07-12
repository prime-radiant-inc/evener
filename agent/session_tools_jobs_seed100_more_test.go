//go:build serffuzz

package agent

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// seed100SessionToolsJobsMore covers the deterministic tail of job-tool
// validation, projection, watch routing, and grep scanning.
func seed100SessionToolsJobsMore(t *testing.T) {
	t.Helper()

	// Pure validation branches.
	for _, args := range []map[string]any{
		{"max_wait_ms": minJobBlockTimeoutMS - 1},
		{"max_wait_ms": maxJobBlockTimeoutMS + 1},
	} {
		_, _ = decodeDelegateArgs(args)
	}
	originalMarshal := marshalJobGrepPattern
	marshalJobGrepPattern = func(string) ([]byte, error) { return nil, errors.New("seed marshal fault") }
	t.Cleanup(func() { marshalJobGrepPattern = originalMarshal })
	_ = validateJobGrepPattern("x", 100)
	marshalJobGrepPattern = originalMarshal

	// Nil guards and stable watch ordering.
	_, _, _ = (*Session)(nil).configureDescendantReceiverWatch(watchArgs{Target: "job_x"})
	_ = (*Session)(nil).liveDescendantSessions()
	local := jobWatchListToolResult{Watches: []jobWatchInspectToolResult{
		{WatchID: "b", Source: "z"},
		{WatchID: "b", Source: "a"},
		{WatchID: "a", Source: "a"},
	}}
	_ = (&Session{}).watchListToolResultWithDescendantReceivers(local)

	// A nil child session is retained by the subagent registry and exercises the
	// defensive descendant walkers without needing a runtime.
	root := newSession(t)
	root.subagents.subs["nil-child"] = &subagent{id: "nil-child"}
	_ = root.watchListToolResultWithDescendantReceivers(jobWatchListToolResult{})
	_, _ = root.inspectDescendantReceiverWatchByID("missing")
	_, _, _ = root.clearDescendantReceiverWatchByID("missing")
	delete(root.subagents.subs, "nil-child")
	root.subagents.subs["bare-child"] = &subagent{id: "bare-child", sess: &Session{}}
	_ = root.watchListToolResultWithDescendantReceivers(jobWatchListToolResult{})
	_, _ = root.inspectDescendantReceiverWatchByID("missing")
	_, _, _ = root.clearDescendantReceiverWatchByID("missing")
	delete(root.subagents.subs, "bare-child")

	// Delegate projection ownership and absent-current/latest filtering.
	jobs := []jobListEntry{{JobID: "job_one", DelegateID: "dlg_one"}}
	_ = jobListDelegatesForJobs(root, map[string]*jobstore.DelegateRecord{
		"dlg_one": nil,
	}, jobs)
	_ = jobListDelegatesForJobs(root, map[string]*jobstore.DelegateRecord{
		"dlg_one": {DelegateID: "dlg_one", OwnerSessionID: root.ID(), CurrentJobID: "missing", LatestJobID: "missing"},
	}, jobs)
	_ = jobListDelegatesForJobs(root, map[string]*jobstore.DelegateRecord{
		"dlg_one": {DelegateID: "dlg_one", OwnerSessionID: root.ID(), CurrentJobID: "job_one", LatestJobID: "missing"},
	}, jobs)

	// Bounding reaches the successful empty-output fallback before the final
	// structured-result degradation.
	_, _ = marshalBoundedDelegateResult(delegateToolResult{Output: ptrString(strings.Repeat("x", 100))}, 128)
	for limit := 1; limit <= 512; limit++ {
		_, _ = marshalBoundedDelegateResult(delegateToolResult{Output: ptrString(strings.Repeat("x", 100))}, limit)
	}

	// Granted reads expose both independent provider error paths.
	want := errors.New("seed read fault")
	granted := &grantedJobRead{
		record:     &jobstore.JobRecord{JobID: "job_x"},
		readWindow: func(int, bool) (string, int64, int64, bool, error) { return "", 0, 0, false, want },
	}
	_, _ = granted.snapshot(1, false, nil)
	granted.readWindow = func(int, bool) (string, int64, int64, bool, error) { return "x", 1, 0, false, nil }
	granted.grepOutput = func(*regexp.Regexp) ([]jobstore.Match, error) { return nil, want }
	_, _ = granted.snapshot(1, false, regexp.MustCompile("x"))

	// Closed managers cover the transient grep scanner and output-read failures.
	closed := newTestJM(t)
	if err := closed.store.Close(); err != nil {
		t.Fatal(err)
	}
	g := &jobGrepScan{}
	_ = g.step(closed, "missing", regexp.MustCompile("x"), 10)
	g.lastTotal = -1
	_ = g.step(closed, "missing", regexp.MustCompile("x"), 10)

	// Retention clamping is reachable with a synthetic lifetime total even when
	// the underlying record is absent.
	_, _, _ = readJobOutputFrom(closed, "missing", 0, maxJobOutputRetentionBytes+1)

	// Closed-store front doors surface their durable lookup/load errors.
	closedSession := newSession(t)
	if err := closedSession.jobManager.store.Close(); err != nil {
		t.Fatal(err)
	}
	_, _ = jobWatchTool(closedSession, map[string]any{"operation": "clear", "watch_id": "watch_x"}, 100)
	_, _ = jobStatusTool(closedSession, map[string]any{"job_id": "job_x"}, 100)
	_, _ = jobListTool(closedSession, nil, 100)
	_, _ = jobReadOutputTool(nil, closedSession, map[string]any{"job_id": "job_x"}, 100)

	// Each snapshot phase fails once, falls back, and is retried successfully.
	origFind := findJobRecordForSnapshot
	origWindow := readJobWindowForSnapshot
	origGrep := grepJobOutputForSnapshot
	origFallback := jobReadFallbackForSnapshot
	rec := &jobstore.JobRecord{JobID: "job_snapshot", Status: jobstore.StatusRunning}
	findCalls := 0
	findJobRecordForSnapshot = func(*jobManager, string) (*jobstore.JobRecord, error) {
		findCalls++
		if findCalls == 2 || findCalls == 4 || findCalls == 9 {
			return nil, errors.New("seed find fault")
		}
		return rec, nil
	}
	windowCalls := 0
	readJobWindowForSnapshot = func(*jobManager, string, int, bool) (string, int64, int64, bool, error) {
		windowCalls++
		if windowCalls == 1 {
			return "", 0, 0, false, errors.New("seed window fault")
		}
		return "match", 5, 0, false, nil
	}
	grepCalls := 0
	grepJobOutputForSnapshot = func(*jobManager, string, *regexp.Regexp) ([]jobstore.Match, error) {
		grepCalls++
		if grepCalls == 1 {
			return nil, errors.New("seed grep fault")
		}
		return []jobstore.Match{{Line: "match"}}, nil
	}
	jobReadFallbackForSnapshot = func(*Session, *jobManager, error) (*jobManager, bool, error) {
		return closedSession.jobManager, true, nil
	}
	_, _ = root.readJobOutputSnapshot(closedSession.jobManager, root, rec.JobID, 10, false, regexp.MustCompile("match"))
	findJobRecordForSnapshot = origFind
	readJobWindowForSnapshot = origWindow
	grepJobOutputForSnapshot = origGrep
	jobReadFallbackForSnapshot = origFallback
	t.Cleanup(func() {
		findJobRecordForSnapshot = origFind
		readJobWindowForSnapshot = origWindow
		grepJobOutputForSnapshot = origGrep
		jobReadFallbackForSnapshot = origFallback
	})

	// Script a continuously growing output window to exhaust the retry budget,
	// then feed that not-ok result through the incremental scanner.
	originalRead := readJobOutputForScan
	originalBytes := jobOutputBytesForScan
	calls := 0
	readJobOutputForScan = func(*jobManager, string, int) (string, int64, bool, error) {
		calls++
		return "x", int64(calls + 1), false, nil
	}
	_, _, _ = readJobOutputFrom(nil, "job_x", 0, 1)
	readJobOutputForScan = func(*jobManager, string, int) (string, int64, bool, error) {
		return "abc", 3, false, nil
	}
	jobOutputBytesForScan = func(*jobManager, string) (int64, error) { return 3, nil }
	g = &jobGrepScan{scanned: 2, lastTotal: 0}
	_ = g.step(nil, "job_x", regexp.MustCompile("x"), 10)
	readJobOutputForScan = func(*jobManager, string, int) (string, int64, bool, error) {
		return "", 0, false, errors.New("seed scan fault")
	}
	g = &jobGrepScan{lastTotal: 0}
	_ = g.step(nil, "job_x", regexp.MustCompile("x"), 10)
	readJobOutputForScan = originalRead
	jobOutputBytesForScan = originalBytes
	t.Cleanup(func() {
		readJobOutputForScan = originalRead
		jobOutputBytesForScan = originalBytes
	})
}
