package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// TestCovClearDescendantReceiverWatchByID covers clearDescendantReceiverWatchByID
// (session_tools_jobs.go lines 298-300): a thin wrapper around
// clearStableReceiverWatchByID.
func TestCovClearDescendantReceiverWatchByID(t *testing.T) {
	s := &Session{}
	// No delegate controller, no subagents — should return (zero, false, nil).
	res, found, err := s.clearDescendantReceiverWatchByID("watch_nonexistent")
	if err != nil {
		t.Fatalf("clearDescendantReceiverWatchByID: %v", err)
	}
	if found {
		t.Fatal("should not find watch with no controller")
	}
	if res.Watching {
		t.Fatal("result should not be watching")
	}
}

// TestCovLiveSteerWaitIgnoredReason covers liveSteerWaitIgnoredReason
// (session_tools_jobs.go lines 129-134): all branches.
func TestCovLiveSteerWaitIgnoredReason(t *testing.T) {
	const wantIgnored = "live steer returns on delivery; max_wait_ms applies only to started jobs"
	// blockTimeoutMS=0 — not requested, empty result.
	if got := liveSteerWaitIgnoredReason(0, jobstore.StatusRunning, "steered"); got != "" {
		t.Fatalf("zero timeout should return empty, got %q", got)
	}

	// blockTimeoutMS>0, status=running, action=steered — should return message.
	if got := liveSteerWaitIgnoredReason(1000, jobstore.StatusRunning, "steered"); got != wantIgnored {
		t.Fatalf("running+steered = %q, want %q", got, wantIgnored)
	}

	// blockTimeoutMS>0, action=delivered — should return message regardless of status.
	if got := liveSteerWaitIgnoredReason(1000, jobstore.StatusCompleted, "delivered"); got != wantIgnored {
		t.Fatalf("delivered = %q, want %q", got, wantIgnored)
	}

	// blockTimeoutMS>0, status=completed, action=steered — NOT the condition.
	if got := liveSteerWaitIgnoredReason(1000, jobstore.StatusCompleted, "steered"); got != "" {
		t.Fatalf("completed+steered should return empty, got %q", got)
	}

	// blockTimeoutMS>0, status=running, action=started — NOT the condition.
	if got := liveSteerWaitIgnoredReason(1000, jobstore.StatusRunning, "started"); got != "" {
		t.Fatalf("running+started should return empty, got %q", got)
	}
}

// TestCovWatchInspectFound covers watchInspectFound
// (session_tools_jobs.go lines 336-338).
func TestCovWatchInspectFound(t *testing.T) {
	// Empty result — not found.
	if watchInspectFound(jobWatchInspectToolResult{}) {
		t.Fatal("empty result should not be found")
	}

	// Watching=true — found.
	if !watchInspectFound(jobWatchInspectToolResult{Watching: true}) {
		t.Fatal("watching=true should be found")
	}

	// Source non-empty — found.
	if !watchInspectFound(jobWatchInspectToolResult{Source: "job_1"}) {
		t.Fatal("source non-empty should be found")
	}

	// EndReason non-empty — found.
	if !watchInspectFound(jobWatchInspectToolResult{EndReason: "cleared"}) {
		t.Fatal("end_reason non-empty should be found")
	}
}

// TestCovMarshalBoundedJSON covers marshalBoundedJSON
// (session_tools_jobs.go lines 1978-1987).
func TestCovMarshalBoundedJSON2(t *testing.T) {
	// Successful marshal within bounds.
	got, err := marshalBoundedJSON(map[string]any{"key": "val"}, 1000)
	if err != nil {
		t.Fatalf("marshalBoundedJSON: %v", err)
	}
	if got != `{"key":"val"}` {
		t.Fatalf("bounded JSON = %q, want compact object", got)
	}

	// Exceeds bounds — error.
	_, err = marshalBoundedJSON(map[string]any{"key": "val"}, 5)
	if err == nil {
		t.Fatal("exceeding bounds should return error")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error should mention exceeds: %v", err)
	}

	// maxChars=0 — no bounding.
	got, err = marshalBoundedJSON(map[string]any{"key": "val"}, 0)
	if err != nil || got != `{"key":"val"}` {
		t.Fatalf("maxChars=0 should not bound: %q %v", got, err)
	}

	// Marshal error — unmarshallable type.
	_, err = marshalBoundedJSON(make(chan int), 1000)
	if err == nil {
		t.Fatal("channel should cause marshal error")
	}
}

// TestCovMarshalBoundedJSONWithFit covers marshalBoundedJSONWithFit
// (session_tools_jobs.go lines 1992-2001).
func TestCovMarshalBoundedJSONWithFit2(t *testing.T) {
	// Fits within bounds.
	got, fits, err := marshalBoundedJSONWithFit(map[string]any{"k": "v"}, 1000)
	if err != nil || !fits || got != `{"k":"v"}` {
		t.Fatalf("should fit: got=%q fits=%v err=%v", got, fits, err)
	}

	// Does not fit.
	got, fits, err = marshalBoundedJSONWithFit(map[string]any{"k": "v"}, 5)
	if err != nil || fits || got != "" {
		t.Fatalf("should not fit: got=%q fits=%v err=%v", got, fits, err)
	}

	// maxChars<=0 — always fits.
	got, fits, err = marshalBoundedJSONWithFit(map[string]any{"k": "v"}, -1)
	if err != nil || !fits || got != `{"k":"v"}` {
		t.Fatalf("negative maxChars = (%q, %v, %v), want exact JSON fit", got, fits, err)
	}

	// Marshal error.
	_, _, err = marshalBoundedJSONWithFit(make(chan int), 1000)
	if err == nil {
		t.Fatal("channel should cause marshal error")
	}
}

// TestCovSessionJobManager covers sessionJobManager
// (session_tools_jobs.go lines 1637-1642).
func TestCovSessionJobManager(t *testing.T) {
	// nil session — error.
	_, err := sessionJobManager(nil)
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("nil session: %v", err)
	}

	// nil jobManager — error.
	s := &Session{}
	_, err = sessionJobManager(s)
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("nil jm: %v", err)
	}

	// Valid — returns jm.
	jm := newTestJM(t)
	s2 := &Session{jobManager: jm}
	got, err := sessionJobManager(s2)
	if err != nil || got != jm {
		t.Fatalf("valid session: got=%v err=%v", got, err)
	}
}

// TestCovSessionRunningJobIDs covers sessionRunningJobIDs
// (session_tools_jobs.go lines 1648-1662).
func TestCovSessionRunningJobIDs(t *testing.T) {
	// nil session — nil.
	if got := sessionRunningJobIDs(nil); got != nil {
		t.Fatal("nil session should return nil")
	}

	// No jobManager — nil.
	s := &Session{}
	if got := sessionRunningJobIDs(s); got != nil {
		t.Fatal("no jm should return nil")
	}

	// JobManager with no running jobs — nil.
	jm := newTestJM(t)
	s2 := &Session{jobManager: jm}
	if got := sessionRunningJobIDs(s2); got != nil {
		t.Fatalf("no running jobs should return nil, got %v", got)
	}

	// With a running job — returns its ID.
	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	ids := sessionRunningJobIDs(s2)
	if len(ids) != 1 || ids[0] != rec.JobID {
		t.Fatalf("running job IDs = %v, want [%s]", ids, rec.JobID)
	}
}

