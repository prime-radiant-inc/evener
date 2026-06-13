# job-shell-lifecycle: foreground inline, promotion at timeout, launch-and-return, max_runtime kill, complete-or-handle

**What this covers**: the shell tool's whole job-capable lifecycle
(`docs/job-control.md` "Existing shell/bash tool", lines 139-249).
(a) Foreground inline result with stdout+stderr+exit code, ephemeral —
no durable record (line 179); (b) nonzero exit reported honestly as a
normal tool result, not hidden (line 112: `failed` / `exit_nonzero`);
(c) promotion at `max_wait_ms` — job_id returned, process keeps
running, later output readable (lines 180, 229-242);
(d) launch-and-return via small `max_wait_ms` (1000) — command still
running at the bound is promoted, result carries the promotion shape
(`timed_out:true`, `reason:"foreground_timeout"`,
`running_in_background:true`; spec §3 merge of explicit-background and
promotion result shapes);
(e) `max_runtime_ms` kills a runaway and finalizes `stopped` /
`run_timeout` (line 183), with `timed_out` never meaning the runtime
kill (line 185);
(f) complete-or-handle invariant (spec §0.6/§3): a fast chatty command
whose output exceeds the inline embed budget returns completed + job_id
+ truncated:true and a kept durable record; a fast quiet command is
ephemeral. Terminal-notification cardinality/format for these jobs is
job-notification-semantics.md, not re-asserted here.

## Pre-state

- Fresh binaries from the branch under test; hub on `127.0.0.1:9180`
  (`docs/agentic-testing.md` setup checklist); credentialed model.
- `tmpdir=$(mktemp -d -t serf-e2e-shlife-XXXXX)`.

## Steps

1. Spawn a session via `/api/spawn` with `working_dir=$tmpdir`.
   Capture `SID`.
2. Turn 1 — fast arms (a), (b), (d):

   > Do these steps in order.
   > 1. Run the shell tool with command:
   >    `sh -c 'echo INLINE_OUT_OK; echo INLINE_ERR_OK >&2; exit 0'`
   >    (no max_wait_ms). Report the full result JSON verbatim.
   > 2. Run the shell tool with command:
   >    `sh -c 'echo FAIL_OUT_7; echo FAIL_ERR_7 >&2; exit 7'`.
   >    Report the full result JSON verbatim.
   > 3. Run the shell tool with max_wait_ms 1000 and command:
   >    `sh -c 'echo BG_START_MARK; exec sleep 27182'`. Report the
   >    full result JSON verbatim.
   > 4. Call job_list with no filters and report the jobs array.
   > 5. End your turn.
3. Turn 2 — timing arms (c) and (e) (new user prompt):

   > Do these steps in order.
   > 1. Run the shell tool with max_wait_ms 5000 and command:
   >    `sh -c 'echo EARLY_MARK; sleep 25; echo LATE_MARK'`. Report
   >    the full result JSON verbatim.
   > 2. Run the shell tool with max_wait_ms 1000, max_runtime_ms 5000,
   >    and command: `sh -c 'echo RUNAWAY_START_71; exec sleep 31415'`.
   >    Report the full result JSON verbatim.
   > 3. Run the foreground shell command `sleep 30` to let both jobs
   >    settle.
   > 4. Call job_read_output for the step-1 job_id and report the full
   >    JSON. Call job_read_output for the step-2 job_id and report
   >    the full JSON.
   > 5. End your turn.
4. Turn 3 — arm (f) complete-or-handle (new user prompt):

   > Do these steps in order. Report every tool result verbatim.
   > 1. Run the shell tool with max_wait_ms 5000 and command:
   >    `yes COH_CHATTY_LINE | head -100000` (produces ~1.8MB of output,
   >    finishes in well under 5s). Report the full result JSON.
   > 2. If the step-1 result had a job_id (chatty — kept durable), call
   >    job_read_output for it and report the total_bytes and whether
   >    the content starts with COH_CHATTY_LINE.
   > 3. Run the shell tool with max_wait_ms 5000 and command:
   >    `sh -c 'exit 0'` (fast quiet, no output). Report the full
   >    result JSON.
   > 4. Call job_list with no filters and report whether any job_id
   >    from steps 1 or 3 appears. End your turn.
5. Read the transcript
   (`find ~/.local/state/serf/projects -name "$SID.transcript.jsonl"`)
   and the durable log
   (`find ~/.local/state/serf/projects -path "*sessions/$SID/jobs.jsonl"`),
   plus a runaway-liveness check after turn 2 that cannot match the
   checking shell itself: `ps -eo args | grep -c '^sleep 31415$'`
   (expect 0). Do NOT use `pgrep -f` — the pattern matches the
   checker's own command line and reports a phantom orphan
   (false-positive observed live 2026-06-12; group-kill semantics are
   pinned by `TestStreamCommandSignalKillsWholeProcessGroup`).

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
- Ephemerality: the turn-1 step-4 `job_list` does NOT contain a job
  for steps 1 or 2 (inline foreground completions create no durable
  record, contract line 179), and `jobs.jsonl` has no `job_started`
  for them. It DOES contain the step-3 promoted job as `running`.
