package agent

import (
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
)

// TestLiveSteerWaitIgnoredReason covers all branches of
// liveSteerWaitIgnoredReason (session_tools_jobs.go:130-135).
func TestLiveSteerWaitIgnoredReason(t *testing.T) {
	t.Parallel()
	// Positive timeout, running + steered -> non-empty reason.
	if got := liveSteerWaitIgnoredReason(100, jobstore.StatusRunning, "steered"); got == "" {
		t.Fatal("steered+running should have a reason")
	}
	// Positive timeout, delivered -> non-empty reason.
	if got := liveSteerWaitIgnoredReason(100, jobstore.StatusCompleted, "delivered"); got == "" {
		t.Fatal("delivered should have a reason")
	}
	// Zero timeout -> empty reason.
	if got := liveSteerWaitIgnoredReason(0, jobstore.StatusRunning, "steered"); got != "" {
		t.Fatalf("zero timeout should have empty reason, got %q", got)
	}
	// Positive timeout but non-running/non-delivered -> empty.
	if got := liveSteerWaitIgnoredReason(100, jobstore.StatusCompleted, "steered"); got != "" {
		t.Fatalf("completed+steered should have empty reason, got %q", got)
	}
}

// TestFormatJobWatchInspect covers all branches of formatJobWatchInspect
// (session_tools_jobs.go:1617-1636).
func TestFormatJobWatchInspect(t *testing.T) {
	t.Parallel()
	// Watching with condition.
	got := formatJobWatchInspect(jobWatchInspectToolResult{
		Watching:  true,
		WatchID:   "w1",
		Source:    "self",
		Condition: "output_match: ready",
	})
	if got != "w1  watching self  output_match: ready" {
		t.Fatalf("watching+condition: %q", got)
	}
	// Watching without condition.
	got = formatJobWatchInspect(jobWatchInspectToolResult{
		Watching: true,
		WatchID:  "w1",
		Source:   "self",
	})
	if got != "w1  watching self" {
		t.Fatalf("watching no condition: %q", got)
	}
	// End reason set.
	got = formatJobWatchInspect(jobWatchInspectToolResult{
		WatchID:   "w1",
		EndReason: "cleared",
		Source:    "self",
	})
	if got != "w1  cleared  self" {
		t.Fatalf("end reason: %q", got)
	}
	// Pending with source and condition.
	got = formatJobWatchInspect(jobWatchInspectToolResult{
		WatchID:   "w1",
		Source:    "parent",
		Condition: "events: communicate",
	})
	if got != "w1  pending  parent  events: communicate" {
		t.Fatalf("pending+condition: %q", got)
	}
	// Pending with source only.
	got = formatJobWatchInspect(jobWatchInspectToolResult{
		WatchID: "w1",
		Source:  "parent",
	})
	if got != "w1  pending  parent" {
		t.Fatalf("pending no condition: %q", got)
	}
	// Not found.
	got = formatJobWatchInspect(jobWatchInspectToolResult{
		WatchID: "w1",
	})
	if got != "w1  not found" {
		t.Fatalf("not found: %q", got)
	}
}

// TestWatchSendArg covers all branches of watchSendArg
// (session_tools_jobs.go:1838-1858).
func TestWatchSendArg(t *testing.T) {
	t.Parallel()
	// No send key.
	got, err := watchSendArg(map[string]any{})
	if err != nil || got != nil {
		t.Fatalf("no send key: got=%v err=%v", got, err)
	}
	// Send not an object.
	_, err = watchSendArg(map[string]any{"send": "not an object"})
	if err == nil {
		t.Fatal("non-object send should error")
	}
	// Empty send object returns nil.
	got, err = watchSendArg(map[string]any{"send": map[string]any{}})
	if err != nil || got != nil {
		t.Fatalf("empty send: got=%v err=%v", got, err)
	}
	// Missing "to" field.
	_, err = watchSendArg(map[string]any{"send": map[string]any{"message": "hi"}})
	if err == nil {
		t.Fatal("missing to should error")
	}
	// Valid send.
	got, err = watchSendArg(map[string]any{"send": map[string]any{
		"to":              "caller",
		"message":         "observe",
		"include_excerpt": true,
	}})
	if err != nil || got == nil {
		t.Fatalf("valid send: got=%v err=%v", got, err)
	}
	if got.To != "caller" || got.Message != "observe" || !got.IncludeExcerpt {
		t.Fatalf("send args = %+v", got)
	}
}

