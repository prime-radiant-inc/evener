package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/llm"
)

func runningJobByID(t *testing.T, jm *jobManager, jobID string) *runningJob {
	t.Helper()
	jm.mu.Lock()
	defer jm.mu.Unlock()
	run := jm.running[jobID]
	if run == nil {
		t.Fatalf("job %q is not running", jobID)
	}
	return run
}

func readJobListEntry(t *testing.T, s *Session, jobID string) jobListToolEntry {
	t.Helper()
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "list",
		Name:      "job_list",
		Arguments: json.RawMessage(`{"include_nested":true,"status":["running","completed","failed","cancelled","stopped"]}`),
	})
	if res.IsError {
		t.Fatalf("job_list returned error: %s", res.Output)
	}
	var out jobListToolOutput
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal job_list output: %v (output: %s)", err, res.Output)
	}
	entry := findJobListToolOutput(out.Jobs, jobID)
	if entry == nil {
		t.Fatalf("job_list missing job %q; jobs = %+v", jobID, out.Jobs)
	}
	return *entry
}

func readJobStatus(t *testing.T, s *Session, jobID string) jobStatusToolOutput {
	t.Helper()
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "status",
		Name:      "job_status",
		Arguments: json.RawMessage(fmt.Sprintf(`{"target":%q}`, jobID)),
	})
	if res.IsError {
		t.Fatalf("job_status returned error: %s", res.Output)
	}
	var out jobStatusToolOutput
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal job_status: %v (output: %s)", err, res.Output)
	}
	return out
}

type jobStatusToolOutput struct {
	JobID              string `json:"job_id"`
	Kind               string `json:"kind"`
	Status             string `json:"status"`
	Phase              string `json:"phase"`
	Reason             string `json:"reason"`
	RunningForMS       int64  `json:"running_for_ms"`
	DurationMS         int64  `json:"duration_ms"`
	QuietForMS         int64  `json:"quiet_for_ms"`
	StartedAt          string `json:"started_at"`
	EndedAt            string `json:"ended_at"`
	LastEventAt        string `json:"last_event_at"`
	TranscriptRef      string `json:"transcript_ref"`
	NotificationStatus string `json:"notification_status"`
}

func TestJobStatusRunningShellProjectsSupervisionFields(t *testing.T) {
	clk := agenttest.NewFakeClockAt(time.Unix(5000, 0).UTC())
	s := newSession(t, withConfig(SessionConfig{clock: clk}))
	jm := s.jobManager

	rec, err := jm.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, jm, rec.JobID) })

	clk.Advance(90 * time.Second)
	out := readJobStatus(t, s, rec.JobID)
	if out.JobID != rec.JobID {
		t.Fatalf("job_id = %q, want %q", out.JobID, rec.JobID)
	}
	if out.Kind != "shell" {
		t.Fatalf("kind = %q, want shell", out.Kind)
	}
	if out.Status != "running" {
		t.Fatalf("status = %q, want running", out.Status)
	}
	if out.Phase != "process_running" {
		t.Fatalf("phase = %q, want process_running", out.Phase)
	}
	if out.RunningForMS != 90000 {
		t.Fatalf("running_for_ms = %d, want 90000", out.RunningForMS)
	}
	if out.QuietForMS != 90000 {
		t.Fatalf("quiet_for_ms = %d, want 90000", out.QuietForMS)
	}
	if out.TranscriptRef != "job:"+rec.JobID {
		t.Fatalf("transcript_ref = %q, want job:%s", out.TranscriptRef, rec.JobID)
	}
	if out.StartedAt == "" || out.LastEventAt == "" {
		t.Fatalf("missing timestamps: %+v", out)
	}
	if out.NotificationStatus != "" {
		t.Fatalf("notification_status leaked into normal status: %+v", out)
	}
}

