package agent

import (
	"errors"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
)

// TestCovTerminalRecordPersistError covers terminalRecordPersistError.Error
// and Unwrap (jobs.go lines 56-62).
func TestCovTerminalRecordPersistError(t *testing.T) {
	inner := errors.New("disk full")
	e := &terminalRecordPersistError{status: jobstore.StatusFailed, err: inner}
	if got := e.Error(); !strings.Contains(got, "persist terminal job record") || !strings.Contains(got, "disk full") {
		t.Fatalf("Error() = %q", got)
	}
	if !errors.Is(e, inner) {
		t.Fatal("Unwrap should return inner error")
	}
}

// TestCovJobsDir covers jobsDir (jobs.go lines 534-539).
func TestCovJobsDir(t *testing.T) {
	got := jobsDir("/state", "SESS")
	if got != "/state/sessions/SESS" {
		t.Fatalf("jobsDir with stateDir = %q", got)
	}
	got = jobsDir("", "SESS")
	if !strings.Contains(got, "evener-jobs") || !strings.HasSuffix(got, "SESS") {
		t.Fatalf("jobsDir without stateDir = %q", got)
	}
	got = jobsDir("  ", "SESS")
	if !strings.Contains(got, "evener-jobs") {
		t.Fatalf("jobsDir with whitespace stateDir = %q", got)
	}
}

// TestCovKeepListedJobRow exercises all filter branches of keepListedJobRow
// (jobs.go lines 991-1007).
func TestCovKeepListedJobRow(t *testing.T) {
	sessionID := "sess1"
	owningSession := "sess1"

	// No filter — everything passes.
	rec := &jobstore.JobRecord{JobID: "job1", OwnerSessionID: owningSession}
	if !keepListedJobRow(rec, listFilter{}, sessionID) {
		t.Fatal("empty filter should keep record")
	}

	// Nested job from different session, IncludeNested false.
	nested := &jobstore.JobRecord{JobID: "job2", ParentJobID: "parent", OwnerSessionID: "other"}
	if keepListedJobRow(nested, listFilter{IncludeNested: false}, sessionID) {
		t.Fatal("nested job from different session should be excluded")
	}
	// Nested job with IncludeNested true.
	if !keepListedJobRow(nested, listFilter{IncludeNested: true}, sessionID) {
		t.Fatal("nested job with IncludeNested should be kept")
	}
	// Nested job owned by same session — still kept even without IncludeNested.
	ownNested := &jobstore.JobRecord{JobID: "job3", ParentJobID: "parent", OwnerSessionID: sessionID}
	if !keepListedJobRow(ownNested, listFilter{IncludeNested: false}, sessionID) {
		t.Fatal("nested job owned by same session should be kept")
	}

	// Status filter mismatch.
	if keepListedJobRow(rec, listFilter{Status: jobstore.StatusRunning}, sessionID) {
		t.Fatal("status mismatch should exclude")
	}
	// Status filter match.
	rec.Status = jobstore.StatusRunning
	if !keepListedJobRow(rec, listFilter{Status: jobstore.StatusRunning}, sessionID) {
		t.Fatal("status match should keep")
	}

	// Statuses filter.
	if keepListedJobRow(rec, listFilter{Statuses: []jobstore.Status{jobstore.StatusCompleted}}, sessionID) {
		t.Fatal("statuses mismatch should exclude")
	}
	if !keepListedJobRow(rec, listFilter{Statuses: []jobstore.Status{jobstore.StatusRunning, jobstore.StatusCompleted}}, sessionID) {
		t.Fatal("statuses match should keep")
	}

	// Type filter mismatch.
	delegateType := jobstore.JobType(delegateResourceType)
	if keepListedJobRow(rec, listFilter{Type: delegateType}, sessionID) {
		t.Fatal("type mismatch should exclude")
	}
	// Type filter match.
	rec.Type = jobstore.JobShell
	if !keepListedJobRow(rec, listFilter{Type: jobstore.JobShell}, sessionID) {
		t.Fatal("type match should keep")
	}

	// Types filter.
	if keepListedJobRow(rec, listFilter{Types: []jobstore.JobType{delegateType}}, sessionID) {
		t.Fatal("types mismatch should exclude")
	}
	if !keepListedJobRow(rec, listFilter{Types: []jobstore.JobType{jobstore.JobShell, delegateType}}, sessionID) {
		t.Fatal("types match should keep")
	}
}

