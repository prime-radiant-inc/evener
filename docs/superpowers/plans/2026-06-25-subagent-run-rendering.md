# Subagent Run Rendering Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make delegate/subagent jobs render as one coherent run in the web UI and TUI by carrying backend linkage through AppWire, merging live/cold signals, shortening IDs, and lazily showing bounded child-step previews.

**Architecture:** Add optional Serf-only delegate linkage fields to existing job event and `SerfJobInfo` payloads; do not add a new notification method. Web and TUI clients fold `serf/job/started`, `serf/job/finished`, delegate tool output, and cold thread data into a shared SubagentRun concept keyed by origin item/call first, then `jobId`, then `delegateId`. Preview content is fetched lazily and bounded by `transcriptRef` instead of embedded in notifications.

**Tech Stack:** Go backend/AppWire/projector tests, plain JavaScript web renderer with JSDOM tests, Bubble Tea TUI reducer/render tests, deterministic scripted tests only.

## Global Constraints

- Keep existing AppWire notification methods: `serf/job/started` and `serf/job/finished`.
- Preserve Codex app server compatibility: all new wire fields are optional `omitempty` Serf fields, and existing required fields keep their names and meanings.
- Do not add `serf/delegate/updated` in the initial implementation.
- Backend owns delegate/job/origin linkage; UI owns rendering, merging, short IDs, and previews.
- Populate delegate linkage from live `run.rec` / folded job records, not only flattened events.
- Include `transcriptRef` on delegate job start, not only finish.
- Non-delegate jobs must omit delegate-only fields.
- Default tests must be deterministic and must not require provider credentials, network access, quota, live model behavior, or ambient developer machine state.
- Inline child preview must be lazy-loaded, bounded to 3-5 direct child steps/items, sanitized like normal transcript items, and non-recursive by default.
- Never use raw long `job_...` or `dlg_...` as the primary collapsed label when task/job type/transcript label exists.
- Leave the pre-existing untracked file `kimi-jobs-ux-cleanup.md` untouched unless a later task explicitly scopes it in.

---

## File Structure

- Modify `agent/events/payloads.go`
  - Extend `JobStartedData` and `JobFinishedData` with optional delegate linkage fields.

- Modify `agent/jobs.go`
  - Populate linkage from `runningJob.rec` and `jobstore.Event` in `emitJobStarted` / `emitJobFinished`.

- Modify `agent/job_delegate_create_test.go` and `agent/job_delegate_send_test.go`
  - Add low-level event emission assertions proving start and finish events include delegate linkage for create and resume flows.

- Modify `appwire/types.go`
  - Extend `SerfJobInfo` with optional `delegateId`, `task`, `originTurnId`, `originToolCallId`, and `originItemId`.

- Modify `internal/appprojector/appwire_projection.go`
  - Copy new event fields into job notification `SerfJobInfo`.

- Modify `internal/appprojector/appwire_projection_test.go`
  - Assert enriched delegate notifications, shell omissions, and `omitempty` compatibility.

- Modify `cmd/serf-hub/assets/renderer-format.js`
  - Normalize snake/camel delegate linkage fields and add short-ID helper.

- Modify `cmd/serf-hub/assets/renderer.js`
  - Add SubagentRun indexes and merge by origin item/call, job ID, and delegate ID.
  - Update rows idempotently from delegate tool state and job notifications.
  - Add detail metadata and preview container hooks.

- Modify `cmd/serf-hub/assets/style.css`
  - Add subagent detail/metadata/preview styles if missing.

- Modify `cmd/serf-hub/jstest/test-subagents.js`
  - Add ordering, duplicate, delegate resume, orphan, short-ID, and preview scenarios.

- Modify `cmd/serf-tui/internal/transcript/types.go`
  - Add `SubagentRunInfo` and attach it to `ToolCallInfo`.

- Modify `cmd/serf-tui/internal/transcript/reducer.go`
  - Parse SubagentRun metadata from delegate tool raw/output and add notification merge methods.

- Modify `cmd/serf-tui/hub_notifications.go`
  - Handle `NotifySerfJobStarted` and `NotifySerfJobFinished` by updating the transcript reducer.

- Modify `cmd/serf-tui/internal/msgrender/tool_bodies.go`
  - Render delegate rows from structured `SubagentRunInfo`, using short IDs as secondary metadata.

- Modify `cmd/serf-tui/hub_appwire_test.go`, `cmd/serf-tui/hub_status_test.go`, and `cmd/serf-tui/internal/transcript/reducer_test.go`
  - Add deterministic notification/reducer/render status tests.

- Modify `internal/apptranscript/apptranscript.go` and `internal/apptranscript/apptranscript_test.go`
  - Preserve delegate tool-state linkage in `ThreadItem.Raw` and add cold reconciliation helper input/output seams.

- Add or modify a small Serf-only preview RPC in the existing appwire/appserver stack if existing transcript paging cannot fetch the latest bounded items by `transcriptRef`.
  - Prefer reusing existing thread/turn read plumbing; add a new RPC only if it keeps the implementation smaller and deterministic.

---

### Task 1: Backend event payload linkage

**Files:**
- Modify: `agent/events/payloads.go:207-225`
- Modify: `agent/jobs.go:664-701`
- Test: `agent/job_delegate_create_test.go`
- Test: `agent/job_delegate_send_test.go`

**Interfaces:**
- Consumes: `runningJob.rec` fields `DelegateID`, `Task`, `TranscriptRef`, `OriginTurnID`, `OriginToolCallID` and `jobstore.Event` fields with the same names.
- Produces: enriched event payloads:

```go
type JobStartedData struct {
    JobID            string `json:"job_id"`
    JobType          string `json:"job_type"`
    Status           string `json:"status"`
    FromWatch        bool   `json:"from_watch,omitempty"`
    DelegateID       string `json:"delegate_id,omitempty"`
    Task             string `json:"task,omitempty"`
    TranscriptRef    string `json:"transcript_ref,omitempty"`
    OriginTurnID     string `json:"origin_turn_id,omitempty"`
    OriginToolCallID string `json:"origin_tool_call_id,omitempty"`
    OriginItemID     string `json:"origin_item_id,omitempty"`
}

type JobFinishedData struct {
    JobID            string `json:"job_id"`
    JobType          string `json:"job_type"`
    Status           string `json:"status"`
    Reason           string `json:"reason"`
    ExitCode         *int   `json:"exit_code,omitempty"`
    OutputBytes      int64  `json:"output_bytes"`
    TranscriptRef    string `json:"transcript_ref,omitempty"`
    FromWatch        bool   `json:"from_watch,omitempty"`
    DelegateID       string `json:"delegate_id,omitempty"`
    Task             string `json:"task,omitempty"`
    OriginTurnID     string `json:"origin_turn_id,omitempty"`
    OriginToolCallID string `json:"origin_tool_call_id,omitempty"`
    OriginItemID     string `json:"origin_item_id,omitempty"`
}
```

- Later tasks rely on these event field names exactly.

- [ ] **Step 1: Write a failing create-flow event linkage test**

Append this test to `agent/job_delegate_create_test.go`. If helper names in this file differ at the exact insertion site, reuse the existing session/test harness in the file and keep the assertions unchanged.

```go
func TestDelegateJobEventsCarrySubagentRunLinkage(t *testing.T) {
    sess, cleanup := newTestSessionWithScriptedProvider(t, []string{
        `{"tool_calls":[{"id":"call_delegate_linkage","name":"delegate","arguments":{"task":"inspect linkage","max_wait_ms":0}}]}`,
        `{"message":"parent done"}`,
    })
    defer cleanup()

    var started []events.JobStartedData
    var finished []events.JobFinishedData
    sess.SetEventSinkForTest(func(ev events.SessionEvent) {
        switch data := ev.Data.(type) {
        case events.JobStartedData:
            if data.JobType == "delegate" {
                started = append(started, data)
            }
        case events.JobFinishedData:
            if data.JobType == "delegate" {
                finished = append(finished, data)
            }
        }
    })

    res := runDelegateToolForTest(t, sess, `{"task":"inspect linkage","max_wait_ms":0}`, "call_delegate_linkage")
    if res.JobID == "" || res.DelegateID == "" {
        t.Fatalf("delegate result missing ids: %+v", res)
    }

    if len(started) == 0 {
        t.Fatalf("no delegate JOB_STARTED events captured")
    }
    got := started[len(started)-1]
    if got.JobID != res.JobID || got.DelegateID != res.DelegateID || got.Task != "inspect linkage" || got.TranscriptRef == "" || got.OriginToolCallID != "call_delegate_linkage" {
        t.Fatalf("JOB_STARTED linkage = %+v, want job/delegate/task/transcript/origin call", got)
    }
}
```

- [ ] **Step 2: Run the focused test and verify it fails**

Run:

