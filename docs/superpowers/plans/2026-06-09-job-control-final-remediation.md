# Job Control Final Remediation Spec

Date: 2026-06-09

## Purpose

This spec aggregates the open Roborev findings on `job-control-spec` into one final remediation pass. It is intentionally a remediation spec, not an implementation patch.

The source data is `roborev fix --list` plus detailed `roborev show --job <id> --json` output collected on 2026-06-09. At collection time Roborev reported 121 open jobs. Many open jobs are successful reviews with no findings; this document tracks open failing findings only.

## Source Jobs

Open failing jobs grouped by highest reported severity:

- High: 1300, 1324.
- Medium: 1276, 1277, 1278, 1279, 1283, 1284, 1285, 1286, 1287, 1288, 1289, 1295, 1298, 1301, 1303, 1304, 1305, 1307, 1308, 1310, 1311, 1312, 1313, 1314, 1316, 1317, 1319, 1320, 1322, 1323, 1325, 1326, 1327, 1329, 1331, 1333, 1336, 1337, 1338, 1340, 1342, 1343, 1345, 1347, 1349, 1351, 1352, 1353, 1358, 1359, 1360, 1361, 1362, 1363, 1365, 1368, 1369, 1371, 1402, 1403, 1404, 1405, 1406, 1407, 1408, 1409, 1410, 1411.
- Low: 1297, 1302, 1346, 1354, 1355, 1356, 1364, 1370, 1372, 1373, 1374.

Do not assume every historical finding still applies to current `HEAD`. Many are duplicates or were fixed in later commits but remain open in Roborev. Each task below includes a "verify or fix" step and a Roborev closure requirement.

## Execution Rules

- Do not make broad refactors while remediating. Fix the specific correctness gaps and add focused regression tests.
- Backward compatibility or migration behavior requires Jesse's explicit approval before implementation. This matters for legacy output sidecars and old terminal records without metadata.
- Prefer one implementation subagent for the tightly coupled recovery/lifecycle fixes, then fresh spec and quality reviewers after each task group.
- Do not close Roborev jobs just because later code appears to address them. Close only after the corresponding regression tests and final branch review verify the behavior.
- Keep low-severity-only findings from blocking progress, but record them and close them if fixed incidentally.

## Task 1: Delegate Startup And Finalization Durability

Source jobs: 1411, plus related delegate durability coverage from 1402.

Problem summary:

- `delegate` startup can durably append `job_started` before `job_session_assigned`. If the second append fails, the folded record is `running` with no transcript reference and no live runtime tracked by the manager.
- Delegate finalization currently has paths that can attempt durable finalization once and then exit. A transient output, `job_finished`, or notification append error can leave the delegate stuck in `jm.running` with `done` unclosed.

Requirements:

- Make delegate startup atomic from the folded record perspective.
  - Preferred shape: include `transcript_ref` in the first durable startup event if the child session already exists at that point.
  - Acceptable alternative: if `job_session_assigned` cannot be durably appended, durably close/cancel the partial job before returning, and make the caller see a failed start instead of a live running job.
- Mirror the shell finalization retry shape for delegate jobs.
  - Retry delegate finalization until terminal state and notification state are durable, or until the store/manager is closing.
  - Do not remove the live runtime or close `done` before durable ownership of the terminal result is established.
  - Keep finalization idempotent across duplicate child terminal notifications.
- Preserve existing structured-result behavior.
  - The delegate job remains the durable owner of `structured_result`.
  - The child result is marked consumed/reclaimable only after durable delegate finalization.

Tests:

- Startup append failure after `job_started` leaves no folded live delegate without `transcript_ref`.
- Delegate `job_finished` append failure retries and eventually finalizes after the store accepts writes again.
- Delegate output append failure retries without closing `done`.
- Duplicate child terminal notifications do not duplicate terminal events or output.

Roborev closure:

- After tests pass and aggregate review passes, close job 1411 findings for `agent/job_delegate.go:568` and `agent/job_delegate.go:513`.

## Task 2: Output Metadata, Retention, And Grep Recovery

Source jobs: 1279, 1285, 1287, 1288, 1329, 1333, 1371, 1372, 1373, 1374, 1402, 1403, 1404, 1405, 1406, 1407, 1408, 1409, 1410, 1411.

Problem summary:

- Output retention metadata has had several crash windows: lifetime byte loss after prune, stale final sidecars after ordinary append, pending prune sidecars accepted too broadly, metadata durable before output bytes, and missing/corrupt metadata migration questions.
- JSONL recovery and append rollback must preserve complete events and never accept corrupt complete records.
- Grep must honor byte and line bounds without skipping valid boundary lines.

Requirements:

- Pending prune metadata recovery:
  - Pending sidecars must not use ordinary append-ahead recovery.
  - If pending metadata describes a retained suffix while the output file is still larger than the retained window, the prefix must be validated against final sidecar metadata when present.
  - If no full-file or prefix integrity proof exists, do not promote the larger file to valid metadata. Fail loud rather than silently resetting lifetime offsets.
- Final sidecar stale-append recovery:
  - Allow recovery only when the current output file starts with the sidecar-authenticated retained bytes.
  - Preserve rejection for replaced or corrupt retained prefixes.
- Metadata durability:
  - Fsync output bytes/truncation before writing final sidecar metadata that hashes those bytes.
  - Fsync temp metadata files and parent directory around rename.
  - Fsync rollback truncation after failed event append.
- Jobstore JSONL recovery:
  - Truncate only incomplete trailing JSONL fragments from crash/EOF.
  - Preserve errors for corrupt complete JSON records.
  - Repair valid trailing JSON objects missing a newline before future appends.
- Retention accounting:
  - Reconciliation must use durable lifetime byte count and retained start, not retained file size alone.
  - `job_read_output` and grep offsets must use validated retained offsets.
- Grep bounds:
  - Do not allocate unbounded lines while scanning retained output.
  - Enforce match count and byte budgets on live and terminal output paths.
  - Count logical content bytes for `maxLineBytes`; do not reject exactly capped content just because LF or CRLF is present.

Decision point:

- Jobs 1406 and 1408 request legacy metadata/missing-sidecar migration behavior. That is backward compatibility. Get Jesse's explicit approval before implementing any legacy migration path. If not approved, document that these Roborev findings are intentionally not fixed and close/respond accordingly only with Jesse's direction.

Tests:

- Pending sidecar plus pre-truncation output with corrupt prefix and valid suffix is rejected.
- Pending sidecar plus valid final sidecar prefix recovers correct lifetime accounting.
- Stale final sidecar after ordinary append recovers only when prefix matches.
- Replaced/corrupt retained output with a sidecar is rejected.
- Crash after prune before final sidecar preserves total bytes or fails loud without rewriting wrong metadata.
- Partial JSON cuts inside strings, booleans, and numbers recover; complete corrupt records fail.
- Grep boundary tests for exactly `maxLineBytes` content ending in LF and CRLF.
- Very large single-line output does not allocate past configured line bounds.

Roborev closure:

- Close jobs 1409, 1410, and the output metadata finding in 1411 only after a clean aggregate review.
- Audit historical output jobs 1279, 1285, 1287, 1288, 1402-1408 and close only findings verified fixed or explicitly superseded.

## Task 3: Shell Process Lifetime, Stop Semantics, And Shutdown Ordering

Source jobs: 1300, 1301, 1302, 1311, 1312, 1313, 1314, 1316, 1323, 1324, 1336, 1337, 1338, 1340, 1342, 1343, 1349, 1351, 1352, 1353, 1354, 1355, 1356, 1358, 1359, 1368, 1369, 1370.

Problem summary:

- Durable shell jobs must survive caller context cancellation after start, while still respecting pre-start cancellation and foreground cancellation.
- Shutdown, explicit stop, and runtime timeout classification have race-prone ordering.
- Some failure paths can close `done` before the process actually exits or leave jobs running after store close.
- Test coverage has a few timer-based races.

Requirements:

- Streaming executor correctness:
  - `LocalExecutionEnvironment` must implement `StreamingExecutor` in the same commit as any compile-time assertion.
  - `Wait` must return non-exit errors rather than coercing them to exit code 127.
  - `Signal` escalation must not send SIGKILL after `Wait` has reaped the process.
- Context lifetime:
  - Background/promoted shell process lifetime must detach from tool-turn context after successful process start.
  - Pre-start cancellation must still abort startup.
  - The start/detach handoff must be atomic enough that cancellation immediately after process start cannot kill a committed durable background job.