// TestCovStatusAllowed covers statusAllowed (jobs.go lines 1010-1012).
func TestCovStatusAllowed(t *testing.T) {
	allowed := []jobstore.Status{jobstore.StatusRunning, jobstore.StatusCompleted}
	if !statusAllowed(jobstore.StatusRunning, allowed) {
		t.Fatal("running should be allowed")
	}
	if statusAllowed(jobstore.StatusFailed, allowed) {
		t.Fatal("failed should not be allowed")
	}
}

// TestCovTypeAllowed covers typeAllowed (jobs.go lines 1014-1016).
func TestCovTypeAllowed(t *testing.T) {
	allowed := []jobstore.JobType{jobstore.JobShell}
	if !typeAllowed(jobstore.JobShell, allowed) {
		t.Fatal("shell should be allowed")
	}
	delegateType := jobstore.JobType(delegateResourceType)
	if typeAllowed(delegateType, allowed) {
		t.Fatal("delegate should not be allowed")
	}
}

// TestCovOutputPathForJob covers outputPathForJob (jobs.go lines 1436-1441).
func TestCovOutputPathForJob(t *testing.T) {
	jm := &jobManager{dir: "/test"}
	// Record with OutputPath — use that.
	rec := &jobstore.JobRecord{OutputPath: "/custom/path.log"}
	if got := jm.outputPathForJob(rec, "job1"); got != "/custom/path.log" {
		t.Fatalf("got %q", got)
	}
	// Record without OutputPath — construct from dir.
	rec.OutputPath = ""
	got := jm.outputPathForJob(rec, "job1")
	if !strings.HasSuffix(got, "job1.log") {
		t.Fatalf("got %q", got)
	}
	// Nil record.
	got = jm.outputPathForJob(nil, "job2")
	if !strings.HasSuffix(got, "job2.log") {
		t.Fatalf("nil rec got %q", got)
	}
}

// TestCovCloseDoneWith_Nil covers closeDoneWith with nil receiver (jobs.go line 357).
func TestCovCloseDoneWith_Nil(t *testing.T) {
	var run *runningJob
	run.closeDoneWith(runningJobCompletionDurable) // should not panic
}

// TestCovStampLastActivityLocked_NilRun covers stampLastActivityLocked nil paths
// (jobs.go lines 370-377).
func TestCovStampLastActivityLocked_NilRun(t *testing.T) {
	jm := &jobManager{running: map[string]*runningJob{}}
	jm.stampLastActivityLocked("nonexistent") // should not panic
}

// TestCovNoteJobActivity_NilAndEmpty covers noteJobActivity nil/empty guards
// (jobs.go lines 380-395).
func TestCovNoteJobActivity_NilAndEmpty(t *testing.T) {
	var jm *jobManager
	jm.noteJobActivity("job1", "phase") // nil manager — no panic

	nowFn := func() time.Time { return time.Now() }
	jm = &jobManager{running: map[string]*runningJob{}, now: nowFn}
	jm.noteJobActivity("", "phase")            // empty jobID — no panic
	jm.noteJobActivity("  ", "phase")          // whitespace jobID — no panic
	jm.noteJobActivity("nonexistent", "phase") // no running job — no panic

	// With a running job but nil rec.
	jm.running["job1"] = &runningJob{}
	jm.noteJobActivity("job1", "phase") // nil rec — no panic

	// With a running job and terminal rec — should be no-op.
	rec := &jobstore.JobRecord{Status: jobstore.StatusCompleted}
	jm.running["job2"] = &runningJob{rec: rec}
	jm.noteJobActivity("job2", "running")
	if rec.Phase != "" {
		t.Fatalf("phase should not be set on terminal rec, got %q", rec.Phase)
	}

	// With a running job and non-terminal rec — should set phase.
	rec2 := &jobstore.JobRecord{Status: jobstore.StatusRunning}
	jm.running["job3"] = &runningJob{rec: rec2}
	jm.noteJobActivity("job3", "working")
	if rec2.Phase != "working" {
		t.Fatalf("phase should be set to 'working', got %q", rec2.Phase)
	}
	if rec2.LastActivity == nil {
		t.Fatal("LastActivity should be set")
	}
}