// TestCovJobStatusArrayArg covers jobStatusArrayArg
// (session_tools_jobs.go lines 1701-1723): all valid/invalid statuses.
func TestCovJobStatusArrayArg2(t *testing.T) {
	// Not present — nil, no error.
	statuses, err := jobStatusArrayArg(map[string]any{}, "status")
	if err != nil || statuses != nil {
		t.Fatalf("not present: statuses=%v err=%v", statuses, err)
	}

	// Wrong type — error.
	_, err = jobStatusArrayArg(map[string]any{"status": "not-array"}, "status")
	if err == nil || !strings.Contains(err.Error(), "must be an array") {
		t.Fatalf("wrong type: %v", err)
	}

	// Valid statuses.
	statuses, err = jobStatusArrayArg(map[string]any{"status": []any{"running", "completed"}}, "status")
	if err != nil {
		t.Fatalf("valid statuses: %v", err)
	}
	if want := []jobstore.Status{jobstore.StatusRunning, jobstore.StatusCompleted}; !reflect.DeepEqual(statuses, want) {
		t.Fatalf("statuses = %v, want %v", statuses, want)
	}

	// Invalid status value.
	_, err = jobStatusArrayArg(map[string]any{"status": []any{"invalid_status"}}, "status")
	if err == nil || !strings.Contains(err.Error(), "invalid job status") {
		t.Fatalf("invalid status: %v", err)
	}

	// All valid status values.
	validStatuses := []any{
		"running", "idle", "settling", "stopping", "closed",
		"completed", "failed", "exhausted", "cancelled", "stopped",
	}
	statuses, err = jobStatusArrayArg(map[string]any{"status": validStatuses}, "status")
	if err != nil {
		t.Fatalf("all valid: %v", err)
	}
	wantStatuses := []jobstore.Status{
		jobstore.StatusRunning, jobstore.Status("idle"), jobstore.Status("settling"),
		jobstore.Status("stopping"), jobstore.Status("closed"), jobstore.StatusCompleted,
		jobstore.StatusFailed, jobstore.StatusExhausted, jobstore.StatusCancelled,
		jobstore.StatusStopped,
	}
	if !reflect.DeepEqual(statuses, wantStatuses) {
		t.Fatalf("all statuses = %v, want %v", statuses, wantStatuses)
	}

	// Wrong-typed element — named error, not fmt.Sprint noise.
	for _, bad := range []any{float64(1), true, nil, map[string]any{"status": "running"}} {
		_, err = jobStatusArrayArg(map[string]any{"status": []any{bad}}, "status")
		if err == nil || !strings.Contains(err.Error(), "status values must be strings") {
			t.Fatalf("wrong-typed element %#v: err = %v", bad, err)
		}
	}
}

// TestCovJobTypeArrayArg covers jobTypeArrayArg
// (session_tools_jobs.go lines 1867-1887).
func TestCovJobTypeArrayArg2(t *testing.T) {
	// Not present — nil.
	types, err := jobTypeArrayArg(map[string]any{}, "type")
	if err != nil || types != nil {
		t.Fatalf("not present: types=%v err=%v", types, err)
	}

	// Wrong type — error.
	_, err = jobTypeArrayArg(map[string]any{"type": "not-array"}, "type")
	if err == nil || !strings.Contains(err.Error(), "must be an array") {
		t.Fatalf("wrong type: %v", err)
	}

	// Valid types.
	types, err = jobTypeArrayArg(map[string]any{"type": []any{"shell", "delegate"}}, "type")
	if err != nil {
		t.Fatalf("valid types: %v", err)
	}
	if want := []jobstore.JobType{jobstore.JobShell, jobstore.JobType(delegateResourceType)}; !reflect.DeepEqual(types, want) {
		t.Fatalf("types = %v, want %v", types, want)
	}

	// Invalid type.
	_, err = jobTypeArrayArg(map[string]any{"type": []any{"invalid_type"}}, "type")
	if err == nil || !strings.Contains(err.Error(), "invalid job type") {
		t.Fatalf("invalid type: %v", err)
	}

	// Wrong-typed element — named error, not fmt.Sprint noise.
	for _, bad := range []any{float64(1), true, nil, map[string]any{"type": "shell"}} {
		_, err = jobTypeArrayArg(map[string]any{"type": []any{bad}}, "type")
		if err == nil || !strings.Contains(err.Error(), "type values must be strings") {
			t.Fatalf("wrong-typed element %#v: err = %v", bad, err)
		}
	}
}

// TestCovWatchArgsFromToolArgs covers watchArgsFromToolArgs
// (session_tools_jobs.go lines 1725-1784): validation branches.
func TestCovWatchArgsFromToolArgs2(t *testing.T) {
	// Missing operation.
	_, err := watchArgsFromToolArgs(map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "operation is required") {
		t.Fatalf("missing op: %v", err)
	}

	// Using "target" instead of "source".
	_, err = watchArgsFromToolArgs(map[string]any{"operation": "create", "target": "job_1"})
	if err == nil || !strings.Contains(err.Error(), "uses source, not target") {
		t.Fatalf("target arg: %v", err)
	}

	// Using "send" as public arg.
	_, err = watchArgsFromToolArgs(map[string]any{"operation": "create", "source": "job_1", "send": map[string]any{}})
	if err == nil || !strings.Contains(err.Error(), "send is not a public argument") {
		t.Fatalf("send arg: %v", err)
	}

	// Using "receiver_session_id".
	_, err = watchArgsFromToolArgs(map[string]any{"operation": "create", "source": "job_1", "receiver_session_id": "x"})
	if err == nil || !strings.Contains(err.Error(), "derives its receiver") {
		t.Fatalf("receiver_session_id: %v", err)
	}

	// Using "receiver_delegate_id".
	_, err = watchArgsFromToolArgs(map[string]any{"operation": "create", "source": "job_1", "receiver_delegate_id": "x"})
	if err == nil || !strings.Contains(err.Error(), "derives its receiver") {
		t.Fatalf("receiver_delegate_id: %v", err)
	}

	// Create without source.
	_, err = watchArgsFromToolArgs(map[string]any{"operation": "create"})
	if err == nil || !strings.Contains(err.Error(), "source is required") {
		t.Fatalf("create without source: %v", err)
	}

	// List with source — error.
	_, err = watchArgsFromToolArgs(map[string]any{"operation": "list", "source": "job_1"})
	if err == nil || !strings.Contains(err.Error(), "list requires no source") {
		t.Fatalf("list with source: %v", err)
	}

	// List with watch_id — error.
	_, err = watchArgsFromToolArgs(map[string]any{"operation": "list", "watch_id": "w1"})
	if err == nil || !strings.Contains(err.Error(), "list requires no") {
		t.Fatalf("list with watch_id: %v", err)
	}

	// Inspect without watch_id.
	_, err = watchArgsFromToolArgs(map[string]any{"operation": "inspect"})
	if err == nil || !strings.Contains(err.Error(), "watch_id is required") {
		t.Fatalf("inspect without watch_id: %v", err)
	}

	// Clear without watch_id.
	_, err = watchArgsFromToolArgs(map[string]any{"operation": "clear"})
	if err == nil || !strings.Contains(err.Error(), "watch_id is required") {
		t.Fatalf("clear without watch_id: %v", err)
	}

	// Unsupported operation.
	_, err = watchArgsFromToolArgs(map[string]any{"operation": "delete", "watch_id": "w1"})
	if err == nil || !strings.Contains(err.Error(), "unsupported operation") {
		t.Fatalf("unsupported op: %v", err)
	}

	// Wildcard source.
	_, err = watchArgsFromToolArgs(map[string]any{"operation": "create", "source": "*"})
	if err == nil || !strings.Contains(err.Error(), "wildcard") {
		t.Fatalf("wildcard: %v", err)
	}

	// Valid create.
	args, err := watchArgsFromToolArgs(map[string]any{
		"operation":            "create",
		"source":               "job_1",
		"output_match":         "ready",
		"progress_interval_ms": 5000,
		"events":               []any{"communicate"},
		"event_filter":         map[string]any{"tool_name": "communicate", "status": "ok"},
		"every":                2,
	})
	if err != nil {
		t.Fatalf("valid create: %v", err)
	}
	if args.Operation != "create" || args.Source != "job_1" || args.OutputMatch != "ready" {
		t.Fatalf("args mismatch: %+v", args)
	}
	if args.ProgressIntervalMS != 5000 {
		t.Fatalf("progress interval = %d, want 5000", args.ProgressIntervalMS)
	}
	if args.Every != 2 {
		t.Fatalf("every = %d, want 2", args.Every)
	}
	if !reflect.DeepEqual(args.Events, []string{"communicate"}) || args.EventFilter == nil || args.EventFilter.ToolName != "communicate" || args.EventFilter.Status != "ok" {
		t.Fatalf("event arguments mismatch: %+v", args)
	}

	// Valid list.
	args, err = watchArgsFromToolArgs(map[string]any{"operation": "list"})
	if err != nil {
		t.Fatalf("valid list: %v", err)
	}
	if args.Operation != "list" {
		t.Fatalf("operation = %q, want list", args.Operation)
	}

	// Valid inspect.
	args, err = watchArgsFromToolArgs(map[string]any{"operation": "inspect", "watch_id": "w_123"})
	if err != nil {
		t.Fatalf("valid inspect: %v", err)
	}
	if args.WatchID != "w_123" {
		t.Fatalf("watch_id = %q, want w_123", args.WatchID)
	}
}

