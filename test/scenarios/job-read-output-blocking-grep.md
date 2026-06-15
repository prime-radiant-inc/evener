# job-read-output-blocking-grep: max_wait_ms + grep waits for the match, not for just any output

**What this covers**: watch-mailbox spec §7.2. `job_read_output(max_wait_ms,
grep=...)` changes from "wait for any new output, then grep" to "wait
until the retained output contains a grep match, the job goes
terminal, or max_wait_ms elapses" — with a mandatory entry check
(the match may already exist before the first wait) and a
normal-snapshot timeout. This is the one-call "wait for the server to
print ready" primitive that should keep most monitoring away from
`job_watch`. Contract anchor: `docs/job-control.md` `job_read_output`
rules. Executed by plan Phase 5.2.

## Pre-state

- Fresh binaries from the branch under test; hub on `127.0.0.1:9180`
  (`docs/agentic-testing.md`); credentialed model.
- `tmpdir=$(mktemp -d -t serf-e2e-bgrep-XXXXX)`.

## Steps

1. Spawn a session via `/api/spawn` with `working_dir=$tmpdir`.
   Capture `SID`.
2. Turn 1 — mid-stream match, with noise lines as bait for the old
   semantics:

   > Do these steps in order, with no other tool calls in between.
   > 1. Run the shell tool with background true and command:
   >    `sh -c 'sleep 3; echo boot_noise_alpha; sleep 7; echo boot_noise_beta; sleep 10; echo GREP_READY_TOKEN_9; sleep 300'`.
   >    Capture the job_id.
   > 2. Immediately call job_read_output with: that job_id,
   >    max_wait_ms 60000, grep "GREP_READY_TOKEN_9". Report the
   >    full JSON verbatim.
   > 3. End your turn.
3. Clock the step-2 tool round: wall clock externally, or afterwards
   via the gap between the consecutive api_call timestamps that
   bracket it in the transcript JSONL. The token prints ~20s after the
   job starts.
4. Turn 2 — entry check (new user prompt):

   > Call job_read_output once with: the same job_id, max_wait_ms 30000,
   > grep "GREP_READY_TOKEN_9". Report the full JSON verbatim.
5. Turn 3 — timeout arm (new user prompt):

   > Call job_read_output once with: the same job_id, max_wait_ms 5000,
   > grep "NO_SUCH_TOKEN_XYZ". Report the full JSON verbatim. Then call
   > job_list filtered to running jobs and report whether the job is
   > still running. Do not repeat the blocking call.

## Expected

- Turn 1: the blocking read returns WITH the match — `matches`
  non-empty, a match line containing `GREP_READY_TOKEN_9`, and
  `status` `"running"`. Status running is the structural proof it
  returned before job end (the 300s tail), independent of clocks; the
  measured round is ~20s from job start, well under the 60s bound.
  <!-- pin: contract job_read_output return shape — matches[] entries
       carry line + byte_offset today; re-verify entry field names. -->
- Falsification (old semantics): the result has empty `matches` while
  the job is still running — the wait returned on the FIRST new output
  (`boot_noise_alpha` at ~+3s) instead of waiting for the match. The
  two noise lines exist precisely to bait this.
- Falsification (no early return): the round consumes ~the full 60s
  and returns only at timeout although the token printed at ~+20s.
- Turn 2 (entry check): returns promptly — well under the 30s bound
  (assert the tool round took <10s; generous margin over model
  latency) — with `matches` again non-empty. Falsification: the round
  runs ~30s to the timeout, which means the §7.2 entry check ("grep
  retained output before the first wait") is missing. Note the timeout
  snapshot would ALSO contain the match (grep runs over retained
  output at return time), so TIME is the discriminator here, not match
  presence.
- Turn 3 (timeout): returns at ~5s with the normal snapshot — `status`
  `"running"`, no match entries for NO_SUCH_TOKEN_XYZ, and NOT a tool
  error. The follow-up job_list confirms the job still running: a
  blocking-read timeout never stops the job. Falsification: a tool
  error, a job that is no longer running, or a round that blocks far
  past the 5s bound.
- Reads are non-consuming: turn 2 sees the same retained output turn 1
  saw; nothing about turns 2-3 is affected by earlier reads.

## Cleanup

- `job_stop` the job (its 300s tail outlives the card).
- Shut down the session; `rm -rf "$tmpdir"`.

## Sharp edges

- Model thinking time inflates every wall-clock measure by seconds;
  the margins (10s vs 30s, ~5s vs a 60s bound) absorb that. When
  external clocking is too coarse, the api_call timestamps in the
  transcript JSONL bracket each tool round exactly.
- One bounded-wait call per arm, by design — `max_wait_ms` with grep
  must not become a polling loop (contract anti-pattern). The card
  issues exactly three.
- If the model inserts extra tool calls before the turn-1 bounded read
  and the token lands first, arm 1 degrades into a second entry-check
  (instant return with match) — still §7.2-conformant but no longer
  the mid-stream proof; rerun with the prompt tightened.
- Without `grep`, `max_wait_ms` semantics are unchanged (any new
  output or terminal state ends the wait) — out of scope here; the
  existing job cards cover plain reads.
