# job-stop-and-children: shell stop confirms and retains output

**What this covers**: `job_stop` semantics from `docs/job-control.md`
"`job_stop`" and the nested-stop rules in "Nested jobs". (a) A
confirmed stop of a running shell job lands `cancelled` /
`stopped_by_parent` ("If stop is confirmed, terminal status is
`cancelled` with reason `stopped_by_parent`") with retained output
still readable afterward ("Stopping does not delete output,
transcript, or durable job records"); (b) `max_wait_ms` on
job_stop makes the stop call itself wait for finalization, so the
result carries the terminal status instead of `running`/`stop_pending`
("the tool performs one bounded wait of up to that many ms for the
stop to finalize"; "If still running after timeout, status remains
`running` with reason `stop_pending`");
The stable delegate stop/resume path is covered by
subagent-cancel-runaway.md; post-terminal readability of nested shell
output is asserted in depth by job-nested-visibility.md.

## Pre-state

- Fresh binaries from the branch under test; an isolated hub
  (`docs/developing-evener/agentic-testing.md` setup checklist); credentialed model.
- `tmpdir=$(mktemp -d -t evener-e2e-jstop-XXXXX)`.

## Steps

1. Spawn a session via `/api/spawn` with `working_dir=$tmpdir`.
   Capture `SID`.
2. Turn 1 — arms (a) and (b), shell stop:

   > Do these steps in order.
   > 1. Run the shell tool with mode: "background" and command:
   >    `sh -c 'echo STOP_RETAIN_TOKEN; sleep 300'`. Capture the
   >    job_id.
   > 2. Run the foreground shell command `sleep 3` so the token has
   >    printed.
   > 3. Call job_stop with that job_id and max_wait_ms 5000. Report the
   >    full result JSON verbatim.
   > 4. Call read_transcript with transcript_ref "job:<the same job_id>"
   >    and report the full JSON verbatim. Then call job_status for that
   >    job_id and report the full JSON verbatim.
   > 5. End your turn.
3. Read the transcript and the parent durable log
   (`find ~/.local/state/evener/projects -path "*sessions/$SID/jobs.jsonl"`).

## Expected

Turn 1:

- The step-3 `job_stop` result is TERMINAL in the result itself —
  `status` `"cancelled"`, `reason` `"stopped_by_parent"` — because
  `max_wait_ms 5000` waited for finalization. Falsification (wait
  regression): the result says `running` with reason `stop_pending`
  although the job is a plain interruptible sleep that confirms
  cancellation in well under the 5s bound.
- Output survives the stop: the step-4 `job:` read returns
  `- status: cancelled` with a fenced block containing
  `STOP_RETAIN_TOKEN`, and the `job_status` call reports
  `"status":"cancelled"` / `"reason":"stopped_by_parent"`.
  Falsification: `job ... not found`, an empty block, or a read error —
  stop deleted or hid history (violates "`job_stop`" "Stopping does
  not delete output, transcript, or durable job records").
- `jobs.jsonl` has exactly one `job_finished` for the job with
  `status:"cancelled"`, `reason:"stopped_by_parent"`.

## Cleanup

- All jobs are terminal by design. Shut down the session
  (`POST /s/$SID/shutdown`); `rm -rf "$tmpdir"`.

## Sharp edges

- Terminal notifications are owner-scoped: a direct delegate completion
  arrives as `<delegate-notification delegate_id="dlg_...">`, while a
  shell job's terminal arrives as `<job-notification job_id="job_...">`.
  Cardinality and shell-frame assertions live in
  job-notification-semantics.md.