// TestCovWatchEventFilterArg covers watchEventFilterArg
// (session_tools_jobs.go lines 1786-1814).
func TestCovWatchEventFilterArg2(t *testing.T) {
	// Not present — nil.
	f, err := watchEventFilterArg(map[string]any{})
	if err != nil || f != nil {
		t.Fatalf("not present: f=%v err=%v", f, err)
	}

	// Wrong type — error.
	_, err = watchEventFilterArg(map[string]any{"event_filter": "not-object"})
	if err == nil || !strings.Contains(err.Error(), "must be an object") {
		t.Fatalf("wrong type: %v", err)
	}

	// Non-string value — error.
	_, err = watchEventFilterArg(map[string]any{"event_filter": map[string]any{"tool_name": 123}})
	if err == nil || !strings.Contains(err.Error(), "must be a string") {
		t.Fatalf("non-string: %v", err)
	}

	// Unknown field — error.
	_, err = watchEventFilterArg(map[string]any{"event_filter": map[string]any{"unknown": "val"}})
	if err == nil || !strings.Contains(err.Error(), "unknown event_filter field") {
		t.Fatalf("unknown field: %v", err)
	}

	// Both empty — nil filter.
	f, err = watchEventFilterArg(map[string]any{"event_filter": map[string]any{"tool_name": "", "status": ""}})
	if err != nil || f != nil {
		t.Fatalf("both empty: f=%v err=%v", f, err)
	}

	// Valid filter with tool_name only.
	f, err = watchEventFilterArg(map[string]any{"event_filter": map[string]any{"tool_name": "exec_command"}})
	if err != nil {
		t.Fatalf("tool_name only: %v", err)
	}
	if f == nil || f.ToolName != "exec_command" {
		t.Fatalf("filter: %+v", f)
	}

	// Valid filter with status only.
	f, err = watchEventFilterArg(map[string]any{"event_filter": map[string]any{"status": "ok"}})
	if err != nil {
		t.Fatalf("status only: %v", err)
	}
	if f == nil || f.Status != "ok" {
		t.Fatalf("filter: %+v", f)
	}

	// Valid filter with both.
	f, err = watchEventFilterArg(map[string]any{"event_filter": map[string]any{"tool_name": "read_file", "status": "error"}})
	if err != nil {
		t.Fatalf("both: %v", err)
	}
	if f == nil || f.ToolName != "read_file" || f.Status != "error" {
		t.Fatalf("filter: %+v", f)
	}
}

// TestCovStringArrayArg covers stringArrayArg
// (session_tools_jobs.go lines 1816-1834).
func TestCovStringArrayArg2(t *testing.T) {
	// Not present — nil.
	arr, err := stringArrayArg(map[string]any{}, "events")
	if err != nil || arr != nil {
		t.Fatalf("not present: arr=%v err=%v", arr, err)
	}

	// Wrong type — error.
	_, err = stringArrayArg(map[string]any{"events": "not-array"}, "events")
	if err == nil || !strings.Contains(err.Error(), "must be an array") {
		t.Fatalf("wrong type: %v", err)
	}

	// Non-string element — error.
	_, err = stringArrayArg(map[string]any{"events": []any{123}}, "events")
	if err == nil || !strings.Contains(err.Error(), "must be strings") {
		t.Fatalf("non-string: %v", err)
	}

	// Valid.
	arr, err = stringArrayArg(map[string]any{"events": []any{"a", "b"}}, "events")
	if err != nil || !reflect.DeepEqual(arr, []string{"a", "b"}) {
		t.Fatalf("valid: arr=%v err=%v", arr, err)
	}
}

// TestCovWatchSendArg covers watchSendArg
// (session_tools_jobs.go lines 1837-1858).
func TestCovWatchSendArg(t *testing.T) {
	// Not present — nil.
	s, err := watchSendArg(map[string]any{})
	if err != nil || s != nil {
		t.Fatalf("not present: s=%v err=%v", s, err)
	}

	// Wrong type — error.
	_, err = watchSendArg(map[string]any{"send": "not-object"})
	if err == nil || !strings.Contains(err.Error(), "must be an object") {
		t.Fatalf("wrong type: %v", err)
	}

	// Empty send — nil (isEmptyWatchSend).
	s, err = watchSendArg(map[string]any{"send": map[string]any{}})
	if err != nil || s != nil {
		t.Fatalf("empty send: s=%v err=%v", s, err)
	}

	// Send with "to" — valid.
	s, err = watchSendArg(map[string]any{"send": map[string]any{"to": "caller", "message": "hi"}})
	if err != nil {
		t.Fatalf("valid send: %v", err)
	}
	if s == nil || s.To != "caller" || s.Message != "hi" {
		t.Fatalf("send: %+v", s)
	}

	// Send without "to" — error.
	_, err = watchSendArg(map[string]any{"send": map[string]any{"message": "hi"}})
	if err == nil || !strings.Contains(err.Error(), "to is required") {
		t.Fatalf("no to: %v", err)
	}

	// Send with include_excerpt.
	s, err = watchSendArg(map[string]any{"send": map[string]any{"to": "caller", "include_excerpt": true}})
	if err != nil {
		t.Fatalf("include_excerpt: %v", err)
	}
	if s == nil || !s.IncludeExcerpt {
		t.Fatal("include_excerpt should be true")
	}
}

// TestCovIsEmptyWatchSend covers isEmptyWatchSend
// (session_tools_jobs.go lines 1861-1865).
func TestCovIsEmptyWatchSend(t *testing.T) {
	// All empty — true.
	if !isEmptyWatchSend(map[string]any{}) {
		t.Fatal("empty map should be true")
	}

	// With "to" — false.
	if isEmptyWatchSend(map[string]any{"to": "caller"}) {
		t.Fatal("with to should be false")
	}

	// With "message" — false.
	if isEmptyWatchSend(map[string]any{"message": "hi"}) {
		t.Fatal("with message should be false")
	}

	// With "include_excerpt" — false.
	if isEmptyWatchSend(map[string]any{"include_excerpt": true}) {
		t.Fatal("with include_excerpt should be false")
	}
}

