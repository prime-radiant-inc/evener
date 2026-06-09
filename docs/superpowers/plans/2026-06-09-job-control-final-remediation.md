# Job Control Final Remediation Spec

Date: 2026-06-09

## Critical Read

The raw Roborev backlog looks much larger than the real remaining work. That is because it includes per-commit findings from intermediate states of this branch. Many of those commits were deliberately followed by fix commits, but their Roborev jobs remain open.

This branch has not shipped. Support only the final branch contract. Do not support intermediate branch formats, earlier output sidecars, earlier terminal records, removed shell parameters, or earlier structured-result records.

The implementation scope should be driven by the latest aggregate Roborev review, job 1411. Older jobs are closeout evidence: verify whether each theme is covered by current code, fix only if still true at `HEAD`, then comment and close the stale jobs.

## Current Blocking Findings

Source: Roborev job 1411.

These are the only findings that should be treated as known live blockers before moving on:

- Medium: `agent/job_delegate.go:568`
  Delegate startup persists `job_started` before `job_session_assigned`. If the second append fails, the folded durable state can show a running delegate with no live runtime or transcript ref.
- Medium: `agent/job_delegate.go:513`
  Delegate finalization can exit after one append/output failure, leaving a delegate stuck in `jm.running` with `done` unclosed.
- Medium: `agent/internal/jobstore/output.go:429`
  Pending prune metadata recovery validates only the retained suffix and can promote a larger file with a stale/corrupt prefix.
- Low: `agent/internal/jobstore/output.go:584`
  Grep line-length enforcement counts `\n`/`\r\n` even though returned lines strip terminators, so exact-boundary lines can be skipped.

## Non-Goals

- No support for intermediate on-branch output sidecars.
- No upgrade path for metadata written by earlier commits on this branch.
- No shell `timeout_ms` shim.
- No projection path for earlier structured-result records.
- No broad rewrite of job control, session lifecycle, or tool execution.
- No attempt to fix every historical Roborev finding as a separate implementation task.

If Roborev flags a pre-final branch artifact that requires support for an earlier branch format, prefer closing it as superseded by the final branch contract after Jesse agrees.

## Task 1: Delegate Startup Atomicity

Source jobs: 1411, historically related 1402.

Problem:

- Delegate startup currently has a two-event durable startup sequence. If `job_started` succeeds and `job_session_assigned` fails, the job store can fold a delegate as `running` without the transcript reference needed to route messages or recover cleanly.

Requirements:

- Make delegate startup atomic from the folded record's perspective.
- Preferred shape: include `transcript_ref` in the first durable startup event if the child session exists before the event is appended.
- Acceptable shape: if transcript assignment cannot be durably recorded, durably close/cancel the partial job before returning startup failure.
- Do not return a live delegate job unless the folded durable record has the transcript reference needed for recovery and `job_send_message`.

Tests:

- Inject append failure after the first startup write and assert no folded live delegate exists without `transcript_ref`.
- Assert the caller receives a failed startup result rather than a usable `job_id` for a non-recoverable partial start.
- Assert successful delegate startup still records a usable `transcript_ref` and can receive `job_send_message`.

Closeout:

- Close the `agent/job_delegate.go:568` finding in job 1411 after focused tests and aggregate review pass.

## Task 2: Delegate Finalization Retry

Source jobs: 1411, historically related 1402.

Problem:

- Delegate finalization can give up after output or jobstore append failure. Once the child has finished, that can leave the parent-side delegate job live forever in memory, with `done` unclosed and no production retry.

Requirements:

- Delegate finalization must retry until terminal state and notification state are durable, or until the job manager/store is closing.
- Do not close `done`, remove the runtime, or mark the child result consumed until the delegate job durably owns the terminal result.
- Keep duplicate child terminal notifications idempotent.
- Preserve the current structured-result behavior: the delegate job is the durable owner of the child `structured_result` after successful finalization.

Tests:

- `job_finished` append failure retries and eventually finalizes when the store accepts writes.
- Output append failure retries without closing `done`.
- Notification-pending append failure retries without dropping the terminal result.
- Duplicate terminal notifications do not duplicate terminal events or output.

Closeout:

- Close the `agent/job_delegate.go:513` finding in job 1411 after focused tests and aggregate review pass.

## Task 3: Pending Prune Metadata Recovery

Source jobs: 1411, 1410, 1409.

Problem:

