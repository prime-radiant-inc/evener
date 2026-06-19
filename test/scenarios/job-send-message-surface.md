# delegate-send-surface: job handles are rejected, running delegates steer, and idle delegates start only when explicit

**What this covers**: the core `delegate_send` outcomes against the
handle-split API. (a) A concrete `job_id` is a turn handle and is
rejected with guidance to use the delegate's `delegate_id`; (b) a
RUNNING delegate receives a live steer through `delegate_send`, with
`action:"steered"` and no new job; (c) an IDLE delegate rejects
`delegate_send` by default with `target_idle`; (d)
`delegate_send(on_idle:"start")` starts the delegate's next job in the
same conversation and returns `started_job_id`/`current_job_id`.

## Pre-state

- Fresh binaries from the branch under test; hub on `127.0.0.1:9180`
  (`docs/agentic-testing.md` setup checklist); credentialed model.
- `tmpdir=$(mktemp -d -t serf-e2e-dsend-XXXXX)`.

## Steps

1. Spawn a session via `/api/spawn` with `working_dir=$tmpdir`.
   Capture `SID`.
2. Turn 1 — job-handle rejection and live steer:

   > Do these steps in order.
   > 1. Call delegate (background default) with this exact task:
   >    "FIRST run the shell command `sleep 15`. Then communicate
   >    exactly RUNNING_DONE. If you receive a steering message
   >    containing STEER_MARK_88, include STEER_MARK_88 in your final
   >    communicate." Capture the `delegate_id`, `job_id` (JA), and
   >    `transcript_ref`.
   > 2. Immediately call delegate_send with `to` set to JA and message
   >    "wrong handle probe". Report the error verbatim.
   > 3. Immediately call delegate_send with `to` set to the captured
   >    delegate_id and message "Mid-task instruction: include
   >    STEER_MARK_88." Report the full result JSON verbatim.
   > 4. Say STEER_SENT and end your turn. When JA's completion
   >    notification arrives, call job_read_output for JA and report
   >    the full JSON.
3. Turn 2 — idle default failure and explicit start:

   > Do these steps in order.
   > 1. Call delegate with max_wait_ms 120000 and this exact task:
   >    "Remember the codeword AZURE_FALCON for later turns of this
   >    conversation. Communicate exactly READY_TO_RESUME and finish."
   >    Capture the returned `delegate_id`, `job_id` (JB), and
   >    `transcript_ref`.
   > 2. Call delegate_send with `to` set to that delegate_id and
   >    message "Reply with the codeword." Do not set on_idle. Report
   >    the error verbatim.
   > 3. Call delegate_send with `to` set to that delegate_id,
   >    `on_idle` "start", `max_wait_ms` 120000, and message "Reply via
   >    communicate with exactly the codeword you were told earlier,
   >    and nothing else." Report the full result JSON verbatim.
   > 4. Call job_read_output for the returned `current_job_id` and
   >    report the full JSON.
4. Read the parent transcript, the child transcript (by JB's
   `transcript_ref` via `read_session_transcript` or on disk), and
   `find ~/.local/state/serf/projects -path "*sessions/$SID/jobs.jsonl"`.

## Expected

- Job-handle rejection: the step-2 error contains `job_id is a
  job/turn handle` and, when Serf can resolve it, the corresponding
  `delegate_id`. No new `job_started` appears for that rejected call.
- Live steer: the step-3 result has `action` `"steered"`,
  `current_job_id` or `latest_job_id` equal to JA, `status`
  `"running"`, and the captured `delegate_id`. The eventual JA read's
  `output` contains `STEER_MARK_88`, and the child transcript carries
  the instruction as a STEERING entry before the child's final
  communicate. Falsification: the token is absent from both result
  content and child transcript, or a new job is created for the live
  steer.
- Idle default: the turn-2 step-2 call fails synchronously with
  `target_idle` and creates no new job. This proves idle delegates do
  not restart by default.
- Explicit start: the turn-2 step-3 result has `action` `"started"`, a
  NEW `started_job_id`/`current_job_id` different from JB, the same
  `delegate_id`, and the same `transcript_ref` as JB. The result or
  follow-up `job_read_output` contains `AZURE_FALCON`, a fact present
  only in the prior turn's context — retention proven, not assumed.
  Falsification: the codeword is missing, the transcript ref changes,
  or the call starts from a blank conversation.

## Cleanup

- All delegate jobs should be terminal by the end of the card. If JA is
  still running after the bound, `job_stop` it. Shut down the session
  (`POST /s/$SID/shutdown`); `rm -rf "$tmpdir"`.

## Sharp edges

- The live steer must land while JA is still in its initial sleep; the
  parent step-3 call follows the delegate call immediately, so the
  expected path is stable under normal hub latency.
- A `max_wait_ms` on a live steer cannot wait for the next delegate
  reply; live steers return on delivery. Use the terminal notification
  and `job_read_output` to inspect the eventual result.