// TestCovJobListFilterFromArgs covers jobListFilterFromArgs (session_tools_jobs.go lines 1664-1698).
func TestCovJobListFilterFromArgs(t *testing.T) {
	// Default filter.
	f, err := jobListFilterFromArgs(map[string]any{})
	if err != nil || f.Limit != defaultJobListLimit || f.Offset != 0 {
		t.Fatalf("default filter = %+v, err = %v", f, err)
	}

	// Custom limit and offset.
	f, err = jobListFilterFromArgs(map[string]any{"limit": 5, "offset": 10})
	if err != nil || f.Limit != 5 || f.Offset != 10 {
		t.Fatalf("custom filter = %+v, err = %v", f, err)
	}

	// Limit exceeding max.
	f, err = jobListFilterFromArgs(map[string]any{"limit": maxJobListLimit + 100})
	if err != nil || f.Limit != maxJobListLimit {
		t.Fatalf("clamped limit = %d, err = %v", f.Limit, err)
	}

	// Limit <= 0 error.
	_, err = jobListFilterFromArgs(map[string]any{"limit": 0})
	if err == nil || !strings.Contains(err.Error(), "limit must be greater than 0") {
		t.Fatalf("expected limit error, got %v", err)
	}
	_, err = jobListFilterFromArgs(map[string]any{"limit": -1})
	if err == nil {
		t.Fatal("expected error for negative limit")
	}

	// Negative offset error.
	_, err = jobListFilterFromArgs(map[string]any{"offset": -1})
	if err == nil || !strings.Contains(err.Error(), "offset must be non-negative") {
		t.Fatalf("expected offset error, got %v", err)
	}

	// Statuses and types.
	f, err = jobListFilterFromArgs(map[string]any{
		"status":              []any{"running", "completed"},
		"type":                []any{"shell"},
		"include_nested":      true,
		"include_descendants": true,
	})
	if err != nil || len(f.Statuses) != 2 || len(f.Types) != 1 || !f.IncludeNested || !f.IncludeDescendants {
		t.Fatalf("statuses/types filter = %+v, err = %v", f, err)
	}
}

// TestCovJobStatusArrayArg covers jobStatusArrayArg (session_tools_jobs.go lines 1701-1723).
func TestCovJobStatusArrayArg(t *testing.T) {
	// Missing key.
	got, err := jobStatusArrayArg(map[string]any{}, "status")
	if err != nil || got != nil {
		t.Fatalf("missing key: got=%v, err=%v", got, err)
	}

	// Not an array.
	_, err = jobStatusArrayArg(map[string]any{"status": "running"}, "status")
	if err == nil || !strings.Contains(err.Error(), "must be an array") {
		t.Fatalf("expected array error, got %v", err)
	}

	// Valid statuses.
	got, err = jobStatusArrayArg(map[string]any{"status": []any{"running", "completed", "failed", "idle", "settling", "stopping", "closed", "exhausted", "cancelled", "stopped"}}, "status")
	if err != nil || len(got) != 10 {
		t.Fatalf("valid statuses: got=%v, err=%v", got, err)
	}

	// Invalid status.
	_, err = jobStatusArrayArg(map[string]any{"status": []any{"bogus"}}, "status")
	if err == nil || !strings.Contains(err.Error(), "invalid job status") {
		t.Fatalf("expected invalid status error, got %v", err)
	}
}