```bash
go test ./agent -run TestDelegateJobEventsCarrySubagentRunLinkage -count=1 -v
```

Expected: FAIL because `JobStartedData` does not yet expose/populate `DelegateID`, `Task`, `TranscriptRef`, or origin fields. If the harness helper names need adjustment, fix only the test harness wiring and keep the failing assertion target the same.

- [ ] **Step 3: Extend event payload structs**

In `agent/events/payloads.go`, replace the current `JobStartedData` / `JobFinishedData` definitions with the exact interface shown above.

- [ ] **Step 4: Populate `emitJobStarted` from `run.rec` and `jobstore.Event`**

Replace `emitJobStarted` in `agent/jobs.go` with:

```go
func (jm *jobManager) emitJobStarted(e jobstore.Event, run *runningJob) {
    if jm == nil || jm.emit == nil {
        return
    }
    fromWatch := false
    jobType := string(e.Type)
    delegateID := e.DelegateID
    task := e.Task
    transcriptRef := e.TranscriptRef
    originTurnID := e.OriginTurnID
    originToolCallID := e.OriginToolCallID
    if run != nil {
        fromWatch = run.fromWatch.Load()
        if run.rec != nil {
            if run.rec.Type != "" {
                jobType = string(run.rec.Type)
            }
            delegateID = firstNonEmptyString(run.rec.DelegateID, delegateID)
            task = firstNonEmptyString(run.rec.Task, task)
            transcriptRef = firstNonEmptyString(run.rec.TranscriptRef, transcriptRef)
            originTurnID = firstNonEmptyString(run.rec.OriginTurnID, originTurnID)
            originToolCallID = firstNonEmptyString(run.rec.OriginToolCallID, originToolCallID)
        }
    }
    jm.emit(events.EventJobStarted, events.JobStartedData{
        JobID:            e.JobID,
        JobType:          jobType,
        Status:           string(jobstore.StatusRunning),
        FromWatch:        fromWatch,
        DelegateID:       delegateID,
        Task:             task,
        TranscriptRef:    transcriptRef,
        OriginTurnID:     originTurnID,
        OriginToolCallID: originToolCallID,
    }, e.Provenance)
}
```

If `firstNonEmptyString` is not visible in `agent/jobs.go`, add this private helper near `emitJobStarted`:

```go
func firstNonEmptyJobString(values ...string) string {
    for _, value := range values {
        if strings.TrimSpace(value) != "" {
            return value
        }
    }
    return ""
}
```

and use `firstNonEmptyJobString` in this function instead of `firstNonEmptyString`. Add `strings` to the import list only if the new helper is needed.

- [ ] **Step 5: Populate `emitJobFinished` from `run.rec` and terminal event**

Replace the body of `emitJobFinished` in `agent/jobs.go` with:

```go
func (jm *jobManager) emitJobFinished(e jobstore.Event, run *runningJob) {
    if jm == nil || jm.emit == nil {
        return
    }
    jobType := string(e.Type)
    transcriptRef := e.TranscriptRef
    delegateID := e.DelegateID
    task := e.Task
    originTurnID := e.OriginTurnID
    originToolCallID := e.OriginToolCallID
    fromWatch := false
    if run != nil && run.rec != nil {
        if run.rec.Type != "" {
            jobType = string(run.rec.Type)
        }
        transcriptRef = firstNonEmptyJobString(run.rec.TranscriptRef, transcriptRef)
        delegateID = firstNonEmptyJobString(run.rec.DelegateID, delegateID)
        task = firstNonEmptyJobString(run.rec.Task, task)
        originTurnID = firstNonEmptyJobString(run.rec.OriginTurnID, originTurnID)
        originToolCallID = firstNonEmptyJobString(run.rec.OriginToolCallID, originToolCallID)
        fromWatch = run.fromWatch.Load()
    }
    jm.emit(events.EventJobFinished, events.JobFinishedData{
        JobID:            e.JobID,
        JobType:          jobType,
        Status:           string(e.Status),
        Reason:           e.Reason,
        ExitCode:         e.ExitCode,
        OutputBytes:      e.OutputBytes,
        TranscriptRef:    transcriptRef,
        FromWatch:        fromWatch,
        DelegateID:       delegateID,
        Task:             task,
        OriginTurnID:     originTurnID,
        OriginToolCallID: originToolCallID,
    }, e.Provenance)
}
```

- [ ] **Step 6: Add resume-flow assertion**

Append this test to `agent/job_delegate_send_test.go` near existing resume tests:

```go
func TestDelegateResumeJobStartedKeepsOriginalOriginLinkage(t *testing.T) {
    parent := newDelegateSendHarness(t)
    first := parent.startDelegate(t, "finish first", "call_original_delegate")

    parent.sendDelegate(t, first.DelegateID, `run again`, "start")

    var starts []events.JobStartedData
    for _, ev := range parent.events() {
        data, ok := ev.Data.(events.JobStartedData)
        if ok && data.JobType == "delegate" && data.DelegateID == first.DelegateID {
            starts = append(starts, data)
        }
    }
    if len(starts) < 2 {
        t.Fatalf("starts=%+v, want initial and resumed delegate starts", starts)
    }
    resumed := starts[len(starts)-1]
    if resumed.JobID == first.JobID || resumed.DelegateID != first.DelegateID || resumed.Task != "finish first" || resumed.OriginToolCallID != "call_original_delegate" || resumed.TranscriptRef == "" {
        t.Fatalf("resumed JOB_STARTED linkage = %+v", resumed)
    }
}
```

If this file already has a differently named harness, adapt only these calls to the existing helper names. The assertion contract must remain: same `delegateId`, new `jobId`, original task/origin, non-empty transcript ref.

- [ ] **Step 7: Run backend delegate event tests**

Run:

```bash
go test ./agent -run 'TestDelegateJobEventsCarrySubagentRunLinkage|TestDelegateResumeJobStartedKeepsOriginalOriginLinkage' -count=1 -v
```

Expected: PASS.

- [ ] **Step 8: Commit backend event linkage**

```bash
git add agent/events/payloads.go agent/jobs.go agent/job_delegate_create_test.go agent/job_delegate_send_test.go
git commit -m "feat(agent): carry delegate linkage in job events"
```

---

### Task 2: AppWire job notification projection compatibility

**Files:**
- Modify: `appwire/types.go:284-293`
- Modify: `internal/appprojector/appwire_projection.go:564-593`
- Modify: `internal/appprojector/appwire_projection_test.go:554-618`

**Interfaces:**
- Consumes: enriched `events.JobStartedData` and `events.JobFinishedData` from Task 1.
- Produces: enriched `appwire.SerfJobInfo`:

```go
type SerfJobInfo struct {
    JobID            string `json:"jobId"`
    JobType          string `json:"jobType"`
    Status           string `json:"status"`
    Reason           string `json:"reason,omitempty"`
    ExitCode         *int   `json:"exitCode,omitempty"`
    OutputBytes      int64  `json:"outputBytes"`
    TranscriptRef    string `json:"transcriptRef,omitempty"`
    FromWatch        bool   `json:"fromWatch,omitempty"`
    DelegateID       string `json:"delegateId,omitempty"`
    Task             string `json:"task,omitempty"`
    OriginTurnID     string `json:"originTurnId,omitempty"`
    OriginToolCallID string `json:"originToolCallId,omitempty"`
    OriginItemID     string `json:"originItemId,omitempty"`
}
```

- Later web/TUI tasks rely on these JSON names exactly.

- [ ] **Step 1: Update projection test to require delegate linkage**

In `internal/appprojector/appwire_projection_test.go`, update `TestAppEventProjectorProjectsJobEvents` started data to:

```go
Data: events.JobStartedData{
    JobID:            "job_1",
    JobType:          "delegate",
    Status:           "running",
    FromWatch:        true,
    DelegateID:       "dlg_1",
    Task:             "inspect invoices",
    TranscriptRef:    "local:child-start",
    OriginTurnID:     "turn_parent",
    OriginToolCallID: "call_delegate",
    OriginItemID:     "item_delegate",
},
```

Replace the started assertion with:

```go
if startedJob.JobID != "job_1" || startedJob.JobType != "delegate" || startedJob.Status != "running" || !startedJob.FromWatch ||
    startedJob.DelegateID != "dlg_1" || startedJob.Task != "inspect invoices" || startedJob.TranscriptRef != "local:child-start" ||
    startedJob.OriginTurnID != "turn_parent" || startedJob.OriginToolCallID != "call_delegate" || startedJob.OriginItemID != "item_delegate" {
    t.Fatalf("started job=%+v", startedJob)
}
```

Update finished data similarly:

```go
Data: events.JobFinishedData{
    JobID:            "job_1",
    JobType:          "delegate",
    Status:           "failed",
    Reason:           "signal",
    ExitCode:         &exitCode,
    OutputBytes:      0,
    TranscriptRef:    "local:child",
    DelegateID:       "dlg_1",
    Task:             "inspect invoices",
    OriginTurnID:     "turn_parent",
    OriginToolCallID: "call_delegate",
    OriginItemID:     "item_delegate",
},
```

and extend the finished assertion with those same field checks.

- [ ] **Step 2: Add omitempty compatibility test**

Append this test to `internal/appprojector/appwire_projection_test.go`:

```go
func TestSerfJobInfoDelegateFieldsAreOptional(t *testing.T) {
    payload, err := json.Marshal(appwire.SerfJobInfo{
        JobID:       "job_shell",
        JobType:     "shell",
        Status:      "running",
        OutputBytes: 0,
    })
    if err != nil {
        t.Fatal(err)
    }
    text := string(payload)
    for _, forbidden := range []string{"delegateId", "task", "originTurnId", "originToolCallId", "originItemId", "transcriptRef"} {
        if strings.Contains(text, forbidden) {
            t.Fatalf("shell job payload %s unexpectedly contains %s", text, forbidden)
        }
    }

    var oldClient struct {
        JobID   string `json:"jobId"`
        JobType string `json:"jobType"`
        Status  string `json:"status"`
    }
    enriched := []byte(`{"jobId":"job_1","jobType":"delegate","status":"running","delegateId":"dlg_1","task":"inspect"}`)
    if err := json.Unmarshal(enriched, &oldClient); err != nil {
        t.Fatal(err)
    }
    if oldClient.JobID != "job_1" || oldClient.JobType != "delegate" || oldClient.Status != "running" {
        t.Fatalf("old client decode = %+v", oldClient)
    }
}
```

Ensure `encoding/json` and `strings` imports exist in the test file.

- [ ] **Step 3: Run AppWire projection tests and verify they fail**

Run:

```bash
go test ./internal/appprojector -run 'TestAppEventProjectorProjectsJobEvents|TestSerfJobInfoDelegateFieldsAreOptional' -count=1 -v
```

Expected: FAIL because `SerfJobInfo` lacks the new fields and projection does not populate them.

- [ ] **Step 4: Extend `SerfJobInfo`**

In `appwire/types.go`, replace `SerfJobInfo` with the interface shown at the top of this task.

- [ ] **Step 5: Project started linkage**

In `internal/appprojector/appwire_projection.go`, add these fields to the `appwire.SerfJobInfo` constructed for `events.EventJobStarted`:

```go
DelegateID:       data.DelegateID,
Task:             data.Task,
TranscriptRef:    data.TranscriptRef,
OriginTurnID:     data.OriginTurnID,
OriginToolCallID: data.OriginToolCallID,
OriginItemID:     data.OriginItemID,
```

The full started job literal should include existing fields plus the new fields:

```go
job := appwire.SerfJobInfo{
    JobID:            data.JobID,
    JobType:          data.JobType,
    Status:           data.Status,
    FromWatch:        data.FromWatch,
    DelegateID:       data.DelegateID,
    Task:             data.Task,
    TranscriptRef:    data.TranscriptRef,
    OriginTurnID:     data.OriginTurnID,
    OriginToolCallID: data.OriginToolCallID,
    OriginItemID:     data.OriginItemID,
}
```

- [ ] **Step 6: Project finished linkage**

In the `events.EventJobFinished` branch, add:

```go
DelegateID:       data.DelegateID,
Task:             data.Task,
OriginTurnID:     data.OriginTurnID,
OriginToolCallID: data.OriginToolCallID,
OriginItemID:     data.OriginItemID,
```

- [ ] **Step 7: Run AppWire projection tests**

Run:

```bash
go test ./internal/appprojector -run 'TestAppEventProjectorProjectsJobEvents|TestSerfJobInfoDelegateFieldsAreOptional' -count=1 -v
```

Expected: PASS.

- [ ] **Step 8: Commit AppWire projection**

```bash
git add appwire/types.go internal/appprojector/appwire_projection.go internal/appprojector/appwire_projection_test.go
git commit -m "feat(appwire): expose delegate job linkage"
```

---

### Task 3: Web SubagentRun merge model and ID presentation

**Files:**
- Modify: `cmd/serf-hub/assets/renderer-format.js:47-59`
- Modify: `cmd/serf-hub/assets/renderer.js:128-140, 2421-2482, 2715-2813`
- Modify: `cmd/serf-hub/assets/style.css`
- Modify: `cmd/serf-hub/jstest/test-subagents.js`

**Interfaces:**
- Consumes: AppWire job payloads with `jobId`, `delegateId`, `task`, `originToolCallId`, `originItemId`, and `transcriptRef`.
- Produces: browser-side row state with datasets:

```text
data-job-id
data-delegate-id
data-origin-tool-call-id
data-origin-item-id
data-transcript-ref
data-status
data-status-kind
data-full-job-id
data-full-delegate-id
```

- Produces helpers:

```js
normalizedJobRefData(data) -> {
  jobId, jobType, status, reason, outputBytes, transcriptRef, label,
  delegateId, originTurnId, originToolCallId, originItemId
}
shortMachineID(id) -> string
```

- [ ] **Step 1: Add failing web ordering and short-ID scenarios**

Append these scenarios near the top of `cmd/serf-hub/jstest/test-subagents.js` after `spawnDelegate`:

```js
function jobStarted(jobId, delegateId, task, transcriptRef, callId) {
  return ["JOB_STARTED", {
    jobId, jobType: "delegate", status: "running", delegateId, task,
    transcriptRef, originToolCallId: callId, originItemId: "item_" + callId,
  }];
}

function jobFinished(jobId, delegateId, task, transcriptRef, callId, status) {
  return ["JOB_FINISHED", {
    jobId, jobType: "delegate", status: status || "completed", delegateId, task,
    transcriptRef, originToolCallId: callId, originItemId: "item_" + callId, outputBytes: 42,
  }];
}
```

Then add scenarios:

```js
await scenario("JOB_STARTED before delegate TOOL_CALL_END merges into one row", [
  ["SESSION_START", { session_id: "01TEST" }],
  jobStarted("job_01KW0LONGSTARTIDABCDEFG", "dlg_01KW0DELEGATEABCDEFG", "inspect billing", "local:child-A", "d1"),
  ...spawnDelegate("d1", "job_01KW0LONGSTARTIDABCDEFG", "inspect billing", "local:child-A"),
], ({ conv }) => {
  const rows = conv.querySelectorAll(".subs .sub-r");
  if (rows.length !== 1) return { ok: false, detail: "expected one merged row, got " + rows.length };
  const row = rows[0];
  if (row.dataset.delegateId !== "dlg_01KW0DELEGATEABCDEFG") return { ok: false, detail: "missing delegateId dataset" };
  if (row.dataset.originToolCallId !== "d1") return { ok: false, detail: "missing origin call dataset" };
  if (!row.textContent.includes("inspect billing")) return { ok: false, detail: "task should be primary label: " + row.textContent };
  if (row.querySelector(".nm").textContent.includes("job_01KW0LONGSTARTIDABCDEFG")) return { ok: false, detail: "raw long job id used as primary label" };
  return { ok: true };
});

await scenario("JOB_FINISHED updates originating delegate row", [
  ["SESSION_START", { session_id: "01TEST" }],
  ...spawnDelegate("d1", "job_A", "inspect billing", "local:child-A"),
  jobFinished("job_A", "dlg_A", "inspect billing", "local:child-A", "d1", "completed"),
], ({ conv }) => {
  const rows = conv.querySelectorAll(".subs .sub-r");
  if (rows.length !== 1) return { ok: false, detail: "expected one row after finish, got " + rows.length };
  const glyph = rows[0].querySelector(".g");
  if (!glyph || !glyph.classList.contains("done")) return { ok: false, detail: "finished row should be done: " + (glyph && glyph.className) };
  if (rows[0].dataset.status !== "completed") return { ok: false, detail: "status dataset not terminal" };
  return { ok: true };
});

await scenario("delegate_send second job creates second row under same delegate", [
  ["SESSION_START", { session_id: "01TEST" }],
  jobStarted("job_first", "dlg_same", "inspect billing", "local:child", "d1"),
  jobFinished("job_first", "dlg_same", "inspect billing", "local:child", "d1", "completed"),
  jobStarted("job_second", "dlg_same", "inspect billing", "local:child", "send1"),
], ({ conv }) => {
  const rows = conv.querySelectorAll('.subs .sub-r[data-delegate-id="dlg_same"]');
  if (rows.length !== 2) return { ok: false, detail: "expected two runs under delegate, got " + rows.length };
  const first = conv.querySelector('.sub-r[data-job-id="job_first"] .g');
  const second = conv.querySelector('.sub-r[data-job-id="job_second"] .g');
  if (!first || !first.classList.contains("done")) return { ok: false, detail: "first run overwritten or not done" };
  if (!second || !second.classList.contains("run")) return { ok: false, detail: "second run should be running" };
  return { ok: true };
});
```

