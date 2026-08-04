# Task 1 report: remove idle-policy plumbing from delegate send

## Summary
Implemented the Task 1 contract change so `delegate_send` no longer carries `on_idle` through the send path, and idle delegates are resumed through the existing restore path when sent to without `on_idle`.

## Changed files
- `agent/job_delegate.go`
  - Removed `onIdle` from `classifyDelegateSendTarget`.
  - Removed the local defaulting of empty `OnIdle` to `fail` in `sendDelegateMessage`.
  - Removed the idle-failure branch that returned `target_idle` for omitted policy.
  - Kept the existing restore/resume path intact for idle resumable delegates.
- `agent/session_tools_jobs.go`
  - Stopped populating `sendMessageArgs.OnIdle` in `delegateSendTool`.
- `agent/internal/tool/definitions.go`
  - Removed `on_idle` from the `delegate_send` tool schema and updated the description.
- `agent/internal/tool/definitions_test.go`
  - Updated the schema regression test to assert that `delegate_send` no longer exposes `on_idle`.
- `agent/session_tools_jobs_stop_delegate_test.go`
  - Reworked the focused regression test to expect the no-`on_idle` call to resume an idle delegate.
  - Added a separate schema-level assertion that an explicit obsolete `on_idle` field is rejected by the tool contract.
  - Updated the related property assertions to stop expecting `on_idle`.
  - Removed obsolete `on_idle` fields from nearby delegate_send registry call fixtures.
- `agent/job_delegate_send_test.go`
  - Updated direct send-path regression coverage to expect idle delegates to resume instead of failing closed.
- `agent/session_tools_jobs_seed100_more_test.go`
  - Updated the fuzz-seed fixture list to point at the renamed regression test.
- `agent/transcript_render_job_test.go`
  - Removed obsolete `on_idle` from the transcript-render regression fixture.
- `agent/fuzz_jd_classify_delegate_send_target_test.go`
  - Updated the classify fuzz harness to match the new `classifyDelegateSendTarget` signature.

## Tests / commands run
- `go test ./agent -run 'TestDelegateSendIdleDefault' -count=1 -v`
  - Expected focused regression run from the task brief, but it did not complete in this harness and was promoted to a background job.
- `go test ./agent/internal/tool -run '^TestDefDelegateSendShape$' -count=1 -v`
  - Attempted multiple times; the command did not finish within the harness timeout here.
- `timeout 20s go test ./agent -run '^TestDelegateSendIdleDefaultResumesAndOnIdleIsRejected$' -count=1 -v`
  - Timed out in this harness.
- `gofmt -w ...`
  - Completed successfully on the edited Go files.

## Commit
- `38c817949d72a298a6985de25312e74ae1a40f6b` — `fix: make delegate sends wake idle delegates`

## Concerns
- I was not able to get the focused `go test` invocations to complete inside this harness; they timed out or were promoted to background jobs, so I cannot honestly claim a verified pass from the captured output here.
- I updated a few adjacent regression fixtures (`transcript_render_job_test.go`, fuzz seed wiring, and the fuzz classifier signature) because the removed `on_idle` contract would otherwise leave stale coverage behind.

## Fix follow-up
- Required review fix addressed: the retained-subagent `running` branch in `agent/job_delegate.go` now restores the prior fall-through behavior when `findRunningDelegateByTranscriptRef` cannot resolve an active job, instead of converting that case into a hard `target_not_resumable` error.
- Scope note: no additional files were added beyond the already-related task fixtures; this follow-up only corrected the retained-running control flow and did not expand scope further.

## Fix turn details
- `agent/job_delegate.go`
  - Restored the fall-through in the retained-running branch: on `findRunningDelegateByTranscriptRef` error, `delegate_send` no longer returns `target_not_resumable` and instead continues into the existing restore/resume path.
- `agent/cov_w3dlg_send_test.go`
  - Replaced the prior idle-policy regression with `TestW3Dlg_SendTerminalRunningSubLookupFailureFallsThroughToResume`, which forces the retained-running lookup to fail and verifies the send still resumes.
- Tests / commands run in this fix turn:
  - `timeout 20s go test ./agent -run '^TestW3Dlg_SendTerminalRunningSubLookupFailureFallsThroughToResume$|^TestW3Dlg_SendTerminalRunningSubNoActiveJobIdleFails$|^TestDelegateSendIdleDefaultResumesAndOnIdleIsRejected$' -count=1 -v`
    - Exit: `124`
  - `go test ./agent -run '^TestW3Dlg_SendTerminalRunningSubLookupFailureFallsThroughToResume$' -count=1 -v`
    - Timed out in background as `job_034183Ur4WtW62SznhD82d_7zxLaa7RBBPQ`
  - `go test ./agent/internal/tool -run '^TestDefDelegateSendShape$' -count=1 -v`
    - Timed out in background as `job_034183Ur4WtW62SznhD82d_55buAJmV8wjY`
  - `timeout 20s go test ./agent -run '^TestW3Dlg_SendTerminalRunningSubLookupFailureFallsThroughToResume$' -count=1 -v -timeout 60s`
    - Exit: `124`
  - `timeout 20s go test ./agent -run '^TestW3Dlg_SendTerminalRunningSubLookupFailureFallsThroughToResume$' -count=1 -v -timeout 2m`
    - Timed out in background as `job_034183Ur4WtW62SznhD82d_08OBxP5nHmpU`