// TestCovJobTypeArrayArg covers jobTypeArrayArg (session_tools_jobs.go lines 1867-1887).
func TestCovJobTypeArrayArg(t *testing.T) {
	// Missing key.
	got, err := jobTypeArrayArg(map[string]any{}, "type")
	if err != nil || got != nil {
		t.Fatalf("missing key: got=%v, err=%v", got, err)
	}

	// Not an array.
	_, err = jobTypeArrayArg(map[string]any{"type": "shell"}, "type")
	if err == nil || !strings.Contains(err.Error(), "must be an array") {
		t.Fatalf("expected array error, got %v", err)
	}

	// Valid types.
	got, err = jobTypeArrayArg(map[string]any{"type": []any{"shell", "delegate"}}, "type")
	if err != nil || len(got) != 2 {
		t.Fatalf("valid types: got=%v, err=%v", got, err)
	}

	// Invalid type.
	_, err = jobTypeArrayArg(map[string]any{"type": []any{"bogus"}}, "type")
	if err == nil || !strings.Contains(err.Error(), "invalid job type") {
		t.Fatalf("expected invalid type error, got %v", err)
	}
}

// TestCovStringArrayArg covers stringArrayArg (session_tools_jobs.go lines 1816-1834).
func TestCovStringArrayArg(t *testing.T) {
	// Missing key.
	got, err := stringArrayArg(map[string]any{}, "key")
	if err != nil || got != nil {
		t.Fatalf("missing key: got=%v, err=%v", got, err)
	}

	// Not an array.
	_, err = stringArrayArg(map[string]any{"key": "val"}, "key")
	if err == nil || !strings.Contains(err.Error(), "must be an array") {
		t.Fatalf("expected array error, got %v", err)
	}

	// Valid.
	got, err = stringArrayArg(map[string]any{"key": []any{"a", "b"}}, "key")
	if err != nil || len(got) != 2 {
		t.Fatalf("valid: got=%v, err=%v", got, err)
	}

	// Non-string value.
	_, err = stringArrayArg(map[string]any{"key": []any{123}}, "key")
	if err == nil || !strings.Contains(err.Error(), "must be strings") {
		t.Fatalf("expected string error, got %v", err)
	}
}

// TestCovWatchEventFilterArg covers watchEventFilterArg (session_tools_jobs.go lines 1786-1813).
func TestCovWatchEventFilterArg(t *testing.T) {
	// Missing key.
	f, err := watchEventFilterArg(map[string]any{})
	if err != nil || f != nil {
		t.Fatalf("missing key: f=%v, err=%v", f, err)
	}

	// Not an object.
	_, err = watchEventFilterArg(map[string]any{"event_filter": "val"})
	if err == nil || !strings.Contains(err.Error(), "must be an object") {
		t.Fatalf("expected object error, got %v", err)
	}

	// Non-string value.
	_, err = watchEventFilterArg(map[string]any{"event_filter": map[string]any{"tool_name": 123}})
	if err == nil || !strings.Contains(err.Error(), "must be a string") {
		t.Fatalf("expected string error, got %v", err)
	}

	// Unknown field.
	_, err = watchEventFilterArg(map[string]any{"event_filter": map[string]any{"bogus": "val"}})
	if err == nil || !strings.Contains(err.Error(), "unknown event_filter field") {
		t.Fatalf("expected unknown field error, got %v", err)
	}

	// Valid tool_name.
	f, err = watchEventFilterArg(map[string]any{"event_filter": map[string]any{"tool_name": "exec_command"}})
	if err != nil || f == nil || f.ToolName != "exec_command" {
		t.Fatalf("tool_name: f=%v, err=%v", f, err)
	}

	// Valid status.
	f, err = watchEventFilterArg(map[string]any{"event_filter": map[string]any{"status": "ok"}})
	if err != nil || f == nil || f.Status != "ok" {
		t.Fatalf("status: f=%v, err=%v", f, err)
	}

	// Both empty after trim → nil filter.
	f, err = watchEventFilterArg(map[string]any{"event_filter": map[string]any{"tool_name": "  ", "status": ""}})
	if err != nil || f != nil {
		t.Fatalf("empty filter: f=%v, err=%v", f, err)
	}
}