- [ ] **Step 2: Run JS test and verify it fails**

Run:

```bash
node cmd/serf-hub/jstest/test-subagents.js
```

Expected: FAIL because the web renderer only indexes by `jobId`, does not persist delegate/origin datasets, and may use raw IDs as labels.

- [ ] **Step 3: Normalize linkage and short IDs**

In `cmd/serf-hub/assets/renderer-format.js`, replace `normalizedJobRefData` with:

```js
function normalizedJobRefData(data) {
  data = data || {};
  const outputBytes = data.outputBytes != null ? data.outputBytes : data.output_bytes;
  return {
    jobId: data.jobId || data.job_id || "",
    jobType: data.jobType || data.job_type || data.type || "",
    status: data.status || "",
    reason: data.reason || "",
    outputBytes,
    transcriptRef: data.transcriptRef || data.transcript_ref || "",
    label: data.label || data.task || data.description || "",
    delegateId: data.delegateId || data.delegate_id || "",
    originTurnId: data.originTurnId || data.origin_turn_id || "",
    originToolCallId: data.originToolCallId || data.origin_tool_call_id || data.call_id || "",
    originItemId: data.originItemId || data.origin_item_id || data.item_id || "",
  };
}

function shortMachineID(id) {
  const s = String(id || "").trim();
  if (s.length <= 18) return s;
  const prefix = s.startsWith("job_") ? "job " : (s.startsWith("dlg_") ? "delegate " : "");
  const body = prefix ? s.slice(4) : s;
  if (body.length <= 14) return prefix + body;
  return prefix + body.slice(0, 6) + "…" + body.slice(-6);
}
```

Export `shortMachineID` next to `normalizedJobRefData` in the module export block at the bottom of `renderer-format.js`.

- [ ] **Step 4: Add indexes to renderer init state**

In `cmd/serf-hub/assets/renderer.js`, in the object initialization where `activeJobs` is created, add:

```js
this.subagentRowsByDelegate = new Map();
this.subagentRowsByOriginCall = new Map();
this.subagentRowsByOriginItem = new Map();
```

Also clear these maps anywhere `this.activeJobs.clear()` is called during session reset:

```js
this.activeJobs.clear();
this.subagentRowsByDelegate.clear();
this.subagentRowsByOriginCall.clear();
this.subagentRowsByOriginItem.clear();
```

- [ ] **Step 5: Replace `findSubagentRow` with multi-key resolution**

Replace `findSubagentRow(jobId)` with:

```js
findSubagentRunRow(data) {
  const norm = normalizedJobRefData(data);
  if (!this.conversation) return null;
  if (norm.originItemId) {
    const row = this.subagentRowsByOriginItem.get(norm.originItemId) || this.conversation.querySelector('.sub-r[data-origin-item-id="' + CSS.escape(norm.originItemId) + '"]');
    if (row) return row;
  }
  if (norm.originToolCallId) {
    const row = this.subagentRowsByOriginCall.get(norm.originToolCallId) || this.conversation.querySelector('.sub-r[data-origin-tool-call-id="' + CSS.escape(norm.originToolCallId) + '"]');
    if (row) return row;
  }
  if (norm.jobId) {
    const row = this.activeJobs.get(norm.jobId) || this.conversation.querySelector('.sub-r[data-job-id="' + CSS.escape(norm.jobId) + '"]');
    if (row) return row;
  }
  if (norm.delegateId) {
    const rows = this.subagentRowsByDelegate.get(norm.delegateId) || [];
    for (let i = rows.length - 1; i >= 0; i--) {
      if (rows[i] && rows[i].isConnected && rows[i].dataset.statusKind === "running") return rows[i];
    }
  }
  return null;
},

findSubagentRow(jobId) {
  return this.findSubagentRunRow({ jobId });
},
```

- [ ] **Step 6: Add row indexing helper**

Add this method near `updateSubagentRow`:

```js
indexSubagentRow(row) {
  if (!row) return;
  if (row.dataset.jobId) this.activeJobs.set(row.dataset.jobId, row);
  if (row.dataset.originToolCallId) this.subagentRowsByOriginCall.set(row.dataset.originToolCallId, row);
  if (row.dataset.originItemId) this.subagentRowsByOriginItem.set(row.dataset.originItemId, row);
  if (row.dataset.delegateId) {
    const rows = this.subagentRowsByDelegate.get(row.dataset.delegateId) || [];
    if (!rows.includes(row)) rows.push(row);
    this.subagentRowsByDelegate.set(row.dataset.delegateId, rows);
  }
},
```

- [ ] **Step 7: Update `upsertJobRef` and row datasets**

In `upsertJobRef`, change row lookup to:

```js
let row = this.findSubagentRunRow(merged);
```

In `updateSubagentRow`, after the existing `data.jobId` handling, add:

```js
if (data.delegateId) {
  row.dataset.delegateId = data.delegateId;
  row.dataset.fullDelegateId = data.delegateId;
}
if (data.originTurnId) row.dataset.originTurnId = data.originTurnId;
if (data.originToolCallId) row.dataset.originToolCallId = data.originToolCallId;
if (data.originItemId) row.dataset.originItemId = data.originItemId;
if (data.jobId) row.dataset.fullJobId = data.jobId;
if (data.transcriptRef) row.dataset.transcriptRef = data.transcriptRef;
```

Change the label selection to avoid raw IDs:

```js
const label = data.label || row.dataset.label || row.dataset.jobType || data.jobType || "delegate";
if (data.label) row.dataset.label = data.label;
if (label && (!name.textContent || data.label)) name.textContent = clip(label, 80);
```

At the end of `updateSubagentRow`, call:

```js
this.renderSubagentMachineMeta(row, data);
this.indexSubagentRow(row);
```

- [ ] **Step 8: Add secondary machine metadata rendering**

Add this method near `renderSubagentResult`:

```js
renderSubagentMachineMeta(row, data) {
  let meta = row.querySelector(".sub-meta");
  if (!meta) {
    meta = document.createElement("span");
    meta.className = "sub-meta";
    const res = row.querySelector(".res");
    row.insertBefore(meta, res || null);
  }
  const bits = [];
  const jobId = data.jobId || row.dataset.jobId || "";
  const delegateId = data.delegateId || row.dataset.delegateId || "";
  const transcriptRef = data.transcriptRef || row.dataset.transcriptRef || "";
  if (jobId) bits.push(shortMachineID(jobId));
  if (delegateId) bits.push(shortMachineID(delegateId));
  if (transcriptRef) bits.push("transcript");
  meta.textContent = bits.join(" · ");
  meta.title = [jobId && "job " + jobId, delegateId && "delegate " + delegateId, transcriptRef && "transcript " + transcriptRef].filter(Boolean).join("\n");
}
```

Ensure `shortMachineID` is imported from `renderer-format.js` at the top of `renderer.js` the same way `normalizedJobRefData` is imported.

- [ ] **Step 9: Preserve linkage in reconcile/finalize**

In `reconcileSubagent(info)`, use `const norm = normalizedJobRefData(info);` and call `findSubagentRunRow(norm)`. Pass delegate/origin fields into `updateSubagentRow`:

```js
this.updateSubagentRow(row, Object.assign({}, norm, {
  status: norm.status || "",
  resultText: info.resultText || "",
  lastAction: info.lastAction || "",
}));
```

In `finalizeJobRef`, replace `this.findSubagentRow(jobId)` with `this.findSubagentRunRow(data)`.

- [ ] **Step 10: Add CSS for metadata**

Add near the `.sub-r` styles in `cmd/serf-hub/assets/style.css`:

```css
.sub-r .sub-meta {
  color: var(--muted);
  font-size: 0.78rem;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
```

- [ ] **Step 11: Run web subagent tests**

Run:

```bash
node cmd/serf-hub/jstest/test-subagents.js
```

Expected: PASS.

- [ ] **Step 12: Commit web merge model**

```bash
git add cmd/serf-hub/assets/renderer-format.js cmd/serf-hub/assets/renderer.js cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-subagents.js
git commit -m "feat(hub): merge delegate job notifications into subagent runs"
```

---

### Task 4: TUI structured SubagentRun metadata and notifications

**Files:**
- Modify: `cmd/serf-tui/internal/transcript/types.go:21-33`
- Modify: `cmd/serf-tui/internal/transcript/reducer.go`
- Modify: `cmd/serf-tui/hub_notifications.go:37-173`
- Modify: `cmd/serf-tui/internal/msgrender/tool_bodies.go:202-238`
- Test: `cmd/serf-tui/internal/transcript/reducer_test.go`
- Test: `cmd/serf-tui/hub_appwire_test.go`

