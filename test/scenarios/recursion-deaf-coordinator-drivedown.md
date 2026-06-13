# recursion-deaf-coordinator-drivedown: an idle coordinator is driven to receive its workers' completions; the root hears only the coordinator

**What this covers**: the recursion design spec §9 headline regression —
**the deaf-coordinator, drive-down form** — end to end through the REAL
interface rather than the unit test's injected notifications
(`docs/job-control.md` lines 1079, 1226; design §3/§9). A coordinator
(granted `delegation_allowance=1` under a raised `MaxSubagentDepth=2`)
backgrounds workers (`max_wait_ms` unset = fire-and-return, line 1244)
and ENDS ITS TURN. The workers finish while the coordinator is IDLE.
Drive-down (line 1079, design §3) means the parent DRIVES the idle
coordinator so the COORDINATOR's model gets a notification turn for the
workers' completions — while the ROOT's model is told ONLY about the
coordinator itself finishing. Before drive-down ("today, red"), an idle
coordinator never woke for its own workers, and a nested terminal
pushed onto the PARENT's model. This card proves the shipped behavior:
the coordinator wakes for its workers; the root does not.

Two falsifiable claims carry the card:
1. **The coordinator is driven (not deaf):** the coordinator's
   transcript shows a POST-IDLE notification turn carrying the workers'
   completions — a turn that begins after the coordinator already ended
   its prior turn.
2. **Owner-scoped:** the ROOT's rail shows only the COORDINATOR's
   terminal; NO worker job_id appears in any root-rail notification
   frame.

This is the single-coordinator focus of the broader
`recursion-coordinator-fanout.md` card; here the deaf-coordinator wake
IS the test, asserted on the coordinator's own transcript.

## Pre-state

- Fresh binaries from the branch under test (`job-control-spec`); hub
  on `127.0.0.1:9180` (`docs/agentic-testing.md` setup checklist);
  credentialed model (the orchestrator picks the spawn model at run
  time, e.g. `openai/gpt-5.5`).
- `tmpdir=$(mktemp -d -t serf-e2e-recdeaf-XXXXX)`.
- **Recursion is dark by default (line 104); arm it with the config +
  per-spawn grant.** Raise `MaxSubagentDepth` to 2 via
  `launch_overrides` so the root's own allowance is 2 and a grant of 1
  is legal. The wire key is `maxSubagentDepth` (camelCase,
  `appwire/types.go:839`):

  ```json
  {"prompt":"...","model":"openai/gpt-5.5","working_dir":"$tmpdir",
   "harness":"serf","access_mode":"full","agent":"default",
   "launch_overrides":{"maxSubagentDepth":2}}
  ```

## Steps

1. Spawn a session via `/api/spawn` with `working_dir=$tmpdir` and
   `launch_overrides.maxSubagentDepth=2`. Capture `SID`.
2. Turn 1 — arm the deaf coordinator (one user prompt):

   > Call delegate with max_wait_ms UNSET and `delegation_allowance` 1
   > and this exact task: "You are a COORDINATOR. Background TWO worker
   > delegates, each with max_wait_ms UNSET and delegation_allowance 0:
   > worker-1 runs the shell command `sh -c 'echo W1_DONE; sleep 6'`,
   > worker-2 runs `sh -c 'echo W2_DONE; sleep 6'`. After spawning BOTH,
   > call communicate exactly 'COORD_WORKERS <worker-1 job_id>
   > <worker-2 job_id>' and END YOUR TURN IMMEDIATELY — do NOT call
   > job_list, do NOT wait for the workers, do NOT poll. You must be
   > IDLE when they finish." Report the coordinator's job_id (COORD),
   > then END your turn. Do NOT poll.
3. Let the workers finish while the coordinator is idle: wait ~15s
   (their 6s sleeps plus drive latency). Do not send any prompt during
   this window — the coordinator must be genuinely idle so the wake is
   a DRIVE, not a same-turn poll.
4. Turn 2 — inspect what the ROOT was told (new user prompt, after the
   coordinator's own terminal notification has rendered):

   > Report verbatim EVERY `<job-notification ...>` frame that has
   > rendered on YOUR rail this session. Then call job_read_output for
   > COORD and report the full JSON (including its `transcript_ref`).
   > Then end your turn.
5. Inspect the COORDINATOR's transcript directly (ground truth — the
   UI rail can't show the coordinator's own turns):
   - resolve the coordinator's `transcript_ref` from step 4 /
     `job_read_output(COORD)`;
   - `read_session_transcript` (or read the coordinator session's
     `*.transcript.jsonl` under
     `~/.local/state/serf/projects/.../sessions/<coord-sid>/`).
6. Read the durable logs:
   - root: `find ~/.local/state/serf/projects -path "*sessions/$SID/jobs.jsonl"`.
   - coordinator + workers: the `jobs.jsonl` under each child session
     dir.

## Expected

