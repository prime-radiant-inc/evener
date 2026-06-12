# job-nested-visibility: forwarded nested jobs list, read, and stop through the parent-visible job_id

**What this covers**: nested shell jobs started by a delegate
(`docs/job-control.md` lines 1019-1065). (a) The parent sees the
nested job ONLY via `job_list(include_nested=true)` (line 672 default
false, line 1029), with `parent_job_id` linking it to the delegate
(line 1023); (b) the parent reads its output by the one parent-visible
`job_id` (lines 98-100, 1032); (c) parent `job_stop` on the nested job
routes to the live owner runtime and stops it — line 1035 specifies
routing-if-live, so a live in-process owner yields a confirmed stop,
NOT `not_controllable` (which line 122 reserves for a believed-live
owner that cannot route/control, and restart loss is `runtime_lost`,
line 1003); (d) after the delegate is finished and the nested job is
terminal, the forwarded job's retained output is STILL readable by the
parent — the forwarded-output promise (line 940; design spec
2026-06-08 §3.4: mirroring or durable routing metadata, model-
invisible). The cross-store read GRANT flavor (observer reads a
watched job) is job-watch-sidecar-observer.md, not this card.

## Pre-state

- Fresh binaries from the branch under test; hub on `127.0.0.1:9180`
  (`docs/agentic-testing.md` setup checklist); credentialed model.
- `tmpdir=$(mktemp -d -t serf-e2e-jnest-XXXXX)`.

## Steps

1. Spawn a session via `/api/spawn` with `working_dir=$tmpdir`.
   Capture `SID`.
2. Turn 1 — a delegate starts a background shell job and finishes:

   > Call delegate (background default) with this exact task: "Run the
   > shell tool with background true, description nested-probe, and
   > this command: `sh -c 'echo NEST_TOKEN_1; sleep 300; echo NEST_TOKEN_2'`.
   > Then communicate exactly 'NESTED_JOB <its job_id>' and finish."
   > Report the delegate's job_id, then end your turn and wait for its
   > completion notification.
3. Turn 2 — visibility, read, stop, and post-terminal read (new user
   prompt after the delegate's terminal notification):

   > Do these steps in order. Report every tool result verbatim.
   > 1. Call job_list with NO include_nested and report every job_id
   >    and type.
   > 2. Call job_list with include_nested true and report every
   >    job_id, type, status, parent_job_id, owner_session_id, and
   >    visible_to_session_id.
   > 3. Call job_read_output with the NESTED shell job's job_id (from
   >    step 2 / the delegate's report). Report the full JSON.
   > 4. Call job_stop with that nested job_id and block true. Report
   >    the full JSON.
   > 5. Call job_read_output for the nested job_id one more time.
   >    Report the full JSON.
   > 6. End your turn.
4. Read the parent's durable log
   (`find ~/.local/state/serf/projects -path "*sessions/$SID/jobs.jsonl"`)
   and the transcript.

## Expected

- Arm (a): the step-1 listing (default `include_nested=false`)
  contains the DELEGATE job (terminal `completed`) and NOT the nested
  shell job. The step-2 listing adds exactly one more row: the nested
  job with `type` `"shell"`, `status` `"running"`, `parent_job_id`
  equal to the delegate's job_id, `owner_session_id` NOT equal to
  `$SID` (the child session owns the runtime), and
  `visible_to_session_id` equal to `$SID` (line 76). Falsification:
  the nested job leaks into the default listing, or appears with a
  different/namespaced id than the one the delegate reported — the
  parent-visible `job_id` must be the same opaque handle everywhere
  (lines 98-100).
- The nested job OUTLIVES its creating delegate: the delegate is
  `completed` while the nested job still runs — background jobs are
  not tied to the creating turn (line 9).
- Arm (b): the step-3 read returns `status` `"running"` and `content`
  containing `NEST_TOKEN_1` — the parent read its output through the
  parent-visible id with no extra handle. Falsification:
  `target_not_found`/`not found` or empty content while the child
  store holds bytes (forwarded-output routing broken, line 940).
- Arm (c): the step-4 stop returns `status` `"cancelled"`, `reason`
  `"stopped_by_parent"` — the parent's stop ROUTED to the live owner
  runtime and confirmed (line 1035). Falsification (the cited rule):
  a `not_controllable` error here, with the owner session live
  in-process, is wrong — line 122 + line 1003 reserve
  `not_controllable` for a believed-live owner that cannot
  route/control the job, and the shipped router routes any live
  in-process owner directly (`agent/jobs_nested.go:76-78` states
  exactly this).
- Arm (d): the step-5 read — AFTER the delegate finished (long since
  terminal) and the nested job itself is now terminal — still returns
  the retained output: `status` `"cancelled"`, `content` containing
  `NEST_TOKEN_1` and NOT `NEST_TOKEN_2` (never printed; the sleep was
  cut short). The forwarded job's output remains readable by the
  parent after both lifetimes ended (line 940). Falsification: the
  read degrades to `not found` or `output_unavailable` once the jobs
  are terminal while retention obviously still holds (fresh session,
  tiny output).
- Durable forwarding: the parent's `jobs.jsonl` contains forwarded
  `job_started` and `job_finished` events for the nested job_id with
  `parent_job_id` = the delegate job and `owner_session_id` = the
  child session — the durable substrate behind parent visibility
  (line 1033).
- The nested job's own terminal (cancelled) notification arrives to
  the parent like any armed background job's (line 1034); count it in
  job-notification-semantics.md terms, not here.

## Cleanup

- All jobs are terminal after step 4. Shut down the session
  (`POST /s/$SID/shutdown`); `rm -rf "$tmpdir"`.

## Sharp edges

- The delegate must START the nested job before finishing; its task
  deliberately reports the nested job_id in its communicate so the
  parent (and the operator) can cross-check the id equality between
  the child's report and the parent's listing — that equality IS the
  no-namespacing assertion.
- "Owner runtime live" in arm (c) means the child session object is
  retained in-process (it is — terminal delegates are kept for
  resume). The owner-truly-GONE variants are elsewhere: restart turns
  control attempts into `stopped/runtime_lost` reconciliation
  (job-restart-durability.md mechanics; line 1036), which is the only
  live route to asserting the `not_controllable`-vs-`runtime_lost`
  boundary honestly.
- Whether the parent's read in arm (d) is served by the retained
  child store or by the forwarded record plus durable output routing
  is model-invisible by design (line 100, line 940 "either by
  mirroring ... or by durable routing metadata") — do not assert the
  mechanism, only the readability.
- If step 2's listing shows the nested job's `status` already
  terminal, the 300s sleep died early (crashed shell?) — investigate
  the output before proceeding; the stop arm needs a running target.