**Interfaces:**
- Consumes: `appwire.SerfJobInfo` from `serf/job/started` and `serf/job/finished`.
- Produces:

```go
type SubagentRunInfo struct {
    DelegateID       string
    JobID            string
    JobType          string
    Status           string
    Reason           string
    Task             string
    TranscriptRef    string
    OriginTurnID     string
    OriginToolCallID string
    OriginItemID     string
    OutputBytes      int64
}

type ToolCallInfo struct {
    // existing fields...
    Subagent *SubagentRunInfo
}

func (r *TranscriptReducer) ApplySerfJob(job appwire.SerfJobInfo)
```

- [ ] **Step 1: Write failing reducer test**

Append to `cmd/serf-tui/internal/transcript/reducer_test.go`:

```go
func TestTranscriptReducerAppliesSerfJobNotificationsToDelegateTool(t *testing.T) {
    reducer := NewTranscriptReducer(nil, nil, nil)
    reducer.ApplyThreadItem(appwire.ThreadItem{
        Type:          "commandExecution",
        ID:            "item_delegate",
        CallID:        "call_delegate",
        TurnID:        "turn_1",
        ToolName:      "delegate",
        ArgumentsJSON: `{"task":"inspect billing"}`,
        Output:        `{"job_id":"job_A","delegate_id":"dlg_A","status":"running","task":"inspect billing","transcript_ref":"local:child"}`,
        Status:        appwire.TurnStatusCompleted,
    }, 1, true)

    reducer.ApplySerfJob(appwire.SerfJobInfo{
        JobID:            "job_A",
        JobType:          "delegate",
        Status:           "completed",
        OutputBytes:      42,
        TranscriptRef:    "local:child",
        DelegateID:       "dlg_A",
        Task:             "inspect billing",
        OriginToolCallID: "call_delegate",
        OriginItemID:     "item_delegate",
    })

    tools := transcriptTools(reducer.messages)
    if len(tools) != 1 || tools[0].Subagent == nil {
        t.Fatalf("tools=%+v, want one delegate tool with Subagent metadata", tools)
    }
    got := tools[0].Subagent
    if got.JobID != "job_A" || got.DelegateID != "dlg_A" || got.Status != "completed" || got.Task != "inspect billing" || got.TranscriptRef != "local:child" || got.OriginToolCallID != "call_delegate" || got.OutputBytes != 42 {
        t.Fatalf("Subagent = %+v", got)
    }
    if !tools[0].Done {
        t.Fatalf("delegate tool should be marked done after terminal job")
    }
}
```

- [ ] **Step 2: Run reducer test and verify it fails**

Run:

```bash
go test ./cmd/serf-tui/internal/transcript -run TestTranscriptReducerAppliesSerfJobNotificationsToDelegateTool -count=1 -v
```

Expected: FAIL because `SubagentRunInfo` and `ApplySerfJob` do not exist.

- [ ] **Step 3: Add TUI SubagentRun types**

In `cmd/serf-tui/internal/transcript/types.go`, add below `ToolCallInfo` or above it:

```go
type SubagentRunInfo struct {
    DelegateID       string
    JobID            string
    JobType          string
    Status           string
    Reason           string
    Task             string
    TranscriptRef    string
    OriginTurnID     string
    OriginToolCallID string
    OriginItemID     string
    OutputBytes      int64
}
```

Add this field to `ToolCallInfo`:

```go
Subagent *SubagentRunInfo
```

- [ ] **Step 4: Add conversion and merge helpers**

In `cmd/serf-tui/internal/transcript/reducer.go`, add:

```go
func subagentRunFromJob(job appwire.SerfJobInfo) SubagentRunInfo {
    return SubagentRunInfo{
        DelegateID:       strings.TrimSpace(job.DelegateID),
        JobID:            strings.TrimSpace(job.JobID),
        JobType:          strings.TrimSpace(job.JobType),
        Status:           strings.TrimSpace(job.Status),
        Reason:           strings.TrimSpace(job.Reason),
        Task:             strings.TrimSpace(job.Task),
        TranscriptRef:    strings.TrimSpace(job.TranscriptRef),
        OriginTurnID:     strings.TrimSpace(job.OriginTurnID),
        OriginToolCallID: strings.TrimSpace(job.OriginToolCallID),
        OriginItemID:     strings.TrimSpace(job.OriginItemID),
        OutputBytes:      job.OutputBytes,
    }
}

func mergeSubagentRun(dst *SubagentRunInfo, src SubagentRunInfo) SubagentRunInfo {
    if dst == nil {
        return src
    }
    out := *dst
    if src.DelegateID != "" { out.DelegateID = src.DelegateID }
    if src.JobID != "" { out.JobID = src.JobID }
    if src.JobType != "" { out.JobType = src.JobType }
    if src.Status != "" { out.Status = src.Status }
    if src.Reason != "" { out.Reason = src.Reason }
    if src.Task != "" { out.Task = src.Task }
    if src.TranscriptRef != "" { out.TranscriptRef = src.TranscriptRef }
    if src.OriginTurnID != "" { out.OriginTurnID = src.OriginTurnID }
    if src.OriginToolCallID != "" { out.OriginToolCallID = src.OriginToolCallID }
    if src.OriginItemID != "" { out.OriginItemID = src.OriginItemID }
    if src.OutputBytes != 0 { out.OutputBytes = src.OutputBytes }
    return out
}

func subagentTerminalStatus(status string) bool {
    switch strings.ToLower(strings.TrimSpace(status)) {
    case "completed", "done", "failed", "cancelled", "stopped", "succeeded":
        return true
    default:
        return false
    }
}
```

`reducer.go` already imports `strings`; add it if missing.

- [ ] **Step 5: Parse delegate output into `ToolCallInfo.Subagent`**

In `toolInfoFromThreadItem` or `mergeThreadItemIntoToolInfo`, after setting name/output/raw fields, add:

```go
if info.Name == "delegate" || info.Name == "delegate_send" {
    if run := subagentRunFromToolItem(item); run.JobID != "" || run.DelegateID != "" {
        merged := mergeSubagentRun(info.Subagent, run)
        info.Subagent = &merged
    }
}
```

Add helper:

```go
func subagentRunFromToolItem(item appwire.ThreadItem) SubagentRunInfo {
    raw := item.Raw
    if len(raw) == 0 && strings.TrimSpace(item.Output) != "" {
        raw = json.RawMessage(item.Output)
    }
    var payload struct {
        DelegateID       string `json:"delegate_id"`
        JobID            string `json:"job_id"`
        StartedJobID     string `json:"started_job_id"`
        CurrentJobID     string `json:"current_job_id"`
        LatestJobID      string `json:"latest_job_id"`
        Type             string `json:"type"`
        Status           string `json:"status"`
        Reason           string `json:"reason"`
        Task             string `json:"task"`
        TranscriptRef    string `json:"transcript_ref"`
        OriginTurnID     string `json:"origin_turn_id"`
        OriginToolCallID string `json:"origin_tool_call_id"`
        OriginItemID     string `json:"origin_item_id"`
        OutputBytes      int64  `json:"output_bytes"`
        TotalBytes       int64  `json:"total_bytes"`
    }
    if len(raw) == 0 || json.Unmarshal(raw, &payload) != nil {
        return SubagentRunInfo{}
    }
    jobID := firstNonEmptyString(payload.JobID, payload.StartedJobID, payload.CurrentJobID, payload.LatestJobID)
    outputBytes := payload.OutputBytes
    if outputBytes == 0 {
        outputBytes = payload.TotalBytes
    }
    return SubagentRunInfo{
        DelegateID:       strings.TrimSpace(payload.DelegateID),
        JobID:            strings.TrimSpace(jobID),
        JobType:          strings.TrimSpace(payload.Type),
        Status:           strings.TrimSpace(payload.Status),
        Reason:           strings.TrimSpace(payload.Reason),
        Task:             strings.TrimSpace(payload.Task),
        TranscriptRef:    strings.TrimSpace(payload.TranscriptRef),
        OriginTurnID:     strings.TrimSpace(payload.OriginTurnID),
        OriginToolCallID: strings.TrimSpace(payload.OriginToolCallID),
        OriginItemID:     strings.TrimSpace(payload.OriginItemID),
        OutputBytes:      outputBytes,
    }
}
```

If `firstNonEmptyString` is not available in this package, add:

```go
func firstNonEmptyString(values ...string) string {
    for _, value := range values {
        if strings.TrimSpace(value) != "" {
            return value
        }
    }
    return ""
}
```

- [ ] **Step 6: Implement `ApplySerfJob`**

Add to `cmd/serf-tui/internal/transcript/reducer.go`:

```go
func (r *TranscriptReducer) ApplySerfJob(job appwire.SerfJobInfo) {
    run := subagentRunFromJob(job)
    if run.JobID == "" && run.DelegateID == "" && run.OriginToolCallID == "" && run.OriginItemID == "" {
        return
    }
    if idx, ok := r.subagentMessageIndex(run); ok {
        info := r.messages[idx].Tool
        if info == nil {
            return
        }
        merged := mergeSubagentRun(info.Subagent, run)
        info.Subagent = &merged
        if merged.Task != "" && info.Description == "" {
            info.Description = merged.Task
        }
        if subagentTerminalStatus(merged.Status) {
            info.Done = true
        }
        return
    }
    info := &ToolCallInfo{Name: "delegate", Description: run.Task, Done: subagentTerminalStatus(run.Status)}
    info.Subagent = &run
    r.messages = append(r.messages, ChatMessage{Kind: MsgTool, ItemID: run.OriginItemID, ToolCallID: run.OriginToolCallID, Tool: info})
}

func (r *TranscriptReducer) subagentMessageIndex(run SubagentRunInfo) (int, bool) {
    for i := range r.messages {
        msg := r.messages[i]
        if msg.Kind != MsgTool || msg.Tool == nil {
            continue
        }
        if run.OriginItemID != "" && msg.ItemID == run.OriginItemID {
            return i, true
        }
        if run.OriginToolCallID != "" && msg.ToolCallID == run.OriginToolCallID {
            return i, true
        }
        if msg.Tool.Subagent != nil {
            existing := msg.Tool.Subagent
            if run.JobID != "" && existing.JobID == run.JobID {
                return i, true
            }
            if run.DelegateID != "" && existing.DelegateID == run.DelegateID && existing.JobID == run.JobID {
                return i, true
            }
        }
    }
    return 0, false
}
```

- [ ] **Step 7: Wire TUI notifications**

In `cmd/serf-tui/hub_notifications.go`, add cases inside `applyHubNotification`:

```go
case appwire.NotifySerfJobStarted, appwire.NotifySerfJobFinished:
    var params struct {
        Job appwire.SerfJobInfo `json:"job"`
    }
    if json.Unmarshal(notification.Params, &params) == nil {
        reducer := m.sessionTranscriptReducer()
        reducer.ApplySerfJob(params.Job)
        m.applySessionTranscriptReducer(reducer)
    }
```

Place before warning handling and after normal item/reducer cases.

- [ ] **Step 8: Render structured delegate body**

In `cmd/serf-tui/internal/msgrender/tool_bodies.go`, update `delegateBody` to prefer structured metadata by adding a new exported renderer helper if the existing render path can pass `ToolCallInfo`. If the render path only passes args/output, keep this fallback but improve label selection:

```go
summaryLabel := "delegate"
if task := strings.TrimSpace(args.Str("task")); task != "" {
    summaryLabel = task
}
identity := shortID(jobID)
if identity != "" {
    summaryLabel += " · job " + identity
}
summary := fmt.Sprintf("%s (%s)", summaryLabel, status)
```

Then add a structured helper used by the message renderer when `info.Subagent != nil`:

```go
func SubagentRunBody(run transcript.SubagentRunInfo, width int) string {
    status := strings.TrimSpace(run.Status)
    if status == "" {
        status = "running"
    }
    label := strings.TrimSpace(run.Task)
    if label == "" {
        label = "delegate"
    }
    parts := []string{label, "(" + status + ")"}
    if run.JobID != "" {
        parts = append(parts, "job "+shortID(run.JobID))
    }
    if run.DelegateID != "" && width >= 60 {
        parts = append(parts, "delegate "+shortID(run.DelegateID))
    }
    if run.TranscriptRef != "" && width >= 70 {
        parts = append(parts, "transcript "+run.TranscriptRef)
    }
    return lipgloss.NewStyle().Foreground(tuitheme.ActiveTheme().StateSubagent).Render(strings.Join(parts, " · "))
}
```

Update the message rendering switch to call `SubagentRunBody(*info.Subagent, width)` for delegate tools with structured metadata.

- [ ] **Step 9: Add TUI notification integration test**

Append to `cmd/serf-tui/hub_appwire_test.go`:

```go
func TestHubModelAppliesSerfJobNotificationsToDelegateTool(t *testing.T) {
    m := newHubModel(nil, "")
    m.mode = hubModeSession
    m.detail = hubSessionDetail{Ref: "local:th_1", SessionID: "sess_1"}

    updated, _ := m.Update(hubNotificationMsg{ok: true, notification: *appwire.NotificationMessage(appwire.NotifyItemCompleted, map[string]any{
        "threadId": "th_1",
        "turnId":   "turn_1",
        "item": appwire.ThreadItem{
            Type:          "commandExecution",
            ID:            "item_delegate",
            TurnID:        "turn_1",
            CallID:        "call_delegate",
            ToolName:      "delegate",
            ArgumentsJSON: `{"task":"inspect billing"}`,
            Output:        `{"job_id":"job_A","delegate_id":"dlg_A","status":"running","task":"inspect billing","transcript_ref":"local:child"}`,
            Status:        appwire.TurnStatusCompleted,
        },
    }).Notification})

    updated, _ = updated.(hubModel).Update(hubNotificationMsg{ok: true, notification: *appwire.NotificationMessage(appwire.NotifySerfJobFinished, map[string]any{
        "threadId": "th_1",
        "ref":      "local:th_1",
        "job": appwire.SerfJobInfo{
            JobID: "job_A", JobType: "delegate", Status: "completed", DelegateID: "dlg_A", Task: "inspect billing",
            TranscriptRef: "local:child", OriginToolCallID: "call_delegate", OriginItemID: "item_delegate", OutputBytes: 42,
        },
    }).Notification})

    got := updated.(hubModel)
    if len(got.session.messages) != 1 || got.session.messages[0].Tool == nil || got.session.messages[0].Tool.Subagent == nil {
        t.Fatalf("messages=%+v, want delegate message with Subagent metadata", got.session.messages)
    }
    run := got.session.messages[0].Tool.Subagent
    if run.Status != "completed" || run.JobID != "job_A" || run.DelegateID != "dlg_A" || !got.session.messages[0].Tool.Done {
        t.Fatalf("run=%+v tool=%+v", run, got.session.messages[0].Tool)
    }
}
```

- [ ] **Step 10: Run TUI tests**

Run:

```bash
go test ./cmd/serf-tui/internal/transcript -run TestTranscriptReducerAppliesSerfJobNotificationsToDelegateTool -count=1 -v
go test ./cmd/serf-tui -run TestHubModelAppliesSerfJobNotificationsToDelegateTool -count=1 -v
```

Expected: PASS.

- [ ] **Step 11: Commit TUI subagent metadata**

```bash
git add cmd/serf-tui/internal/transcript/types.go cmd/serf-tui/internal/transcript/reducer.go cmd/serf-tui/hub_notifications.go cmd/serf-tui/internal/msgrender/tool_bodies.go cmd/serf-tui/internal/transcript/reducer_test.go cmd/serf-tui/hub_appwire_test.go
git commit -m "feat(tui): update delegate rows from job notifications"
```

---

### Task 5: Cold transcript reconciliation

**Files:**
- Modify: `internal/apptranscript/apptranscript.go`
- Modify: `internal/apptranscript/apptranscript_test.go`
- Modify: `cmd/serf-hub/app_threadread.go`
- Add or modify tests in the package that serves `ThreadRead` responses.

**Interfaces:**
- Consumes: `ThreadItem.Raw` delegate tool state containing `job_id` / `delegate_id` and a jobstore folded record for the same job.
- Produces: cold `ThreadItem.Raw` with terminal SubagentRun fields overlaid before UI render:

```json
{
  "job_id": "job_A",
  "delegate_id": "dlg_A",
  "status": "completed",
  "task": "inspect billing",
  "transcript_ref": "local:child",
  "origin_tool_call_id": "call_delegate"
}
```

- [ ] **Step 1: Add failing apptranscript raw-preservation test**

Append to `internal/apptranscript/apptranscript_test.go`:

```go
func TestProjectTurnPreservesDelegateToolStateForColdReconciliation(t *testing.T) {
    turn := schema.Turn{Messages: []schema.Message{{Role: "assistant", Parts: []schema.Part{{
        Type: "tool_result",
        ToolResult: &schema.ToolResult{
            CallID: "call_delegate",
            Name:   "delegate",
            Output: `{"job_id":"job_A","delegate_id":"dlg_A","status":"running","task":"inspect billing","transcript_ref":"local:child"}`,
            ToolState: json.RawMessage(`{"job_id":"job_A","delegate_id":"dlg_A","status":"running","task":"inspect billing","transcript_ref":"local:child"}`),
        },
    }}}}

    items := ProjectTurn("turn_1", 1, turn, map[string]string{"call_delegate": "delegate"}, nil)
    if len(items) != 1 || items[0].Type != "commandExecution" || items[0].CallID != "call_delegate" {
        t.Fatalf("items=%+v", items)
    }
    if !strings.Contains(string(items[0].Raw), `"delegate_id":"dlg_A"`) || !strings.Contains(string(items[0].Raw), `"transcript_ref":"local:child"`) {
        t.Fatalf("delegate raw = %s", items[0].Raw)
    }
}
```