// ---- session_queue.go pure functions ----

// TestCovFirstQueueLine covers firstQueueLine (session_queue.go lines 544-549).
func TestCovFirstQueueLine(t *testing.T) {
	// No newline — returns full string with trailing CR trimmed.
	if got := firstQueueLine("hello"); got != "hello" {
		t.Fatalf("no newline: got %q", got)
	}

	// With newline — returns first line.
	if got := firstQueueLine("line1\nline2\nline3"); got != "line1" {
		t.Fatalf("with newline: got %q", got)
	}

	// Trailing CR trimmed.
	if got := firstQueueLine("hello\r"); got != "hello" {
		t.Fatalf("trailing CR: got %q", got)
	}

	// Newline + trailing CR on first line.
	if got := firstQueueLine("hello\r\nworld"); got != "hello" {
		t.Fatalf("CR+newline: got %q", got)
	}

	// Empty string.
	if got := firstQueueLine(""); got != "" {
		t.Fatalf("empty: got %q", got)
	}

	// Only newline.
	if got := firstQueueLine("\nrest"); got != "" {
		t.Fatalf("only newline: got %q", got)
	}
}

// TestCovQueuedEntryPreviewLine covers queuedEntryPreviewLine
// (session_queue.go lines 555-566).
func TestCovQueuedEntryPreviewLine(t *testing.T) {
	// Text present — returns first line.
	got := queuedEntryPreviewLine(queuedInput{Text: "hello world"})
	if got != "hello world" {
		t.Fatalf("text present: got %q", got)
	}

	// Text with newline — first line only.
	got = queuedEntryPreviewLine(queuedInput{Text: "line1\nline2"})
	if got != "line1" {
		t.Fatalf("multiline text: got %q", got)
	}

	// Empty text, one image — [image].
	got = queuedEntryPreviewLine(queuedInput{Images: []ImageAttachment{{}}})
	if got != "[image]" {
		t.Fatalf("one image: got %q", got)
	}

	// Empty text, multiple images — [N images].
	got = queuedEntryPreviewLine(queuedInput{Images: []ImageAttachment{{}, {}, {}}})
	if got != "[3 images]" {
		t.Fatalf("3 images: got %q", got)
	}

	// Empty text, no images — empty.
	got = queuedEntryPreviewLine(queuedInput{})
	if got != "" {
		t.Fatalf("empty entry: got %q", got)
	}

	// Whitespace-only text, one image — [image].
	got = queuedEntryPreviewLine(queuedInput{Text: "  \n  ", Images: []ImageAttachment{{}}})
	if got != "[image]" {
		t.Fatalf("whitespace text + image: got %q", got)
	}
}

// TestCovSteeringMessageToLLM covers steeringMessageToLLM
// (session_queue.go lines 1033-1038).
func TestCovSteeringMessageToLLM(t *testing.T) {
	// Text-only — llm.User(text).
	msg := steeringMessageToLLM(steeringMessage{Text: "hello"})
	if msg.Role != llm.RoleUser {
		t.Fatalf("role = %q, want %q", msg.Role, llm.RoleUser)
	}
	if len(msg.Content) != 1 || msg.Content[0].Text != "hello" {
		t.Fatalf("content mismatch: %+v", msg.Content)
	}

	// With images — multi-part message.
	msg = steeringMessageToLLM(steeringMessage{
		Text:   "see this",
		Images: []ImageAttachment{{MediaType: "image/png", Data: []byte("data")}},
	})
	if msg.Role != llm.RoleUser {
		t.Fatalf("role = %q, want %q", msg.Role, llm.RoleUser)
	}
	want := llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: "see this"},
		{Kind: llm.ContentImage, Image: &llm.ImageData{MediaType: "image/png", Data: []byte("data")}},
	}}
	if !reflect.DeepEqual(msg, want) {
		t.Fatalf("image message = %#v, want %#v", msg, want)
	}
}

// TestCovRouteSystemNotification covers routeSystemNotification
// (session_queue.go lines 128-144).
func TestCovRouteSystemNotification(t *testing.T) {
	// nil session — false.
	var s *Session
	if s.routeSystemNotification("sess_1", "msg") {
		t.Fatal("nil session should return false")
	}

	// Empty receiverSessionID — false.
	s2 := &Session{}
	if s2.routeSystemNotification("", "msg") {
		t.Fatal("empty receiver should return false")
	}

	// Whitespace receiverSessionID — false.
	if s2.routeSystemNotification("  ", "msg") {
		t.Fatal("whitespace receiver should return false")
	}

	// Same session ID — should enqueue system notification.
	// This requires a full session setup; the nil-fields path would panic
	// on enqueueSystemNotification -> trySteerWithProvenanceAndNotify.
	// Test the descendant lookup path with no descendants — false (no parent router).
	s3 := &Session{id: "sess_3"}
	if s3.routeSystemNotification("other_session", "msg") {
		t.Fatal("non-matching session with no descendants/parent should return false")
	}

	// With parent system notification router.
	called := false
	s4 := &Session{id: "sess_4"}
	s4.cfg.spawn.parentSystemNotification = func(receiver, msg string) bool {
		if receiver != "ancestor_session" || msg != "msg" {
			t.Fatalf("parent router arguments = (%q, %q)", receiver, msg)
		}
		called = true
		return true
	}
	if !s4.routeSystemNotification("ancestor_session", "msg") {
		t.Fatal("parent router should return true")
	}
	if !called {
		t.Fatal("parent router should be called")
	}
}

// TestCovWrapHookContext covers wrapHookContext (session_queue.go lines 279-281).
func TestCovWrapHookContext2(t *testing.T) {
	got := wrapHookContext("additional context")
	if !strings.Contains(got, "<SYSTEM-REMINDER>") || !strings.Contains(got, "additional context") {
		t.Fatalf("wrapHookContext: %q", got)
	}
	if !strings.HasSuffix(got, "</SYSTEM-REMINDER>") {
		t.Fatalf("should end with closing tag: %q", got)
	}
}

// TestCovSteeringInjectedDataFromMessage covers steeringInjectedDataFromMessage
// (session_queue.go lines 98-107).
func TestCovSteeringInjectedDataFromMessage(t *testing.T) {
	msg := steeringMessage{
		Text:             "hello",
		ClientMutationID: "mut_1",
		StableTurnID:     "turn_1",
		Source:           "user",
		Kind:             "notification",
	}
	data := steeringInjectedDataFromMessage(msg)
	if data.Text != "hello" || data.ClientMutationID != "mut_1" || data.StableTurnID != "turn_1" {
		t.Fatalf("data mismatch: %+v", data)
	}
	if data.Source != "user" || data.Kind != "notification" {
		t.Fatalf("source/kind mismatch: %+v", data)
	}
}

// TestCovWithQueuedClientMutation covers withQueuedClientMutation and
// queuedClientMutationFromContext (session_queue.go lines 29-40).
func TestCovWithQueuedClientMutation(t *testing.T) {
	queued := queuedInput{
		ID:               "q_1",
		ClientMutationID: "mut_1",
		StableTurnID:     "turn_1",
	}
	ctx := withQueuedClientMutation(context.Background(), queued)
	identity := queuedClientMutationFromContext(ctx)
	if identity.ClientMutationID != "mut_1" || identity.StableTurnID != "turn_1" || identity.QueueEntryID != "q_1" {
		t.Fatalf("identity mismatch: %+v", identity)
	}

	// No context value — returns zero identity.
	identity = queuedClientMutationFromContext(context.Background())
	if identity.ClientMutationID != "" {
		t.Fatalf("no context value: %+v", identity)
	}
}

