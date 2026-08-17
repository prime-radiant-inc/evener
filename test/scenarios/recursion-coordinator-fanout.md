# recursion-coordinator-fanout: a granted coordinator fans out workers; visibility, cascade, and owner-scoped notifications hold

**What this covers**: recursive delegation behind the double opt-in
(`docs/job-control.md` "Delegation allowance", "Nested jobs",
"Capacity and discovery requirements") and the owner-scoped drive-down
notification rule ("Nested jobs" "Terminal attention is owner-scoped.";
recursion design spec §3/§9/§10). A root with a raised `MaxSubagentDepth` (=2, so
the root's own allowance is 2 — "Delegation allowance" "A root
session's allowance equals `MaxSubagentDepth`") spawns a COORDINATOR
delegate with `delegation_allowance=1`; the coordinator fans out 2-3
WORKER delegates (`delegate` always returns immediately after durable
`delegate_id` admission and rejects `max_wait_ms` outright —
"`delegate`" "Creation returns after the descriptor and initial input
are durable. It does not wait for a model result and rejects
`max_wait_ms`.") and ends its turn. This card asserts, falsifiably: (a) the grant rule — a grant
`>=` the granter's own allowance is rejected verbatim ("Delegation
allowance" "**The grant rule.**"), a
grant of 1 succeeds; (b) the allowance gate — the coordinator (allowance
1 > 0) CAN delegate, a worker (allowance 0) CANNOT ("Delegation
allowance" "**Availability matrix (allowance-gated).**"; "Nested jobs"
"allowance zero remains the default, so observer sidecars are leaves
unless explicitly granted otherwise");
(c) the COORDINATOR is driven to receive its workers' completions in
its OWN turns (drive-down — "Notifications" "A parent drives an idle
child so that child processes its own shell/watch attention");
(d) **OWNER-SCOPED** — the
ROOT's model is NEVER interrupted with a worker's terminal; the root is
told ONLY when the COORDINATOR itself finishes ("Nested jobs"
owner-scoped bullet; "Shipped recursion and owner attention" "A session
renders only attention for work it owns.");
(e) visibility is preserved via `job_list(include_descendants=true)`,
which surfaces the live tree with per-row `depth`
("Nested jobs" "walks the live descendant tree at read time");
(f) `job_stop` on the coordinator's delegate CASCADES into
the subtree — the workers actually stop, leaving the reusable delegate
resource `idle` with a last outcome of `stopped`/`stopped_by_parent`
("`job_stop`" "always cascades into the stable delegate subtree"), not
orphaned.

This is the live-interface counterpart to the unit-level depth-N trees
in `agent/delegate_tree_stop_test.go` (e.g.
`agent/delegate_tree_stop_test.go#TestDelegateControllerStopCancellationPlanIsLeafFirst`
seeds a parent → child → grandchild tree) / the §9 testing list. Recursion is
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
  wire key is `maxSubagentDepth` (camelCase, `appwire/types.go:2046`):

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
   >    delegate_id>' and END YOUR TURN — do NOT wait for the workers."
   >    Report the coordinator's delegate_id (call it COORD), then END
   >    your turn. Do NOT call job_list yet.
3. Turn 2 — visibility while the tree is live (new user prompt, sent
   while the workers' 8s sleeps are still running):

   > Call job_list with `include_descendants` true. Report EVERY row
   > exactly as the tool printed it, verbatim and unedited — do not
   > normalise, complete or reorder the fields.
   > Then end your turn.

   Ask for the line, not for fields. `job_list`'s model-facing output is
   `formatJobList` (`agent/session_tools_jobs.go#formatJobList`): one line
   per row of `id  type  status`, plus `depth=N` ONLY when non-zero, a
   label, and a `[started · reason · exit · bytes]` tail. Naming fields in
   the prompt invites the runner to supply the ones the tool omits:
   `owner_session_id` and `parent_delegate_id` live only on the structured
   state (`agent/session_tools_jobs.go#jobListEntry`), which the model never
   sees, and a depth-0 row prints no depth token at all. Asking for the raw
   line makes an absent token observable as an absence instead of something
   the runner has to invent or explain away.
4. Turn 3 — wait for the COORDINATOR's own terminal, then inspect what
   the ROOT was actually told (new user prompt):

   > Report verbatim the text of EVERY job-notification and
   > delegate-notification block that has rendered on YOUR rail so far
   > this session (the `<job-notification ...>` and
   > `<delegate-notification delegate_id="...">` frames you were woken
   > with — a direct delegate's terminal renders as
   > `<delegate-notification>`, never `<job-notification>`). Then call
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
   >    `sh -c 'echo LONG_B; sleep 300'`. Report each worker delegate_id
   >    via communicate 'COORD2_WORKERS <ids>' and END YOUR TURN."
   >    Capture its delegate_id (COORD2).
   > 2. Run the foreground shell command `sleep 12` so COORDINATOR-2
   >    has spawned its workers.
   > 3. Call job_list `include_descendants` true; report every row
   >    verbatim, exactly as the tool printed it.
   > 4. Call job_stop with COORD2 and max_wait_ms 8000. Report the full
   >    result JSON.
   > 5. Call job_list `include_descendants` true again; report every row
   >    verbatim, exactly as the tool printed it.
   > 6. Call job_status once for EACH of COORDINATOR-2's two worker
   >    delegate_ids (from its COORD2_WORKERS message) and report each
   >    full JSON, including `last_outcome`. This is the only place the
   >    stopped RUN's outcome is observable — the listing shows the
   >    reusable resource's lifecycle instead.
   > 7. End your turn.
6. Read the durable logs and the transcripts:
   - root: `find $HOME/.local/state/serf/projects -path "*sessions/$SID/delegates.jsonl"`
     for delegate lifecycle — COORD/COORD2 and every worker live in
     this single root-owned journal, never a per-descendant copy
     (`agent/delegate_runtime.go#bootstrapDelegateResources`: a child
     session inherits its root's delegate controller instead of
     opening its own store). `jobs.jsonl` under `$SID` or a descendant
     session dir is shell-job evidence only.

     **The journal and `job_status` can disagree about a stopped run's
     outcome, and both are right.** `delegate_run_finished` records the
     outcome as SUBMITTED (`agent/internal/delegatestore/event.go#RunFinished.Outcome`),
     so a worker whose run died of context cancellation is written to disk
     as `cancelled`. The stopping-phase override to
     `stopped`/`stopped_by_parent` happens in the in-memory fold
     (`agent/internal/delegatestore/fold.go#applyRunFinished`) and reaches
     `LatestOutcome`, which is what `job_status` projects. Assert the
     stopped pair against `job_status`; read the journal for lineage and
     ordering, and do not score an on-disk `cancelled` as a contradiction.
   - the coordinator's transcript via its `transcript_ref` (from the
     turn-1 delegate result / the turn-3 `job_status` result).

## Expected

- **Grant ceiling (step 2.1).** The `delegation_allowance=2` delegate
  is REJECTED with the verbatim error
  `invalid_request: delegation_allowance must be less than your own allowance (2); valid grants: 0..1`
  ("Delegation allowance" "**The grant rule.**";
  `agent/delegate_runtime.go:836`) — the `; valid grants: <range>`
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
- **Visibility — live descendant walk (step 3).** Two halves, judged
  from two different sources, because the model-facing listing does not
  print ownership.

  From the reported listing: the `include_descendants=true` walk
  surfaces the live tree at read time ("Nested jobs" "walks the live
  descendant tree at read time instead of one hop") — the root's OWN
  coordinator delegate carrying NO depth token (depth 0 is not printed),
  and the three worker delegates each carrying `depth=1`, each appearing
  EXACTLY ONCE ("Nested jobs" "a forwarded copy of a `job_id` whose owner
  is reached live during the walk is suppressed in favor of that owner's
  record"). Falsification: a worker
  missing entirely (the live walk did not recurse into the live child), a
  worker line with no depth token (the walk mis-attributed the hop and
  scored it as the root's own), or a worker listed twice (a forwarded copy
  was not suppressed in favour of the owner record).

  From the durable journals read in step 6 — NOT from the model's
  report: each worker's lineage is `parent_delegate_id` = COORD, never
  `parent_job_id` (`agent/session_tools_jobs.go#jobListEntry`;
  `parent_job_id` is shell-to-shell lineage only — "Vocabulary"
  "`parent_job_id` never encodes delegate lineage."). Falsification: a
  worker whose lineage is recorded under `parent_job_id`.

  Ownership is NOT per-hop. Every stable delegate descriptor is created
  with the controller root's session as its owner --
  `descriptor.OwnerSessionID = c.rootSessionID`
  (`agent/delegate_tree_start.go#delegateTreeController.ReserveCreate`),
  unconditionally and regardless of depth. So every worker reports
  `owner_session_id` = `$SID`, the same as COORD. Lineage is carried by
  `parent_delegate_id`, which is the field that distinguishes a worker
  from the coordinator; `depth` is what the live walk contributes.
  Falsification: a worker whose `owner_session_id` is the coordinator's
  session rather than `$SID`.
- **Drive-down — the coordinator receives worker completions in its
  OWN turns (coordinator transcript).** After the coordinator ended its
  turn, the workers finish while it is idle; the coordinator's
  transcript shows a POST-IDLE notification turn (an `EntryNotification`
  the parent drove — "Why drive-down, not a flat session scheduler"
  "each parent starts one child's `EntryNotification` turn at a safe
  loop boundary", design §3) carrying the workers'
  terminal completions. The coordinator's model — not the root's — is
  the one woken for them. Falsification: the coordinator's transcript
  has NO post-idle notification turn for the workers (the worker
  terminals vanished instead of being driven into the owner), OR those
  worker terminals appear as notification frames on the ROOT's rail
  (the next assertion).
- **OWNER-SCOPED — the ROOT is told only about the COORDINATOR, never
  about a worker (step 4, the key new assertion).** COORD is a direct
  delegate, so its terminal renders as `<delegate-notification
  delegate_id="dlg_...">`, never `<job-notification>` — a
  direct-delegate terminal ("Notifications" "It never carries `job_id`
  or `job_type="delegate"`."). The set of
  `<delegate-notification>` frames rendered on the ROOT's rail contains
  the COORDINATOR's terminal (COORD finishing — that is the root's OWN
  direct delegate ending: "Notifications" "the parent renders only
  its own shells and direct delegates").

  **The assertion is on the frame SUBJECT, and only the subject.** For
  every worker delegate_id the coordinator reported in
  `COORDINATOR_SPAWNED`, no root-rail frame carries that id in its
  `delegate_id=` attribute. Falsification (this is the regression this
  card exists to catch): a worker's delegate_id IS the `delegate_id=` of
  a `<delegate-notification>` frame on the root's rail — the root was
  interrupted about a delegate a DESCENDANT created, which the
  owner-scoped rule ("Nested jobs" "an ancestor is not interrupted
  about a descendant's children") forbids.

  Do NOT run this as a substring search for a worker id across the root's
  rail, and do not fail the run on worker text appearing INSIDE a
  COORD-subject frame. Step 2 instructs the coordinator to `communicate`
  exactly `COORDINATOR_SPAWNED <each worker delegate_id>`, and a
  delegate's communicate output becomes its terminal packet's `message`
  (`agent/subagents.go#stableDelegateFinishFromRun`), which is marshalled
  whole into the frame body
  (`agent/delegate_delivery.go#delegateNotificationContent` renders
  `<delegate-notification delegate_id="COORD">{...packet...}</delegate-notification>`).
  Every worker id is therefore GUARANTEED to appear inside COORD's own
  frame, by this card's own instruction. That is a direct delegate
  reporting to its owner — correct owner scoping, and the drive substrate
  the card asks for in the first place. A blanket-absence check would
  fail every healthy run.

  The same goes for the rest of the substrate: a worker delegate_id and
  its `WORKER_x` payload legitimately appear in the COORDINATOR's
  transcript and in the root's `delegates.jsonl`. (The root may also SEE
  the workers on demand via `include_descendants` — visibility ≠ a
  notification; do not count a `job_list` row as a notification.)
- **Cascade stop (step 5).** Before the stop (step 5.3), the
  `include_descendants` listing shows COORD2 with no depth token and its
  TWO workers `running` at `depth=1`. The step-5.4
  `job_stop(COORD2, max_wait_ms 8000)` HALTS the live subtree: the
  step-5.5 re-list shows BOTH workers no longer `running` (the cascade
  reached into the subtree without a flag — "`job_stop`"
  "`job_stop(target=dlg_...)` always cascades into the stable delegate
  subtree" and "`include_children` has no effect"). This is the DECISIVE
  assertion — a `job_stop` on the coordinator MUST stop its running
  workers.

  **Read the stopped state in the two places that carry it, not from the
  status column.** A delegate is a reusable resource, and its `status` is
  a two-valued lifecycle — `running` or `idle`, nothing else
  (`agent/delegate_tree_controller.go#captureDelegateSnapshot`, which
  maps both the idle and the closed phase to `idle`;
  `agent/session_tools_jobs.go#projectStableDelegateStatus` is what puts
  that string in the listing's status column). So a stopped worker reads
  `idle` there, and NO delegate row ever reads `cancelled`, `stopped` or
  `completed` — those words belong to the latest RUN's outcome:

  - in the step-5.5 listing, the run's reason rides the bracket tail
    (`formatJobList` prints `[started · reason · exit · bytes]` from
    `jobListEntry.Reason`, which is copied from the delegate's last
    outcome): expect `stopped_by_parent` there;
  - the outcome STATUS needs `job_status` with the worker's
    `delegate_id`, whose `last_outcome` carries
    `{"status":"stopped","reason":"stopped_by_parent"}`. `stopped`, not
    `cancelled`: the fold forces both fields for any finish that lands
    while the delegate is in the stopping phase
    (`agent/internal/delegatestore/fold.go#applyRunFinished`;
    `agent/delegate_tree_finish.go#delegateTreeController.FinishGeneration`),
    so no other pair is reachable through a parent stop.

  The step-5.4 stop RESULT is a third shape again and is the one place
  the word cancel appears: `status` `idle`, `reason` `stopped_by_parent`,
  and `outcome` `cancelled_by_request` — meaning the request cancelled
  live runs in the subtree, versus `already_idle` when it found none
  (`agent/session_tools_jobs.go#stableDelegateStopResult`). Expect
  `previous_status` `idle`, NOT `running`: it is computed from the
  TARGET's own open run, while the outcome is computed from the whole
  subtree's (`agent/delegate_tree_stop.go#classifyDelegateStopAdmission`).
  COORD2 is fire-and-return — step 5.1 has it communicate and end its
  turn, and step 5.2 sleeps 12s — so its own run is closed well before
  5.4 even though its workers are still live. That asymmetry is the
  point: `already_idle` would mean the cascade found nothing to stop.
  (Contrast `subagent-cancel-runaway.md`, where the same triple reads
  `previous_status` `running` because that delegate is stopped mid-sleep.)
  If the stop
  does not confirm inside `max_wait_ms` the result reads `status`
  `running` / `reason` `stop_pending` / `outcome` `stop_requested`
  instead; that is an unfinished stop, so poll before judging rather than
  recording it as the cascade failing.

  COORD2's own last outcome may read `completed` rather than `stopped`: a
  fire-and-return coordinator finishes its own turn and goes terminal
  while its workers keep running, and the cascade targets the live
  subtree, NOT the coordinator's already-terminal own record — so do NOT
  require COORD2's own outcome to be a stop. The cascade fires regardless
  of the coordinator's own terminal status (the stop-cascade has no
  terminal gate, `agent/delegate_tree_stop.go#subtreeMembersLocked`): a
  fire-and-return coordinator that has already reported STILL has its
  live workers stopped.

  Falsification (the cascade hole this guards): either worker still reads
  `running` in the step-5.5 listing after the stop confirmed — `job_stop`
  on the coordinator failed to halt its work. A worker that went `idle`
  with a `runtime_lost` reason instead is the owner-runtime-teardown
  variant: record what you see, it is not the hole. A worker left
  silently `running` is the failure.
- **Durable substrate.** Delegate generations never create job
  records, so there is no forwarded `job_started`/`job_finished` copy
  for a delegate at all ("Durable job records" "Delegate generations
  never create job records."). The root's `delegates.jsonl` — one
  journal for the whole tree, not a per-descendant forwarded copy,
  because a child session shares its root's delegate controller
  ("Durable reconstruction invariants" "Stable delegate lifecycle,
  descriptor, lineage, resumability, transcript reference, last
  outcome, and ordered owner delivery live in the root
  `delegates.jsonl` journal.") — carries `delegate_created` and
  `delegate_run_finished` events, keyed by `delegate_id`, for
  COORD/COORD2 and for each worker, with each worker's descriptor
  carrying `owner_session_id` = `$SID` (the controller root owns every
  descriptor at any depth) and `parent_delegate_id` = the coordinator's
  `delegate_id`, which is the field that carries lineage
  (`agent/internal/delegatestore/event.go#EventDelegateRunFinished`;
  `agent/internal/delegatestore/record.go#Descriptor.ParentDelegateID`).
  The presence of these durable records is the visibility substrate;
  it is NOT a notification.

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
- **Owner-scoped is asserted by ABSENCE of a SUBJECT on the root's
  rail.** The falsifiable form is "no root-rail notification frame has a
  worker delegate_id as its `delegate_id=` attribute." Parse the
  attribute; do not grep the rail for the id. The looser form — "no
  worker delegate_id appears in any root-rail frame" — reads stronger and
  is worth less than nothing here: the coordinator is told to
  `communicate` those very ids, so they ride inside its own terminal
  frame on every healthy run, and a card that fails a healthy run gets
  its guard deleted. Capture the root's rendered notifications explicitly
  (step 4 asks the model to echo them; cross-check against the root
  transcript's notification entries) — a vacuous "I didn't see any" is
  not enough. The worker ids to exclude are the ones the coordinator
  reported in `COORDINATOR_SPAWNED`; if the coordinator failed to report
  them, re-run rather than asserting absence against an unknown set.
- **Drive-down is parent-cadence (design §3 / `docs/architecture.md`
  "Drive-down").** The coordinator's notification turn fires at the
  ROOT's loop boundary (the root drives its direct child), so the
  coordinator may receive worker completions a beat after the workers
  actually finished. Poll the coordinator's transcript for the
  post-idle notification turn rather than expecting it instantaneously;
  a short worker sleep (8s) keeps the window tight without racing the
  drive.
- **Tree-wide cap is 50 ("Capacity and discovery requirements"
  "**Tree-wide running-delegate cap.**"; `--max-concurrent-delegates`,
  `agent/tree_counter.go#defaultMaxConcurrentDelegateTurns`).** It was
  raised from the original hardcoded 16, and drive turns now budget
  separately (`agent/tree_counter.go#defaultMaxConcurrentDriveTurns` = 8).
  This card's
  fan-out (1 coordinator + 3 workers = 4 running, then COORD2 + 2 = 3)
  stays well under the cap; a spawn/resume at the cap fails
  `tree_at_capacity: 50 delegate turn slots in use across this session tree (J delegate jobs, D drive turns). Wait for completions to free slots, job_stop work you no longer need, or narrow your fan-out and retry.`
  (`agent/tree_counter.go:79-80`) — note "turn slots", not "jobs running",
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