- [ ] **Step 2: Run apptranscript focused test**

Run:

```bash
go test ./internal/apptranscript -run TestProjectTurnPreservesDelegateToolStateForColdReconciliation -count=1 -v
```

Expected: PASS if raw preservation already works; FAIL only if delegate tool state is being dropped. If it passes, keep the test as a regression guard and continue.

- [ ] **Step 3: Add reconciliation helper test**

In the package that owns `cmd/serf-hub/app_threadread.go`, add this test near existing thread-read tests:

```go
func TestThreadReadReconcilesDelegateRawWithTerminalJobstoreState(t *testing.T) {
    raw := json.RawMessage(`{"job_id":"job_A","delegate_id":"dlg_A","status":"running","task":"inspect billing","transcript_ref":"local:child"}`)
    item := appwire.ThreadItem{Type: "commandExecution", ID: "item_delegate", CallID: "call_delegate", ToolName: "delegate", Raw: raw, Status: appwire.TurnStatusCompleted}
    rec := jobstore.Record{JobID: "job_A", DelegateID: "dlg_A", Type: jobstore.JobTypeDelegate, Status: jobstore.StatusCompleted, Task: "inspect billing", TranscriptRef: "local:child", OriginToolCallID: "call_delegate", OutputBytes: 42}

    got := reconcileDelegateThreadItemForTest(item, rec)
    if got.Status != "completed" {
        t.Fatalf("item status=%q, want completed", got.Status)
    }
    if !strings.Contains(string(got.Raw), `"status":"completed"`) || !strings.Contains(string(got.Raw), `"output_bytes":42`) {
        t.Fatalf("raw after reconcile = %s", got.Raw)
    }
}
```

- [ ] **Step 4: Implement reconciliation helper**

Add this helper in `cmd/serf-hub/app_threadread.go` or a focused new file in the same package:

```go
func reconcileDelegateThreadItemForTest(item appwire.ThreadItem, rec jobstore.Record) appwire.ThreadItem {
    return reconcileDelegateThreadItem(item, rec)
}

func reconcileDelegateThreadItem(item appwire.ThreadItem, rec jobstore.Record) appwire.ThreadItem {
    if item.Type != "commandExecution" || item.ToolName != "delegate" || rec.JobID == "" {
        return item
    }
    var raw map[string]any
    if len(item.Raw) != 0 {
        _ = json.Unmarshal(item.Raw, &raw)
    }
    if raw == nil {
        raw = map[string]any{}
    }
    rawJobID, _ := raw["job_id"].(string)
    if rawJobID != "" && rawJobID != rec.JobID {
        return item
    }
    raw["job_id"] = rec.JobID
    if rec.DelegateID != "" { raw["delegate_id"] = rec.DelegateID }
    if rec.Task != "" { raw["task"] = rec.Task }
    if rec.TranscriptRef != "" { raw["transcript_ref"] = rec.TranscriptRef }
    if rec.OriginTurnID != "" { raw["origin_turn_id"] = rec.OriginTurnID }
    if rec.OriginToolCallID != "" { raw["origin_tool_call_id"] = rec.OriginToolCallID }
    if rec.Status != "" {
        status := string(rec.Status)
        raw["status"] = status
        item.Status = status
    }
    if rec.Reason != "" { raw["reason"] = rec.Reason }
    raw["output_bytes"] = rec.OutputBytes
    if b, err := json.Marshal(raw); err == nil {
        item.Raw = b
    }
    return item
}
```

- [ ] **Step 5: Wire reconciliation into thread read projection**

In `cmd/serf-hub/app_threadread.go`, after turns/items are projected and before returning the `appwire.Thread`, build a map of delegate job records visible to the parent session and apply:

```go
for ti := range thread.Turns {
    for ii := range thread.Turns[ti].Items {
        item := thread.Turns[ti].Items[ii]
        if item.Type != "commandExecution" || item.ToolName != "delegate" {
            continue
        }
        jobID := delegateJobIDFromRaw(item.Raw)
        if jobID == "" {
            continue
        }
        if rec, ok := jobsByID[jobID]; ok {
            thread.Turns[ti].Items[ii] = reconcileDelegateThreadItem(item, rec)
        }
    }
}
```

Add helper:

```go
func delegateJobIDFromRaw(raw json.RawMessage) string {
    var payload struct {
        JobID        string `json:"job_id"`
        StartedJobID string `json:"started_job_id"`
        CurrentJobID string `json:"current_job_id"`
        LatestJobID  string `json:"latest_job_id"`
    }
    if len(raw) == 0 || json.Unmarshal(raw, &payload) != nil {
        return ""
    }
    for _, value := range []string{payload.JobID, payload.StartedJobID, payload.CurrentJobID, payload.LatestJobID} {
        if strings.TrimSpace(value) != "" {
            return strings.TrimSpace(value)
        }
    }
    return ""
}
```

Use the existing jobstore access path in this file/server package; do not introduce a second store root or scan raw filesystem paths directly.

- [ ] **Step 6: Run cold reconciliation tests**

Run:

```bash
go test ./internal/apptranscript -run TestProjectTurnPreservesDelegateToolStateForColdReconciliation -count=1 -v
go test ./cmd/serf-hub -run 'TestThreadReadReconcilesDelegateRawWithTerminalJobstoreState|Test.*ThreadRead.*' -count=1 -v
```

Expected: PASS.

- [ ] **Step 7: Commit cold reconciliation**

```bash
git add internal/apptranscript/apptranscript.go internal/apptranscript/apptranscript_test.go cmd/serf-hub/app_threadread.go cmd/serf-hub/*test.go
git commit -m "feat(hub): reconcile delegate runs on thread read"
```

---

### Task 6: Bounded child-step preview

**Files:**
- Modify: existing AppWire/server transcript read path, or add a small Serf-only preview RPC in `appwire/types.go` and appserver registration files.
- Modify: `cmd/serf-hub/assets/renderer.js`
- Modify: `cmd/serf-hub/assets/style.css`
- Modify: `cmd/serf-hub/jstest/test-subagents.js`

**Interfaces:**
- Consumes: `transcriptRef` from SubagentRun rows.
- Produces: lazy preview response with at most 5 projected child items:

```json
{
  "ref": "local:child",
  "items": [
    {"type":"agentMessage","text":"found callers"},
    {"type":"commandExecution","toolName":"grep_files","description":"search callers","status":"completed"}
  ],
  "truncated": true
}
```

- [ ] **Step 1: Write failing web preview test**

Append to `cmd/serf-hub/jstest/test-subagents.js`:

```js
await scenario("expanded subagent row lazy-loads bounded preview", [
  ["SESSION_START", { session_id: "01TEST" }],
  jobFinished("job_preview", "dlg_preview", "inspect billing", "local:child-preview", "dpreview", "completed"),
], async ({ conv, window }) => {
  let requested = "";
  window.fetch = (url) => {
    requested = String(url);
    return Promise.resolve({ ok: true, json: () => Promise.resolve({ ref: "local:child-preview", truncated: true, items: [
      { type: "agentMessage", text: "found three callers" },
      { type: "commandExecution", toolName: "grep_files", description: "search billing", status: "completed" },
      { type: "agentMessage", text: "recommended fix" },
      { type: "agentMessage", text: "extra item should not render when limit is 3" },
    ] }) });
  };
  const row = conv.querySelector('.sub-r[data-job-id="job_preview"]');
  if (!row) return { ok: false, detail: "missing preview row" };
  row.click();
  await new Promise(r => setTimeout(r, 20));
  const preview = row.parentElement.querySelector('.sub-preview');
  if (!requested.includes("local%3Achild-preview")) return { ok: false, detail: "preview endpoint not requested: " + requested };
  if (!preview) return { ok: false, detail: "missing preview container" };
  if (!preview.textContent.includes("found three callers") || !preview.textContent.includes("search billing")) return { ok: false, detail: "missing preview snippets: " + preview.textContent };
  if (preview.textContent.includes("extra item should not render")) return { ok: false, detail: "preview rendered more than bounded limit" };
  return { ok: true };
});
```

If the existing scenario helper does not support async `check`, first update it from:

```js
const result = check({ conv, window });
```

to:

```js
const result = await check({ conv, window });
```

- [ ] **Step 2: Run web preview test and verify it fails**

Run:

```bash
node cmd/serf-hub/jstest/test-subagents.js
```

Expected: FAIL because rows do not lazy-load preview content.

- [ ] **Step 3: Add or reuse preview endpoint**

