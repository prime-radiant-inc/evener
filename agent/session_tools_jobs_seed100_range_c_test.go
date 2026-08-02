//go:build serffuzz

package agent

import (
	"encoding/json"
	"errors"
	"strings"
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
		MessageType: "runtime", Delivered: true, Action: "steer",
	}, 1)
	_, _ = marshalDelegateSendResult(sendMessageResult{
		DelegateID: "dlg_seed", StartedJobID: "job_start", JobID: "job_current",
		LatestJobID: "job_latest", Type: "message", Status: jobstore.StatusCompleted,
		Action: "send", Output: "reply", Truncated: true, TimedOut: true,
		StructuredResult: map[string]any{"ok": true}, StructuredResultValidSet: true,
		StructuredResultValid: true, Worktree: wt,
	}, 1)
	_ = deliveredStatus(false)

	valid := true
	output := "reply\n"
	_ = formatDelegateSend(delegateSendResult{
		DelegateID: "dlg_seed", StartedJobID: "job_seed", Status: "completed",
		Action: "send", Output: &output, RunningInBackground: true, Watching: true,
		WaitIgnoredReason: "idle", Watches: []watchListEntry{{ID: "watch_seed", Source: "self", Condition: "terminal"}},
		StructuredResult: map[string]any{"ok": true}, StructuredResultValid: &valid,
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

	_, _ = marshalDelegateResult(delegateResult{
		DelegateID: "dlg_seed", JobID: "job_seed", Type: "task",
		Status: jobstore.StatusCompleted, Output: "done", TimedOut: true,
		StructuredResult: map[string]any{"ok": true}, StructuredResultValidSet: true,
		StructuredResultValid: true, Worktree: wt, Sandbox: sb,
	}, 4096)
	_, _ = marshalBoundedDelegateResult(delegateToolResult{
		JobID: "job_seed", Output: ptrString(strings.Repeat("x", 64)),
		StructuredResult: strings.Repeat("y", 256),
	}, 16)
	changing := &seed100ChangingJSON{}
	_, _ = marshalBoundedDelegateResult(delegateToolResult{
		JobID: "job_seed", Output: ptrString(""), StructuredResult: changing,
	}, 256)
	_, _, _ = marshalDelegateResultWithOutputLimit(delegateToolResult{
		JobID: "job_seed", Output: ptrString("x"), StructuredResult: make(chan int),
	}, 100)
	want := errors.New("seed marshal fault")
	_, _, _ = marshalWithOutputLimit(100, 1, func(int) (string, error) { return "", want })

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

type seed100ChangingJSON struct{ calls int }

func (v *seed100ChangingJSON) MarshalJSON() ([]byte, error) {
	v.calls++
	if v.calls == 1 {
		return json.Marshal(strings.Repeat("x", 512))
	}
	return []byte(`"ok"`), nil
}