func TestJobListRowsIncludeStatusSupervisionFields(t *testing.T) {
	clk := agenttest.NewFakeClockAt(time.Unix(6000, 0).UTC())
	s := newSession(t, withConfig(SessionConfig{clock: clk}))
	jm := s.jobManager

	rec, err := jm.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, jm, rec.JobID) })

	clk.Advance(3 * time.Second)
	entry := readJobListEntry(t, s, rec.JobID)
	if entry.Kind != "shell" {
		t.Fatalf("kind = %q, want shell", entry.Kind)
	}
	if entry.Phase != "process_running" {
		t.Fatalf("phase = %q, want process_running", entry.Phase)
	}
	if entry.RunningForMS != 3000 {
		t.Fatalf("running_for_ms = %d, want 3000", entry.RunningForMS)
	}
	if entry.QuietForMS != 3000 {
		t.Fatalf("quiet_for_ms = %d, want 3000", entry.QuietForMS)
	}
	if entry.TranscriptRef == nil || *entry.TranscriptRef != "job:"+rec.JobID {
		t.Fatalf("transcript_ref = %v, want job:%s", entry.TranscriptRef, rec.JobID)
	}
}

func TestJobListLastActivityAdvancesWithShellOutput(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClockAt(time.Unix(1000, 0).UTC())
	s := newSession(t, withConfig(SessionConfig{clock: clk}))
	jm := s.jobManager

	rec, err := jm.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, jm, rec.JobID) })

	start := readJobListEntry(t, s, rec.JobID)
	if start.LastActivity == nil || *start.LastActivity != start.StartedAt {
		t.Fatalf("initial last_activity = %v, want StartedAt %q", start.LastActivity, start.StartedAt)
	}

	clk.Advance(5 * time.Minute)
	run := runningJobByID(t, jm, rec.JobID)
	if _, err := jm.appendJobOutput(rec.JobID, run.output, []byte("progress\n")); err != nil {
		t.Fatalf("appendJobOutput: %v", err)
	}

	after := readJobListEntry(t, s, rec.JobID)
	if after.LastActivity == nil {
		t.Fatal("after output, last_activity is nil")
	}
	want := clk.Now().Format(time.RFC3339Nano)
	if *after.LastActivity != want {
		t.Fatalf("last_activity after output = %q, want %q", *after.LastActivity, want)
	}
	if *after.LastActivity == *start.LastActivity {
		t.Fatalf("last_activity did not advance after output: %q", *after.LastActivity)
	}
}

func TestJobListTerminalLastActivityFallsBackToEndedAt(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClockAt(time.Unix(4000, 0).UTC())
	s := newSession(t, withConfig(SessionConfig{clock: clk}))
	jm := s.jobManager

	rec, err := jm.createShell(createShellOpts{Command: "true"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	clk.Advance(2 * time.Minute)
	finishRunningTestJob(t, jm, rec.JobID)

	entry := readJobListEntry(t, s, rec.JobID)
	if entry.EndedAt == nil || entry.LastActivity == nil {
		t.Fatalf("terminal job row missing ended_at or last_activity: %+v", entry)
	}
	if *entry.LastActivity != *entry.EndedAt {
		t.Fatalf("terminal last_activity = %q, want EndedAt fallback %q", *entry.LastActivity, *entry.EndedAt)
	}
	if *entry.LastActivity == entry.StartedAt {
		t.Fatalf("terminal last_activity fell back to StartedAt %q, want EndedAt %q", entry.StartedAt, *entry.EndedAt)
	}
}

func TestQuietWatchdogMessageNamesProductionWindow(t *testing.T) {
	t.Parallel()
	last := time.Unix(1000, 0).UTC()
	msg := quietWatchdogMessage(10*time.Minute, last)
	if !strings.HasPrefix(msg, "quiet for 10m;") {
		t.Fatalf("production quiet message = %q, want it to start with %q", msg, "quiet for 10m;")
	}
	if !strings.Contains(msg, last.Format(time.RFC3339Nano)) {
		t.Fatalf("quiet message = %q, want it to carry last activity %q", msg, last.Format(time.RFC3339Nano))
	}
}
