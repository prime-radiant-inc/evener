//go:build serffuzz

package agent

import (
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// seed100ToolsRangeC covers the result projection, formatting, bounding, and
// argument-validation branches in the middle of session_tools_jobs.go.
func seed100ToolsRangeC(t *testing.T) {
	t.Helper()

	wt := &delegateWorktreeReport{Path: "/tmp/seed", Branch: "seed", Ahead: 2, Dirty: true}
	sb := &delegateSandboxReport{Mode: "workspace-write", Network: true}
	_ = delegateWorktreeToolResultFrom(wt)
	_ = delegateSandboxToolResultFrom(sb)

	_, _ = marshalDelegateSendResult(sendMessageResult{
		DelegateID: "dlg_seed", Type: delegateResourceType, Status: jobstore.StatusCompleted,
		Action: "send", Output: "reply", Truncated: true, TimedOut: true,
		StructuredResult: map[string]any{"ok": true}, StructuredResultValidSet: true,
		StructuredResultValid: true, Worktree: wt,
	}, 1)
	valid := true
	output := "reply\n"
	_ = formatDelegateSend(delegateSendResult{
		DelegateID: "dlg_seed", Status: "completed",
		Action: "send", Output: &output, RunningInBackground: true,
		WaitIgnoredReason: "idle",
		StructuredResult:  map[string]any{"ok": true}, StructuredResultValid: &valid,
	})
	_ = formatDelegateSend(delegateSendResult{Action: "send", StructuredResult: make(chan int)})

	_, _ = marshalWatchResult(watchResult{
		WatchID: "watch_seed", Source: "self", Watching: true,
		Events: []string{"tool_result"}, EventFilter: &watchEventFilter{ToolName: "shell", Status: "completed"},
		Send: &watchSendArgs{To: "parent", Message: "done", IncludeExcerpt: true},
	}, 1)
	_, _ = marshalWatchListResult(jobWatchListToolResult{}, 1)
	_, _ = marshalWatchInspectResult(jobWatchInspectToolResult{}, 1)

	_ = formatJobWatch(jobWatchToolResult{
		WatchID: "watch_seed", Source: "self", Watching: true,
		OutputMatch: "done", Events: []string{"tool_result"},
		EventFilter:        &jobWatchToolEventFilter{ToolName: "shell", Status: "completed"},
		ProgressIntervalMS: 10, Send: &jobWatchToolSendArgs{To: "parent"},
		ReplacedExisting: true, Fired: true, Status: "completed",
	})
	_ = formatJobWatchEventFilter(nil)
	_ = formatJobWatchEventFilter(&jobWatchToolEventFilter{})
	_ = formatJobWatchEventFilter(&jobWatchToolEventFilter{Status: "failed"})
	_ = formatJobWatchList(jobWatchListToolResult{
		Watches:       []jobWatchInspectToolResult{{WatchID: "active", Source: "self", Condition: "terminal", Watching: false}},
		RecentWatches: []jobWatchInspectToolResult{{WatchID: "recent", Source: "parent", Condition: "output", EndReason: "cleared"}},
	})
	for _, out := range []jobWatchInspectToolResult{
		{WatchID: "ended", Source: "self", EndReason: "cleared"},
		{WatchID: "pending", Source: "parent", Condition: "terminal"},
		{WatchID: "missing"},
	} {
		_ = formatJobWatchInspect(out)
	}

	_, _ = marshalStableDelegateCreateResult(stableDelegateCreateResult{
		DelegateID: "dlg_seed", ChildSessionID: "child-dlg_seed", Type: delegateResourceType,
		Status: string(jobstore.StatusRunning), TranscriptRef: "local:child-dlg_seed",
		Sandbox: delegateSandboxToolResultFrom(sb),
	}, 4096)

	_, _ = jobListFilterFromArgs(map[string]any{"status": "running"})
	_, _ = jobListFilterFromArgs(map[string]any{"type": "shell"})
	_, _ = jobStatusArrayArg(map[string]any{}, "status")
	_, _ = jobStatusArrayArg(map[string]any{"status": "running"}, "status")
	_, _ = jobStatusArrayArg(map[string]any{"status": []any{"bogus"}}, "status")
	_, _ = watchArgsFromToolArgs(map[string]any{"operation": "create", "target": "self"})
	_, _ = watchArgsFromToolArgs(map[string]any{
		"operation": "create", "source": "self", "progress_interval_ms": 10, "every": 2,
		"events": "terminal",
	})
	_, _ = watchArgsFromToolArgs(map[string]any{"operation": "create", "source": "dlg_seed"})
}