// TestMarshalBoundedJSON covers marshalBoundedJSON
// (session_tools_jobs.go:1979-1988) including the error and bounds-exceeded paths.
func TestMarshalBoundedJSON(t *testing.T) {
	t.Parallel()
	// Happy path.
	got, err := marshalBoundedJSON(map[string]any{"ok": true}, 0)
	if err != nil || got == "" {
		t.Fatalf("happy path: got=%q err=%v", got, err)
	}
	// Max chars exceeded.
	got, err = marshalBoundedJSON(map[string]any{"big": "value"}, 5)
	if err == nil {
		t.Fatal("should exceed max chars")
	}
	// Max chars not exceeded.
	got, err = marshalBoundedJSON("short", 100)
	if err != nil || got == "" {
		t.Fatalf("within bounds: got=%q err=%v", got, err)
	}
}

// TestDefaultJobPhase covers defaultJobPhase
// (session_tools_jobs.go:2063-2075).
func TestDefaultJobPhase(t *testing.T) {
	t.Parallel()
	// Nil record.
	if got := defaultJobPhase(nil); got != "" {
		t.Fatalf("nil: %q", got)
	}
	// Terminal record.
	if got := defaultJobPhase(&jobstore.JobRecord{Status: jobstore.StatusCompleted}); got != "" {
		t.Fatalf("terminal: %q", got)
	}
	// Non-terminal with explicit phase.
	if got := defaultJobPhase(&jobstore.JobRecord{Status: jobstore.StatusRunning, Phase: "custom"}); got != "custom" {
		t.Fatalf("explicit phase: %q", got)
	}
	// Non-terminal shell, no phase.
	if got := defaultJobPhase(&jobstore.JobRecord{Status: jobstore.StatusRunning, Type: jobstore.JobShell}); got != jobPhaseProcessRunning {
		t.Fatalf("shell no phase: %q", got)
	}
	// Non-terminal, non-shell.
	if got := defaultJobPhase(&jobstore.JobRecord{Status: jobstore.StatusRunning, Type: jobstore.JobType("other")}); got != "" {
		t.Fatalf("non-shell: %q", got)
	}
}

// TestJobTranscriptRef covers jobTranscriptRef
// (session_tools_jobs.go:2078-2086).
func TestJobTranscriptRef(t *testing.T) {
	t.Parallel()
	// Nil record.
	if got := jobTranscriptRef(nil); got != "" {
		t.Fatalf("nil: %q", got)
	}
	// Shell job.
	if got := jobTranscriptRef(&jobstore.JobRecord{Type: jobstore.JobShell, JobID: "job_123"}); got != "job:job_123" {
		t.Fatalf("shell: %q", got)
	}
	// Non-shell job.
	if got := jobTranscriptRef(&jobstore.JobRecord{Type: jobstore.JobType("other"), JobID: "job_123"}); got != "" {
		t.Fatalf("non-shell: %q", got)
	}
}

// TestShortTimestamp covers shortTimestamp
// (session_tools_jobs.go:808-817).
func TestShortTimestamp(t *testing.T) {
	t.Parallel()
	// Empty string.
	if got := shortTimestamp(""); got != "" {
		t.Fatalf("empty: %q", got)
	}
	// Invalid timestamp.
	if got := shortTimestamp("not a timestamp"); got != "" {
		t.Fatalf("invalid: %q", got)
	}
	// Valid timestamp.
	if got := shortTimestamp("2026-01-15T10:30:00Z"); got != "2026-01-15 10:30" {
		t.Fatalf("valid: %q", got)
	}
}

