# job-shell-lifecycle: foreground inline, promotion at timeout, explicit background, max_runtime kill, rejected combo

**What this covers**: the shell tool's whole job-capable lifecycle
(`docs/job-control.md` "Existing shell/bash tool", lines 139-249).
(a) Foreground inline result with stdout+stderr+exit code, ephemeral —
no durable record (line 179); (b) nonzero exit reported honestly as a
normal tool result, not hidden (line 112: `failed` / `exit_nonzero`); (c) promotion at `block_timeout_ms` — job_id returned,
process keeps running, later output readable (lines 180, 229-242);
(d) `background=true` from the start (lines 181, 217-227);
(e) `max_runtime_ms` kills a runaway and finalizes `stopped` /
`run_timeout` (line 183), with `timed_out` never meaning the runtime
kill (line 185); (f) the contradictory `background=true` +
`block_timeout_ms` combo errors `invalid_request` (ergonomics addendum
§2 P6). Terminal-notification cardinality/format for these jobs is
job-notification-semantics.md, not re-asserted here.

## Pre-state

- Fresh binaries from the branch under test; hub on `127.0.0.1:9180`
  (`docs/agentic-testing.md` setup checklist); credentialed model.
- `tmpdir=$(mktemp -d -t serf-e2e-shlife-XXXXX)`.

## Steps

1. Spawn a session via `/api/spawn` with `working_dir=$tmpdir`.
   Capture `SID`.
2. Turn 1 — fast arms (a), (b), (d), (f):

   > Do these steps in order. Step 4 is expected to return a tool
   > error — report it verbatim and continue.
   > 1. Run the shell tool with command:
   >    `sh -c 'echo INLINE_OUT_OK; echo INLINE_ERR_OK >&2; exit 0'`
   >    (no background, no timeouts). Report the full result JSON
   >    verbatim.
   > 2. Run the shell tool with command:
   >    `sh -c 'echo FAIL_OUT_7; echo FAIL_ERR_7 >&2; exit 7'`.
   >    Report the full result JSON verbatim.
   > 3. Run the shell tool with background true and command:
   >    `sh -c 'echo BG_START_MARK; exec sleep 27182'`. Report the
   >    full result JSON verbatim.
   > 4. Run the shell tool with background true, block_timeout_ms
   >    30000, and command: `echo COMBO_PROBE`. Report the result or
   >    error verbatim.
   > 5. Call job_list with no filters and report the jobs array.
   > 6. End your turn.
3. Turn 2 — timing arms (c) and (e) (new user prompt):

   > Do these steps in order.
   > 1. Run the shell tool with block_timeout_ms 5000 and command:
   >    `sh -c 'echo EARLY_MARK; sleep 25; echo LATE_MARK'`. Report
   >    the full result JSON verbatim.
   > 2. Run the shell tool with background true, max_runtime_ms 5000,
   >    and command: `sh -c 'echo RUNAWAY_START_71; exec sleep 31415'`.
   >    Report the full result JSON verbatim.
   > 3. Run the foreground shell command `sleep 30` to let both jobs
   >    settle.
   > 4. Call job_read_output for the step-1 job_id and report the full
   >    JSON. Call job_read_output for the step-2 job_id and report
   >    the full JSON.
   > 5. End your turn.
4. Read the transcript
   (`find ~/.local/state/serf/projects -name "$SID.transcript.jsonl"`)
   and the durable log
   (`find ~/.local/state/serf/projects -path "*sessions/$SID/jobs.jsonl"`),
   plus `pgrep -f "sleep 31415"` after turn 2.

## Expected

- Arm (a): the step-1 TOOL_RESULTS JSON has `status` `"completed"`,
  `reason` `"exit_zero"`, `exit_code` `0`,
  `running_in_background` `false`, `timed_out` `false`, NO `job_id`,
  and `output` containing BOTH `INLINE_OUT_OK` and `INLINE_ERR_OK`
  (stdout and stderr are both captured inline).