Prefer existing `readThread` / `listTurns` if it can request the child transcript and slice latest items without loading unbounded content in the client. If no existing endpoint can do that cleanly, add this AppWire method:

```go
const MethodSerfSubagentPreview = "serf/subagentPreview"

type SerfSubagentPreviewParams struct {
    Ref   string `json:"ref"`
    Limit int    `json:"limit,omitempty"`
}

type SerfSubagentPreviewResponse struct {
    Ref       string       `json:"ref"`
    Items     []ThreadItem `json:"items"`
    Truncated bool         `json:"truncated"`
}
```

Register the method in the hub appserver beside other Serf-only methods. The handler must clamp `Limit` to `1..5`, default to `3`, read the child transcript by `Ref`, project using the same apptranscript path as normal thread read, and return only the latest direct child `ThreadItem`s.

- [ ] **Step 4: Add renderer preview loading methods**

In `cmd/serf-hub/assets/renderer.js`, add:

```js
loadSubagentPreview(row) {
  if (!row || row.dataset.previewState === "loaded" || row.dataset.previewState === "loading") return;
  const ref = row.dataset.transcriptRef;
  if (!ref) return;
  row.dataset.previewState = "loading";
  const box = this.ensureSubagentPreviewBox(row);
  box.textContent = "loading preview…";
  fetch("/_api/subagent-preview?ref=" + encodeURIComponent(ref) + "&limit=3")
    .then(r => r.ok ? r.json() : Promise.reject(new Error("preview unavailable")))
    .then(data => {
      row.dataset.previewState = "loaded";
      this.renderSubagentPreview(box, data);
    })
    .catch(() => {
      row.dataset.previewState = "failed";
      box.textContent = "preview unavailable";
    });
},

ensureSubagentPreviewBox(row) {
  let box = row.nextElementSibling;
  if (!box || !box.classList || !box.classList.contains("sub-preview")) {
    box = document.createElement("div");
    box.className = "sub-preview";
    row.insertAdjacentElement("afterend", box);
  }
  return box;
},

renderSubagentPreview(box, data) {
  box.innerHTML = "";
  const items = (data && data.items || []).slice(-3);
  for (const item of items) {
    const line = document.createElement("div");
    line.className = "sub-preview-line";
    const label = item.toolName ? item.toolName : (item.type === "agentMessage" ? "assistant" : item.type || "item");
    const text = item.description || item.text || item.output || item.status || "";
    line.textContent = label + (text ? ": " + clip(text, 100) : "");
    box.appendChild(line);
  }
  if (data && data.truncated) {
    const more = document.createElement("div");
    more.className = "sub-preview-more";
    more.textContent = "older child steps hidden";
    box.appendChild(more);
  }
},
```

In `makeSubagentRow`, add a click handler that preserves existing navigation behavior when the user clicks the `view →` link but otherwise expands preview:

```js
row.addEventListener("click", (e) => {
  if (e.target && e.target.classList && e.target.classList.contains("lk")) return;
  this.loadSubagentPreview(row);
});
```

- [ ] **Step 5: Add preview CSS**

Add to `cmd/serf-hub/assets/style.css` near subagent styles:

```css
.sub-preview {
  margin: -0.25rem 0 0.5rem 1.75rem;
  padding: 0.4rem 0.6rem;
  border-left: 1px solid var(--border-muted);
  color: var(--muted);
  font-size: 0.82rem;
}
.sub-preview-line + .sub-preview-line { margin-top: 0.2rem; }
.sub-preview-more { margin-top: 0.25rem; font-style: italic; }
```

- [ ] **Step 6: Run preview tests**

Run:

```bash
node cmd/serf-hub/jstest/test-subagents.js
go test ./cmd/serf-hub -run 'Test.*SubagentPreview|Test.*ThreadRead' -count=1 -v
```

Expected: PASS.

- [ ] **Step 7: Commit bounded preview**

```bash
git add appwire cmd/serf-hub internal/apptranscript
git commit -m "feat(hub): add bounded subagent run previews"
```

---

### Task 7: Final compatibility, status, and regression pass

**Files:**
- Modify: `cmd/serf-tui/hub_status_test.go`
- Modify: `cmd/serf-tui` status rendering files found by `rg -n "job_|delegate|status" cmd/serf-tui`
- Modify: `cmd/serf-hub/jstest/test-tool-renderers.js`
- Modify: any files needed to fix regressions from full test runs.

**Interfaces:**
- Consumes: all previous task outputs.
- Produces: verified final behavior across backend projection, web rendering, TUI rendering, cold reload, and compatibility.

- [ ] **Step 1: Add status/details short-ID regression tests**

In `cmd/serf-tui/hub_status_test.go`, add a test that constructs a status view with one delegate job with a long `job_...` and asserts the collapsed list contains `job 01KW0…` or another `shortID` value, not the full ID. Use the existing status test harness in that file and this assertion shape:

```go
if strings.Contains(collapsed, "job_01KW0VERYVERYLONGIDENTIFIER") {
    t.Fatalf("collapsed status leaked full job id: %s", collapsed)
}
if !strings.Contains(collapsed, "job ") {
    t.Fatalf("collapsed status missing short job label: %s", collapsed)
}
```

In `cmd/serf-hub/jstest/test-tool-renderers.js`, add an assertion for delegate tool rendering that `.tool-title` / primary visible label does not contain the raw long `job_...` ID while details/title/dataset still do.

- [ ] **Step 2: Run targeted UI regression tests**

Run:

```bash
node cmd/serf-hub/jstest/test-subagents.js
node cmd/serf-hub/jstest/test-tool-renderers.js
go test ./cmd/serf-tui -run 'TestHubModelAppliesSerfJobNotificationsToDelegateTool|Test.*Status.*Job|Test.*Status.*Delegate' -count=1 -v
```

Expected: PASS.

- [ ] **Step 3: Run backend compatibility tests**

Run:

```bash
go test ./agent -run 'TestDelegate.*Job.*Linkage|TestDelegateResumeJobStartedKeepsOriginalOriginLinkage' -count=1 -v
go test ./internal/appprojector -run 'TestAppEventProjectorProjectsJobEvents|TestSerfJobInfoDelegateFieldsAreOptional' -count=1 -v
go test ./internal/apptranscript -run 'TestProjectTurnPreservesDelegateToolStateForColdReconciliation' -count=1 -v
```

Expected: PASS.

- [ ] **Step 4: Run package-level checks for touched packages**

Run:

```bash
go test ./agent ./internal/appprojector ./internal/apptranscript ./cmd/serf-hub ./cmd/serf-tui ./cmd/serf-tui/internal/transcript ./cmd/serf-tui/internal/msgrender -count=1
```

Expected: PASS.

- [ ] **Step 5: Run JS test suite**

If `cmd/serf-hub/jstest/run-all.sh` exists and is executable, run:

```bash
bash cmd/serf-hub/jstest/run-all.sh
```

Expected: PASS.

If the runner does not include the modified tests, run the individual tests instead:

```bash
node cmd/serf-hub/jstest/test-subagents.js
node cmd/serf-hub/jstest/test-tool-renderers.js
```

Expected: PASS.

- [ ] **Step 6: Inspect git status**

Run:

```bash
git status --short
```

Expected: only intentional modified files from the final task and the pre-existing untracked `kimi-jobs-ux-cleanup.md` if it is still present. Do not stage `kimi-jobs-ux-cleanup.md`.

- [ ] **Step 7: Commit final polish**

```bash
git add cmd/serf-tui cmd/serf-hub agent/events/payloads.go agent/jobs.go appwire/types.go internal/appprojector internal/apptranscript
git reset -- kimi-jobs-ux-cleanup.md 2>/dev/null || true
git commit -m "test: cover subagent run rendering regressions"
```

- [ ] **Step 8: Final full verification before handoff**

Run:

```bash
go test ./...
bash cmd/serf-hub/jstest/run-all.sh
```

Expected: PASS. If either command fails, fix the root cause before reporting completion.

---

## Self-Review Notes

- Spec coverage:
  - Backend/AppWire linkage: Tasks 1-2.
  - Web SubagentRun model, out-of-order merging, duplicate handling, short IDs: Task 3.
  - TUI structured metadata and notification handling: Task 4.
  - Cold reload reconciliation: Task 5.
  - Bounded child-step preview: Task 6.
  - Final ID/status polish and deterministic regression coverage: Task 7.
- Placeholder scan:
  - No banned placeholder markers or unspecified generic edge-case steps remain.
  - Task 5 includes one conditional seam because the existing thread-read/jobstore access path must be reused rather than inventing a second store root; the exact helper contract and assertions are specified.
- Type consistency:
  - Backend snake_case event fields project to camelCase `SerfJobInfo` fields.
  - Web normalizer accepts both snake_case and camelCase.
  - TUI consumes `appwire.SerfJobInfo` directly and stores the same semantic names in `SubagentRunInfo`.
