# job-shell-lifecycle: foreground inline, background launch-and-return, max_runtime kill, complete-or-handle + output window

**What this covers**: the shell tool's whole job-capable lifecycle
(`docs/job-control.md`, "Existing shell/bash tool"). Shell's wait knob is
`background` (bool), not `max_wait_ms` — false (default) runs foreground and
returns when the command finishes (promoting to a durable job only if it
outlives the session command timeout); true starts it and returns a `job_id`
immediately.
(a) Foreground inline result with stdout+stderr+exit code, ephemeral — no
durable record; (b) nonzero exit reported honestly as a normal tool result, not
hidden (`failed` / `exit_nonzero`); (c) `background: true` launch-and-return —
`job_id` returned immediately, process keeps running, later output readable;
(d) `max_runtime_ms` kills a runaway and finalizes `stopped` / `run_timeout`;
(e) complete-or-handle + the output window (spec §0.6 + the
context-managed-output change): a fast chatty command whose output exceeds the
8 KiB ride-whole budget returns `completed` + `job_id` + a small peek tail +
`output_status:"windowed"` + `total_bytes`, and the full output is reachable via
`job_read_output`; a fast quiet command is ephemeral (`output_status:"all_retained"`).
Terminal-notification cardinality/format is job-notification-semantics.md.

## Pre-state

- Fresh binaries from the branch under test; hub on `127.0.0.1:9180`
  (`docs/agentic-testing.md` setup checklist); credentialed model.
- `tmpdir=$(mktemp -d -t serf-e2e-shlife-XXXXX)`.

## Steps

1. Spawn a session via `/api/spawn` with `working_dir=$tmpdir`. Capture `SID`.
2. Turn 1 — foreground arms (a), (b) and background arm (c):

   > Do these steps in order.
   > 1. Run the shell tool with command:
   >    `sh -c 'echo INLINE_OUT_OK; echo INLINE_ERR_OK >&2; exit 0'`
   >    (foreground, no background). Report the full result JSON verbatim.
   > 2. Run the shell tool with command:
   >    `sh -c 'echo FAIL_OUT_7; echo FAIL_ERR_7 >&2; exit 7'`.
   >    Report the full result JSON verbatim.
   > 3. Run the shell tool with background true and command:
   >    `sh -c 'echo BG_START_MARK; exec sleep 27182'`. Report the
   >    full result JSON verbatim.
   > 4. Call job_list with no filters and report the jobs array.
   > 5. End your turn.
3. Turn 2 — `max_runtime_ms` kill arm (d) (new user prompt):

   > Do these steps in order.
   > 1. Run the shell tool with background true, max_runtime_ms 5000, and
   >    command: `sh -c 'echo RUNAWAY_START_71; exec sleep 31415'`.
   >    Report the full result JSON verbatim.
   > 2. Run the foreground shell command `sleep 30` to let the job settle.
   > 3. Call job_read_output for the step-1 job_id and report the full JSON.
   > 4. End your turn.
4. Turn 3 — arm (e) complete-or-handle + output window (new user prompt):

   > Do these steps in order. Report every tool result verbatim.
   > 1. Run the shell tool with command:
   >    `yes COH_CHATTY_LINE | head -100000` (produces ~1.8MB of output,
   >    finishes in well under the session timeout). Report the full result JSON.
   > 2. If the step-1 result had a job_id, call job_read_output for it with
   >    head_lines 200 and report total_bytes, dropped_bytes, output_status,
   >    and whether the output starts with COH_CHATTY_LINE.
   > 3. Run the shell tool with command: `sh -c 'exit 0'` (fast quiet, no
   >    output). Report the full result JSON.
   > 4. Call job_list with no filters and report whether any job_id from
   >    steps 1 or 3 appears. End your turn.
5. Read the transcript
   (`find ~/.local/state/serf/projects -name "$SID.transcript.jsonl"`)
   and the durable log
   (`find ~/.local/state/serf/projects -path "*sessions/$SID/jobs.jsonl"`),
   plus a runaway-liveness check after turn 2 that cannot match the
   checking shell itself: `ps -eo args | grep -c '^sleep 31415$'`
   (expect 0). Do NOT use `pgrep -f` — the pattern matches the
   checker's own command line and reports a phantom orphan
   (group-kill semantics are pinned by
   `TestStreamCommandSignalKillsWholeProcessGroup`).

## Expected

- Arm (a): the step-1 result JSON has `status` `"completed"`,
  `reason` `"exit_zero"`, `exit_code` `0`, `running_in_background` `false`,
  `timed_out` `false`, NO `job_id`, `output_status` `"all_retained"`, and
  `output` containing BOTH `INLINE_OUT_OK` and `INLINE_ERR_OK`.