// TestClassifyStopOutcome covers all branches of classifyStopOutcome
// (session_tools_jobs.go:1255-1266).
func TestClassifyStopOutcome(t *testing.T) {
	t.Parallel()
	// Previous terminal.
	if got := classifyStopOutcome(jobstore.StatusCompleted, &jobstore.JobRecord{Status: jobstore.StatusCompleted}); got != "already_terminal" {
		t.Fatalf("already terminal: %q", got)
	}
	// Nil record.
	if got := classifyStopOutcome(jobstore.StatusRunning, nil); got != "stop_requested" {
		t.Fatalf("nil rec: %q", got)
	}
	// Non-terminal record.
	if got := classifyStopOutcome(jobstore.StatusRunning, &jobstore.JobRecord{Status: jobstore.StatusRunning}); got != "stop_requested" {
		t.Fatalf("non-terminal: %q", got)
	}
	// Cancelled by request.
	if got := classifyStopOutcome(jobstore.StatusRunning, &jobstore.JobRecord{Status: jobstore.StatusCancelled}); got != "cancelled_by_request" {
		t.Fatalf("cancelled: %q", got)
	}
	// Completed during stop.
	if got := classifyStopOutcome(jobstore.StatusRunning, &jobstore.JobRecord{Status: jobstore.StatusCompleted}); got != "completed_during_stop" {
		t.Fatalf("completed: %q", got)
	}
}

// TestPublicJobKind covers publicJobKind (session_tools_jobs.go:2054-2057).
func TestPublicJobKind(t *testing.T) {
	t.Parallel()
	if got := publicJobKind(jobstore.JobShell); got != jobKindShell {
		t.Fatalf("shell: %q", got)
	}
}

// TestProjectJobStatus covers projectJobStatus
// (session_tools_jobs.go:2088-2123) for both terminal and non-terminal records.
func TestProjectJobStatus(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	started := now.Add(-5 * time.Minute)

	// Terminal record with EndedAt.
	endedAt := now.Add(-1 * time.Minute)
	result := projectJobStatus(now, &jobstore.JobRecord{
		JobID:     "job_1",
		Type:      jobstore.JobShell,
		Status:    jobstore.StatusCompleted,
		StartedAt: started,
		EndedAt:   &endedAt,
		Reason:    "exit_zero",
		ExitCode:  jobIntPtr(0),
	})
	if result.JobID != "job_1" || result.Status != "completed" {
		t.Fatalf("terminal result = %+v", result)
	}
	if result.DurationMS == nil || *result.DurationMS != int64(4*60*1000) {
		t.Fatalf("duration = %v, want 4 min", result.DurationMS)
	}
	if result.Phase != "" {
		t.Fatalf("terminal phase should be empty: %q", result.Phase)
	}
	if result.TranscriptRef != "job:job_1" {
		t.Fatalf("transcript ref = %q", result.TranscriptRef)
	}

	// Non-terminal record with LastActivity.
	lastActivity := now.Add(-30 * time.Second)
	result = projectJobStatus(now, &jobstore.JobRecord{
		JobID:        "job_2",
		Type:         jobstore.JobShell,
		Status:       jobstore.StatusRunning,
		StartedAt:    started,
		LastActivity: &lastActivity,
	})
	if result.RunningForMS == nil || *result.RunningForMS != int64(5*60*1000) {
		t.Fatalf("running for = %v, want 5 min", result.RunningForMS)
	}
	if result.QuietForMS == nil || *result.QuietForMS != int64(30*1000) {
		t.Fatalf("quiet for = %v, want 30s", result.QuietForMS)
	}
	if result.Phase != jobPhaseProcessRunning {
		t.Fatalf("non-terminal shell phase = %q, want %q", result.Phase, jobPhaseProcessRunning)
	}

	// Non-terminal without LastActivity uses EndedAt for "last".
	result = projectJobStatus(now, &jobstore.JobRecord{
		JobID:     "job_3",
		Type:      jobstore.JobShell,
		Status:    jobstore.StatusRunning,
		StartedAt: started,
		EndedAt:   &endedAt,
	})
	if result.QuietForMS == nil {
		t.Fatal("quiet for should not be nil")
	}
}

