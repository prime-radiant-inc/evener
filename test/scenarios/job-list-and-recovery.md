# job-list-and-recovery: filters, newest-first order, the short-job race, and list-as-reorientation

**What this covers**: `job_list` as authoritative durable inventory
(`docs/job-control.md` lines 654-726). (a) `status[]` and `type[]`
filters select exactly the matching records; (b) results are
newest-first — `started_at` descending, tie-broken by `job_id`
(line 674); (c) the short-job race: a job that completes before any
running-filtered list observes it is still visible without the status
filter (the `job_list` description's own warning, ergonomics §3);
(d) the re-orientation flow — before any terminal notification has
been delivered, a mid-turn `job_list` already shows the durable truth
(line 675: authoritative inventory; durable overlay of in-memory
running state, line 922); (e) post-F2, the result also reports active
watches. Delegate enumeration + double-read non-consumption is
already covered by subagent-list-and-output.md.

## Pre-state

- Fresh binaries from the branch under test; hub on `127.0.0.1:9180`
  (`docs/agentic-testing.md` setup checklist); credentialed model.
- `tmpdir=$(mktemp -d -t serf-e2e-jlist-XXXXX)`.

## Steps

1. Spawn a session via `/api/spawn` with `working_dir=$tmpdir`.
   Capture `SID`.
2. Turn 1 — build a mixed inventory, then filter (one turn, ordered):

   > Do these steps in order. Report every tool result verbatim.
   > 1. Run the shell tool with background true and command:
   >    `sh -c 'sleep 2; exit 7'`. Capture the job_id (call it J1).
   > 2. Run the shell tool with background true and command:
   >    `sh -c 'sleep 2; true'`. Capture the job_id (J2). Call
   >    job_list with status ["running"] and report whether J2 appears.
   >    Then call job_list with no filters and report whether J2 appears
   >    and with what status.
   > 3. Run the shell tool with background true and command:
   >    `sh -c 'echo LIST_RUN_TOKEN; sleep 300'`. Capture the job_id
   >    (J3).
   > 4. Call delegate with this exact task: "Communicate exactly
   >    DLG_LIST_DONE and finish." Capture the job_id (J4).
   > 5. Call job_list with type ["shell"] and report the job_ids.
   > 6. Call job_list with status ["running"] and report the job_ids.
   > 7. Call job_list with status ["failed", "completed"] and report
   >    the job_ids and statuses.
   > 8. Call job_list with no filters and report the job_ids IN ORDER.
   > 9. Call job_stop with J3 and max_wait_ms 5000. Then call job_list
   >    with status ["cancelled"] and report the job_ids.
   > 10. End your turn.
3. Turn 2 — re-orientation before any notification (new user prompt):

   > Do these steps in order, with no other tool calls.
   > 1. Run the shell tool with background true and command:
   >    `sh -c 'sleep 6; echo REORIENT_DONE'`. Capture the job_id (J5).
   > 2. Run the foreground shell command `sleep 15`.
   > 3. Call job_list with no filters and report J5's status verbatim.
   > 4. Say REORIENTED and end your turn.
4. Read the transcript
   (`find ~/.local/state/serf/projects -name "$SID.transcript.jsonl"`)
   and check entry ordering around turn 2.

## Expected

Turn 1:

- Filters are exact set selections:
  - step 5 (`type=["shell"]`): J1, J2, J3 — and NOT J4. J1 and J2 are
    backgrounded (`background true`) over a `sleep 2`, so they are
    durable running records at step-1/2 call time; by step 5 (~2s
    later) they are terminal (spec §3 complete-or-handle: background
    jobs finalize normally).
  - step 6 (`status=["running"]`): J3 (plus J4 only if the delegate
    is still mid-run at that moment); J1 and J2 may be terminal by
    this point — the 2s sleep vs ~1s between steps makes this timing-
    soft; accept either running or terminal for J1/J2 here.
  - step 7 (`status=["failed","completed"]`): J1 with `status`
    `"failed"` (reason `exit_nonzero`, `exit_code` 7) and J2 with
    `"completed"` — multi-value filters OR together.
  - step 9: after the confirmed stop, `status=["cancelled"]` returns
    exactly J3.
- Ordering: the step-8 unfiltered listing returns the jobs
  newest-first by `started_at` — J4, J3, J2, J1 (creation order was
  J1→J4). Falsification: ascending or insertion order.
- Durable-record presence (replaces the old short-job race arm c):
  in step 2, both the running-filtered and unfiltered lists contain J2
  (`status` `"running"` — the `sleep 2` command outlives the 1s bound,
  so J2 is reliably promoted before the listing). The original short-job
  race (a fast job that completes before any running-filtered list) is
  inexpressible with a bound-outliving fixture; the assertion that
  matters is that J2 appears in BOTH lists as a durable running record
  (spec §3: promoted jobs are kept, complete-or-handle invariant).
  Falsification: J2 absent from either list, or present with a phantom
  status outside the canonical five.
- Every entry carries the documented row fields: `job_id`, `type`,
  `status`, `reason`, `started_at`, `output_bytes`; J4's row also has
  `transcript_ref` and `resumable` (line 678).

Turn 2:

- The step-3 listing reports J5 `"completed"` — INSIDE the same turn,
  BEFORE any terminal notification was delivered (notifications queue
  for the turn boundary, line 961). This is the re-orientation
  promise: when no notification has arrived yet, one `job_list` shows
  the durable truth.
- Transcript ordering proof: the TOOL_RESULTS entry for step 3
  (containing J5 as completed) appears BEFORE the first STEERING entry
  containing `<job-notification` for J5. The J5 notification then
  arrives at the boundary after the turn — exactly once.
- Falsification (stale inventory): step 3 reports J5 still `running`
  ~9s after its process exited — `job_list` is reconstructing from a
  stale snapshot instead of durable state + live overlay.
- Active watches (e): a `watches` array is present in the `job_list`
  result, empty in this card's runs (no watch installed).
  <!-- pin: ergonomics §4 F2 — the watches array
       ({target, condition, send_to, deliveries, created_at}) lands
       with Phase 2. On pre-F2 builds the field is absent; once it
       ships, extend turn 1 with a job_watch install and assert the
       row appears here and disappears after clear=true. -->
- Paging surface: the result reports `count` and the jobs array;
  there is no cursor paging.
  <!-- pin: ergonomics §2 P1 deletes job_list.cursor/next_cursor in
       Phase 1.9. Pre-1.9 the result carries next_cursor: null —
       accept either; flag a NON-null next_cursor (paging implemented
       without contract update). -->

## Cleanup

- All jobs are terminal after step 9 / turn 2 (J3 stopped, the rest
  completed or failed). Shut down the session; `rm -rf "$tmpdir"`.

## Sharp edges

- Step 2's J2 fixture (`sleep 2; true`, `background true`) returns a
  running record immediately and stays running at list time (~2s window).
  The running-filter SHOULD show J2; a miss means promotion didn't
  create a durable record (a regression). The original short-job race
  (spec §3 arc) is subsumed — the normative assertion is now durable
  presence in BOTH filtered and unfiltered listings.
- The turn-1 step-6 assertion about J4 is deliberately soft — the
  delegate's lifetime depends on model latency. Branch the assertion
  on J4's status as reported in the same listing.
- Turn 2's foreground `sleep 15` is the no-notification window: J5
  finishes at ~6s while the turn is busy, and nothing can be delivered
  until the boundary. If the model skips the sleep and ends the turn
  early, the notification may beat the listing — rerun rather than
  reinterpret.
- One listing per question, by design: this card's repeated job_list
  calls each answer a DIFFERENT filter question. Do not present this
  as a sanctioned poll-for-completion loop (anti-pattern, line 693).
