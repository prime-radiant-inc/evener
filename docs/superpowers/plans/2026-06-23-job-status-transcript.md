# Job Status Transcript Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `job_read_output` as the primary job supervision surface with `job_status`, `read_transcript`, transcript refs for agent and shell jobs, and readiness notifications through `job_watch`.

**Architecture:** Add compact job supervision projection in `agent/session_tools_jobs.go`, with `kind/status/phase/running_for_ms/quiet_for_ms/transcript_ref` computed from live job records. Add a generic transcript reader in `agent/session_tools_transcript.go` that reads existing session transcripts and shell job output refs. Route server-readiness signals through the existing non-blocking `job_watch(output_match=...)` mechanism before removing `job_read_output` from the model-facing registry.

**Tech Stack:** Go, Serf agent tool registry, jobstore event/output logs, scripted-provider tests, deterministic package tests.

---

## File Structure

- Modify `agent/internal/jobstore/record.go`
  - Add lightweight `Phase` to live/durable job records.
- Modify `agent/jobs.go`
  - Seed shell transcript refs, phase, and event-clock timestamps.
  - Add helpers for job status projection and live phase/activity updates.
- Modify `agent/job_shell.go`
  - Seed delayed shell jobs with shell transcript refs and process phases.
- Modify `agent/job_delegate.go`
  - Seed agent jobs with `phase: "starting"`.
  - Link child sessions back to parent job activity updates.
- Modify `agent/subagents.go`
  - Install a parent-job activity callback into spawned child sessions.
- Modify `agent/session_config.go`
  - Add the callback field to `spawnConfig`.
- Modify `agent/session_lifecycle.go`, `agent/session_stream.go`, and `agent/session_tool_round.go`
  - Report observable child phases: `awaiting_model`, `model_streaming`, `tool_running`, and event-clock activity.
- Modify `agent/session_tools_jobs.go`
  - Register `job_status`.
  - Project richer `job_list` rows.
  - Remove model-facing `job_read_output` registration.
- Modify `agent/session_tools_transcript.go`
  - Register `read_transcript`.
  - Resolve `job:<job_id>` refs to shell output logs.
- Modify `agent/internal/tool/definitions.go`
  - Add `DefJobStatus` and `DefReadTranscript`.
  - Remove or stop advertising `DefJobReadOutput`.
  - Update `job_list` and transcript descriptions.
- Modify `agent/prompts/sections/background-jobs.md`
  - Route orientation to `job_status`, raw evidence to `read_transcript`, completion to notifications, and readiness signals to `job_watch(output_match=...)`.
- Modify tests in:
  - `agent/job_supervision_test.go`
  - `agent/session_tools_jobs_test.go`
  - `agent/transcript_tools_test.go`
  - `agent/internal/tool/definitions_test.go`

## Task 1: Add Status Projection And `job_status`

**Files:**
- Modify: `agent/job_supervision_test.go`
- Modify: `agent/session_tools_jobs.go`
- Modify: `agent/internal/tool/definitions.go`

- [ ] **Step 1: Write failing tests for running shell status**

Add this test to `agent/job_supervision_test.go`:

```go
func TestJobStatusRunningShellProjectsSupervisionFields(t *testing.T) {
	s := newTestSession(t)
	jm := s.jobManager
	clk := newMutableClock(time.Unix(5000, 0).UTC())
	jm.now = clk.now

	rec, err := jm.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, jm, rec.JobID) })

	clk.advance(90 * time.Second)
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "status",
		Name:      "job_status",
		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q}`, rec.JobID)),
	})
	if res.IsError {
		t.Fatalf("job_status returned error: %s", res.Output)
	}

	var out jobStatusToolOutput
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal job_status: %v (output: %s)", err, res.Output)
	}
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
```

Add this local test shape near the existing test helper structs:

```go
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
	LastEventAt         string `json:"last_event_at"`
	TranscriptRef      string `json:"transcript_ref"`
	NotificationStatus string `json:"notification_status"`
}
```

- [ ] **Step 2: Run test to verify RED**

Run:

```bash
go test ./agent -run '^TestJobStatusRunningShellProjectsSupervisionFields$' -count=1 -v
```

Expected: FAIL because `job_status` is not registered.

- [ ] **Step 3: Implement minimal `job_status` for shell jobs**

In `agent/internal/tool/definitions.go`, add:

```go
func DefJobStatus() llm.ToolDefinition {
	strictFalse := false
	return llm.ToolDefinition{
		Name:        "job_status",
		Description: "Inspect one durable job by job_id. Use this for orientation: kind, lifecycle status, observable phase, running/quiet time, and transcript_ref. Completion is notification-driven; do not poll this waiting for completed. Read raw evidence with read_transcript(transcript_ref).",
		Strict:      &strictFalse,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"job_id": map[string]any{"type": "string"},
			},
			"required": []string{"job_id"},
		},
	}
}
```

In `agent/session_tools_jobs.go`, register it before `job_list`:

```go
if err := reg.Register(tool.RegisteredTool{
	Tool:  llm.Tool{Definition: tool.DefJobStatus(), ReadOnly: true},
	Limit: schema.ToolOutputLimit{MaxChars: jobToolResultDefaultMaxChar, Strategy: schema.TruncTail},
	Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
		_ = ctx
		_ = env
		return jobStatusTool(s, args, jobToolResultMaxChars(reg, "job_status"))
	},
}); err != nil {
	return err
}
```

Add constants and result shape in `agent/session_tools_jobs.go`:

```go
const (
	jobKindAgent = "agent"
	jobKindShell = "shell"

	jobPhaseStarting       = "starting"
	jobPhaseAwaitingModel  = "awaiting_model"
	jobPhaseModelStreaming = "model_streaming"
	jobPhaseToolRunning    = "tool_running"
	jobPhaseProcessRunning = "process_running"
	jobPhaseStopping       = "stopping"
)

type jobStatusResult struct {
	JobID         string  `json:"job_id"`
	Kind          string  `json:"kind"`
	Status        string  `json:"status"`
	Phase         string  `json:"phase,omitempty"`
	Reason        *string `json:"reason,omitempty"`
	RunningForMS  *int64  `json:"running_for_ms,omitempty"`
	DurationMS    *int64  `json:"duration_ms,omitempty"`
	QuietForMS    *int64  `json:"quiet_for_ms,omitempty"`
	StartedAt     string  `json:"started_at"`
	EndedAt       *string `json:"ended_at,omitempty"`
	LastEventAt   *string `json:"last_event_at,omitempty"`
	TranscriptRef string  `json:"transcript_ref"`
	ExitCode      *int    `json:"exit_code,omitempty"`
}
```

Add helpers:

```go
func publicJobKind(t jobstore.JobType) string {
	if t == jobstore.JobDelegate {
		return jobKindAgent
	}
	return jobKindShell
}

func shellTranscriptRef(jobID string) string {
	return "job:" + jobID
}

func defaultJobPhase(rec *jobstore.JobRecord) string {
	if rec == nil || rec.Status.IsTerminal() {
		return ""
	}
	switch rec.Type {
	case jobstore.JobDelegate:
		return jobPhaseStarting
	case jobstore.JobShell:
		return jobPhaseProcessRunning
	default:
		return ""
	}
}

func jobTranscriptRef(rec *jobstore.JobRecord) string {
	if rec == nil {
		return ""
	}
	if rec.TranscriptRef != "" {
		return rec.TranscriptRef
	}
	if rec.Type == jobstore.JobShell && rec.JobID != "" {
		return shellTranscriptRef(rec.JobID)
	}
	return ""
}
```

Add projection and tool handler:

```go
func projectJobStatus(now time.Time, rec *jobstore.JobRecord) jobStatusResult {
	last := rec.StartedAt
	if rec.LastActivity != nil {
		last = *rec.LastActivity
	} else if rec.EndedAt != nil {
		last = *rec.EndedAt
	}

	out := jobStatusResult{
		JobID:         rec.JobID,
		Kind:          publicJobKind(rec.Type),
		Status:        string(rec.Status),
		Phase:         defaultJobPhase(rec),
		Reason:        stringPtrOrNil(rec.Reason),
		StartedAt:     rec.StartedAt.Format(time.RFC3339Nano),
		EndedAt:       timePtrOrNil(rec.EndedAt),
		LastEventAt:   timePtrOrNil(&last),
		TranscriptRef: jobTranscriptRef(rec),
		ExitCode:      rec.ExitCode,
	}
	if rec.Status.IsTerminal() {
		end := now
		if rec.EndedAt != nil {
			end = *rec.EndedAt
		}
		d := end.Sub(rec.StartedAt).Milliseconds()
		out.DurationMS = &d
		out.Phase = ""
	} else {
		running := now.Sub(rec.StartedAt).Milliseconds()
		quiet := now.Sub(last).Milliseconds()
		out.RunningForMS = &running
		out.QuietForMS = &quiet
	}
	return out
}

