# job-send-message-surface: live steer, resume with context, on_finished=fail, and the foreground-timeout resume

**What this covers**: the four `job_send_message` outcomes against
delegate targets (`docs/job-control.md` lines 347-446). (a) A RUNNING
delegate target gets the message injected mid-run — same `job_id`,
`action:"sent"`, and the guidance visibly incorporated (line 373);
(b) a FINISHED delegate resumes in the SAME conversation as a new job
— new `job_id`, `resumed_from_job_id`, same `transcript_ref`, with the
prior conversation's context demonstrably retained (line 374);
(c) `on_finished="fail"` against a finished target fails synchronously
with `target_terminal` and creates nothing (line 375); (d) a
`background=false` resume whose `block_timeout_ms` expires returns the
foreground-timeout shape with the job left running (line 382).
Resume-after-STOP is subagent-cancel-runaway.md; the observer
`caller`-alias send is job-watch-sidecar-observer.md.

## Pre-state

- Fresh binaries from the branch under test; hub on `127.0.0.1:9180`
  (`docs/agentic-testing.md` setup checklist); credentialed model.
- `tmpdir=$(mktemp -d -t serf-e2e-jsend-XXXXX)`.

## Steps

1. Spawn a session via `/api/spawn` with `working_dir=$tmpdir`.
   Capture `SID`.
2. Turn 1 — arm (a), steer a running delegate:

   > Do these steps in order.
   > 1. Call delegate (background default) with this exact task:
   >    "FIRST run the shell command `sleep 15`. Then write a six-line
   >    poem about rivers, ONE line at a time, running the shell
   >    command `sleep 8` between lines. Then communicate the full
   >    poem and finish." Capture the job_id (call it JA) and its
   >    transcript_ref.
   > 2. Immediately call job_send_message with target JA and this
   >    message: "Mid-task instruction: your final communicate must
   >    also contain the exact token STEER_MARK_88." Report the full
   >    result JSON verbatim.
   > 3. Say STEER_SENT and end your turn. When JA's completion
   >    notification arrives, call job_read_output for JA and report
   >    the full JSON.
3. Turn 2 — arm (b), resume a finished delegate (new user prompt):

   > Do these steps in order.
   > 1. Call delegate (background default) with this exact task:
   >    "Remember the codeword AZURE_FALCON for later turns of this
   >    conversation. Communicate exactly READY_TO_RESUME and finish."
   >    Capture the job_id (JB) and transcript_ref. End your turn and
   >    wait for its completion notification.
   > 2. (After the notification) Call job_send_message with target JB
   >    and this message: "Reply via communicate with exactly the
   >    codeword you were told earlier, and nothing else." Report the
   >    full result JSON verbatim, end your turn, and when the resumed
   >    job completes, call job_read_output for the NEW job_id and
   >    report the full JSON.
4. Turn 3 — arms (c) and (d) (new user prompt):

   > Do these steps in order. Step 1 is expected to return a tool
   > error — report it verbatim and continue.
   > 1. Call job_send_message with target JB, on_finished "fail", and
   >    message "live-only nudge". Report the result or error
   >    verbatim.
   > 2. Call job_list with no filters and report the count and
   >    job_ids.
   > 3. Call job_send_message with target JB, background false,
   >    block_timeout_ms 2000, and this message: "Run the shell
   >    command `sleep 20`, then communicate exactly SLOW_RESUME_DONE."
   >    Report the full result JSON verbatim.
   > 4. Call job_list with status ["running"] and report whether the
   >    new job from step 3 is running. End your turn.
5. Read the parent transcript, the child transcript (by JB's
   transcript_ref via `read_session_transcript` or on disk), and
   `find ~/.local/state/serf/projects -path "*sessions/$SID/jobs.jsonl"`.

## Expected

- Arm (a): the send-to-a-running-delegate race has TWO legal outcomes;
  the invariant is DELIVERY, not which path carried it.
  - Live steer (the outcome this card biases toward via the initial
    sleep): `action` `"sent"`, `job_id` == JA (NO new job), `status`
    `"running"`; the eventual JA read's `content` contains
    `STEER_MARK_88`, and the child transcript carries the instruction
    as a STEERING entry BEFORE the child's final communicate.
  - Legal race outcome (delegate finished first — the contract's
    default `on_finished="resume"`): `action` `"resumed"` with a NEW
    `job_id`, `resumed_from_job_id` == JA, same `transcript_ref`; the
    resumed job's read contains `STEER_MARK_88`. Record which outcome
    occurred in the result block.
  - Falsification (either path): the token absent from both the result
    content and the child transcript (message dropped), or a tool
    error.
- Arm (b): the resume result has `action` `"resumed"`, a NEW `job_id`
  != JB, `resumed_from_job_id` == JB, `running_in_background` `true`
  (default), and `transcript_ref` EQUAL to JB's — same conversation,
  new job (vocabulary table, line 74). The resumed job's read contains
  `AZURE_FALCON`, a fact present only in the prior turn's context —
  retention proven, not assumed. `jobs.jsonl` records the resumed job
  as a fresh `job_started` (same store, new job_id). Falsification:
  the codeword missing or the resumed job answering from a blank
  context ("what codeword?"), or a different transcript_ref (a NEW
  conversation was started — `delegate` semantics leaked into
  follow-up).
- Arm (c): step 1 fails synchronously with an error containing
  `target_terminal` (line 375); the step-2 listing shows NO job
  created by it (count unchanged from the end of turn 2, no new
  `job_started` in `jobs.jsonl` between the error and step 3).
  Falsification: a resumed job exists despite `on_finished="fail"` —
  the live-only guard raced into a resume.
- Arm (d): the step-3 result returns at ~2s with `action` `"resumed"`,
  a new `job_id`, `status` `"running"`, `reason`
  `"foreground_timeout"`, `timed_out` `true`,
  `running_in_background` `true`, and bounded `output`-so-far — the
  foreground-timeout result shape (line 382 + the delegate timeout
  shape, lines 329-343). The step-4 listing confirms the job still
  `running` (timeout never stops work). Its later completion delivers
  the normal terminal notification carrying `SLOW_RESUME_DONE` output.
  Falsification: the call blocks ~20s for completion (timeout
  ignored), or the job is terminal/cancelled right after the timeout.

## Cleanup

- Wait for the arm-(d) job to finish (~25s) or `job_stop` it; all
  other jobs are terminal. Shut down the session
  (`POST /s/$SID/shutdown`); `rm -rf "$tmpdir"`.

## Sharp edges

- Arm (a)'s steer must land while JA is still mid-poem — the paced
  sleeps give a ~48s window, and the parent's step-2 call follows the
  delegate return within one tool round. If JA finished first, the
  call resumes instead of steering (the documented race, line 380:
  state observed at delivery wins); rerun rather than reinterpret an
  `action:"resumed"` result as arm (a).
- Arms (b)-(d) hit the same delegate session sequentially; each step
  waits for the prior resume to be terminal or
  `delegate_session_busy` fires (line 379). The waits in the prompts
  are load-bearing.
- Arm (c) targets JB even though a NEWER terminal job now exists in
  that session; on_finished=fail checks the TARGET's terminal state,
  so the error is stable regardless of which job in the session is
  newest.
- `block_timeout_ms` 2000 is above the 1000 minimum (the normative
  bounds at lines 187-192 govern the resumed-delegate foreground wait
  via line 382); values below 1000 clamp up and would still beat the
  20s sleep.
- The arm-(d) timing assertion (~2s vs ~20s) brackets via the
  api_call timestamps around the tool round when wall-clocking is
  coarse.