- Pending prune metadata represents an in-progress destructive rewrite. The recovery path must not treat it like an ordinary append-ahead final sidecar.
- The current finding says recovery validates only that the retained suffix matches pending metadata, then trusts and rehashes the entire larger file. That can promote stale/corrupt prefix bytes and produce wrong lifetime offsets.

Requirements:

- Pending sidecars must use strict pending-prune recovery rules.
- If the output file is larger than the pending retained window, do not promote the larger file unless the prefix is independently validated by current final metadata.
- If the prefix cannot be validated, fail loud. Do not rewrite metadata over uncertain bytes.
- Keep ordinary append-ahead recovery limited to final sidecars.
- Preserve existing rejection for replaced/corrupt retained output.

Tests:

- Pending sidecar plus pre-truncation output with corrupt prefix and valid retained suffix is rejected.
- Pending sidecar plus valid final metadata for the prefix recovers correct lifetime accounting.
- Ordinary append-ahead recovery still works for a stale final sidecar when the retained prefix matches.
- Replaced/corrupt retained output with sidecar metadata still errors.

Closeout:

- Close the output metadata finding in job 1411 and the superseded pending-metadata jobs 1409 and 1410 after focused tests and aggregate review pass.

## Task 4: Grep Boundary Bug

Source jobs: 1411, 1372, 1373, 1374.

Problem:

- The grep line cap counts line terminator bytes even though returned lines strip them. A line with exactly `maxLineBytes` content plus LF or CRLF can be incorrectly skipped.

Requirements:

- Enforce the line cap against logical content bytes.
- Allow a trailing LF or CRLF beyond the logical content limit.
- Keep overlong line protection for content that actually exceeds the cap.

Tests:

- A line with exactly `maxLineBytes` content plus LF is eligible for matching.
- A line with exactly `maxLineBytes` content plus CRLF is eligible for matching.
- A line with `maxLineBytes + 1` content bytes is skipped or handled by the existing overlong-line behavior.

Closeout:

- Close the low finding in job 1411 and related low grep-boundary jobs after tests pass.

## Task 5: Historical Roborev Audit And Closure

Source jobs:

- High: 1300, 1324.
- Medium: 1276, 1277, 1278, 1279, 1283, 1284, 1285, 1286, 1287, 1288, 1289, 1295, 1298, 1301, 1303, 1304, 1305, 1307, 1308, 1310, 1311, 1312, 1313, 1314, 1316, 1317, 1319, 1320, 1322, 1323, 1325, 1326, 1327, 1329, 1331, 1333, 1336, 1337, 1338, 1340, 1342, 1343, 1345, 1347, 1349, 1351, 1352, 1353, 1358, 1359, 1360, 1361, 1362, 1363, 1365, 1368, 1369, 1371, 1402, 1403, 1404, 1405, 1406, 1407, 1408.
- Low: 1297, 1302, 1346, 1354, 1355, 1356, 1364, 1370.

Purpose:

- This is an audit and Roborev database cleanup task, not a blanket implementation task.
- Review these jobs after Tasks 1-4 are complete.
- For each job, decide one of:
  - Fixed by current code and tests.
  - Superseded by later code and final branch review.
  - Still live at `HEAD` and needs a new focused remediation task.

Rules:

- Do not add support code for historical findings about earlier branch formats.
- Do not implement speculative fixes based only on stale per-commit findings.
- Do not close high-severity jobs until their exact current-HEAD behavior has been verified directly, even if the latest aggregate review no longer reports them.
- Comment clearly when closing a historical job as superseded, referencing the final contract and the commit/test that covers it.

Suggested audit order:

1. High jobs 1300 and 1324. Verify they are genuinely fixed at `HEAD`; if not, stop and write a focused task.
2. Phase 3 output/delegate jobs 1402-1408. Most should be covered by Tasks 1-4 or later fix commits.
3. Phase 2 shell/session jobs 1301-1371. Verify current aggregate review no longer sees the issue before closing.
4. Documentation jobs 1276-1298. Prefer one docs errata/closeout pass, not implementation work.

## Final Gate

After Tasks 1-4 and the historical audit:

- Run focused tests for every changed subsystem.
- Run `make test`.
- Run `make lint`.
- Run race tests for affected job manager/session paths.
- Run live smoke for delegate/job tools if credentials and model configuration are available.
- Run `roborev review --branch --wait`.

Stop and discuss if the final Roborev review reports any Medium or High findings. If it reports only Low findings, record them and pause for Jesse.