- **The coordinator was DRIVEN (not deaf) — coordinator transcript.**
  The coordinator's transcript contains a notification turn that begins
  AFTER the turn in which it spawned the workers and ended — i.e. a
  post-idle `EntryNotification` turn (design §3; `acceptNotificationInput`
  on the child's own loop, `agent/subagents.go`) — and that turn
  carries BOTH workers' terminal completions (the `W1_DONE`/`W2_DONE`
  workers reaching `completed`). The coordinator's OWN model is the one
  woken. Falsification (the deaf-coordinator regression, red before
  drive-down): the coordinator's transcript ends at the turn where it
  spawned the workers, with NO later notification turn — the idle
  coordinator never woke for its own workers, so the completions were
  dropped or stranded.
- **OWNER-SCOPED — the ROOT heard only the coordinator (step 4).** The
  `<job-notification>` frames on the ROOT's rail contain the
  COORDINATOR's terminal (COORD finishing — the root's OWN direct
  delegate ending, line 1079 / line 1226) and contain NONE of the
  worker job_ids reported in `COORD_WORKERS`. For each worker job_id:
  it does NOT appear in any notification frame on the root's rail, and
  neither do the `W1_DONE`/`W2_DONE` worker payloads. Falsification
  (the pre-drive-down behavior, and the regression this card guards): a
  worker job_id or a worker's completion text appears in a notification
  frame on the ROOT's rail — the root was interrupted about a job its
  DESCENDANT created, which the owner-scoped rule forbids ("an agent is
  never interrupted about a *subagent's* children", line 1079 /
  line 1234).
- **Visibility preserved, not removed.** Even though the root was not
  notified about the workers, a `job_list(include_descendants=true)`
  from the root would surface them on demand (line 1079: the ancestor
  retains on-demand visibility). This card does not re-prove the full
  descendant walk (that is `recursion-coordinator-fanout.md` /
  `job-nested-visibility.md`); it only asserts the notification
  surface. If you list anyway, the workers appear at `depth` 1 owned by
  the coordinator's session — but a `job_list` ROW is NOT a
  notification, and must not be counted as one for the owner-scoped
  assertion above.
- **Durable substrate.** The root's `jobs.jsonl` carries forwarded
  `job_started` (typed `delegate`, line 1077) and `job_finished`
  records for COORD and one-hop forwarded copies for the workers
  (`owner_session_id` = the coordinator's session, `parent_job_id` =
  COORD). These forwarded worker records are the DRIVE SIGNAL the
  parent used to drive the coordinator (line 1079) — their presence in
  the durable store is expected and is NOT a rail notification.
- **One terminal per owner.** The coordinator's drive turn delivers
  each worker's terminal exactly once to the coordinator (no-loss /
  dedupe, line 1245); the root gets exactly one terminal frame for
  COORD. Falsification: duplicate worker terminals inside the
  coordinator's notification turn, or a missing one (no-loss broken).

## Cleanup

- All jobs are terminal by design (the workers' 6s sleeps finish; the
  coordinator completes after its drive turn). Shut down the session
  (`POST /s/$SID/shutdown`); `rm -rf "$tmpdir"`.

## Sharp edges

- **The coordinator MUST be idle when the workers finish.** That
  idleness is what makes the wake a DRIVE rather than a same-turn poll
  — and the deaf-coordinator regression only manifests against an idle
  coordinator. The task prompt forbids polling and orders an immediate
  end-of-turn; if the model keeps its turn open (polls / sleeps until
  the workers finish), the coordinator handles the completions inline
  and the drive-down path is never exercised — re-run with the
  no-polling instruction emphasized. The coordinator transcript shows
  whether it ended its turn before the workers finished (the gate for
  this whole card).
- **Drive latency is parent-cadence (design §3 / architecture.md
  "Drive-down").** The coordinator's notification turn fires at the
  ROOT's loop boundary, so it can lag the workers' actual completion by
  a beat. Poll the coordinator's transcript for the post-idle
  notification turn (step 5) rather than asserting against a fixed
  delay; the ~15s window in step 3 is a floor, not a deadline.
- **Assert the coordinator's wake on its TRANSCRIPT, not the UI rail.**
  The hub rail renders the ROOT's turns; the coordinator's own
  notification turn is only visible in the coordinator session's
  transcript (step 5). Reading the rail alone cannot confirm claim 1 —
  it is, correctly, silent about the coordinator's internal turns.
- **Owner-scoped is asserted by ABSENCE.** Capture the root's rendered
  notification frames explicitly (step 4 echoes them; cross-check the
  root transcript's notification entries) and confirm each worker
  job_id is absent. A vacuous "I saw nothing about workers" is not
  enough — exclude the SPECIFIC worker ids from `COORD_WORKERS`; if the
  coordinator didn't report them, re-run rather than asserting absence
  against an unknown set.
- **Config gate.** Without `launch_overrides.maxSubagentDepth=2` the
  root's allowance is 1, the grant of 1 is rejected
  (`delegation_allowance must be less than your own allowance (1)`),
  and the coordinator never spawns — the card cannot arm. Confirm the
  delegate in step 2 was ACCEPTED (returned COORD) before proceeding.