- Arm (d): step 3 returns at ~1s (the `max_wait_ms` bound) with a
  `job_id`, `status` `"running"`, `reason` `"foreground_timeout"`,
  `timed_out` `true`, `running_in_background` `true`, and `output`
  containing `BG_START_MARK` (whatever was printed in the first 1s).
  This is the promotion shape — the single result shape for any shell
  call whose command outlives the `max_wait_ms` bound (spec §3: merges
  the old explicit-background and promotion result shapes into one).
  Falsification: no `job_id`, or `timed_out` false, or result blocks
  until the `sleep 27182` finishes.
- Arm (c): the turn-2 step-1 result returns at ~5s (not ~25s) with a
  `job_id`, `status` `"running"`, `reason` `"foreground_timeout"`,
  `timed_out` `true`, `running_in_background` `true`, and `output`
  containing `EARLY_MARK` but NOT `LATE_MARK`. The process kept
  running: the step-4 read of that job shows `status` `"completed"`,
  `exit_code` `0`, and content containing BOTH `EARLY_MARK` and
  `LATE_MARK`. Falsification: the result has no `job_id` (the command
  was killed at the wait timeout) or the later read never gains
  `LATE_MARK`.
- Arm (e): the step-2 result returns at ~1s (the `max_wait_ms` bound)
  with the promotion shape — `job_id`, `timed_out` `true`,
  `reason` `"foreground_timeout"`, `running_in_background` `true`,
  output containing `RUNAWAY_START_71`. The `max_runtime_ms 5000` then
  kills the command ~5s after it started; the step-4 read of that job
  shows `status` `"stopped"`, `reason` `"run_timeout"`. The runaway
  is actually dead: the exact-args liveness check counts zero
  `sleep 31415` processes after the turn. `jobs.jsonl` has one
  `job_finished` for it with `status:"stopped"`, `reason:"run_timeout"`.
  Falsification: the job is still `running` past ~20s, the process
  survives, or the kill is reported as `failed`/`cancelled` instead of
  `stopped`/`run_timeout` (line 183: a runtime-limit kill is neither
  command failure nor parent cancellation).
- `timed_out` discipline: `timed_out` means the foreground WAIT
  (`max_wait_ms`) expired, never the `max_runtime_ms` kill (line 185).
  In arm (d) and arm (e)'s initial result it is `true` (the 1s bound
  expired). In arm (c)'s initial result it is `true` (the 5s bound
  expired). In the terminal job_read_output of arm (e), the record
  shows the finalized `stopped/run_timeout` state — `timed_out` there
  is `false` or absent (the final status reflects the kill, not the
  foreground wait). Falsification: `timed_out` present and `true` in
  the arm-(e) FINAL read.
- Arm (f) complete-or-handle:
  - Turn-3 step-1 (chatty command): the result contains a `job_id`
    (output exceeded the inline embed budget), `status` `"completed"`,
    `truncated` `true`, and a bounded tail of the output. The step-2
    `job_read_output` returns `total_bytes` ≫ the truncated inline size
    and `content` beginning with `COH_CHATTY_LINE` — full retention
    survived (spec §0.6). The kept job emits NO terminal notification
    (spec §3: synchronous completion needs no duplicate notification).
    Falsification: no `job_id` (the megabyte was silently dropped), or
    `job_read_output` returns empty/truncated bytes (retention broken).
  - Turn-3 step-3 (fast quiet command): the result has NO `job_id`,
    `status` `"completed"`, `reason` `"exit_zero"` — ephemeral,
    exactly today's foreground behavior (spec §3 dominant case).
    Falsification: a `job_id` present for a quiet zero-exit command
    (spurious durability noise).
  - Step-4 `job_list`: the fast-quiet job (step 3) does NOT appear.
    The chatty job (step 1) MAY appear as `completed` if still within
    retention window — its presence is acceptable; its absence after
    the turn is acceptable. What is forbidden: the step-3 job_id in
    any listing.

## Cleanup

- `job_stop` the arm-(d) job (`sleep 27182` outlives the card); arms
  (c), (e), and (f) jobs are already terminal.
- Shut down the session (`POST /s/$SID/shutdown`); `rm -rf "$tmpdir"`.

## Sharp edges

- Arm (c)'s 5s-vs-25s timing assertion needs the tool round bracketed:
  use the api_call timestamps in the transcript JSONL if wall-clocking
  is too coarse. Model thinking time inflates everything by seconds;
  the 20s gap absorbs it.
- Arm (e) uses `exec sleep` so the runaway is a single recognizable
  process; without `exec` the `sh` wrapper dies but the sleep can be
  orphaned by a process-group miss — if the exact-args liveness check
  still finds it, that is a real signal-delivery finding (contract
  line 745: signal the process group where supported), not a card bug.
- Terminal notifications for arms (c)/(d)/(e) arrive as notification
  turns once armed (promotion arms the notification too, line 180);
  this card does not assert their format — see
  job-notification-semantics.md. Arm (f)'s kept chatty job must NOT
  produce a terminal notification (it completed synchronously before
  the tool returned; spec §3 no-notification rule for within-bound
  completions).
- If the model inlines extra tool calls between turn-2 steps 1 and 2,
  the clocks still work — each arm's timing is internal to its own
  tool round.
- Arm (f)'s chatty fixture (`yes ... | head -100000`) relies on `yes`
  being available and `head` buffering. If the shell environment lacks
  `yes`, substitute `dd if=/dev/zero bs=1 count=1900000 2>/dev/null |
  tr '\0' 'C'` — any large fast output works; the goal is to exceed
  the ~64KB inline embed budget within the 5s bound.
