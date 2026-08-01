# job-restart-durability: kill -9 mid-job; restart reconciles stopped/runtime_lost, retains output, notifies exactly once

**What this covers**: the durable-substrate headline of
`docs/job-control.md` — jobs survive a Serf process death as durable
records even though running processes do not ("Summary" "Running
processes are not required to survive a Serf process restart";
"Design principles" "No automatic process resume after restart").
Restart reconciliation ("Restart behavior"): a `running` record with
no live runtime is finalized exactly once as `stopped`/`runtime_lost`
with a stable `terminal_generation` ("Durable reconstruction
invariants" "The first canonical terminal durable record/event for a
job defines `terminal_generation`"), pre-crash retained
output stays readable via `read_transcript(transcript_ref="job:<job_id>")`
after the runtime is gone, the terminal notification is delivered exactly once
post-restart and deduped durably across a SECOND restart
("Notifications" "Terminal notification dedupe is durable" and
"Notification delivery must also avoid lost notifications"), and
`job_list` stays consistent throughout. Runtime loss is reported as
supervision loss, never as command failure ("Restart behavior" "This
is not command failure. It is supervision loss").

## Pre-state

- Fresh binaries from the branch under test. Serve mode through the
  hub is REQUIRED, with the hub's `-serf` flag set so it can respawn
  daemons: the restart leg rides the hub's auto-resume
  (`reconnect-auto-resume.md` is the plumbing-side card for that
  path). An isolated hub, per the Setup checklist in
  `docs/agentic-testing.md` (never Jesse's real hub on `9180`).
- Credentialed model; `tmpdir=$(mktemp -d -t serf-e2e-jrestart-XXXXX)`;
  `TOKEN=$(cat "$HOME/.serf/auth-token")`; `HUB=http://127.0.0.1:$PORT`.

## Steps

1. Spawn a session via `/api/spawn` with `working_dir=$tmpdir`.
   Capture `SID`. Prompt:

   > Run the shell tool with background true and this command:
   > `sh -c 'i=0; while [ "$i" -lt 60 ]; do i=$((i+1)); echo "TICK_$i"; sleep 1; done; echo PRODUCER_DONE'`.
   > Report the job_id verbatim, then end your turn. Do not read or
   > wait on it.
2. Capture `JOB` from the transcript; poll `/api/sessions/local:$SID`
   until `state` is `idle`. Locate the durable substrate now:
   `JOBS=$(find $HOME/.local/state/serf/projects -path "*sessions/$SID/jobs.jsonl")`
   and the output log `sessions/$SID/jobs/$JOB.log` next to it.
3. OPERATOR-STEP (the crash): at ~20s after the job started, find the
   session's daemon PID and SIGKILL it —

   ```bash
   PID=$(for f in $HOME/.serf/run/*.json; do python3 -c "
   import json,sys; d=json.load(open('$f'))
   print(d['pid']) if d.get('session_id')=='$SID' else None" 2>/dev/null; done | head -1)
   PRODUCER=$(pgrep -P "$PID")   # this run's producer, while the daemon is still its parent
   kill -9 "$PID"
   ```

   `kill -9` is deliberate: a graceful SIGTERM may run shutdown paths;
   the card asserts crash durability. Confirm the process is gone.
4. Record the pre-restart facts: copy `$JOBS` into this run's own
   directory (`cp "$JOBS" "$run/jobs-before-restart.jsonl"` — never a
   fixed `/tmp` name, which a second agent running this card would
   overwrite between this copy and the comparison, kata `k2rx`) and
   note the last
   retained line of the output log
   (`tail -2 "sessions/$SID/jobs/$JOB.log"`).
5. OPERATOR-STEP (the restart): resume the session by sending a new
   turn through the hub — this is the daemon's actual restart path
   (the hub spawns `serf serve --resume $SID`):

   ```bash
   curl -s -X POST -H "Content-Type: application/json" \
     -H "Authorization: Bearer $TOKEN" \
     -d '{"text":"Call job_list with no filters and report every job_id, status, and reason verbatim. Then call read_transcript with transcript_ref job:<the only job_id> and report its status, total_bytes, the first retained line, and the last retained line. Then call job_status for that job_id and report its status and reason."}' \
     "$HUB/s/$SID/send"
   ```

   Confirm a NEW daemon exists (`ps aux | grep "serve.*--resume.*$SID"`)
   and wait for the turn to complete (state back to `idle`).
6. Inspect the transcript, `$JOBS`, and run the read assertions below.
7. OPERATOR-STEP (the second restart — dedupe): kill -9 the NEW
   daemon's PID (re-run the step-3 lookup; ignore the stale
   rendezvous file of the first PID). Resume again:

   ```bash
   curl -s ... -d '{"text":"Call job_list once more and report each job_id, status, and reason."}' "$HUB/s/$SID/send"
   ```

   Wait for `idle`; inspect transcript and `$JOBS` again.

## Expected

- Pre-restart durability (step 4): `jobs.jsonl` already contains
  `job_started` for `$JOB` (appends are fsynced) and NO `job_finished`
  for it; the output log holds a contiguous `TICK_1..TICK_n` prefix
  with n ≈ 15-25 (the ~20s of pre-crash output) and no
  `PRODUCER_DONE`.
- Reconciliation (step 6, `jobs.jsonl`): exactly one `job_finished`
  for `$JOB` with `status:"stopped"`, `reason:"runtime_lost"`, an
  `ended_at`, `output_bytes` > 0, and a non-empty
  `terminal_generation`; followed by one `job_notification_pending`
  and, after delivery, one `job_notification_delivered` carrying the
  SAME `terminal_generation`.
- Model-visible truth (step 6, transcript): the job_list TOOL_RESULTS
  reports `$JOB` as `stopped` / `runtime_lost` (never `failed` — this
  is supervision loss, "Restart behavior", not command failure); the
  read_transcript TOOL_RESULTS shows `- status: stopped` and a fenced
  block whose first line is `TICK_1` and whose last line equals the
  pre-restart tail recorded in step 4 — the retained pre-crash output
  is fully readable with the runtime gone; the job_status TOOL_RESULTS
  carries the matching `"status":"stopped"` / `"reason":"runtime_lost"`
  pair (the `job:` read renders status but no reason for a shell job).
- No phantom growth: `total_bytes` in step 6 equals the step-4 file
  size; a second read 10s later is identical (the orphaned producer
  cannot reach the store once its pump died with the daemon).
- Exactly-once notification: the transcript contains EXACTLY ONE
  STEERING entry with `<job-notification job_id="$JOB"
  event="stopped" ... status="stopped" reason="runtime_lost"` —
  delivered in the step-5 processing episode (before or after the
  user-turn answer; ordering between the two is unspecified).
  <!-- pin: notification turns persist as kind STEERING transcript
       entries today; re-verify the persisted kind on shipped code. -->
- Second-restart dedupe (step 7): `jobs.jsonl` gains NO new
  `job_finished` for `$JOB` (grep count still 1 — `terminal_generation`
  is minted once, "Durable reconstruction invariants"), the
  transcript still contains exactly ONE
  runtime_lost notification block for `$JOB`, and the step-7 job_list
  reports it `stopped`/`runtime_lost` unchanged.
- Falsification (substrate hole): the post-restart job_list omits the
  job, reports it still `running`, or the `job:` read returns
  `not found` while `jobs.jsonl` has the record.
- Falsification (dedupe hole): a second `job_finished` with a NEW
  `terminal_generation`, or a second runtime_lost notification block
  after the second restart — "Notifications" "must not repeatedly
  notify about the same terminal event on every restore".
- Falsification (loss hole): no runtime_lost notification ever
  appears although `job_notification_pending` was appended — pending
  state lost between queueing and delivery ("Notifications" "restart
  between queueing and delivery must not silently lose the terminal
  notification").

## Cleanup

- The orphaned producer self-terminates on its next write after the
  daemon dies (SIGPIPE). If one lingers, `kill $PRODUCER 2>/dev/null` —
  the pid step 3 recorded while the daemon was still its parent. Never a
  `TICK_` pattern kill: every run of this card emits that same marker, so
  the pattern matches a concurrent agent's producer as readily as your own
  (kata `pcev`).
- Shut down the resumed session (`POST /s/$SID/shutdown`); remove the
  stale rendezvous files of the killed PIDs under `$HOME/.serf/run/` if
  present; `rm -rf "$tmpdir"` and `rm -f "$run/jobs-before-restart.jsonl"`.

## Sharp edges

- One daemon per session: the hub spawns a dedicated `serf serve`
  process per spawn/resume, and the rendezvous file
  `$HOME/.serf/run/<pid>.json` carries `session_id` — that is the
  authoritative PID lookup. `kill -9` leaves the stale file behind;
  match on session_id AND a live pid.
- The crash must land while the job is mid-run and the session idle:
  killing mid-turn adds unrelated turn-repair noise to the transcript;
  waiting past 60s lets the producer finish and turns the scenario
  into a different (completed-before-crash) shape.
- SIGTERM is NOT equivalent: the contract does not specify whether a
  graceful daemon shutdown finalizes running jobs differently, so this
  card pins the crash shape only (surfaced as a contract gap while
  writing this card).
- The retained tick count is pump-latency fuzzy (±2 ticks around the
  kill moment); assert the contiguous prefix and the step-4 equality,
  not an exact n.
- Restart reconciliation does NOT auto-resume anything ("Restart
  behavior" "Jobs do not auto-resume after a Serf process restart"); the
  producer does not restart. If a fresh producer process appears after
  resume, that is auto-resume creeping in — file it.
- Shell jobs are not resumable; `resumable` on the row stays
  false/absent. The delegate-flavored runtime_lost resumability path
  ("Restart behavior" "Their delegate conversations remain resumable
  through") is out of this card's scope.
