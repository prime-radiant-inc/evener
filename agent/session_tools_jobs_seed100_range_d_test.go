//go:build serffuzz

package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// seed100ToolsRangeD is the canonical deterministic branch matrix for the tail
// helpers in session_tools_jobs.go. It is called by the tagged seed-100 driver.
func seed100ToolsRangeD(t *testing.T) {
	t.Helper()

	watchCases := []map[string]any{
		{"operation": "list", "source": "self"},
		{"operation": "inspect"},
		{"operation": "clear"},
		{"operation": "unknown"},
		{"operation": "create", "source": "*"},
		{"operation": "list"},
		{"operation": "inspect", "watch_id": "watch_1"},
	}
	for _, args := range watchCases {
		_, _ = watchArgsFromToolArgs(args)
	}

	for _, args := range []map[string]any{
		{},
		{"event_filter": "bad"},
		{"event_filter": map[string]any{"tool_name": 1}},
		{"event_filter": map[string]any{"unknown": "x"}},
		{"event_filter": map[string]any{"tool_name": "  ", "status": ""}},
		{"event_filter": map[string]any{"tool_name": " shell ", "status": "ok"}},
	} {
		_, _ = watchEventFilterArg(args)
	}
	for _, args := range []map[string]any{
		{}, {"events": "bad"}, {"events": []any{"ok", 1}}, {"events": []any{"a", "b"}},
	} {
		_, _ = stringArrayArg(args, "events")
	}
	for _, args := range []map[string]any{
		{}, {"send": "bad"}, {"send": map[string]any{}},
		{"send": map[string]any{"message": "x"}},
		{"send": map[string]any{"to": " dlg_1 ", "message": "hi", "include_excerpt": true}},
	} {
		_, _ = watchSendArg(args)
	}
	_ = isEmptyWatchSend(map[string]any{})
	_ = isEmptyWatchSend(map[string]any{"to": "x"})

	for _, args := range []map[string]any{
		{}, {"types": "bad"}, {"types": []any{"shell", "delegate"}}, {"types": []any{"bogus"}},
	} {
		_, _ = jobTypeArrayArg(args, "types")
	}

	s := newSession(t)
	jm := s.jobManager
	freezeClock(jm)
	_, _ = findJobRecord(jm, "job_missing")
	_ = waitForJobDone(context.Background(), jm, "job_missing", time.Second)
	_, _ = jobDone(jm, "job_missing")

	now := frozenTestTime
	ended := now.Add(3 * time.Second)
	activity := now.Add(time.Second)
	exit := 0
	running := &jobstore.JobRecord{JobID: "job_run", Type: jobstore.JobShell, Status: jobstore.StatusRunning, StartedAt: now.Add(-time.Second), LastActivity: &activity, Command: "echo"}
	terminal := &jobstore.JobRecord{JobID: "job_done", Type: jobstore.JobShell, Status: jobstore.StatusCompleted, StartedAt: now, EndedAt: &ended, ExitCode: &exit, Reason: "done"}
	unknown := &jobstore.JobRecord{JobID: "job_unknown", Type: "other", Status: jobstore.StatusRunning, StartedAt: now}
	_ = projectJobRecord(s, running)
	_ = projectJobRecordForViewer(nil, nil, terminal)
	_ = projectJobRecordForViewer(nil, s, running)
	_ = projectJobStatus(now, running)
	_ = projectJobStatus(now, terminal)
	_ = projectJobStatus(now, unknown)
	_ = lastActivityProjection(running)
	_ = lastActivityProjection(terminal)
	_ = lastActivityProjection(unknown)
	_ = projectDelegateRecord(nil)
	_ = projectDelegateRecord(&jobstore.DelegateRecord{DelegateID: "dlg_1", Status: "running", CurrentJobID: "job_run"})

	_, _ = marshalBoundedJSON(map[string]string{"x": "y"}, 100)
	_, _ = marshalBoundedJSON(map[string]string{"x": "long"}, 1)
	_, _ = marshalBoundedJSON(make(chan int), 100)
	_, _, _ = marshalBoundedJSONWithFit(map[string]string{"x": "y"}, 100)
	_, _, _ = marshalBoundedJSONWithFit(map[string]string{"x": "long"}, 1)
	_, _, _ = marshalBoundedJSONWithFit(make(chan int), 100)

	_ = jobToolResultMaxChars(nil, "missing")
	reg := tool.NewRegistry()
	_ = jobToolResultMaxChars(reg, "missing")
	for _, tc := range []struct {
		name string
		max  int
	}{
		{"job_status", 1}, {"job_list", jobToolResultMinJSONChars}, {"other", 9000},
	} {
		err := reg.Register(tool.RegisteredTool{
			Tool:  llm.Tool{Definition: llm.ToolDefinition{Name: tc.name, Description: "seed"}},
			Limit: schema.ToolOutputLimit{MaxChars: tc.max},
			Exec:  func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) { return "", nil },
		})
		if err != nil {
			t.Fatalf("register %s: %v", tc.name, err)
		}
	}
	_ = jobToolResultMaxChars(reg, "job_status")
	_ = jobToolResultMaxChars(reg, "other")
	enforceJobToolJSONLimits(nil)
	enforceJobToolJSONLimits(reg)

	_ = stringPtrOrNil("")
	_ = stringPtrOrNil("x")
	_ = timePtrOrNil(nil)
	_ = timePtrOrNil(&now)
	_ = int64Ptr(1)
	_ = publicJobKind(jobstore.JobDelegate)
	_ = publicJobKind(jobstore.JobShell)
	_ = shellTranscriptRef("job_1")
	_ = defaultJobPhase(nil)
	_ = defaultJobPhase(terminal)
	_ = defaultJobPhase(&jobstore.JobRecord{Type: jobstore.JobDelegate, Status: jobstore.StatusRunning})
	_ = defaultJobPhase(&jobstore.JobRecord{Type: jobstore.JobShell, Status: jobstore.StatusRunning})
	_ = defaultJobPhase(&jobstore.JobRecord{Type: "other", Status: jobstore.StatusRunning})
	_ = defaultJobPhase(&jobstore.JobRecord{Type: jobstore.JobShell, Status: jobstore.StatusRunning, Phase: "custom"})
	_ = jobTranscriptRef(nil)
	_ = jobTranscriptRef(&jobstore.JobRecord{TranscriptRef: "local:x"})
	_ = jobTranscriptRef(&jobstore.JobRecord{Type: jobstore.JobShell, JobID: "job_1"})
	_ = jobTranscriptRef(&jobstore.JobRecord{Type: jobstore.JobDelegate})
}