// TestCovWatchArgsFromToolArgs covers watchArgsFromToolArgs error paths
// (session_tools_jobs.go lines 1725+).
func TestCovWatchArgsFromToolArgs(t *testing.T) {
	// Missing operation.
	_, err := watchArgsFromToolArgs(map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "operation is required") {
		t.Fatalf("expected operation error, got %v", err)
	}

	// Has target — rejected.
	_, err = watchArgsFromToolArgs(map[string]any{"operation": "create", "target": "self"})
	if err == nil || !strings.Contains(err.Error(), "uses source, not target") {
		t.Fatalf("expected target error, got %v", err)
	}

	// Has send — rejected.
	_, err = watchArgsFromToolArgs(map[string]any{"operation": "create", "send": map[string]any{}})
	if err == nil || !strings.Contains(err.Error(), "send is not a public argument") {
		t.Fatalf("expected send error, got %v", err)
	}

	// Has receiver_session_id — rejected.
	_, err = watchArgsFromToolArgs(map[string]any{"operation": "create", "receiver_session_id": "sess"})
	if err == nil || !strings.Contains(err.Error(), "derives its receiver") {
		t.Fatalf("expected receiver error, got %v", err)
	}

	// Has receiver_delegate_id — rejected.
	_, err = watchArgsFromToolArgs(map[string]any{"operation": "create", "receiver_delegate_id": "dlg"})
	if err == nil || !strings.Contains(err.Error(), "derives its receiver") {
		t.Fatalf("expected receiver error, got %v", err)
	}
}

// TestCovJobListItemActivity covers jobListItemActivity (session_tools_jobs.go lines 720-736).
func TestCovJobListItemActivity(t *testing.T) {
	// LatestActivitySortAt set — returned directly.
	now := time.Now()
	item := jobListEntry{LatestActivitySortAt: now}
	if got := jobListItemActivity(item); !got.Equal(now) {
		t.Fatal("should return LatestActivitySortAt")
	}

	// No sort time, LastActivity set.
	lastAct := time.Now().Add(-time.Hour).Format(time.RFC3339Nano)
	item = jobListEntry{LastActivity: &lastAct}
	got := jobListItemActivity(item)
	if got.IsZero() {
		t.Fatal("should parse LastActivity")
	}

	// No sort time, no LastActivity, EndedAt set.
	endedAt := time.Now().Add(-time.Hour).Format(time.RFC3339Nano)
	item = jobListEntry{EndedAt: &endedAt}
	got = jobListItemActivity(item)
	if got.IsZero() {
		t.Fatal("should parse EndedAt")
	}

	// No sort time, no LastActivity, no EndedAt, StartedAt set.
	started := time.Now().Add(-2 * time.Hour).Format(time.RFC3339Nano)
	item = jobListEntry{StartedAt: started}
	got = jobListItemActivity(item)
	if got.IsZero() {
		t.Fatal("should parse StartedAt")
	}

	// Nothing set — zero time.
	item = jobListEntry{}
	got = jobListItemActivity(item)
	if !got.IsZero() {
		t.Fatal("should return zero time")
	}

	// Invalid LastActivity — falls through.
	bad := "not-a-time"
	item = jobListEntry{LastActivity: &bad}
	got = jobListItemActivity(item)
	if !got.IsZero() {
		t.Fatal("invalid LastActivity should fall through to zero")
	}

	// Empty LastActivity string — falls through.
	empty := ""
	item = jobListEntry{LastActivity: &empty}
	got = jobListItemActivity(item)
	if !got.IsZero() {
		t.Fatal("empty LastActivity should fall through")
	}
}