// TestCovInterruptDrainConfig covers interruptDrainConfig
// (session_queue.go lines 748-768): the pure function that decides whether
// an interrupted turn may drain the queue head.
func TestCovInterruptDrainConfig(t *testing.T) {
	rootCtx := context.Background()
	ctx := WithQueuedInputDrainOnInterrupt(context.Background(), rootCtx)

	// No drain config in context — false.
	_, ok := interruptDrainConfig(context.Background(), context.Canceled)
	if ok {
		t.Fatal("no drain config should return false")
	}

	// context.Canceled (bare) with drain config — should drain.
	_, ok = interruptDrainConfig(ctx, context.Canceled)
	if !ok {
		t.Fatal("bare context.Canceled with drain config should drain")
	}

	// context.DeadlineExceeded — should NOT drain.
	_, ok = interruptDrainConfig(ctx, context.DeadlineExceeded)
	if ok {
		t.Fatal("deadline exceeded should not drain")
	}

	// nil error — should NOT drain (no error to classify).
	_, ok = interruptDrainConfig(ctx, nil)
	if ok {
		t.Fatal("nil error should not drain")
	}

	// rootCtx canceled — should NOT drain.
	canceledRoot, cancel := context.WithCancel(rootCtx)
	ctx2 := WithQueuedInputDrainOnInterrupt(context.Background(), canceledRoot)
	cancel()
	_, ok = interruptDrainConfig(ctx2, context.Canceled)
	if ok {
		t.Fatal("canceled root should not drain")
	}
}

// TestCovNextTurnContext covers queuedInputDrainConfig.nextTurnContext
// (session_queue.go lines 781-790).
func TestCovNextTurnContext2(t *testing.T) {
	rootCtx := context.Background()

	// No nextCtx factory — returns rootCtx.
	cfg := queuedInputDrainConfig{rootCtx: rootCtx}
	if cfg.nextTurnContext() != rootCtx {
		t.Fatal("nil nextCtx should return rootCtx")
	}

	// nextCtx returns nil — returns rootCtx.
	cfg.nextCtx = func(ctx context.Context) (context.Context, context.CancelFunc) {
		return nil, nil
	}
	if cfg.nextTurnContext() != rootCtx {
		t.Fatal("nil next should return rootCtx")
	}

	// nextCtx returns non-nil — returns that context.
	childCtx, childCancel := context.WithCancel(rootCtx)
	cfg.nextCtx = func(ctx context.Context) (context.Context, context.CancelFunc) {
		if ctx != rootCtx {
			t.Fatal("nextCtx should receive rootCtx")
		}
		return childCtx, childCancel
	}
	if cfg.nextTurnContext() != childCtx {
		t.Fatal("non-nil next should return child context")
	}
	childCancel()
}

// TestCovPopFollowUp covers popFollowUp (session_queue.go lines 1040-1049).
func TestCovPopFollowUp(t *testing.T) {
	s := &Session{}
	// No followups — empty string.
	if got := s.popFollowUp(); got != "" {
		t.Fatalf("empty followups: got %q", got)
	}

	// Add followups.
	s.mu.Lock()
	s.followups = []string{"first", "second"}
	s.mu.Unlock()

	// Pop first.
	if got := s.popFollowUp(); got != "first" {
		t.Fatalf("first pop: got %q", got)
	}
	// Pop second.
	if got := s.popFollowUp(); got != "second" {
		t.Fatalf("second pop: got %q", got)
	}
	// Empty again.
	if got := s.popFollowUp(); got != "" {
		t.Fatalf("third pop: got %q", got)
	}
}

// TestCovHasPendingSteering covers hasPendingSteering
// (session_queue.go lines 969-973).
func TestCovHasPendingSteering(t *testing.T) {
	s := &Session{}
	if s.hasPendingSteering() {
		t.Fatal("empty should be false")
	}

	s.mu.Lock()
	s.steeringQueue = []steeringMessage{{Text: "msg"}}
	s.mu.Unlock()

	if !s.hasPendingSteering() {
		t.Fatal("with steering should be true")
	}
}

// TestCovHasPendingUserSteering covers hasPendingUserSteering
// (session_queue.go lines 981-990).
func TestCovHasPendingUserSteering(t *testing.T) {
	s := &Session{}
	// No steering — false.
	if s.hasPendingUserSteering() {
		t.Fatal("no steering should be false")
	}

	// Only daemon steering — false.
	s.mu.Lock()
	s.steeringQueue = []steeringMessage{{Text: "daemon msg"}}
	s.mu.Unlock()
	if s.hasPendingUserSteering() {
		t.Fatal("daemon-only steering should be false")
	}

	// With user steering — true.
	s.mu.Lock()
	s.steeringQueue = append(s.steeringQueue, steeringMessage{Text: "user msg", Source: events.SteeringSourceUser})
	s.mu.Unlock()
	if !s.hasPendingUserSteering() {
		t.Fatal("user steering should be true")
	}
}

// TestCovPrependSteering covers prependSteering
// (session_queue.go lines 992-1000).
func TestCovPrependSteering(t *testing.T) {
	s := &Session{}
	// Empty entries — no-op.
	s.prependSteering(nil)
	if s.hasPendingSteering() {
		t.Fatal("nil entries should be no-op")
	}

	// Prepend entries.
	entries := []steeringMessage{{Text: "first"}, {Text: "second"}}
	s.prependSteering(entries)
	s.mu.Lock()
	if len(s.steeringQueue) != 2 {
		t.Fatalf("want 2 entries, got %d", len(s.steeringQueue))
	}
	if s.steeringQueue[0].Text != "first" || s.steeringQueue[1].Text != "second" {
		t.Fatalf("order mismatch: %+v", s.steeringQueue)
	}
	s.mu.Unlock()
}

// TestCovSteeringQueueSnapshot covers SteeringQueueSnapshot
// (session_queue.go lines 1015-1027).
func TestCovSteeringQueueSnapshot(t *testing.T) {
	s := &Session{}
	// Empty — nil.
	if got := s.SteeringQueueSnapshot(); got != nil {
		t.Fatal("empty should return nil")
	}

	// With entries.
	s.mu.Lock()
	s.steeringQueue = []steeringMessage{
		{Text: "msg1", Images: []ImageAttachment{{MediaType: "image/png"}}},
		{Text: "msg2"},
	}
	s.mu.Unlock()

	entries := s.SteeringQueueSnapshot()
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
	if entries[0].Text != "msg1" || len(entries[0].Images) != 1 {
		t.Fatalf("entry 0 mismatch: %+v", entries[0])
	}
	if entries[1].Text != "msg2" {
		t.Fatalf("entry 1 text: %q", entries[1].Text)
	}

	// Verify it's a copy — mutating should not affect original.
	entries[0].Text = "modified"
	s.mu.Lock()
	if s.steeringQueue[0].Text == "modified" {
		t.Fatal("snapshot should be a copy")
	}
	s.mu.Unlock()
}

