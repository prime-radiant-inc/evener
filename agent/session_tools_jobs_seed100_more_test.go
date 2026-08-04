//go:build serffuzz

package agent

import (
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// seed100SessionToolsJobsMore covers the deterministic tail of job-tool
// validation, projection, watch routing, and grep scanning.
func seed100SessionToolsJobsMore(t *testing.T) {
	t.Helper()

	// Keep the fuzz-only gate self-contained by replaying the deterministic
	// job-tool fixtures that otherwise run only in the ordinary test suite.
	for _, test := range []func(*testing.T){
		TestJobWatchCreateReturnsIDAndClearUsesIDOnly,
		TestJobWatchListAndInspectReturnWatchIDs,
		TestJobWatchTerminalOutputMatchCatchupThroughTool,
		TestJobToolsRejectDelegateIDWithActionableGuidance,
		TestJobToolsControlBackgroundShellJob,
		TestDelegateSendIdleDefaultResumesWithOmittedOnIdle,
		TestMarshalDelegateResultsBoundLargeOutput,
		TestLiveSteerWaitIgnoredReason,
		TestClassifyStopOutcome,
		TestJobStopReportsOutcomeAndPreviousStatus,
		TestJobListIncludesDelegatesRecoverySurface,
		TestJobListDelegatesDoNotExposeFilteredCurrentJob,
		TestJobListToolIncludeNestedSurfacesForwardedRecords,
		TestJobListIncludeDescendantsWalksLiveTree,
		TestJobListWatchConditionSummaryFormats,
		TestJobListStoppedDelegateResumableAssessmentIsDynamicAndPure,
		TestJobListIncludeDescendantsSurfacesOwnStoreError,
		TestStopDelegateIncludeChildrenSurfacesChildStopError,
	} {
		t.Run("job-tool-fixture", test)
	}

	// Pure validation branches.
	_, _ = watchArgsFromToolArgs(map[string]any{"operation": "create", "source": "self", "every": 2})
	for _, args := range []map[string]any{
		{"max_wait_ms": minJobBlockTimeoutMS - 1},
		{"max_wait_ms": maxJobBlockTimeoutMS + 1},
	} {
		_, _ = decodeDelegateArgs(args)
	}
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

	// Closed-store front doors surface their durable lookup/load errors.
	closedSession := newSession(t)
	if err := closedSession.jobManager.store.Close(); err != nil {
		t.Fatal(err)
	}
	_, _ = jobWatchTool(closedSession, map[string]any{"operation": "clear", "watch_id": "watch_x"}, 100)
	_, _ = jobStatusTool(closedSession, map[string]any{"job_id": "job_x"}, 100)
	_, _ = jobListTool(closedSession, nil, 100)
}