// TestCovStableJobListItemMatches covers stableJobListItemMatches
// (session_tools_jobs.go lines 702-718).
func TestCovStableJobListItemMatches(t *testing.T) {
	item := jobListEntry{Status: "running", Type: "shell"}

	// No filter — passes.
	if !stableJobListItemMatches(item, listFilter{}) {
		t.Fatal("empty filter should match")
	}

	// Status match.
	if !stableJobListItemMatches(item, listFilter{Status: jobstore.StatusRunning}) {
		t.Fatal("status match should pass")
	}

	// Status mismatch.
	if stableJobListItemMatches(item, listFilter{Status: jobstore.StatusCompleted}) {
		t.Fatal("status mismatch should fail")
	}

	// Statuses match.
	if !stableJobListItemMatches(item, listFilter{Statuses: []jobstore.Status{jobstore.StatusRunning}}) {
		t.Fatal("statuses match should pass")
	}

	// Statuses mismatch.
	if stableJobListItemMatches(item, listFilter{Statuses: []jobstore.Status{jobstore.StatusCompleted}}) {
		t.Fatal("statuses mismatch should fail")
	}

	// Type match.
	if !stableJobListItemMatches(item, listFilter{Type: jobstore.JobShell}) {
		t.Fatal("type match should pass")
	}

	// Type mismatch.
	delegateType := jobstore.JobType(delegateResourceType)
	if stableJobListItemMatches(item, listFilter{Type: delegateType}) {
		t.Fatal("type mismatch should fail")
	}

	// Types match.
	if !stableJobListItemMatches(item, listFilter{Types: []jobstore.JobType{jobstore.JobShell}}) {
		t.Fatal("types match should pass")
	}

	// Types mismatch.
	if stableJobListItemMatches(item, listFilter{Types: []jobstore.JobType{delegateType}}) {
		t.Fatal("types mismatch should fail")
	}
}

// TestCovFormatJobWatchList covers formatJobWatchList (session_tools_jobs.go lines 1590-1613).
func TestCovFormatJobWatchList(t *testing.T) {
	// Empty watches.
	if got := formatJobWatchList(jobWatchListToolResult{}); got != "no watches" {
		t.Fatalf("empty = %q", got)
	}

	// Active watches.
	out := jobWatchListToolResult{
		Watches: []jobWatchInspectToolResult{
			{WatchID: "w1", Watching: true, Source: "self"},
			{WatchID: "w2", Watching: false, Source: "self", Condition: "output_match"},
		},
		RecentWatches: []jobWatchInspectToolResult{
			{WatchID: "w3", EndReason: "cleared", Source: "self", Condition: "progress"},
		},
	}
	got := formatJobWatchList(out)
	if !strings.Contains(got, "w1") || !strings.Contains(got, "watching") || !strings.Contains(got, "pending") || !strings.Contains(got, "cleared") {
		t.Fatalf("output missing expected parts: %q", got)
	}
}

// TestCovFormatJobWatchEventFilter covers formatJobWatchEventFilter
// (session_tools_jobs.go lines 1573-1588).
func TestCovFormatJobWatchEventFilter(t *testing.T) {
	// Nil filter.
	if got := formatJobWatchEventFilter(nil); got != "" {
		t.Fatalf("nil = %q", got)
	}

	// Empty filter.
	if got := formatJobWatchEventFilter(&jobWatchToolEventFilter{}); got != "" {
		t.Fatalf("empty = %q", got)
	}

	// ToolName only.
	got := formatJobWatchEventFilter(&jobWatchToolEventFilter{ToolName: "exec"})
	if !strings.Contains(got, "tool_name=exec") || !strings.HasPrefix(got, "where ") {
		t.Fatalf("tool_name = %q", got)
	}

	// Status only.
	got = formatJobWatchEventFilter(&jobWatchToolEventFilter{Status: "ok"})
	if !strings.Contains(got, "status=ok") {
		t.Fatalf("status = %q", got)
	}

	// Both.
	got = formatJobWatchEventFilter(&jobWatchToolEventFilter{ToolName: "exec", Status: "ok"})
	if !strings.Contains(got, "tool_name=exec") || !strings.Contains(got, "status=ok") {
		t.Fatalf("both = %q", got)
	}
}

