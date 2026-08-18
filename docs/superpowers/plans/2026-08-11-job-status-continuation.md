# Terminal Job Status Continuation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep a session running after it reads a terminal background job status without losing that status result or consuming another session's notification.

**Architecture:** A successful terminal `job_status` result is a notification acknowledgement only after its tool-result turn is durably appended. The session that owns the job may then consume its own pending terminal notification and remove the matching in-memory wake; a parent inspecting a child-owned job must leave the child's notification untouched.

**Tech Stack:** Go, Evener session/jobstore lifecycle, transcript JSONL persistence, deterministic real-session tests.

## Global Constraints

- Make the smallest reasonable change; do not alter the public job-control tool schema or model-facing prompt.
- Preserve the documented rule in `docs/job-control.md`: only the owner's own read consumes; a parent reading a child-owned job never settles the child's notification.
- A terminal notification may be consumed only after the matching `job_status` tool result is durably appended to the caller's transcript.
- Ordinary tool-result rounds retain their existing non-durable append behavior; durability changes only for a batch containing a successful terminal `job_status` result.
- Default tests remain deterministic and offline. Use real Evener sessions, job managers, transcript writer, and shell jobs; fake only the filesystem failure boundary.
- Do not add wording, rendered-command, or large-string tests.

---

### Task 1: Preserve terminal status durability and notification ownership

**Files:**
- Modify: `agent/session.go`
- Modify: `agent/session_tools.go`
- Modify: `agent/session_tool_round.go`
- Modify: `agent/session_tools_jobs.go`
- Test: `agent/job_notify_consume_test.go`

**Interfaces:**
- Consumes: `Session.writeTranscriptDurable(schema.Turn) error`, `Session.nestedOrLocalJobManager(jobID)`, `jobstore.JobRecord.OwnerSessionID`, and the existing `jobStatusResult` stored in `tool.ExecResult.ToolState`.
- Produces: an error-returning durable counterpart to `appendTurnWithTranscriptMessage`; a pure batch predicate identifying successful terminal `job_status` results; owner-only consumption keyed to the calling `Session.id`.

- [ ] **Step 1: Add a failing durability test**

  Add `TestTerminalJobStatusTranscriptFailureLeavesNotificationPending` to `agent/job_notify_consume_test.go`. Start a real background shell job and wait for its terminal notification. Attach a `transcript.Writer` backed by `transcriptWriteFailFS`, enable its write failure, execute a real `job_status`, and call `persistToolResults`. Assert that the call returns `errInjectedTranscriptWrite`, the job remains `NotifyPending`, the in-memory notification count remains one, and no tool-result turn was appended to live history.

- [ ] **Step 2: Add a failing owner-isolation test**

  Add `TestParentTerminalJobStatusReadLeavesChildNotificationPending` to `agent/job_notify_consume_test.go`. Create parent and child sessions, register the live child with `parent.subagents.track`, start a real child-owned background shell job, execute and persist `job_status` through the parent, then assert the child's durable record remains `NotifyPending` and the child's in-memory notification remains queued.

- [ ] **Step 3: Prove both tests are red for the intended defects**

  Run:

  ```sh
  go test ./agent -run 'TestTerminalJobStatusTranscriptFailureLeavesNotificationPending|TestParentTerminalJobStatusReadLeavesChildNotificationPending' -count=1 -v
  ```

  Expected: the durability case shows `persistToolResults` swallowing the transcript write error and consuming the notification; the owner case shows the parent consuming the child-owned notification.

- [ ] **Step 4: Add the minimal durable append path**

  In `agent/session.go`, add an error-returning durable counterpart to `appendTurnWithTranscriptMessage`. It must construct the live and persisted turns, call `writeTranscriptDurable` on the persisted turn first, emit the existing transcript-write warning and return the error on failure, and append the live turn to history only after success.

  In `agent/session_tool_round.go`, add a helper that returns true only when aligned `calls` and `results` contain a non-error `job_status` whose `ToolState` decodes to a terminal `jobStatusResult`. Do not infer terminal state from rendered text.

  In `agent/session_tools.go`, have `appendToolResults` select the durable helper only for such a batch. Propagate the durable write error out of the existing response-side-effect boundary and do not autosave or announce readable images after that failure. Keep the existing non-durable helper for every other batch.

- [ ] **Step 5: Enforce caller ownership before consuming**

  In `agent/session_tools_jobs.go`, derive the owner from `rec.OwnerSessionID`, falling back to `jm.sessionID` only when the record does not carry an owner. Return unless that owner equals the calling `Session.id`. Preserve the durable consume event, matching in-memory wake removal, and forwarded-copy settlement for an actual owner read.

- [ ] **Step 6: Prove the focused tests are green and mutation-sensitive**

  Run the focused tests at least 20 times. Then temporarily reverse each production guard independently: make the terminal batch use the non-durable append, and compare ownership to the resolved manager instead of the caller. Each corresponding test must fail for its named reason. Restore the implementation and rerun the focused tests green.

- [ ] **Step 7: Run repository verification**

  Run:

  ```sh
  go test ./agent -run 'Test.*TerminalJobStatus|Test.*JobStatus.*Notification' -count=20
  go test -race ./agent -run 'Test.*TerminalJobStatus|Test.*JobStatus.*Notification' -count=10
  make lint
  make build
  ROOT_FULL=1 WEB=0 make test
  make test-dev-tooling
  ```

- [ ] **Step 8: Commit**

  Commit only the plan and the terminal-status lifecycle files with a detailed message explaining the QEMU trajectory root cause, the durability ordering, the owner-only rule, and the test evidence.
