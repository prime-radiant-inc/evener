package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	tooldefs "primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

func toolResultJSON(result tooldefs.ExecResult) []byte {
	if len(result.ToolState) > 0 {
		return result.ToolState
	}
	return []byte(result.Output)
}

func handlerJSON(t *testing.T, value any) []byte {
	t.Helper()
	switch result := value.(type) {
	case tooldefs.StateResult:
		encoded, err := json.Marshal(result.State)
		if err != nil {
			t.Fatalf("marshal handler state: %v", err)
		}
		return encoded
	case string:
		return []byte(result)
	default:
		t.Fatalf("unexpected handler result type %T", value)
		return nil
	}
}

func newWalkJobManager(t *testing.T, sessionID string) *jobManager {
	t.Helper()
	manager, err := newJobManager(t.TempDir(), sessionID, func(jobNotification) {})
	if err != nil {
		t.Fatalf("new jobManager %q: %v", sessionID, err)
	}
	return manager
}

func newManualRunningJob(t *testing.T, s *Session) *jobstore.JobRecord {
	t.Helper()
	record, err := s.jobManager.createShell(createShellOpts{Command: "manual running job"})
	if err != nil {
		t.Fatalf("create shell job: %v", err)
	}
	t.Cleanup(func() {
		_ = s.jobManager.finalize(record.JobID, jobstore.StatusCancelled, "test_cleanup", nil)
		waitForShellDone(t, s.jobManager, record.JobID)
	})
	return record
}

func appendManualJobOutput(manager *jobManager, jobID, output string) {
	manager.mu.Lock()
	run := manager.running[jobID]
	manager.mu.Unlock()
	if run != nil {
		_, _ = run.output.Append([]byte(output))
	}
}

type jobListToolOutput struct {
	Jobs []jobListToolEntry `json:"items"`
}

type jobListToolEntry struct {
	ID               string  `json:"id"`
	JobID            string  `json:"job_id"`
	Kind             string  `json:"kind"`
	Type             string  `json:"type"`
	Status           string  `json:"status"`
	Phase            string  `json:"phase"`
	Description      string  `json:"description"`
	ParentJobID      *string `json:"parent_job_id"`
	ExhaustionBudget string  `json:"exhaustion_budget"`
	ExhaustionLimit  int     `json:"exhaustion_limit"`
	Resumable        *bool   `json:"resumable"`
	StartedAt        string  `json:"started_at"`
	EndedAt          *string `json:"ended_at"`
	LastActivity     *string `json:"last_activity"`
	RunningForMS     int64   `json:"running_for_ms"`
	DurationMS       int64   `json:"duration_ms"`
	QuietForMS       int64   `json:"quiet_for_ms"`
	LastEventAt      string  `json:"last_event_at"`
	TranscriptRef    *string `json:"transcript_ref"`
}

func findJobListToolOutput(records []jobListToolEntry, id string) *jobListToolEntry {
	for i := range records {
		if records[i].ID == id || records[i].JobID == id {
			return &records[i]
		}
	}
	return nil
}

func TestJobStatusReportsRunTimeoutAttribution(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	executor, ok := s.env.(execenv.StreamingExecutor)
	if !ok {
		t.Fatal("test environment does not support streaming")
	}
	result := runShell(context.Background(), s.jobManager, executor, shellArgs{
		Command:        "sleep 5",
		BlockTimeoutMS: 5000,
		MaxRuntimeMS:   1000,
	})
	if result.JobID == "" || result.Status != string(jobstore.StatusStopped) || result.Reason != "run_timeout" {
		t.Fatalf("shell result = %#v, want stopped/run_timeout", result)
	}
	waitForShellDone(t, s.jobManager, result.JobID)
	status := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID: "status", Name: "job_status", Arguments: json.RawMessage(fmt.Sprintf(`{"target":%q}`, result.JobID)),
	})
	if status.IsError {
		t.Fatalf("job_status: %s", status.Output)
	}
	var decoded struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(toolResultJSON(status), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Status != string(jobstore.StatusStopped) || decoded.Reason != "run_timeout" {
		t.Fatalf("job_status = %#v, want stopped/run_timeout", decoded)
	}
}

func TestJobListDescriptionFallbackToTask(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	const jobID = "job_task_only"
	started := time.Unix(2000, 0).UTC()
	if err := s.jobManager.appendEvent(jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Task: "my_task", OwnerSessionID: s.ID(), VisibleToSession: s.ID(), StartedAt: &started,
	}); err != nil {
		t.Fatal(err)
	}
	value, err := jobListTool(s, nil, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var decoded jobListToolOutput
	if err := json.Unmarshal(handlerJSON(t, value), &decoded); err != nil {
		t.Fatal(err)
	}
	row := findJobListToolOutput(decoded.Jobs, jobID)
	if row == nil || row.Description != "my_task" {
		t.Fatalf("task-only shell row = %#v", row)
	}
}

func TestJobStatusDescriptionFallbackToTask(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	const jobID = "job_status_task_only"
	started := time.Unix(3000, 0).UTC()
	ended := time.Unix(3001, 0).UTC()
	if err := s.jobManager.appendJobEvents([]jobstore.Event{
		{Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell, Task: "status_task", OwnerSessionID: s.ID(), VisibleToSession: s.ID(), StartedAt: &started},
		{Kind: jobstore.EventJobFinished, TS: ended, JobID: jobID, Status: jobstore.StatusCompleted, EndedAt: &ended, TerminalGen: "GEN_DONE"},
	}); err != nil {
		t.Fatal(err)
	}
	value, err := jobStatusTool(s, map[string]any{"target": jobID}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(handlerJSON(t, value), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Description != "status_task" {
		t.Fatalf("description = %q, want status_task", decoded.Description)
	}
}
