//go:build evenerfuzz

package agent

import "testing"

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
	} {
		t.Run("job-tool-fixture", test)
	}

	// Pure validation branches.
	_, _ = watchArgsFromToolArgs(map[string]any{"operation": "create", "source": "self", "every": 2})
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

	// Closed-store front doors surface their durable lookup/load errors.
	closedSession := newSession(t)
	if err := closedSession.jobManager.store.Close(); err != nil {
		t.Fatal(err)
	}
	_, _ = jobWatchTool(closedSession, map[string]any{"operation": "clear", "watch_id": "watch_x"}, 100)
	_, _ = jobStatusTool(closedSession, map[string]any{"job_id": "job_x"}, 100)
	_, _ = jobListTool(closedSession, nil, 100)
}