// TestCovFollowUp covers FollowUp (session_queue.go lines 303-313).
func TestCovFollowUp(t *testing.T) {
	s := &Session{}
	// Empty message — no-op.
	s.FollowUp("")
	// Whitespace message — no-op.
	s.FollowUp("  ")
	s.mu.Lock()
	if len(s.followups) != 0 {
		t.Fatalf("empty/whitespace should be no-op: %d", len(s.followups))
	}
	s.mu.Unlock()

	// Valid message.
	s.FollowUp("do something")
	s.mu.Lock()
	if len(s.followups) != 1 || s.followups[0] != "do something" {
		t.Fatalf("followups: %+v", s.followups)
	}
	s.mu.Unlock()
}

// TestCovPendingQueueDepth covers pendingQueueDepth
// (session_queue.go lines 514-519).
func TestCovPendingQueueDepth(t *testing.T) {
	s := &Session{}
	// No client mutations, no queue — 0.
	if got := s.pendingQueueDepth(); got != 0 {
		t.Fatalf("empty: got %d", got)
	}

	// With input queue but no held queue.
	s.mu.Lock()
	s.inputQueue = []queuedInput{{ID: "q1", Text: "msg1"}}
	s.mu.Unlock()
	if got := s.pendingQueueDepth(); got != 1 {
		t.Fatalf("with queue: got %d, want 1", got)
	}
}

// TestCovQueueDepth covers QueueDepth (session_queue.go lines 500-504).
func TestCovQueueDepth(t *testing.T) {
	s := &Session{}
	if got := s.QueueDepth(); got != 0 {
		t.Fatalf("empty: got %d", got)
	}

	s.mu.Lock()
	s.inputQueue = []queuedInput{{ID: "q1"}, {ID: "q2"}}
	s.mu.Unlock()
	if got := s.QueueDepth(); got != 2 {
		t.Fatalf("with 2: got %d", got)
	}
}

// TestCovDelegateSendResultFormat covers formatDelegateSend
// (session_tools_jobs.go lines 1428-1470).
func TestCovDelegateSendResultFormat(t *testing.T) {
	output := "hello"
	out := delegateSendResult{
		DelegateID: "dlg_1",
		Action:     "delivered",
		Status:     "completed",
		Output:     &output,
	}
	formatted := formatDelegateSend(out)
	if !strings.Contains(formatted, "hello") {
		t.Fatal("should contain output")
	}
	if !strings.Contains(formatted, "delegate_id dlg_1") {
		t.Fatal("should contain delegate_id")
	}
	if !strings.Contains(formatted, "delivered") {
		t.Fatal("should contain action")
	}
	if !strings.Contains(formatted, "completed") {
		t.Fatal("should contain status")
	}

	// With warnings.
	out.Warnings = []string{"warn1", "warn2"}
	formatted = formatDelegateSend(out)
	if !strings.Contains(formatted, "warning: warn1") || !strings.Contains(formatted, "warning: warn2") {
		t.Fatalf("should contain warnings: %q", formatted)
	}

	// With structured result.
	out.StructuredResult = map[string]any{"key": "val"}
	valid := true
	out.StructuredResultValid = &valid
	formatted = formatDelegateSend(out)
	if !strings.Contains(formatted, "structured_result") {
		t.Fatalf("should contain structured_result: %q", formatted)
	}
}

// TestCovDelegateWorktreeToolResultFrom covers delegateWorktreeToolResultFrom
// (session_tools_jobs.go lines 1364-1369).
func TestCovDelegateWorktreeToolResultFrom(t *testing.T) {
	// nil — returns nil.
	if delegateWorktreeToolResultFrom(nil) != nil {
		t.Fatal("nil should return nil")
	}

	// Valid worktree.
	wt := &delegateWorktreeReport{
		Path:         "/path",
		Branch:       "main",
		HeadSHA:      "abc123",
		Ahead:        3,
		Dirty:        true,
		DisposalHint: "dispose me",
	}
	result := delegateWorktreeToolResultFrom(wt)
	if result == nil {
		t.Fatal("should return non-nil")
	}
	if result.Path != "/path" || result.Branch != "main" || result.HeadSHA != "abc123" {
		t.Fatalf("mismatch: %+v", result)
	}
	if result.Ahead != 3 || result.Dirty != true {
		t.Fatalf("ahead/dirty mismatch: %+v", result)
	}
	if result.DisposalHint != "dispose me" {
		t.Fatalf("disposal hint: %q", result.DisposalHint)
	}
}

// TestCovDelegateSandboxToolResultFrom covers delegateSandboxToolResultFrom
// (session_tools_jobs.go lines 1376-1381).
func TestCovDelegateSandboxToolResultFrom(t *testing.T) {
	// nil — returns nil.
	if delegateSandboxToolResultFrom(nil) != nil {
		t.Fatal("nil should return nil")
	}

	// Valid sandbox.
	sb := &delegateSandboxReport{Mode: "bwrap", Network: true}
	result := delegateSandboxToolResultFrom(sb)
	if result == nil {
		t.Fatal("should return non-nil")
	}
	if result.Mode != "bwrap" || !result.Network {
		t.Fatalf("mismatch: %+v", result)
	}
}

// TestCovDecodeDelegateArgs covers decodeDelegateArgs
// (session_tools_jobs.go lines 345-382): validation branches.
func TestCovDecodeDelegateArgs2(t *testing.T) {
	// Valid minimal.
	a, err := decodeDelegateArgs(map[string]any{"task": "do work"})
	if err != nil {
		t.Fatalf("minimal: %v", err)
	}
	if a.Task != "do work" {
		t.Fatalf("task = %q", a.Task)
	}

	// sandbox_net as non-boolean — error.
	_, err = decodeDelegateArgs(map[string]any{"sandbox_net": "false"})
	if err == nil || !strings.Contains(err.Error(), "sandbox_net must be a JSON boolean") {
		t.Fatalf("non-bool sandbox_net: %v", err)
	}

	// sandbox_net as true.
	a, err = decodeDelegateArgs(map[string]any{"sandbox_net": true})
	if err != nil {
		t.Fatalf("sandbox_net true: %v", err)
	}
	if a.SandboxNet == nil || !*a.SandboxNet {
		t.Fatal("sandbox_net should be true")
	}

	// sandbox_net as false.
	a, err = decodeDelegateArgs(map[string]any{"sandbox_net": false})
	if err != nil {
		t.Fatalf("sandbox_net false: %v", err)
	}
	if a.SandboxNet == nil || *a.SandboxNet {
		t.Fatal("sandbox_net should be false")
	}

	// sandbox_net absent — nil (inherit).
	a, err = decodeDelegateArgs(map[string]any{})
	if err != nil {
		t.Fatalf("absent sandbox_net: %v", err)
	}
	if a.SandboxNet != nil {
		t.Fatal("absent sandbox_net should be nil")
	}

	// delegation_allowance negative — error.
	_, err = decodeDelegateArgs(map[string]any{"delegation_allowance": -1})
	if err == nil || !strings.Contains(err.Error(), "must be non-negative") {
		t.Fatalf("negative allowance: %v", err)
	}

	// delegation_allowance positive — OK.
	a, err = decodeDelegateArgs(map[string]any{"delegation_allowance": 3})
	if err != nil {
		t.Fatalf("positive allowance: %v", err)
	}
	if a.DelegationAllowance != 3 {
		t.Fatalf("allowance = %d, want 3", a.DelegationAllowance)
	}

	// delegation_allowance zero — unset (strict-zero).
	a, err = decodeDelegateArgs(map[string]any{"delegation_allowance": 0})
	if err != nil {
		t.Fatalf("zero allowance: %v", err)
	}
	if a.DelegationAllowance != 0 {
		t.Fatalf("zero allowance should be 0, got %d", a.DelegationAllowance)
	}

	// result_schema present.
	a, err = decodeDelegateArgs(map[string]any{"result_schema": map[string]any{"type": "object"}})
	if err != nil {
		t.Fatalf("result_schema: %v", err)
	}
	if a.ResultSchema == nil {
		t.Fatal("result_schema should be set")
	}

	// Full args.
	a, err = decodeDelegateArgs(map[string]any{
		"task":                 "work",
		"agent_type":           "researcher",
		"model":                "gpt-5",
		"reasoning_effort":     "high",
		"watch_parent":         true,
		"isolation":            "worktree",
		"sandbox":              "bwrap",
		"sandbox_net":          false,
		"delegation_allowance": 2,
	})
	if err != nil {
		t.Fatalf("full args: %v", err)
	}
	if a.AgentType != "researcher" || a.Model != "gpt-5" || a.ReasoningEffort != "high" {
		t.Fatalf("fields mismatch: %+v", a)
	}
	if !a.WatchParent || a.Isolation != "worktree" || a.Sandbox != "bwrap" {
		t.Fatalf("more fields mismatch: %+v", a)
	}
}