- Arm (b): step 2 is a normal tool result (not a tool error) with
  `status` `"failed"`, `reason` `"exit_nonzero"`, `exit_code` `7`, and
  `output` containing `FAIL_OUT_7` and `FAIL_ERR_7`. Falsification:
  the nonzero exit comes back as `completed`, the exit code is absent,
  or the call surfaces as a tool error.
- Ephemerality: the turn-1 step-5 `job_list` does NOT contain a job
  for steps 1 or 2 (inline foreground completions create no durable
  record, contract line 179), and `jobs.jsonl` has no `job_started`
  for them. It DOES contain the step-3 background job as `running`.
- Arm (d): step 3 returns promptly (well under the sleep) with a
  `job_id`, `status` `"running"`, `running_in_background` `true`, and
  no terminal fields. The tool never waited for completion (line 181).
- Arm (f): step 4 fails synchronously with an `invalid_request` error
  naming the foreground-only nature of `block_timeout_ms`; no job
  record is created for it (no `job_started` in `jobs.jsonl`, nothing
  in the step-5 listing).
  <!-- pin: ergonomics §2 P6 — the rejection lands in Phase 1.9. On a
       pre-1.9 build the combo is silently accepted and the timeout
       does nothing (contract line 181); record the shipped error
       wording for future runs. -->
- Arm (c): the turn-2 step-1 result returns at ~5s (not ~25s) with a
  `job_id`, `status` `"running"`, `reason` `"foreground_timeout"`,
  `timed_out` `true`, `running_in_background` `true`, and `output`
  containing `EARLY_MARK` but NOT `LATE_MARK`. The process kept
  running: the step-4 read of that job shows `status` `"completed"`,
  `exit_code` `0`, and content containing BOTH `EARLY_MARK` and
  `LATE_MARK`. Falsification: the result has no `job_id` (the command
  was killed at the wait timeout) or the later read never gains
  `LATE_MARK`.
- Arm (e): the step-2 result returns immediately (`running`); the
  step-4 read of that job shows `status` `"stopped"`, `reason`
  `"run_timeout"`, content containing `RUNAWAY_START_71`. The runaway
  is actually dead: `pgrep -f "sleep 31415"` finds nothing after the
  turn. `jobs.jsonl` has one `job_finished` for it with
  `status:"stopped"`, `reason:"run_timeout"`. Falsification: the job
  is still `running` past ~20s, the process survives, or the kill is
  reported as `failed`/`cancelled` instead of `stopped`/`run_timeout`
  (line 183: a runtime-limit kill is neither command failure nor
  parent cancellation).
- `timed_out` discipline: in arm (e)'s result `timed_out` is `false`
  or absent — `timed_out` means the foreground wait expired, never the
  `max_runtime_ms` kill (line 185). In arm (c) it is `true`.

## Cleanup

- `job_stop` the arm-(d) job (`sleep 27182` outlives the card); arm
  (c) and (e) jobs are already terminal.
- Shut down the session (`POST /s/$SID/shutdown`); `rm -rf "$tmpdir"`.

## Sharp edges

- Arm (c)'s 5s-vs-25s timing assertion needs the tool round bracketed:
  use the api_call timestamps in the transcript JSONL if wall-clocking
  is too coarse. Model thinking time inflates everything by seconds;
  the 20s gap absorbs it.
- Arm (e) uses `exec sleep` so the runaway is a single recognizable
  process; without `exec` the `sh` wrapper dies but the sleep can be
  orphaned by a process-group miss — if `pgrep` still finds it, that
  is a real signal-delivery finding (contract line 745: signal the
  process group where supported), not a card bug.
- Terminal notifications for arms (c)/(d)/(e) arrive as notification
  turns once armed (promotion arms the notification too, line 180);
  this card does not assert their format — see
  job-notification-semantics.md.
- If the model inlines extra tool calls between turn-2 steps 1 and 2,
  the clocks still work — each arm's timing is internal to its own
  tool round.