- Timeout contract:
  - Streaming shell execution must honor `SessionConfig.DefaultCommandTimeoutMS`, `MaxCommandTimeoutMS`, and runtime `SetTimeout`.
  - `timed_out` means foreground wait timeout, not `max_runtime_ms` process kill.
- Stop and terminal classification:
  - Confirmed live `job_stop` should return the cancellation status/reason contract, not stale `running`.
  - Stop vs runtime timeout ordering must be explicit. Stop before runtime timeout should win; stop after runtime timeout must not mask `run_timeout`.
  - Session shutdown should classify managed shell jobs as deliberate shutdown cancellation, including subagent-owned jobs sharing the parent environment.
- Close ordering:
  - Parent close must prevent subagents from starting/promoting shell jobs during shared environment cleanup.
  - `jobManager.close()` must reject new jobs atomically and be idempotent.
  - If a real process has started but durable commit is rejected by close, signal it and wait for process exit before closing `done`.
- Foreground output:
  - Foreground shell results must preserve full event/hook output or promote oversized output durably instead of discarding earlier bytes.
  - Promotion and terminal background result shapes should include `output` and `truncated` even when output is empty.

Tests:

- Cancel the tool context after a background shell result and prove the process remains running.
- Cancel before start and prove no durable job is committed.
- Simulate a writer/streaming wait error and assert a failed job/result reason.
- Stop-before-timeout and stop-after-timeout classification tests.
- Parent close while child is about to start shell: no untracked process and durable cancellation result.
- Commit failure after process start waits for fake `Wait` before closing `done`.
- Replace sleep-based tests with deterministic channels or bounded polling.

Roborev closure:

- High jobs 1300 and 1324 must be explicitly closed after direct tests.
- Close shell lifecycle jobs only after final aggregate review confirms no remaining medium/high shell lifetime findings.

## Task 4: Job Notification Delivery And Session State

Source jobs: 1307, 1308, 1310, 1317, 1319, 1320, 1322, 1342, 1343, 1345, 1346, 1347, 1358, 1360, 1361, 1362, 1363, 1364, 1365.

Problem summary:

- Durable terminal notifications can be stranded after restart, store-load failure, delivered-mark failure, or missing notify callback installation.
- Notification no-op paths can leave sessions in `SessionProcessing` or corrupt `SESSION_END` dedupe state.
- Durable transcript append/sync failure can duplicate notification turns.

Requirements:

- Restore and wake:
  - Rehydrate all `NotifyNotArmed` and `NotifyPending` terminal jobs on startup.
  - If notifications are queued before `SetNotifyFunc`, installing the callback must submit a wake.
  - Requeued notifications must schedule bounded retry wakes.
- Durable delivery:
  - Mark `job_notification_delivered` only after the notification turn is durably recorded.
  - If transcript durable append/sync fails, rollback or avoid duplicate sequence writes before retry.
  - Job notification delivered-mark append failures must requeue without losing retry wake.
- Session state:
  - Every no-model/no-op notification path must restore `SessionIdle`.
  - `finishNotificationNoop` must not suppress `SESSION_END{input_complete}` for real work that follows in the same drain.
  - Clearing no-op suppression must preserve close-path dedupe and never emit `input_complete` after `session_closed`.
- Retry timer:
  - Reset must cancel or invalidate active retry timers only when no pending notifications remain.
  - Mixed injected/delivered failure batches must not cancel the only pending retry.

Tests:

- Restore session with pending terminal jobs, install notify callback afterward, verify wake.
- Store-load failure requeues and autonomously retries.
- Delivered-mark failure requeues with bounded wake.
- Durable transcript sync failure does not duplicate entries with same sequence.
- Notification no-op paths leave session idle.
- Deferred continuation dropped after notification no-op still emits correct session end unless the session is closing.
- Closing session does not later emit `SESSION_END{input_complete}`.

Roborev closure:

- Close notification/session lifecycle jobs only after replay/retry and session-end tests pass.

## Task 5: Job Tool API, Structured Output, And Provider Schemas

Source jobs: 1276, 1277, 1325, 1326, 1327, 1329, 1331, 1333, 1351, 1368, 1374, 1405.

Problem summary:

- Tool schemas, public tool behavior, registry output contracts, and provider-specific schema normalization have had mismatches.
- Some findings may already be fixed in later commits; verify current behavior before changing code.

Requirements:

- Tool availability:
  - Root-only gating must match the current v1 policy: root-only for delegate/watch behavior, but sessions that own jobs must be able to manage their own jobs if that remains the chosen contract.
  - `delegate`, `job_send_message`, observer/watch tools, and job tools must have a documented policy for root, child, and observer sessions.
- Shell tool execution:
  - Schema fields `background`, `block_timeout_ms`, and `max_runtime_ms` must be wired to the runtime executor if advertised.
  - Remove or reject legacy `timeout_ms` in the cutover surface if the spec says no shim.
- JSON/full-output contract:
  - Model-facing shell/job JSON must remain valid under configured output limits.
  - Event/hook `FullOutput` must preserve the untruncated structured result where the registry contract requires it.
  - If minimum JSON envelope exceeds configured limits, avoid generic truncation corrupting JSON.
- `job_read_output` behavior:
  - `block=true` waits for next output or terminal state.
  - Grep searches retained output, not only the tailed content window.
  - Echoed grep patterns and result JSON must fit the effective output budget.
  - For old structured-result records, decide whether `structured_result_valid` defaults to true when `structured_result` exists. If this is backward compatibility, get Jesse's approval before implementation.
- Provider schema:
  - Gemini-compatible schema normalization must handle nullable fields without passing JSON Schema `type` arrays through unsupported provider paths.
- Projection:
  - `job_list` should include shell `command` for shell jobs where present.

Tests:

- Provider schema snapshot/adapter test for nullable cursor with Gemini.
- Tool result tests proving parseable JSON under tiny configured limits.
- `ExecResult.FullOutput` or event hook tests preserving full shell/job output.
- `job_read_output` grep outside `tail_bytes`.
- `job_list` includes shell command.

Roborev closure:

- Close tool/API jobs only after current behavior is verified and any stale findings are explicitly commented as superseded.

## Task 6: Spec And Plan Document Reconciliation

Source jobs: 1276, 1277, 1278, 1279, 1286, 1289, 1295, 1297, 1298.

Problem summary:

- The design and phase plans contain stale or conflicting instructions from earlier iterations.
- Some findings point to implementation-plan text that may be obsolete after later code work.

Requirements:

- Update the design spec to resolve:
  - Transcript wording: the job store is the durable source of truth, while injected notifications may appear in session history unless a non-transcript channel is explicitly designed.
  - Root-only/job ownership policy for root, child, observer, and watched sessions.
  - `job_send_message` availability for observer advice flow, or an alternative observer delivery path.
  - Raw `communicate.output` capture for `structured_result`.
  - Forwarded `job_started` omits `terminal_generation`; terminal generation is copied on forwarded terminal/notification events only.
  - `structured_result_valid=false` testing language if v1 schema enforcement makes false non-meaningful.
  - `SubagentStatus` private runtime wording vs public status/snapshot projections.
- Update phase plans or add an errata section so historical execution instructions no longer contradict current code.
- Add a cutover audit for old shell `timeout_ms` if the no-shim policy remains.

Tests:

- Documentation-only task, but run docs checks through `make lint`.

Roborev closure:

- Close documentation-only jobs after the spec/plan errata commit, or comment that a historical phase plan is superseded by this final remediation spec.

## Task 7: Roborev Closeout And Final Gate

Requirements:

- Before implementing each task group, rerun `roborev fix --list` and confirm the open job IDs still match this spec.
- After each task group:
  - Run focused tests for that group.
  - Run `make lint`.
  - Run spec and quality subagent reviews for the group.
  - Comment and close only the Roborev jobs whose findings are actually fixed or explicitly superseded by Jesse-approved decisions.
- Final verification:
  - `make test`
  - `make lint`
  - race tests for affected job manager/session paths
  - live smoke for delegate/job tools if credentials and model configuration are available
  - `roborev review --branch --wait`
- If the final Roborev review has only low-severity findings, record them and pause for Jesse rather than continuing to Phase 4.

## Suggested Implementation Order

1. Task 1 delegate durability and retry.
2. Task 2 output metadata pending-prune hardening and grep boundary bug.
3. Task 3 shell lifecycle and shutdown races.
4. Task 4 notification retry/session-state correctness.
5. Task 5 tool/provider/output-contract cleanup.
6. Task 6 documentation reconciliation.
7. Task 7 closeout and final gate.

This order puts current aggregate Medium findings first, then clears older high/medium lifecycle issues, then handles API/docs cleanup.