// TestFormatJobStop covers formatJobStop for a typical stop result.
func TestFormatJobStop(t *testing.T) {
	t.Parallel()
	reason := "exit_zero"
	result := jobStopResult{
		ID:             "job_1",
		JobID:          "job_1",
		Type:           "shell",
		Status:         "completed",
		Reason:         &reason,
		PreviousStatus: "running",
		Outcome:        "completed_during_stop",
	}
	formatted := formatJobStop(result)
	if formatted == "" {
		t.Fatal("formatJobStop returned empty")
	}
	if !strings.Contains(formatted, "job_1") {
		t.Fatalf("formatJobStop missing job id: %q", formatted)
	}
	if !strings.Contains(formatted, "completed") {
		t.Fatalf("formatJobStop missing status: %q", formatted)
	}
	if !strings.Contains(formatted, "completed_during_stop") {
		t.Fatalf("formatJobStop missing outcome: %q", formatted)
	}
}

// TestFormatJobList covers formatJobList for various list shapes.
func TestFormatJobList(t *testing.T) {
	t.Parallel()
	// Empty list.
	got := formatJobList(jobListResult{Count: 0})
	if !strings.Contains(got, "No jobs") {
		t.Fatalf("empty list: %q", got)
	}

	// List with items.
	cmd := "echo hello"
	reason := "exit_zero"
	exitCode := 0
	resumable := true
	got = formatJobList(jobListResult{
		Count: 2,
		Items: []jobListEntry{
			{
				ID:         "job_1",
				Type:       "shell",
				Status:     "completed",
				StartedAt:  "2026-01-15T10:30:00Z",
				Reason:     &reason,
				ExitCode:   &exitCode,
				Command:    &cmd,
				TotalBytes: 100,
			},
			{
				ID:         "job_2",
				Type:       "delegate",
				Status:     "idle",
				Depth:      1,
				Resumable:  &resumable,
				TotalBytes: 200,
			},
		},
		Total: 2,
	})
	if !strings.Contains(got, "job_1") || !strings.Contains(got, "echo hello") {
		t.Fatalf("list with items missing data: %q", got)
	}
	if !strings.Contains(got, "depth=1") {
		t.Fatalf("list missing depth: %q", got)
	}
	if !strings.Contains(got, "resumable") {
		t.Fatalf("list missing resumable: %q", got)
	}
	if !strings.Contains(got, "2 job(s)") {
		t.Fatalf("list missing count: %q", got)
	}

	// With offset/pagination.
	got = formatJobList(jobListResult{
		Count: 2,
		Items: []jobListEntry{
			{ID: "job_3", Type: "shell", Status: "running", TotalBytes: 50},
		},
		Total:  5,
		Offset: 2,
	})
	if !strings.Contains(got, "showing 3-3 of 5") {
		t.Fatalf("pagination missing: %q", got)
	}

	// Offset past end.
	got = formatJobList(jobListResult{
		Count:  0,
		Items:  []jobListEntry{},
		Total:  3,
		Offset: 10,
	})
	if !strings.Contains(got, "showing none of 3") {
		t.Fatalf("past end missing: %q", got)
	}

	// With turn slots.
	got = formatJobList(jobListResult{
		Count: 1,
		Items: []jobListEntry{
			{ID: "job_1", Type: "shell", Status: "running", TotalBytes: 10},
		},
		TurnSlots: &turnSlotOccupancy{
			InUse:  2,
			Cap:    5,
			Jobs:   3,
			Drives: 1,
		},
	})
	if !strings.Contains(got, "turn slots") {
		t.Fatalf("turn slots missing: %q", got)
	}

	// With delegation allowance.
	got = formatJobList(jobListResult{
		Count: 1,
		Items: []jobListEntry{
			{ID: "job_1", Type: "shell", Status: "running", TotalBytes: 10},
		},
		DelegationAllowance: 3,
	})
	if !strings.Contains(got, "delegation_allowance: 3") {
		t.Fatalf("delegation allowance missing: %q", got)
	}
}

// TestShellTranscriptRef covers shellTranscriptRef.
func TestShellTranscriptRef(t *testing.T) {
	t.Parallel()
	if got := shellTranscriptRef("job_abc"); got != "job:job_abc" {
		t.Fatalf("shellTranscriptRef = %q", got)
	}
}

func jobIntPtr(v int) *int { return &v }

func shellTranscriptRefTest(jobID string) string {
	return shellTranscriptRef(jobID)
}