func jobStatusTool(s *Session, args map[string]any, maxChars int) (any, error) {
	jobID := strings.TrimSpace(stringArg(args, "job_id"))
	if jobID == "" {
		return "", errors.New("invalid_request: job_id is required")
	}
	jm, rec, err := s.nestedOrLocalJobManager(jobID)
	if err != nil {
		return "", err
	}
	out := projectJobStatus(jm.now(), rec)
	b, err := marshalBoundedJSON(out, maxChars)
	if err != nil {
		return "", err
	}
	return tool.StateResult{Output: b, State: out}, nil
}
```

- [ ] **Step 4: Run test to verify GREEN**

Run:

```bash
go test ./agent -run '^TestJobStatusRunningShellProjectsSupervisionFields$' -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/job_supervision_test.go agent/session_tools_jobs.go agent/internal/tool/definitions.go
git commit -m "Add job status supervision tool" -m "Introduce job_status as the compact orientation surface for durable jobs. The first behavior covers shell jobs with public kind, lifecycle status, phase, elapsed timing, quiet timing, timestamps, and a shell transcript ref."
```

## Task 2: Make `job_list` Good Enough By Default

**Files:**
- Modify: `agent/job_supervision_test.go`
- Modify: `agent/session_tools_jobs.go`
- Modify: `agent/internal/tool/definitions.go`

- [ ] **Step 1: Write failing tests for enriched job_list rows**

Add to `agent/job_supervision_test.go`:

```go
func TestJobListRowsIncludeStatusSupervisionFields(t *testing.T) {
	s := newTestSession(t)
	jm := s.jobManager
	clk := newMutableClock(time.Unix(6000, 0).UTC())
	jm.now = clk.now

	rec, err := jm.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, jm, rec.JobID) })

	clk.advance(3 * time.Second)
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
```

Extend `jobListToolEntry` in `agent/session_tools_jobs_test.go`:

```go
Kind         string `json:"kind"`
Phase        string `json:"phase"`
RunningForMS int64  `json:"running_for_ms"`
DurationMS   int64  `json:"duration_ms"`
QuietForMS   int64  `json:"quiet_for_ms"`
LastEventAt  string `json:"last_event_at"`
```

- [ ] **Step 2: Run test to verify RED**

```bash
go test ./agent -run '^TestJobListRowsIncludeStatusSupervisionFields$' -count=1 -v
```

Expected: FAIL because the row lacks `kind`, `phase`, `running_for_ms`, `quiet_for_ms`, and shell `transcript_ref`.

- [ ] **Step 3: Enrich job_list rows from the same projection**

Update `jobListEntry` in `agent/session_tools_jobs.go`:

```go
Kind         string `json:"kind"`
Phase        string `json:"phase,omitempty"`
RunningForMS *int64 `json:"running_for_ms,omitempty"`
DurationMS   *int64 `json:"duration_ms,omitempty"`
QuietForMS   *int64 `json:"quiet_for_ms,omitempty"`
LastEventAt  *string `json:"last_event_at,omitempty"`
```

In `projectJobRecordForViewer`, call `projectJobStatus` and copy fields:

```go
statusView := projectJobStatus(time.Now(), rec)
if assessor != nil && assessor.jobManager != nil {
	statusView = projectJobStatus(assessor.jobManager.now(), rec)
}
```

Then set:

```go
Kind:          statusView.Kind,
Phase:         statusView.Phase,
TranscriptRef: stringPtrOrNil(statusView.TranscriptRef),
RunningForMS:  statusView.RunningForMS,
DurationMS:    statusView.DurationMS,
QuietForMS:    statusView.QuietForMS,
LastEventAt:   statusView.LastEventAt,
```

Keep existing `type` for internal filters during the transition, but make `kind`
the model-facing field that callers should use.

Update `DefJobList` description so it says rows include `kind`, `status`,
`phase`, `running_for_ms`, `quiet_for_ms`, and `transcript_ref`.

- [ ] **Step 4: Run tests**

```bash
go test ./agent -run '^(TestJobListRowsIncludeStatusSupervisionFields|TestJobStatusRunningShellProjectsSupervisionFields)$' -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/job_supervision_test.go agent/session_tools_jobs_test.go agent/session_tools_jobs.go agent/internal/tool/definitions.go
git commit -m "Enrich job list supervision rows" -m "Make job_list useful for orientation by default. Rows now carry public kind, observable phase, running and quiet durations, event timestamps, and transcript refs so most supervision does not require a follow-up job_status call."
```

## Task 3: Track Agent Phases And Event Quiet Time

**Files:**
- Modify: `agent/job_supervision_test.go`
- Modify: `agent/internal/jobstore/record.go`
- Modify: `agent/jobs.go`
- Modify: `agent/job_delegate.go`
- Modify: `agent/subagents.go`
- Modify: `agent/session_config.go`
- Modify: `agent/session_lifecycle.go`
- Modify: `agent/session_stream.go`
- Modify: `agent/session_tool_round.go`

- [ ] **Step 1: Write failing test for child phase/activity callback**

Add to `agent/job_supervision_test.go`:

```go
func TestAgentJobStatusUpdatesFromChildObservablePhases(t *testing.T) {
	parent := newTestSession(t)
	child := newTestSession(t)
	clk := newMutableClock(time.Unix(7000, 0).UTC())
	parent.jobManager.now = clk.now

	sub := completedDelegateSubagent(child, "report")
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "investigate", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}
	t.Cleanup(func() {
		_ = parent.finalizeDelegate(run.rec.JobID, child.ID(), sub)
		waitForShellDone(t, parent.jobManager, run.rec.JobID)
	})

	clk.advance(2 * time.Second)
	parent.jobManager.noteJobActivity(run.rec.JobID, jobPhaseAwaitingModel)
	status := readJobStatus(t, parent, run.rec.JobID)
	if status.Kind != "agent" {
		t.Fatalf("kind = %q, want agent", status.Kind)
	}
	if status.Phase != "awaiting_model" {
		t.Fatalf("phase = %q, want awaiting_model", status.Phase)
	}
	if status.QuietForMS != 0 {
		t.Fatalf("quiet_for_ms immediately after event = %d, want 0", status.QuietForMS)
	}

	clk.advance(5 * time.Second)
	status = readJobStatus(t, parent, run.rec.JobID)
	if status.QuietForMS != 5000 {
		t.Fatalf("quiet_for_ms after no events = %d, want 5000", status.QuietForMS)
	}

	parent.jobManager.noteJobActivity(run.rec.JobID, jobPhaseToolRunning)
	status = readJobStatus(t, parent, run.rec.JobID)
	if status.Phase != "tool_running" {
		t.Fatalf("phase = %q, want tool_running", status.Phase)
	}
	if status.QuietForMS != 0 {
		t.Fatalf("quiet_for_ms after tool event = %d, want 0", status.QuietForMS)
	}
}
```

Add helper:

```go
func readJobStatus(t *testing.T, s *Session, jobID string) jobStatusToolOutput {
	t.Helper()
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "status",
		Name:      "job_status",
		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q}`, jobID)),
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
```