- Arm (b): step 2 is a normal tool result (not a tool error) with
  `status` `"failed"`, `reason` `"exit_nonzero"`, `exit_code` `7`, and
  `output` containing `FAIL_OUT_7` and `FAIL_ERR_7`. Falsification: the
  nonzero exit comes back as `completed`, the exit code is absent, or the
  call surfaces as a tool error.
- Ephemerality: the turn-1 step-4 `job_list` does NOT contain a job for
  steps 1 or 2 (inline foreground completions create no durable record),
  and `jobs.jsonl` has no `job_started` for them. It DOES contain the
  step-3 background job as `running`.
- Arm (c): step 3 returns immediately (not after `sleep 27182`) with a
  `job_id`, `status` `"running"`, `running_in_background` `true`, and NO
  `timed_out` / `reason:"foreground_timeout"` (background return is a clean
  launch-and-return, not a foreground-wait timeout). Falsification: no
  `job_id`, the result blocks until the sleep finishes, or it carries the
  `timed_out:true` foreground-timeout promotion shape.
- Arm (d): the step-1 result is the background launch shape (`job_id`,
  `running`, `running_in_background:true`). The `max_runtime_ms 5000` kills
  the command ~5s after it started; the step-3 read of that job shows
  `status` `"stopped"`, `reason` `"run_timeout"`. The runaway is actually
  dead: the exact-args liveness check counts zero `sleep 31415` processes.
  `jobs.jsonl` has one `job_finished` for it with `status:"stopped"`,
  `reason:"run_timeout"`. Falsification: the job is still `running` past
  ~20s, the process survives, or the kill is reported as
  `failed`/`cancelled` instead of `stopped`/`run_timeout` (a runtime-limit
  kill is neither command failure nor parent cancellation).
- Arm (e) complete-or-handle + output window:
  - Turn-3 step-1 (chatty command): the result contains a `job_id` (output
    exceeded the 8 KiB ride-whole budget), `status` `"completed"`,
    `truncated` `true`, `output_status` `"windowed"`, `total_bytes` ≫ the
    inline peek, and a small head+tail digest of the output inline. The
    step-2 `job_read_output` (head_lines 200) returns `output` beginning
    with `COH_CHATTY_LINE`, `dropped_bytes` `0` (≪ 8 MiB retained), and
    `output_status` `"windowed"` — proving the head is reachable and nothing
    was evicted. The kept job emits NO terminal notification (synchronous
    completion needs no duplicate notification). Falsification: no `job_id`
    (the megabyte was silently dropped); the full ~1.8MB rode inline (the
    window was not applied); `output_status` absent; or `job_read_output`
    cannot reach the head.
  - Turn-3 step-3 (fast quiet command): the result has NO `job_id`,
    `status` `"completed"`, `reason` `"exit_zero"`, `output_status`
    `"all_retained"` — ephemeral. Falsification: a `job_id` present for a quiet
    zero-exit command (spurious durability noise).
  - Step-4 `job_list`: the fast-quiet job (step 3) does NOT appear. The
    chatty job (step 1) MAY appear as `completed` if still within the
    retention window — presence or absence both acceptable. Forbidden: the
    step-3 job_id in any listing.
- `timed_out` discipline: `timed_out` means the foreground WAIT expired,
  never the `max_runtime_ms` kill. With `background:true` (arms c, d) the
  initial result has no foreground wait, so `timed_out` is `false`/absent.
  In the terminal `job_read_output` of arm (d) the record shows the
  finalized `stopped`/`run_timeout` state. Falsification: `timed_out`
  present and `true` in any background-launch result.

## Cleanup

- `job_stop` the arm-(c) job (`sleep 27182` outlives the card); arms (d)
  and (e) jobs are already terminal.
- Shut down the session (`POST /s/$SID/shutdown`); `rm -rf "$tmpdir"`.

## Sharp edges

- There is no per-call foreground wait knob anymore: a foreground shell
  command waits the session command timeout (120s in stock profiles) before
  promoting. This card does not exercise promotion-at-timeout (it would need
  a >120s command or a configured-short `DefaultCommandTimeoutMS`); that path
  is unit-tested. Launch-and-return is `background:true`.
- Arm (d) uses `exec sleep` so the runaway is a single recognizable process;
  without `exec` the `sh` wrapper dies but the sleep can be orphaned by a
  process-group miss — if the exact-args liveness check still finds it, that
  is a real signal-delivery finding (signal the process group where
  supported), not a card bug.
- Terminal notifications for arm (c)/(d) arrive as notification turns once
  armed; this card does not assert their format — see
  job-notification-semantics.md. Arm (e)'s kept chatty job must NOT produce a
  terminal notification (it completed synchronously before the tool returned).
- Arm (e)'s chatty fixture (`yes ... | head -100000`) relies on `yes` and
  `head` buffering. If the shell environment lacks `yes`, substitute
  `dd if=/dev/zero bs=1 count=1900000 2>/dev/null | tr '\0' 'C'` — any large
  fast output works; the goal is to exceed the 8 KiB ride-whole budget.