// TestCovConsumeTerminalJobNotification covers consumeTerminalJobNotification
// (session_tools_jobs.go lines 446-480): the nil/empty guards.
func TestCovConsumeTerminalJobNotification(t *testing.T) {
	// nil jm — no-op.
	consumeTerminalJobNotification(&Session{}, nil, &jobstore.JobRecord{TerminalGen: "gen_1"})
	// nil rec — no-op.
	consumeTerminalJobNotification(&Session{}, &jobManager{}, nil)
	// Empty TerminalGen — no-op.
	consumeTerminalJobNotification(&Session{}, &jobManager{}, &jobstore.JobRecord{TerminalGen: ""})
	// Non-terminal status — no-op.
	consumeTerminalJobNotification(&Session{}, &jobManager{}, &jobstore.JobRecord{
		TerminalGen: "gen_1",
		Status:      jobstore.StatusRunning,
	})
	// NotifyState != NotifyPending — no-op.
	consumeTerminalJobNotification(&Session{}, &jobManager{}, &jobstore.JobRecord{
		TerminalGen: "gen_1",
		Status:      jobstore.StatusCompleted,
		NotifyState: jobstore.NotifyDelivered,
	})
	// Different owner session — no-op.
	s := &Session{id: "my_session"}
	jm := &jobManager{sessionID: "jm_session"}
	consumeTerminalJobNotification(s, jm, &jobstore.JobRecord{
		TerminalGen:    "gen_1",
		Status:         jobstore.StatusCompleted,
		NotifyState:    jobstore.NotifyPending,
		OwnerSessionID: "other_session",
	})

	// An owner read consumes the pending generation durably and clears its wake.
	jm = newTestJM(t)
	startedAt := jm.now()
	jobID := "job_" + jm.sessionID + "_consume"
	if err := jm.appendEvent(jobstore.Event{Kind: jobstore.EventJobStarted, TS: startedAt, JobID: jobID, Type: jobstore.JobShell, OwnerSessionID: jm.sessionID, StartedAt: &startedAt}); err != nil {
		t.Fatalf("append start: %v", err)
	}
	endedAt := startedAt.Add(time.Second)
	exitCode := 0
	if err := jm.appendEvent(jobstore.Event{Kind: jobstore.EventJobFinished, TS: endedAt, JobID: jobID, Status: jobstore.StatusCompleted, EndedAt: &endedAt, ExitCode: &exitCode, TerminalGen: "gen_consume"}); err != nil {
		t.Fatalf("append finish: %v", err)
	}
	if err := jm.appendEvent(jobstore.Event{Kind: jobstore.EventJobNotificationPending, TS: endedAt, JobID: jobID, TerminalGen: "gen_consume"}); err != nil {
		t.Fatalf("append pending: %v", err)
	}
	records, err := jm.store.Load()
	if err != nil {
		t.Fatalf("load pending record: %v", err)
	}
	rec := records[jobID]
	var consumedID string
	jm.consume = func(id string) { consumedID = id }
	s = &Session{id: jm.sessionID, events: make(chan events.SessionEvent, 1)}
	consumeTerminalJobNotification(s, jm, rec)
	if rec.NotifyState != jobstore.NotifyConsumed || consumedID != jobID {
		t.Fatalf("consumed state/callback = (%q, %q), want (%q, %q)", rec.NotifyState, consumedID, jobstore.NotifyConsumed, jobID)
	}
	records, err = jm.store.Load()
	if err != nil {
		t.Fatalf("reload consumed record: %v", err)
	}
	if records[jobID].NotifyState != jobstore.NotifyConsumed {
		t.Fatalf("durable notify state = %q, want consumed", records[jobID].NotifyState)
	}
}

// TestCovProjectStableDelegateListItem is a helper test covering the projection
// of a stable delegate into a job list entry (used by job_list).
func TestCovProjectStableDelegateListItem(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	startedAt := now.Add(-10 * time.Second)
	endedAt := now.Add(-2 * time.Second)
	latestActivityAt := now.Add(-3 * time.Second)
	entry := projectStableDelegateListItem(now, stableDelegateVisibleRow{
		depth: 2,
		snapshot: delegateSnapshot{
			id: "dlg_1", parentID: "dlg_parent", lifecycle: delegateLifecycleIdle,
			runStartedAt: startedAt, latestActivityAt: latestActivityAt,
			resumable: false, notResumableReason: "exhausted", transcriptRef: "local:child_1",
			descriptor: delegatestore.Descriptor{
				Description: "worker", OwnerSessionID: "owner", VisibleSessionID: "visible",
				Task: "do work", AgentType: "researcher", ResolvedModel: "model-x",
				Config: schema.ConfigSnapshot{ReasoningEffort: "high"},
			},
			lastOutcome: &delegatestore.Outcome{Reason: "budget", EndedAt: endedAt, ExhaustionBudget: delegatestore.ExhaustionBudgetTurns, ExhaustionLimit: 42},
		},
	})
	if entry.ID != "dlg_1" || entry.Kind != "delegate" || entry.Type != "delegate" || entry.Status != string(delegateLifecycleIdle) || entry.Description != "worker" || entry.OwnerSessionID != "owner" || entry.VisibleToSessionID != "visible" {
		t.Fatalf("projected identity = %+v", entry)
	}
	if entry.TranscriptRef == nil || *entry.TranscriptRef != "local:child_1" || entry.Resumable == nil || *entry.Resumable || entry.NotResumableReason == nil || *entry.NotResumableReason != "exhausted" {
		t.Fatalf("projected resume fields = %+v", entry)
	}
	if entry.StartedAt != startedAt.Format(time.RFC3339Nano) || entry.DurationMS == nil || *entry.DurationMS != 8000 || entry.LastActivity == nil || *entry.LastActivity != latestActivityAt.Format(time.RFC3339Nano) || !entry.LatestActivitySortAt.Equal(latestActivityAt) {
		t.Fatalf("projected timing = %+v", entry)
	}
	if entry.Depth != 2 || entry.Task != "do work" || entry.AgentType != "researcher" || entry.Model != "model-x" || entry.ReasoningEffort != "high" || entry.ParentDelegateID == nil || *entry.ParentDelegateID != "dlg_parent" {
		t.Fatalf("projected delegate fields = %+v", entry)
	}
	if entry.Reason == nil || *entry.Reason != "budget" || entry.EndedAt == nil || *entry.EndedAt != endedAt.Format(time.RFC3339Nano) || entry.ExhaustionBudget != string(delegatestore.ExhaustionBudgetTurns) || entry.ExhaustionLimit != 42 {
		t.Fatalf("projected outcome = %+v", entry)
	}
}

