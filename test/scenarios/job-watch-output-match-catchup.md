# job-watch-output-match-catchup: output_match is level-triggered at attach; terminal targets get one-shot catch-up

**What this covers**: watch-mailbox spec §7.1, all arms. (a) A watch
attached AFTER a running job already printed the token fires anyway —
exactly once for the whole retained scan, carrying the LAST matching
line — then fires again per genuinely new match. (b) An
`output_match`-only watch on a TERMINAL job becomes a one-shot
catch-up returning fired:true/false instead of an error. (c) A
terminal target with `events` still fails `target_terminal`.
Pre-design, (a) silently never fired (the matcher only saw post-attach
appends) and (b) errored — the `job_watch` description had to warn
about a race the model could not avoid; these assertions are that
warning's retirement. Contract rows: spec §8 for
`docs/job-control.md` lines 506/534/542/546. Executed by plan
Phase 5.2.

## Pre-state

- Fresh binaries from the branch under test; hub on `127.0.0.1:9180`
  (`docs/agentic-testing.md`); credentialed model.
- `tmpdir=$(mktemp -d -t serf-e2e-catchup-XXXXX)`.

## Steps

1. Spawn a session via `/api/spawn` with `working_dir=$tmpdir`.
   Capture `SID`.
2. Turn 1 — arm (a), attach after the tokens already printed:

   > Do these steps in order.
   > 1. Run the shell tool with max_wait_ms 1000 and command:
   >    `sh -c 'echo LEVEL_TOKEN_A; echo LEVEL_TOKEN_B; sleep 30; echo LEVEL_TOKEN_C; sleep 240'`.
   >    Capture the job_id.
   > 2. Call job_read_output for that job_id once (no block) and
   >    confirm the output already contains LEVEL_TOKEN_A and
   >    LEVEL_TOKEN_B. Report what you saw.
   > 3. Call job_watch with target that job_id and output_match
   >    "LEVEL_TOKEN_[ABC]" (no send). Report the full JSON.
   > 4. Say WATCH_ATTACHED and end your turn. Do not poll.
3. Watch the transcript. Two separate watch notifications should
   arrive: the attach-scan fire right after turn 1 ends, then the
   LEVEL_TOKEN_C fire ~30s after the job started (an idle wake, no
   user input).
4. Turn 2 — arms (b) and (c), against a terminal job (send as a new
   user prompt once the arm-(a) notifications have been observed):

   > Do these steps in order. Steps 3 and 4 may return tool errors —
   > report everything verbatim.
   > 1. Run the shell tool with max_wait_ms 5000 and command:
   >    `sh -c 'echo CATCHUP_TOKEN_OK'`. Capture the job_id, then call
   >    job_read_output for it and confirm status is completed.
   > 2. Call job_watch with target that job_id and output_match
   >    "CATCHUP_TOKEN_OK". Report the full JSON verbatim.
   > 3. Call job_watch with target that job_id and output_match
   >    "CATCHUP_TOKEN_MISSING". Report the full JSON verbatim.
   > 4. Call job_watch with target that job_id and events
   >    ["job.notification"]. Report the result or error verbatim.
   > 5. End your turn.

## Expected

- Turn 1 step 3 returns `watching: true` — a live watch installs on
  the running target; the attach scan does not change the create
  return shape.
  <!-- pin: spec §7.1 specifies result fields only for the terminal
       arm; if the shipped create result also surfaces the attach-scan
       fire, record it. -->
- Attach-scan fire (the level trigger): shortly after turn 1 ends, the
  transcript gains a notification turn — a STEERING entry with a
  `<job-notification` block, `event="watch"` and `job_type="watch"`
  attributes, the concrete job_id, and a reason/trigger referencing
  `output_match:` with `LEVEL_TOKEN_B` — the LAST matching retained
  line. EXACTLY ONE such fire despite two matching retained lines.
  <!-- pin: spec §7.1 — attach scan is a single level check carrying
       the last matching line, not a per-line replay. -->
- Falsification (the old race): no watch notification referencing
  LEVEL_TOKEN_A or LEVEL_TOKEN_B ever arrives — only, possibly, the
  later C fire. The watch was edge-triggered and silently missed
  retained output.
- Falsification (replay abuse): two attach-scan notifications (one per
  retained line), or the attach fire referencing LEVEL_TOKEN_A (first
  line, not last).
- Stream behavior still live after the scan: ~30s after the job
  started a SECOND watch notification arrives referencing
  LEVEL_TOKEN_C, waking the idle session without user input.
- Arm (b) positive: turn 2 step 2 is a one-shot catch-up, NOT an
  error: `watching: false`, `fired: true`, `terminal_catchup: true`,
  and a caller notification referencing the CATCHUP_TOKEN_OK match
  arrives through the normal rail at the next boundary.
  <!-- pin: spec §7.1 — terminal-arm result field names
       (watching/fired/terminal_catchup/status) land in the
       level-trigger phase; re-verify the shipped names. -->
- Arm (b) negative: step 3 returns `watching: false`, `fired: false`,
  `terminal_catchup: true`, plus the terminal `status` ("completed") —
  and NO notification referencing CATCHUP_TOKEN_MISSING ever appears.
- Arm (c): step 4 fails with a `target_terminal` error — event watches
  on a terminal job can never fire. Falsification: it installs a watch
  (`watching: true`) or fails with a different class such as
  `target_not_found`.
- Cross-arm falsification: if turn 2 step 2 or 3 errors
  `target_terminal`/`target_not_found`, terminal catch-up is not
  implemented (pre-design behavior).

## Cleanup

- `job_stop` the turn-1 job (240s tail).
- Shut down the session; `rm -rf "$tmpdir"`.

## Sharp edges

- Turn 1's attach must complete before the +30s LEVEL_TOKEN_C print or
  the attach scan absorbs C and the two fires blur. A three-tool-call
  turn fits comfortably in 30s; if the model dawdled (check api_call
  timestamps), rerun with a longer pre-C sleep rather than weakening
  the cardinality assertion.
- Fires that happen mid-turn queue and deliver as a notification turn
  right after the input ends — the attach-scan notification appears
  after turn 1's final assistant message, not inside the turn.
- The fired:false arm asserts a negative; bound it by the positive:
  once the fired:true notification has arrived, the transcript must
  contain exactly one catch-up watch notification for that job and
  none mentioning CATCHUP_TOKEN_MISSING.
- The catch-up SEND variant (terminal job + output_match + send.to)
  settles through a detached terminal-flush config per spec §7.1 and
  is not covered here — it deserves its own card if live coverage is
  wanted beyond the unit suite.
- Sequential one-shots share the (session, target, send.to) key but
  install nothing: `replaced_existing` should not be true on the
  second catch-up call. If it is, the one-shot leaked into the watch
  table — record what ships.
