# job-stop-and-children: stop confirms and retains output; include_children fells the visible tree

**What this covers**: `job_stop` semantics from `docs/job-control.md`
lines 730-802 and the nested-stop rules at lines 1029-1054. (a) A
confirmed stop of a running shell job lands `cancelled` /
`stopped_by_parent` (line 753) with retained output still readable
afterward (line 750: stopping deletes nothing); (b) `max_wait_ms` on
job_stop makes the stop call itself wait for finalization, so the
result carries the terminal status instead of `running`/`stop_pending`
(lines 744, 755);
(c) `job_stop(delegate, include_children=true)` also stops the
delegate's visible nested shell job (lines 756, 778, 1038), with both
terminal afterward and the child visible via
`job_list(include_nested=true)`. The plain stop-a-delegate +
resume-after-stop path is already covered by
subagent-cancel-runaway.md; post-terminal readability of nested output
is asserted in depth by job-nested-visibility.md.

## Pre-state

- Fresh binaries from the branch under test; hub on `127.0.0.1:9180`
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
   > 4. Call job_read_output for the same job_id. Report the full JSON
   >    verbatim.
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
- Output survives the stop: the step-4 read returns `status`
  `"cancelled"`, `reason` `"stopped_by_parent"`, and `content`
  containing `STOP_RETAIN_TOKEN`. Falsification: `job ... not found`,
  empty content, or `output_unavailable` — stop deleted or hid history
  (violates lines 750-751).
- `jobs.jsonl` has exactly one `job_finished` for the job with
  `status:"cancelled"`, `reason:"stopped_by_parent"`.

Turn 2:

- Step 3 shows BOTH jobs live before the stop: the delegate `running`,
  and a shell job `running` whose `parent_job_id` equals the delegate
  job_id (the gate for the rest of the arm — if the nested job is not
  visible yet, wait and re-list once rather than stopping early).
- The step-4 result reports the delegate terminal: `status`
  `"cancelled"` with reason `"stopped_by_parent"` or
  `"stopped_with_children"` (both contract-legal, line 753; the
  shipped implementation emits `stopped_by_parent` — record what you
  see).
- Step 5 shows BOTH terminal: the delegate `cancelled`, and the nested
  shell job terminal as `cancelled`/`stopped_by_parent` (an explicit
  stop was sent to it, line 778) — still listed, still carrying
  `parent_job_id`. Falsification (recursion hole): the nested job
  still `running` after an `include_children=true` stop. Accept
  `stopped`/`runtime_lost`-or-`supervision_lost` for the child ONLY if
  the delegate cancellation tore down the owner runtime before the
  child stop confirmed (line 757 allows that finalization); a child
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
- `job_stop` is non-recursive by DEFAULT (line 756): without
  `include_children`, the nested shell job may keep running after a
  delegate stop as long as the owner runtime survives (line 767).
  This card deliberately exercises only the recursive arm; do not
  reinterpret a surviving child in a default-stop variant as a bug.
- The delegate's own 240s foreground sleep keeps the child session
  busy so the delegate is still `running` at stop time. If the
  delegate finished early (model skipped the sleep), `job_stop`
  returns the actual terminal status (line 752) and the arm
  degrades — rerun with the prompt tightened.
- Terminal notifications are OWNER-SCOPED (spec §3/§10): the PARENT
  is notified about its OWN delegate finishing (one block, on the
  parent's rail), but the nested shell job's terminal is owner-scoped
  to the CHILD and does NOT land on the parent's rail — the parent
  retains visibility via `job_list(include_nested=true)` /
  `jobs.jsonl`, not a notification. Do not assert a parent-rail block
  for the nested job. Cardinality/format assertions for the parent's
  own-job notifications live in job-notification-semantics.md.