// TestCovWithQueuedInputDrainOnInterrupt covers WithQueuedInputDrainOnInterrupt
// (session_queue.go lines 49-51).
func TestCovWithQueuedInputDrainOnInterrupt(t *testing.T) {
	rootCtx := context.Background()
	ctx := WithQueuedInputDrainOnInterrupt(context.Background(), rootCtx)
	cfg, ok := ctx.Value(queuedInputDrainContextKey{}).(queuedInputDrainConfig)
	if !ok {
		t.Fatal("should set drain config in context")
	}
	if cfg.rootCtx == nil {
		t.Fatal("rootCtx should be set")
	}

	// nil rootCtx — replaced with Background.
	ctx = WithQueuedInputDrainOnInterrupt(context.Background(), nil)
	cfg, ok = ctx.Value(queuedInputDrainContextKey{}).(queuedInputDrainConfig)
	if !ok || cfg.rootCtx == nil {
		t.Fatal("nil rootCtx should be replaced with Background")
	}
}

// TestCovWithQueuedInputDrainOnInterruptHandler covers
// WithQueuedInputDrainOnInterruptHandler (session_queue.go lines 64-72).
func TestCovWithQueuedInputDrainOnInterruptHandler(t *testing.T) {
	rootCtx := context.Background()
	nextCalled := false
	nextCtx := func(ctx context.Context) (context.Context, context.CancelFunc) {
		nextCalled = true
		return context.WithCancel(ctx)
	}
	ctx := WithQueuedInputDrainOnInterruptHandler(context.Background(), rootCtx, nextCtx)
	cfg, ok := ctx.Value(queuedInputDrainContextKey{}).(queuedInputDrainConfig)
	if !ok {
		t.Fatal("should set drain config")
	}
	if cfg.nextCtx == nil {
		t.Fatal("nextCtx should be set")
	}

	// Trigger nextCtx to verify it's wired.
	got := cfg.nextTurnContext()
	if !nextCalled {
		t.Fatal("nextCtx should have been called")
	}
	if got == nil {
		t.Fatal("should return non-nil context")
	}
}

// TestCovDeliverHookContext covers deliverHookContext
// (session_queue.go lines 285-290).
func TestCovDeliverHookContext(t *testing.T) {
	s := &Session{}
	// Empty text — no-op.
	s.deliverHookContext("")
	// Whitespace text — no-op.
	s.deliverHookContext("  ")
	// Valid text — should call SteerKind (which calls trySteerEnqueue,
	// which needs s.mu and checks closingOrClosedLocked; bare Session
	// with no mu set will work since sync.Mutex zero value is usable).
	s.deliverHookContext("additional model context")
	s.mu.Lock()
	if len(s.steeringQueue) != 1 {
		t.Fatalf("should have 1 steering entry, got %d", len(s.steeringQueue))
	}
	if s.steeringQueue[0].Kind != events.SteeringKindHookContext {
		t.Fatalf("kind = %q, want %q", s.steeringQueue[0].Kind, events.SteeringKindHookContext)
	}
	s.mu.Unlock()
}

// TestCovAppendSteeringTurn covers appendSteeringTurn
// (session_queue.go lines 942-947).
func TestCovAppendSteeringTurn(t *testing.T) {
	s := &Session{id: "session_1", events: make(chan events.SessionEvent, 2)}
	s.appendSteeringTurn("steer now", events.SteeringKindHookContext)
	if len(s.history) != 1 || s.history[0].Kind != schema.TurnSteering || s.history[0].SteeringKind != events.SteeringKindHookContext || s.history[0].Message.Text() != "steer now" {
		t.Fatalf("steering history = %+v", s.history)
	}
	if len(s.pendingTranscriptTurns) != 1 || s.pendingTranscriptTurns[0].SteeringKind != events.SteeringKindHookContext || s.pendingTranscriptTurns[0].Message.Text() != "steer now" {
		t.Fatalf("persisted steering queue = %+v", s.pendingTranscriptTurns)
	}
	event := <-s.events
	data, ok := event.Data.(events.SteeringInjectedData)
	if !ok || event.Kind != events.EventSteeringInjected || event.SessionID != "session_1" || data.Text != "steer now" || data.Kind != events.SteeringKindHookContext {
		t.Fatalf("steering event = %#v with data %#v", event, event.Data)
	}
}

// TestCovAppendSteeringTurnDurably covers appendSteeringTurnDurably
// (session_queue.go lines 956-967): durable persistence and failure ordering.
func TestCovAppendSteeringTurnDurably(t *testing.T) {
	t.Run("persists before history", func(t *testing.T) {
		path := t.TempDir() + "/session.transcript.jsonl"
		writer, err := transcript.NewWriter(path, transcript.Header{SessionID: "session_1"})
		if err != nil {
			t.Fatalf("NewWriter: %v", err)
		}
		t.Cleanup(func() { _ = writer.Close() })
		s := &Session{id: "session_1", transcript: writer, transcriptReady: true, events: make(chan events.SessionEvent, 1)}
		if err := s.appendSteeringTurnDurably("durable steer", events.SteeringKindNotification); err != nil {
			t.Fatalf("appendSteeringTurnDurably: %v", err)
		}
		if len(s.history) != 1 || s.history[0].Kind != schema.TurnSteering || s.history[0].SteeringKind != events.SteeringKindNotification || s.history[0].Message.Text() != "durable steer" {
			t.Fatalf("durable steering history = %+v", s.history)
		}
		loaded, err := readTranscriptFull(path)
		if err != nil {
			t.Fatalf("read persisted transcript: %v", err)
		}
		if len(loaded.Entries) != 1 {
			t.Fatalf("persisted entries = %d, want 1", len(loaded.Entries))
		}
		turn := loaded.Entries[0].Turn
		if turn.Kind != schema.TurnSteering || turn.SteeringKind != events.SteeringKindNotification || turn.Message.Role != llm.RoleUser || turn.Message.Text() != "durable steer" {
			t.Fatalf("persisted durable turn = %+v", turn)
		}
	})

	t.Run("sync failure warns and skips history", func(t *testing.T) {
		path := t.TempDir() + "/failed.transcript.jsonl"
		fs := newAttentionFailNextSyncFS()
		writer, err := transcript.NewWriterWithFS(fs, path, transcript.Header{SessionID: "session_fail"})
		if err != nil {
			t.Fatalf("NewWriterWithFS: %v", err)
		}
		t.Cleanup(func() { _ = writer.Close() })
		fs.failNextSync()
		s := &Session{id: "session_fail", transcript: writer, transcriptReady: true, events: make(chan events.SessionEvent, 1)}
		err = s.appendSteeringTurnDurably("must not enter history", events.SteeringKindNotification)
		if err == nil || !strings.Contains(err.Error(), "injected attention resolution sync failure") {
			t.Fatalf("durable append error = %v", err)
		}
		if len(s.history) != 0 {
			t.Fatalf("failed durable append changed history: %+v", s.history)
		}
		event := <-s.events
		warning, ok := event.Data.(events.WarningData)
		wantWarning := events.WarningData{
			Message: "transcript write failed: sync transcript entry: injected attention resolution sync failure",
			Source:  "evener",
			Title:   "Evener error",
			Hint:    "Check the Evener session log and daemon state.",
		}
		if !ok || event.Kind != events.EventWarning || event.SessionID != "session_fail" || !reflect.DeepEqual(warning, wantWarning) {
			t.Fatalf("durable failure warning = %#v with data %#v", event, event.Data)
		}
	})
}
