# job-watch-caller-notification-delivery: a watch fire wakes its idle creator as a notification turn, and busy fires coalesce latest-wins

**What this covers**: delivery is implicit — a watch's fires go to the
session that CREATED it, with no delivery target to configure. Run 1: a
fire while that session is IDLE wakes it with no user input, and the
model receives the notification as a job-notification turn. Run 2: N
fires against a BUSY session produce N wake tokens but coalesce to the
latest-frame-wins current frame, rendered once per delivery boundary,
never one render per fire. Contract anchors: `docs/job-control.md:484-486`
("delivers a bounded notification/frame back to that watcher. There is
no model-facing `send` object"), `:607` (coalescing must not become silence),
`:1067` (mid-turn notifications queue for a safe turn boundary).

**Scope change (kata `f9gn`)**: this card used to install the same
watch two ways — implicit notify, and an explicit `send:{to:"caller"}`
— and assert they were distinct coexisting watch keys. `send` was
removed from the public schema by commit `9d0d777c6` (2026-06-22); it
is now rejected with `additionalProperties 'send' not allowed`
(`agent/session_tools_jobs_watch_test.go:240`) and there is exactly one
delivery model. That arm is gone. The idle-wake and coalescing arms are
untouched by the removal and are what this card now covers.

## Pre-state

- Fresh binaries from the branch under test; an isolated hub
  (`docs/agentic-testing.md`). Serve mode through the hub is REQUIRED:
  idle wake rides the server-wired notify func; a one-shot library/CLI
  run only delivers at natural boundaries and cannot exercise run 1.
- Credentialed model. Two hermetic workdirs:
  `tmpA=$(mktemp -d -t serf-e2e-wake-XXXXX)` and
  `tmpB=$(mktemp -d -t serf-e2e-coalesce-XXXXX)`.
- For run 2, write the AGENTS.md pacing file into `$tmpB` per
  `docs/agentic-testing.md` ("AGENTS.md pacing trick"), phrased for
  serf's shell tool: pause between every paragraph and every action by
  running `sleep 8` via the shell tool, at least four pauses per turn.

## Steps

Run 1 — idle wake:

1. Spawn session A via `/api/spawn` with `working_dir=$tmpA`. Capture
   `SID_A`.
2. Prompt:

   > Do these steps in order.
   > 1. Run the shell tool with background true and command:
   >    `sh -c 'sleep 25; echo WAKE_TOKEN_GO; sleep 240'`. Capture the
   >    job_id.
   > 2. Call job_watch with operation "create", source that job_id,
   >    and output_match "WAKE_TOKEN_GO". Report the full JSON.
   > 3. Call job_watch with operation "create", source that same
   >    job_id, and output_match "WAKE_TOKEN_GO" again — the identical
   >    configuration. Report the full JSON including
   >    replaced_existing.
   > 4. Say WATCHES_ARMED and end your turn. Do not poll; you will be
   >    woken.
3. Poll `/api/sessions/local:$SID_A` and confirm `state` is `idle`
   before the token prints (~+25s from the job start).
4. Keep polling through the fire: watch for the state to leave `idle`
   with NO user input sent, then inspect the transcript
   (`$SID_A.transcript.jsonl`) and read
   `serf-doctor watches $SID_A --state-dir <state base>`.

Run 2 — busy session, three fires, one rendered notification:

5. Spawn session B with `working_dir=$tmpB` (AGENTS.md present).
   Capture `SID_B`.
6. Prompt:

   > Read AGENTS.md in your working directory first; its pacing rules
   > are mandatory for this turn. Then:
   > 1. Run the shell tool with background true and command:
   >    `sh -c 'sleep 10; echo TICK_MARK_1; sleep 6; echo TICK_MARK_2; sleep 6; echo TICK_MARK_3; sleep 240'`.
   >    Capture the job_id.
   > 2. Call job_watch with operation "create", source that job_id,
   >    and output_match "TICK_MARK_[0-9]".
   > 3. Then write a five-paragraph essay about software engineering,
   >    following the AGENTS.md pacing rules exactly, so this turn
   >    stays busy for at least 40 more seconds.
   > 4. End your turn after the essay.
7. After the busy turn ends, inspect session B's transcript for the
   notification turn and read `serf-doctor watches $SID_B` for the
   watch's delivery count.

## Expected

Run 1:

- Step 2 returns `watching: true` with a `watch_id` and
  `replaced_existing: false`.
- Step 3 is IDEMPOTENT, not a second watch: identical
  `(watcher_session_id, source identity, receiver identity, condition
  hash)` is one key (`docs/job-control.md:599`). Expect the same
  `watch_id` back. `job_watch(operation="list")` must show ONE active
  watch on that job, not two. Falsification: two watch_ids for the
  same configuration, or a `replaced_existing: true` that silently
  discarded the first.
- The session is `idle` before the fire, then wakes WITHOUT user
  input: the state leaves `idle`, and the transcript gains NO new
  USER_INPUT entry — instead a notification turn: a STEERING-kind
  entry whose text contains a `<job-notification` block.
- That block is the watch flavour, not a lifecycle terminal:
  `event="watch"` and `job_type="watch"` with `status="watch"`, the
  concrete job_id, and `reason="output_match: <the matched line>"`
  naming WAKE_TOKEN_GO (`watchNotificationFromWatch`,
  `agent/job_watch.go:2855`; rendered by `agent/job_notify.go:178-190`).
- An assistant turn follows the notification turn (the model received
  the wake and reacted), and the session returns to `idle`.
- `serf-doctor watches $SID_A` shows the watch with a delivery count of
  at least 1 and no self-loop verdict. There are NO `watch_send_*` rows
  in `jobs.jsonl` for this watch, and their absence is not a failure: a
  watch with no delivery target enqueues a notification directly
  (`enqueueWatchNotifications`) instead of persisting a send. The
  `watch_send_pending`/`watch_send_delivered` pair belongs to
  cross-session observer frames — see
  `job-watch-observer-snide-thread.md` for that rail.
- Falsification (wake hole): the token printed — confirm with a manual
  follow-up `job_status` — but the session sat `idle` past ~90s:
  observation without wake.
- Falsification (old mechanism): the fire arrives as a bare steering
  message with no `<job-notification` framing — the deleted
  steering-turn delivery path is back.

Run 2:

- All three TICK lines print while the turn is still busy (ticks land
  ~+10/+16/+22s after the job starts; the paced essay holds the turn
  well past that). No notification appears mid-stream (inside a
  streaming model response) — but one MAY surface between tool rounds:
  mid-turn notifications queue for a safe turn boundary
  (`docs/job-control.md:1067`), so a between-rounds delivery during the
  paced essay is contract-true, not a leak.
- Each rendered notification's `reason` references the LATEST tick that
  had fired by its delivery time — latest-wins per watch key; a
  superseded stale token renders nothing. Observed shipped behavior
  with this pacing: one notification, reason `output_match:
  TICK_MARK_3`.
- Falsification: more rendered notifications than delivery boundaries
  (one-per-fire leaked into rendering — coalescing broken); zero
  rendered (the matched condition turned into silence, which the
  contract forbids at `docs/job-control.md:607`); or a notification
  whose `reason` references a tick OLDER than the latest fire at its
  delivery time.

## Cleanup

- In each session: `job_watch(operation="clear", watch_id=...)`, then
  `job_stop` the sleeper job (240s tails), then shut the session down.
- `rm -rf "$tmpA" "$tmpB"`.

## Sharp edges

- Run 1 needs the session idle before +25s: a three-tool-call turn
  fits easily, but if the model dawdles the fire lands mid-turn and
  run 1 degrades into run-2 shape. Re-run rather than reinterpret.
- Run 2 inverts the risk: the essay must HOLD the turn past the last
  tick (+22s). The pacing file plus model latency gives >40s; if the
  model skips the pacing, later ticks fire while idle and arrive as
  separate wakes — rerun rather than reinterpreting a 2-notification
  result.
- Latency note (accepted by the spec): during one long uninterrupted
  model stream, delivery waits for the stream to end — a notification
  landing a few seconds after a long assistant message is normal.
- Duplicate notifications across a daemon crash/restore are legal
  (at-least-once) — only duplicates within one uninterrupted run
  falsify coalescing.
- Each watch has a 50-delivery budget (`watchDeliveryBudget`,
  `agent/job_watch.go:56`); a watch that exhausts it is auto-cleared
  with one final notification. Neither run comes close, but a runaway
  `output_match` in a variant of this card will hit it.
