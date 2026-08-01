# job-stop-and-children: stop confirms and retains output; include_children fells the visible tree

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
(c) `job_stop(delegate, include_children=true)` also stops the
delegate's visible nested shell job ("`job_stop`" "cascades into the
subtree"; "Nested jobs" "each cascade-stopped job finalizes as
`cancelled/stopped_by_parent`"), with both
terminal afterward and the child visible via
`job_list(include_nested=true)`. The plain stop-a-delegate +
resume-after-stop path is already covered by
subagent-cancel-runaway.md; post-terminal readability of nested output
is asserted in depth by job-nested-visibility.md.

## Pre-state

- Fresh binaries from the branch under test; an isolated hub
  (`docs/agentic-testing.md` setup checklist); credentialed model.
- `tmpdir=$(mktemp -d -t serf-e2e-jstop-XXXXX)`.

## Steps

1. Spawn a session via `/api/spawn` with `working_dir=$tmpdir`.
   Capture `SID`.
2. Turn 1 — arms (a) and (b), shell stop:

   > Do these steps in order.
   > 1. Run the shell tool with background true and command:
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
3. Turn 2 — arm (c), delegate with a nested job (new user prompt):

   > Do these steps in order.
   > 1. Call delegate with this exact task: "Run the shell tool with
   >    background true and this command:
   >    `sh -c 'echo CHILD_NEST_TOKEN; sleep 300'` with description
   >    nested-probe. Report its job_id. Then run the foreground shell
   >    command `sleep 240` and finally communicate DONE." Capture the
   >    delegate job_id.
   > 2. Run the foreground shell command `sleep 20` so the delegate
   >    has started its background job.
   > 3. Call job_list with include_nested true and report every job's
   >    job_id, type, status, and parent_job_id.
   > 4. Call job_stop with the DELEGATE job_id, include_children true,
   >    and max_wait_ms 5000. Report the full result JSON verbatim.
   > 5. Call job_list with include_nested true again and report the
   >    same fields.
   > 6. End your turn.
4. Read the transcript and the parent durable log
   (`find ~/.local/state/serf/projects -path "*sessions/$SID/jobs.jsonl"`).

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

Turn 2:

- Step 3 shows BOTH jobs live before the stop: the delegate `running`,
  and a shell job `running` whose `parent_job_id` equals the delegate
  job_id (the gate for the rest of the arm — if the nested job is not
  visible yet, wait and re-list once rather than stopping early).
- The step-4 result reports the delegate terminal: `status`
  `"cancelled"` with reason `"stopped_by_parent"` or
  `"stopped_with_children"` (both contract-legal — "`job_stop`" names
  `stopped_by_parent` for a confirmed stop and "Job status and reason
  model" allows "additional diagnostic text or implementation-specific
  reason values"; the shipped implementation emits `stopped_by_parent`
  — record what you see).
- Step 5 shows BOTH terminal: the delegate `cancelled`, and the nested
  shell job terminal as `cancelled`/`stopped_by_parent` (an explicit
  stop was sent to it — "Nested jobs" "each cascade-stopped job
  finalizes as `cancelled/stopped_by_parent`") — still listed, still carrying
  `parent_job_id`. Falsification (recursion hole): the nested job
  still `running` after an `include_children=true` stop. Accept
  `stopped`/`runtime_lost`-or-`supervision_lost` for the child ONLY if
  the delegate cancellation tore down the owner runtime before the
  child stop confirmed ("`job_stop`" "If no live handle remains and
  cancellation cannot be confirmed, terminal status is `stopped` with
  reason `stop_unconfirmed` or `runtime_lost`", and "Job status and
  reason model" defines `supervision_lost` for an owner runtime that
  ended mid-supervision); a child
  left silently running is the failure, a child finalized by
  supervision loss is a recorded variant.
- The parent's `jobs.jsonl` contains `job_finished` events for both
  the delegate and the nested job (the nested one forwarded with
  `owner_session_id` = the child session).

## Cleanup

- All jobs are terminal by design. Shut down the session
  (`POST /s/$SID/shutdown`); `rm -rf "$tmpdir"`.

## Sharp edges

- Turn 2's step-2 pacing sleep is what makes the nested job exist
  before the stop; a fast model could otherwise stop a delegate that
  has not yet started its background job, making the child-stop arm
  vacuous. The step-3 listing is the explicit gate — re-prompt if the
  nested job is absent.
- `include_children` is the SHELL-job flag: "`job_stop`" makes a shell
  stop non-recursive by default and takes `include_children=true` to
  fell its visible active nested jobs. A DELEGATE stop needs no flag —
  it "cascades into the subtree" regardless (`agent/jobs_nested.go`,
  `stopDelegateSubtree`). This card passes `include_children=true` on
  a delegate stop, which is legal and redundant: the cascade is what
  fells the nested job. A nested job left running after a delegate
  stop is a bug in either variant.
- The delegate's own 240s foreground sleep keeps the child session
  busy so the delegate is still `running` at stop time. If the
  delegate finished early (model skipped the sleep), `job_stop`
  returns the actual terminal status ("`job_stop`" "If the job already
  completed before stop lands, return the actual terminal status") and
  the arm degrades — rerun with the prompt tightened.
- Terminal notifications are OWNER-SCOPED (spec §3/§10): the PARENT
  is notified about its OWN delegate finishing (one block, on the
  parent's rail), but the nested shell job's terminal is owner-scoped
  to the CHILD and does NOT land on the parent's rail — the parent
  retains visibility via `job_list(include_nested=true)` /
  `jobs.jsonl`, not a notification. Do not assert a parent-rail block
  for the nested job. Cardinality/format assertions for the parent's
  own-job notifications live in job-notification-semantics.md.