- [ ] **Step 2: Run test to verify RED**

```bash
go test ./agent -run '^TestAgentJobStatusUpdatesFromChildObservablePhases$' -count=1 -v
```

Expected: FAIL because `noteJobActivity` and `Phase` do not exist.

- [ ] **Step 3: Add phase storage and activity helper**

In `agent/internal/jobstore/record.go`, add:

```go
Phase string `json:"phase,omitempty"`
```

near `LastActivity`.

In `agent/jobs.go`, add:

```go
func (jm *jobManager) noteJobActivity(jobID, phase string) {
	if jm == nil || strings.TrimSpace(jobID) == "" {
		return
	}
	now := jm.now()
	jm.mu.Lock()
	defer jm.mu.Unlock()
	run := jm.running[jobID]
	if run == nil || run.rec == nil || run.rec.Status.IsTerminal() {
		return
	}
	run.rec.LastActivity = &now
	if phase != "" {
		run.rec.Phase = phase
	}
}
```

Seed phases:

```go
// shell create paths
Phase: jobPhaseProcessRunning,

// delegate attach path
Phase: jobPhaseStarting,
```

Update `defaultJobPhase` to return `rec.Phase` first.

- [ ] **Step 4: Run test to verify GREEN**

```bash
go test ./agent -run '^TestAgentJobStatusUpdatesFromChildObservablePhases$' -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Wire child sessions to parent job activity**

In `spawnConfig`, add:

```go
parentJobActivity func(jobID, phase string)
```

In `prepareSubagentRun`, when `parentJobID` is present and `s.jobManager != nil`, set:

```go
subCfg.spawn.parentJobActivity = s.jobManager.noteJobActivity
```

Add on `Session`:

```go
func (s *Session) noteParentJobActivity(phase string) {
	if s == nil || s.cfg.spawn.parentJobActivity == nil || s.cfg.spawn.parentJobID == "" {
		return
	}
	s.cfg.spawn.parentJobActivity(s.cfg.spawn.parentJobID, phase)
}
```

Call it at observable seams:

```go
// before model call
s.noteParentJobActivity(jobPhaseAwaitingModel)

