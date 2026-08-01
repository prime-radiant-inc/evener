# recursion-coordinator-fanout: a granted coordinator fans out workers; visibility, cascade, and owner-scoped notifications hold

**What this covers**: recursive delegation behind the double opt-in
(`docs/job-control.md` "Delegation allowance", "Nested jobs",
"Capacity and discovery requirements") and the owner-scoped drive-down
notification rule ("Nested jobs" "Terminal notifications for a
parent-visible nested background job are **owner-scoped**"; recursion
design spec §3/§9/§10). A root with a raised `MaxSubagentDepth` (=2, so
the root's own allowance is 2 — "Delegation allowance" "A root
session's allowance equals `MaxSubagentDepth`") spawns a COORDINATOR
delegate with `delegation_allowance=1`; the coordinator fans out 2-3
WORKER delegates (`max_wait_ms` unset = fire-and-return, "`delegate`"
"means return the `job_id` immediately without waiting") and ends
its turn. This card asserts, falsifiably: (a) the grant rule — a grant
`>=` the granter's own allowance is rejected verbatim ("Delegation
allowance" "**The grant rule.**"), a
grant of 1 succeeds; (b) the allowance gate — the coordinator (allowance
1 > 0) CAN delegate, a worker (allowance 0) CANNOT ("Delegation
allowance" "**Availability matrix (allowance-gated).**"; "Nested jobs"
"An observer sidecar started without an allowance (allowance 0, the
default) is a leaf and cannot delegate");
(c) the COORDINATOR is driven to receive its workers' completions in
its OWN turns (drive-down — "Notifications" "A parent **drives** a
child for the child's own (delegate-owned) job notifications");
(d) **OWNER-SCOPED** — the
ROOT's model is NEVER interrupted with a worker's terminal; the root is
told ONLY when the COORDINATOR itself finishes ("Nested jobs"
owner-scoped bullet; "Rollout (live vs. dark)" "**Live — nested-job
terminal notifications are owner-scoped.**");
(e) visibility is preserved via `job_list(include_descendants=true)`,
which surfaces the live tree with per-row `owner_session_id` + `depth`
("Nested jobs" "walks the live descendant tree at read time");
(f) `job_stop` on the coordinator's delegate CASCADES into
the subtree — the workers actually stop as `cancelled`/
`stopped_by_parent` ("`job_stop`" "cascades into the subtree";
"Nested jobs" "each cascade-stopped job finalizes as
`cancelled/stopped_by_parent`"), not orphaned.

This is the live-interface counterpart to the unit-level depth-N trees
in `agent/job_delegate_test.go` / the §9 testing list. Recursion is
DARK by default ("Delegation allowance" "**Double opt-in (dark by
default).**"); this card only runs with the raised config below.

## Pre-state

- Fresh binaries from the branch under test (`job-control-spec`); an isolated hub, per the Setup checklist
  (`docs/agentic-testing.md`; never Jesse's real hub on `9180`);
  credentialed model that can drive a multi-step delegation plan
  (the orchestrator picks the spawn model at run time, e.g.
  `openai/gpt-5.5`).
- `tmpdir=$(mktemp -d -t serf-e2e-recfan-XXXXX)`.
- **The recursion opt-in is config + per-spawn grant (BOTH required —
  "Delegation allowance" "**Double opt-in (dark by default).**").**
  Raise `MaxSubagentDepth` to 2 on the spawn so the root's
  own allowance is 2 ("A root session's allowance equals
  `MaxSubagentDepth`"): pass it in `launch_overrides`. The
  wire key is `maxSubagentDepth` (camelCase, `appwire/types.go:1708`):

  ```json
  {"prompt":"...","model":"openai/gpt-5.5","working_dir":"$tmpdir",
   "harness":"serf","access_mode":"full","agent":"default",
   "launch_overrides":{"maxSubagentDepth":2}}
  ```

  Without the raised config the root's allowance is 1, every grant > 0
  is rejected, and this card cannot arm — confirm the config took
  before asserting recursion (the rejection in step 2 proves the
  ceiling is 2, not 1).

## Steps

1. Spawn a session via `/api/spawn` with `working_dir=$tmpdir` and the
   `launch_overrides.maxSubagentDepth=2` above. Capture `SID`.
2. Turn 1 — prove the grant ceiling, then arm the coordinator (one
   user prompt):

   > Do these in order; report every tool result verbatim.
   > 1. Call delegate with `delegation_allowance` 2 and the task
   >    "noop". Report the full error JSON. (This must be REJECTED —
   >    your own allowance is 2 and a grant must be strictly less.)
   > 2. Call delegate (max_wait_ms unset) with `delegation_allowance`
   >    1 and this exact task: "You are a COORDINATOR. Fan out THREE
   >    worker delegates, each with max_wait_ms UNSET and
   >    delegation_allowance 0 (the default — do NOT pass an
   >    allowance), one task each: worker-A runs the shell command
   >    `sh -c 'echo WORKER_A; sleep 8'`, worker-B runs
   >    `sh -c 'echo WORKER_B; sleep 8'`, worker-C runs
   >    `sh -c 'echo WORKER_C; sleep 8'`. After spawning all three,
   >    call communicate exactly 'COORDINATOR_SPAWNED <each worker
   >    job_id>' and END YOUR TURN — do NOT wait for the workers."
   >    Report the coordinator's job_id (call it COORD), then END
   >    your turn. Do NOT call job_list yet.
3. Turn 2 — visibility while the tree is live (new user prompt, sent
   while the workers' 8s sleeps are still running):

   > Call job_list with `include_descendants` true. Report EVERY row's
   > job_id, type, status, owner_session_id, depth, and parent_job_id.
   > Then end your turn.
4. Turn 3 — wait for the COORDINATOR's own terminal, then inspect what
   the ROOT was actually told (new user prompt):

   > Report verbatim the text of EVERY job-notification block that has
   > rendered on YOUR rail so far this session (the
   > `<job-notification ...>` frames you were woken with). Then call
   > job_status for COORD and report the full JSON, including its
   > transcript_ref. Then end your turn.
5. Turn 4 — cascade stop (new user prompt; run a SECOND fan-out first
   so there is a live subtree to fell, since turn-1's workers are
   short-lived):

   > Do these in order; report every result verbatim.
   > 1. Call delegate (max_wait_ms unset) with `delegation_allowance`
   >    1 and this exact task: "You are a COORDINATOR-2. Fan out TWO
   >    worker delegates, each max_wait_ms UNSET, delegation_allowance
   >    0, one task each: `sh -c 'echo LONG_A; sleep 300'` and
   >    `sh -c 'echo LONG_B; sleep 300'`. Report each worker job_id via
   >    communicate 'COORD2_WORKERS <ids>' and END YOUR TURN." Capture
   >    its job_id (COORD2).
   > 2. Run the foreground shell command `sleep 12` so COORDINATOR-2
   >    has spawned its workers.
   > 3. Call job_list `include_descendants` true; report every row's
   >    job_id, type, status, owner_session_id, depth.
   > 4. Call job_stop with COORD2 and max_wait_ms 8000. Report the full
   >    result JSON.
   > 5. Call job_list `include_descendants` true again; report the same
   >    fields.
   > 6. End your turn.
6. Read the durable logs and the transcripts:
   - root: `find $HOME/.local/state/serf/projects -path "*sessions/$SID/jobs.jsonl"`.
   - the coordinator's transcript via its `transcript_ref` (from the
     turn-1 delegate result / the turn-3 `job_status` result) and the
     descendant `jobs.jsonl` under each child session dir.

## Expected

- **Grant ceiling (step 2.1).** The `delegation_allowance=2` delegate
  is REJECTED with the verbatim error
  `invalid_request: delegation_allowance must be less than your own allowance (2); valid grants: 0..1`
  ("Delegation allowance" "**The grant rule.**";
  `agent/job_delegate.go:318`) — the `; valid grants: <range>`
  suffix is part of the message, not an addition. The `(2)` proves the root's
  own allowance is 2 — i.e. `MaxSubagentDepth=2` took. Falsification:
  the call SUCCEEDS (the ceiling didn't apply — config not raised, or
  the grant rule is off), or the parenthesised number is `(1)`
  (config never reached the session).
- **Grant succeeds + leaf gate (step 2.2 + coordinator transcript).**
  The `delegation_allowance=1` delegate is ACCEPTED and returns COORD.
  Inside the coordinator's transcript, its three `delegate` calls
  SUCCEED (its allowance is 1 > 0, so it received the `delegate` tool —
  "Delegation allowance" "At allowance > 0 the child receives
  `delegate` + `job_watch`"). Each worker was granted allowance 0; in a
  worker's transcript/prompt the `delegate` tool is ABSENT ("At
  allowance 0 the child is a leaf: it does not receive
  `delegate`/`job_watch`") —
  a worker that nonetheless tries to call `delegate` gets a
  tool-not-available / rejected result. Falsification (the recursion
  hole): a worker successfully spawns a grandchild — allowance 0 must
  be a hard leaf ("Design principles" "a leaf delegate (allowance 0,
  the default) cannot delegate").
- **Visibility — live descendant walk (step 3).** The
  `include_descendants=true` listing surfaces the live tree at read
  time ("Nested jobs" "walks the live descendant tree at read time
  instead of one hop"): the root's OWN coordinator delegate at `depth` 0
  with `owner_session_id` = `$SID`, and the three worker delegates at
  `depth` 1 (their owner is the coordinator's child session — one live
  hop down) with `owner_session_id` = the coordinator's session id
  (NOT `$SID`) and `parent_job_id` = COORD. Each worker appears EXACTLY
  ONCE ("Nested jobs" "Dedupe rule: the owner session's durable record
  is authoritative for a forwarded job"). Falsification: a worker is
  missing entirely (the live walk didn't recurse into the live child),
  a worker shows `depth` 0 or `owner_session_id` = `$SID` (the walk
  mis-attributed ownership), or a worker is listed twice (a forwarded
  copy wasn't suppressed in favour of the owner record).
- **Drive-down — the coordinator receives worker completions in its
  OWN turns (coordinator transcript).** After the coordinator ended its
  turn, the workers finish while it is idle; the coordinator's
  transcript shows a POST-IDLE notification turn (an `EntryNotification`
  the parent drove — "Notifications" "is **driven** by its parent at
  the parent's own loop boundaries", design §3) carrying the workers'
  terminal completions. The coordinator's model — not the root's — is
  the one woken for them. Falsification: the coordinator's transcript
  has NO post-idle notification turn for the workers (the worker
  terminals vanished instead of being driven into the owner), OR those
  worker terminals appear as notification frames on the ROOT's rail
  (the next assertion).
- **OWNER-SCOPED — the ROOT is told only about the COORDINATOR, never
  about a worker (step 4, the key new assertion).** The set of
  `<job-notification>` frames rendered on the ROOT's rail contains the
  COORDINATOR's terminal (COORD finishing — that is the root's OWN
  direct delegate ending: "Nested jobs" "The parent is told only about
  its OWN jobs, including its direct delegates' terminals") and contains NONE of
  the worker job_ids. Concretely: for every worker job_id the
  coordinator reported in `COORDINATOR_SPAWNED`, that id does NOT appear
  in any notification frame on the root's rail. Falsification (this is
  the regression this card exists to catch): a worker's job_id appears
  as the SUBJECT of a `<job-notification>` frame on the root's rail (the
  frame's `job_id=` attribute), or a worker's completion text appears
  INSIDE such a frame — the root was interrupted about a job a DESCENDANT
  created, which the owner-scoped rule ("Nested jobs" owner-scoped
  bullet; "Rollout (live vs. dark)" quotes Jesse's ruling, "an agent is
  never interrupted about a *subagent's* children")
  forbids. Match on the frame SUBJECT, not a bare substring search: a
  worker job_id and its `WORKER_x` payload legitimately appear in the
  COORDINATOR's transcript and in the root's forwarded durable records
  (`jobs.jsonl`) — that is visibility/drive substrate, not a root-rail
  notification. (The root may still SEE the workers on demand via
  `include_descendants` — visibility ≠ a notification; do not count a
  `job_list` row as a notification.)
- **Cascade stop (step 5).** Before the stop (step 5.3), the
  `include_descendants` listing shows COORD2 (`depth` 0,
  `owner_session_id` = `$SID`) and its TWO workers `running` at
  `depth` 1. The step-5.4 `job_stop(COORD2, max_wait_ms 8000)` HALTS the
  live subtree: the step-5.5 re-list shows BOTH workers terminal —
  `cancelled` with reason `stopped_by_parent` (the cascade reached into
  the subtree without a flag — "Nested jobs" "`job_stop` on a delegate
  job **cascades into its subtree** (no flag needed)"), NOT still
  `running`. This is
  the DECISIVE assertion — a `job_stop` on the coordinator MUST stop its
  running workers. COORD2's OWN record may read `completed` rather than
  `cancelled`: a fire-and-return coordinator finishes its own turn and
  goes terminal while its workers keep running, and the cascade targets
  the live subtree, NOT the coordinator's already-terminal own record —
  so do NOT require COORD2's own status to be `cancelled`. The cascade
  fires regardless of the coordinator's own terminal status (the
  stop-cascade has no terminal gate; `delegateChildSessionToCascade`,
  `agent/jobs_nested.go#delegateChildSessionToCascade`): a fire-and-return coordinator whose own job is
  already `completed` STILL has its live workers stopped. Falsification
  (the cascade hole this guards): either worker is still `running` after
  the stop confirmed — `job_stop` on the coordinator failed to halt its
  work. Accept `stopped`/`runtime_lost` for a worker ONLY if stopping the
  coordinator tore down its owner runtime before the worker stop
  confirmed (record what you see); a worker left silently `running` is
  the failure.
- **Durable substrate.** The root's `jobs.jsonl` contains forwarded
  `job_started` (typed `delegate` — "Nested jobs" "delegate-job
  creation forwards its `job_started` one hop to the parent's store,
  carrying `parent_job_id` plus owner/type identity") and
  `job_finished` events
  for COORD/COORD2 and — as forwarded one-hop copies — for the workers,
  each worker copy carrying `owner_session_id` = the coordinator's
  session and `parent_job_id` = the coordinator. The presence of these
  forwarded records is the visibility substrate; it is NOT a
  notification ("Nested jobs" "the forwarded copy is a drive signal
  for the parent", not a rail-rendered frame).

## Cleanup

- All delegate jobs are terminal after step 5 (turn-1 workers finished
  on their 8s sleeps; COORD/COORD2 and the long workers are stopped).
  Shut down the session (`POST /s/$SID/shutdown`); `rm -rf "$tmpdir"`.

## Sharp edges

- **The config gate is the whole point.** If the spawn's
  `launch_overrides.maxSubagentDepth` is dropped (or the runner's
  recipe omits it), the root's allowance is 1, step-2.1's grant of 2 is
  still rejected (with `(1)`, not `(2)`), AND step-2.2's grant of 1 is
  ALSO rejected (1 is not strictly less than 1) — the card cannot arm.
  The `(2)` in the step-2.1 error is the proof the config landed; treat
  `(1)` as "config didn't reach the session," not a contract bug.
- **Owner-scoped is asserted by ABSENCE on the root's rail.** The
  falsifiable form is "no worker job_id appears in any root-rail
  notification frame." Capture the root's rendered notifications
  explicitly (step 4 asks the model to echo them; cross-check against
  the root transcript's notification entries) — a vacuous "I didn't
  see any" is not enough. The worker ids to exclude are the ones the
  coordinator reported in `COORDINATOR_SPAWNED`; if the coordinator
  failed to report them, re-run rather than asserting absence against
  an unknown set.
- **Drive-down is parent-cadence (design §3 / `docs/architecture.md`
  "Drive-down").** The coordinator's notification turn fires at the
  ROOT's loop boundary (the root drives its direct child), so the
  coordinator may receive worker completions a beat after the workers
  actually finished. Poll the coordinator's transcript for the
  post-idle notification turn rather than expecting it instantaneously;
  a short worker sleep (8s) keeps the window tight without racing the
  drive.
- **Tree-wide cap is 50 ("Capacity and discovery requirements"
  "**Tree-wide running-delegate cap.**"; "Rollout (live vs. dark)"
  "**Live — the tree-wide running-delegate cap binds existing
  fan-out.**"; `--max-concurrent-delegates`,
  `defaultMaxConcurrentDelegateTurns`, `agent/tree_counter.go#defaultMaxConcurrentDelegateTurns`).** It was
  raised from the original hardcoded 16, and drive turns now budget
  separately (`defaultMaxConcurrentDriveTurns` = 8, `:17`). This card's
  fan-out (1 coordinator + 3 workers = 4 running, then COORD2 + 2 = 3)
  stays well under the cap; a spawn/resume at the cap fails
  `tree_at_capacity: 50 delegate turn slots in use across this session tree (J delegate jobs, D drive turns). Wait for completions to free slots, job_stop work you no longer need, or narrow your fan-out and retry.`
  (`agent/tree_counter.go:41-42`) — note "turn slots", not "jobs running",
  and the trailing jobs/drives split. Do not fan out wider here or the cap,
  not the owner-scoping, becomes the thing under test (the cap is
  exercised separately).
- **Worker timing for visibility (step 3).** The turn-1 workers sleep
  8s; step 3's `include_descendants` list must run while they are still
  `running` to assert the `depth`-1 live rows. If they have already
  finished, the rows show terminal status (still owner-attributed at
  `depth` 1 as the forwarded terminal copy — "Nested jobs" "A dead or
  terminated descendant contributes just the terminal forwarded copy
  that survives in an ancestor store") — re-run with a
  longer worker sleep if the live-running assertion needs them up.
- A worker that "helpfully" declines the leaf gate by not attempting a
  nested delegate leaves arm (b)'s leaf assertion resting on the
  prompt-surface check (the `delegate` tool absent from the worker's
  prompt) rather than a live rejection. Both are valid evidence; the
  prompt-absence form ("Delegation allowance" "agent-type listings
  that require those tools are filtered out of its prompt") is the
  normative one and is
  deterministic, so prefer reading the worker's system prompt /
  available tools over coaxing it into a forbidden call.