// TestCovFormatJobStop covers formatJobStop (session_tools_jobs.go lines 1208-1248).
func TestCovFormatJobStop(t *testing.T) {
	// Shell job.
	reason := "done"
	got := formatJobStop(jobStopResult{ID: "job1", Type: "shell", Status: "completed", Outcome: "done", Reason: &reason})
	if !strings.Contains(got, "shell") || !strings.Contains(got, "job1") || !strings.Contains(got, "completed") || !strings.Contains(got, "done") {
		t.Fatalf("shell stop = %q", got)
	}

	// Shell job with JobID when ID empty.
	got = formatJobStop(jobStopResult{JobID: "job2", Type: "shell", Status: "stopped", Outcome: "killed"})
	if !strings.Contains(got, "job2") {
		t.Fatalf("jobid fallback = %q", got)
	}

	// Delegate with previous status.
	got = formatJobStop(jobStopResult{ID: "dlg1", Type: "delegate", Status: "stopped", Outcome: "killed", PreviousStatus: "running"})
	if !strings.Contains(got, "was running") {
		t.Fatalf("delegate with prev = %q", got)
	}

	// Delegate with requested by.
	got = formatJobStop(jobStopResult{ID: "dlg1", Type: "delegate", Status: "stopped", Outcome: "killed", RequestedBy: "user"})
	if !strings.Contains(got, "requested by: user") {
		t.Fatalf("delegate requested = %q", got)
	}

	// Delegate resumable yes.
	resumable := true
	got = formatJobStop(jobStopResult{ID: "dlg1", Type: "delegate", Status: "stopped", Outcome: "killed", Resumable: &resumable})
	if !strings.Contains(got, "resumable: yes") {
		t.Fatalf("delegate resumable yes = %q", got)
	}

	// Delegate resumable no.
	resumable = false
	got = formatJobStop(jobStopResult{ID: "dlg1", Type: "delegate", Status: "stopped", Outcome: "killed", Resumable: &resumable, NotResumableReason: "exhausted"})
	if !strings.Contains(got, "resumable: no (exhausted)") {
		t.Fatalf("delegate resumable no = %q", got)
	}

	// Delegate resumable no, no reason.
	got = formatJobStop(jobStopResult{ID: "dlg1", Type: "delegate", Status: "stopped", Outcome: "killed", Resumable: &resumable})
	if !strings.Contains(got, "resumable: no (not resumable)") {
		t.Fatalf("delegate resumable no default = %q", got)
	}

	// Delegate with scratch path.
	got = formatJobStop(jobStopResult{ID: "dlg1", Type: "delegate", Status: "stopped", Outcome: "killed", ScratchPath: "/tmp/scratch"})
	if !strings.Contains(got, "scratch: /tmp/scratch") {
		t.Fatalf("delegate scratch = %q", got)
	}

	// Delegate with worktree.
	got = formatJobStop(jobStopResult{
		ID: "dlg1", Type: "delegate", Status: "stopped", Outcome: "killed",
		Worktree: &delegateWorktreeToolResult{Path: "/wt", Branch: "br", HeadSHA: "abc", Ahead: 3, Dirty: true},
	})
	if !strings.Contains(got, "worktree:") || !strings.Contains(got, "path=/wt") || !strings.Contains(got, "branch=br") {
		t.Fatalf("delegate worktree = %q", got)
	}

	// Delegate with empty reason pointer.
	emptyReason := ""
	got = formatJobStop(jobStopResult{ID: "dlg1", Type: "delegate", Status: "stopped", Outcome: "killed", Reason: &emptyReason})
	if strings.Contains(got, "·  ·") {
		t.Fatalf("empty reason should not add empty part: %q", got)
	}
}