// in consumeModelStream when assistant text/reasoning/tool stream events arrive
s.noteParentJobActivity(jobPhaseModelStreaming)

// immediately before execToolBatch
s.noteParentJobActivity(jobPhaseToolRunning)

// after persisting tool results, before the next model round
s.noteParentJobActivity(jobPhaseAwaitingModel)
```

Do not force `model_streaming` for providers that complete without deterministic stream events; those jobs remain `awaiting_model` during the model call.

- [ ] **Step 6: Run focused tests**

```bash
go test ./agent -run '^(TestAgentJobStatusUpdatesFromChildObservablePhases|TestJobListDelegateLastActivitySeededAtStart|TestQuietWatchdogFiresOnceForQuietDelegate)$' -count=1 -v
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add agent/job_supervision_test.go agent/internal/jobstore/record.go agent/jobs.go agent/job_shell.go agent/job_delegate.go agent/subagents.go agent/session_config.go agent/session_lifecycle.go agent/session_stream.go agent/session_tool_round.go agent/session_tools_jobs.go
git commit -m "Track observable agent job phases" -m "Thread child-session progress back to the parent delegate job. The event clock now resets on observable model, streaming, and tool phases instead of relying on delegate final output bytes."
```

## Task 4: Add Generic `read_transcript` And Shell Job Transcript Refs

**Files:**
- Modify: `agent/transcript_tools_test.go`
- Modify: `agent/session_tools_transcript.go`
- Modify: `agent/session_tool_registry.go`
- Modify: `agent/internal/tool/definitions.go`
- Modify: `agent/jobs.go`
- Modify: `agent/job_shell.go`

- [ ] **Step 1: Write failing test for shell transcript read**

Add to `agent/transcript_tools_test.go`:

```go
func TestReadTranscriptReadsShellJobRef(t *testing.T) {
	s := newTestSession(t)
	jm := s.jobManager
	rec, err := jm.createShell(createShellOpts{Command: "printf hello"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, jm, rec.JobID) })
	run := runningJobByID(t, jm, rec.JobID)
	if _, err := jm.appendJobOutput(rec.JobID, run.output, []byte("hello\n")); err != nil {
		t.Fatalf("appendJobOutput: %v", err)
	}

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "read",
		Name:      "read_transcript",
		Arguments: json.RawMessage(fmt.Sprintf(`{"transcript_ref":"job:%s"}`, rec.JobID)),
	})
	if res.IsError {
		t.Fatalf("read_transcript returned error: %s", res.Output)
	}
	var out readMarkdownEnvelope
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal read_transcript: %v (output: %s)", err, res.Output)
	}
	if out.TranscriptRef != "job:"+rec.JobID {
		t.Fatalf("transcript_ref = %q, want job:%s", out.TranscriptRef, rec.JobID)
	}
	if out.ContentType != "text/markdown" {
		t.Fatalf("content_type = %q, want text/markdown", out.ContentType)
	}
	if !strings.Contains(out.Content, "hello") {
		t.Fatalf("content missing shell output: %q", out.Content)
	}
}
```

- [ ] **Step 2: Run test to verify RED**

```bash
go test ./agent -run '^TestReadTranscriptReadsShellJobRef$' -count=1 -v
```

Expected: FAIL because `read_transcript` is not registered and `job:` refs are unsupported.

- [ ] **Step 3: Register `read_transcript`**

Add `DefReadTranscript` in `agent/internal/tool/definitions.go` with the same schema as `DefReadSessionTranscript`, but name it `read_transcript` and describe both session and job refs.

In `agent/session_tools_transcript.go`, replace the registered read tool with `readTranscriptTool(deps)` using `DefReadTranscript`.

Add `jobManager *jobManager` to `toolDeps` and populate it in `newToolDeps`.

- [ ] **Step 4: Resolve `job:` refs in the transcript reader**

In `execReadTranscript`, branch before `resolveTranscript`:

```go
if strings.HasPrefix(selector, "job:") {
	return readJobTranscript(deps, selector, rangeArg, format)
}
```

Implement:

```go
func readJobTranscript(deps *toolDeps, ref, rangeArg, format string) (any, error) {
	if deps == nil || deps.jobManager == nil {
		return nil, errors.New("job transcript unavailable: job manager is not available")
	}
	jobID := strings.TrimPrefix(ref, "job:")
	rec, err := findJobRecord(deps.jobManager, jobID)
	if err != nil {
		return nil, err
	}
	path := deps.jobManager.outputPathForJob(rec, jobID)
	content, total, dropped, truncated, err := readShellTranscriptContent(path, rec, rangeArg, format)
	if err != nil {
		return nil, err
	}
	return readMarkdownEnvelope{
		TranscriptRef: ref,
		Format:        formatMarkdown,
		ContentType:   "text/markdown",
		Content:       content,
		Meta: readMarkdownMeta{
			TurnsTotal:    1,
			Range:         "shell-log",
			TurnsRendered: 1,
			Truncated:     truncated || dropped > 0 || total > int64(len(content)),
		},
	}, nil
}
```

Keep shell rendering minimal for the first pass: command/status header plus retained output in a fenced block. Preserve stdout/stderr split later only if the output store actually records it separately.

- [ ] **Step 5: Run test to verify GREEN**

```bash
go test ./agent -run '^TestReadTranscriptReadsShellJobRef$' -count=1 -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add agent/transcript_tools_test.go agent/session_tools_transcript.go agent/session_tool_registry.go agent/internal/tool/definitions.go agent/jobs.go agent/job_shell.go
git commit -m "Read shell jobs through transcript refs" -m "Introduce read_transcript as the raw evidence path and teach it to resolve job:<job_id> refs. Shell job status now points at the same transcript reader path as agent jobs."
```

## Task 5: Use `job_watch` For Readiness Signals

**Files:**
- Modify: `agent/session_tools_jobs_test.go`
- Modify: `agent/internal/tool/definitions.go`
- Modify: `agent/prompts/sections/background-jobs.md`

- [ ] **Step 1: Write failing test for no blocking wait tool**

Add to `agent/session_tools_jobs_test.go`:

```go
func TestBlockingWaitToolsAreNotModelFacing(t *testing.T) {
	s := newTestSession(t)
	if got := s.reg.Get("wait_for_transcript_match"); got != nil {
		t.Fatalf("wait_for_transcript_match is still registered: %+v", got.Tool.Definition.Name)
	}
}
```

- [ ] **Step 2: Run test to verify RED**

```bash
go test ./agent -run '^TestBlockingWaitToolsAreNotModelFacing$' -count=1 -v
```

Expected: FAIL while the blocking wait tool is still registered.

- [ ] **Step 3: Remove the blocking wait surface**

Do not add a new blocking wait primitive. Remove any registration, provider
definition, prompt entry, and tests for `wait_for_transcript_match`. Teach
readiness signals through `job_watch(operation="create", source=<job_id>,
output_match=...)`.

- [ ] **Step 4: Run test to verify GREEN**

```bash
go test ./agent -run '^TestBlockingWaitToolsAreNotModelFacing$' -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session_tools_jobs_test.go agent/internal/tool/definitions.go agent/prompts/sections/background-jobs.md
git commit -m "Use watches for job readiness signals" -m "Avoid adding a blocking transcript wait primitive. Readiness notifications flow through job_watch(output_match), while raw evidence stays on read_transcript."
```

## Task 6: Remove `job_read_output` From The Model-Facing Surface

**Files:**
- Modify: `agent/session_tools_jobs.go`
- Modify: `agent/internal/tool/definitions.go`
- Modify: `agent/prompts/sections/background-jobs.md`
- Modify: tests referencing advertised tools

- [ ] **Step 1: Write failing registry test**

Add to `agent/session_tools_jobs_test.go`:

```go
func TestJobReadOutputIsNotModelFacing(t *testing.T) {
	s := newTestSession(t)
	if got := s.reg.Get("job_read_output"); got != nil {
		t.Fatalf("job_read_output is still registered: %+v", got.Tool.Definition.Name)
	}
	if got := s.reg.Get("job_status"); got == nil {
		t.Fatalf("job_status is not registered")
	}
	if got := s.reg.Get("wait_for_transcript_match"); got != nil {
		t.Fatalf("wait_for_transcript_match is still registered: %+v", got.Tool.Definition.Name)
	}
}
```

- [ ] **Step 2: Run test to verify RED**

```bash
go test ./agent -run '^TestJobReadOutputIsNotModelFacing$' -count=1 -v
```

Expected: FAIL because `job_read_output` is still registered.

- [ ] **Step 3: Remove registration and update prompts**

Remove the `DefJobReadOutput` registration from `registerJobTools`.

Keep internal helpers only if tests still need them for retained-output behavior; do not advertise `job_read_output`.

Update `background-jobs.md`:

```markdown
- Need to know what a job is doing -> `job_status`.
- Need raw evidence -> `read_transcript` with the `transcript_ref` from `job_status` or `job_list`.
- One signal ("the server printed ready") -> `job_watch(output_match=...)`.
- "Tell me when it finishes" -> the terminal notification is automatic.
```

Remove prompt text that tells agents to use job output as active delegate evidence.

- [ ] **Step 4: Update or delete obsolete tests**

For tests whose only purpose is `job_read_output` as the public surface, convert them to one of:

- `job_status` tests for status/timing/transcript refs.
- `read_transcript` tests for raw evidence.
- `job_watch(output_match)` tests for readiness notification behavior.

Do not delete coverage for useful behavior; move it to the right tool.

- [ ] **Step 5: Run focused tests**

```bash
go test ./agent -run 'Job(Status|List|Transcript|ReadOutput|WaitForTranscript|Quiet|Watch)' -count=1
go test ./agent/internal/tool -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add agent/session_tools_jobs.go agent/internal/tool/definitions.go agent/prompts/sections/background-jobs.md agent/session_tools_jobs_test.go agent/job_supervision_test.go agent/transcript_tools_test.go agent/internal/tool/definitions_test.go
git commit -m "Retire job_read_output from job supervision" -m "Remove job_read_output from the model-facing tool registry after replacing its supervision and evidence roles with job_status and read_transcript. Readiness signals use job_watch(output_match)."
```

## Task 7: Final Verification

**Files:**
- No planned production edits.

- [ ] **Step 1: Run package tests**

```bash
go test ./agent -count=1
go test ./agent/internal/tool -count=1
```

Expected: PASS.

- [ ] **Step 2: Run full suite**

```bash
go test ./...
```

Expected: PASS. If `cmd/serf-tui/internal/hubstart::TestStartLocalHubReportsImmediateExitOutput` fails once and passes on direct rerun, record it as the same pre-existing transient baseline observed before implementation.

- [ ] **Step 3: Inspect final diff**

```bash
git status --short
git diff --check
git log --oneline --decorate -6
```

Expected: clean tracked changes after commits; no whitespace errors.

## Self-Review Notes

- Spec coverage:
  - `job_status`: Task 1.
  - `job_list` orientation fields: Task 2.
  - Observable phases and provider-tolerant `model_streaming`: Task 3.
  - Single `transcript_ref` for agent and shell jobs: Tasks 1, 2, 4.
  - Raw evidence through transcript reader: Task 4.
  - Preserve one-shot readiness waits before removing `job_read_output`: Task 5.
  - Retire `job_read_output` from the model-facing surface: Task 6.
  - Prompt/tool description changes: Tasks 2, 4, 5, 6.
- The implementation deliberately keeps `model_streaming` honest: it is emitted only when streaming events are observed. Non-streaming providers can stay in `awaiting_model` until the model call returns.
- The shell transcript first pass projects the existing output log. It does not promise stdout/stderr split unless the store records that distinction.
