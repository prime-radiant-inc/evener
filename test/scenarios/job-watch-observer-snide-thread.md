# job-watch-observer-snide-thread: caller events drive a snide observer's own thread without steering the caller

**What this covers**: a parent-source event watch whose observer
comments in its own transcript instead of steering the caller. This is
the safe "snide commentary in its own thread" shape: the observer is
spawned with `delegate(watch_parent:true)` and installs
`job_watch(source:"parent", events:[...])`; each bounded frame resumes
the observer, which writes its note and finishes with a terminal
`communicate(end_turn:true)` that the parent merely acknowledges — the
note is commentary, not an instruction. Contract anchor:
`docs/job-control.md` "Observer and sidecar composition" plus the
caller-event rail.

## Pre-state

- Fresh binaries from the branch under test; hub reachable (own hub via
  `docs/agentic-testing.md`, or an isolated `$HOME` hub on a spare
  port); credentialed model.
- `tmpdir=$(mktemp -d -t serf-e2e-snide-observer-XXXXX)`.

## Steps

1. Spawn the parent session via `/api/spawn` with
   `working_dir=$tmpdir`. Capture the session `ref`.
2. Prompt the parent:

   > Do these steps in order.
   > 1. Call `delegate` with `watch_parent` true, `max_wait_ms` 120000,
   >    and this exact task: "You are a snide observer sidecar. First:
   >    call job_watch with operation 'create', source 'parent', events
   >    ['assistant.tool', 'communicate']. Then communicate exactly
   >    SNIDE_READY and finish. When later resumed with a message
   >    containing 'Watch frame', read the delivery_id and event kind
   >    from the frame. Write one sardonic one-line comment about the
   >    observed action only; do not insult any person or group. Finish
   >    with communicate end_turn true, message prefixed exactly
   >    'SNIDE_NOTE delivery=<delivery_id> event=<event_kind> '. Do not
   >    start any delegate of your own."
   > 2. After the delegate result reports `watching: true`, run the
   >    shell tool in the foreground with command
   >    `printf 'MAIN_ACTION_ONE\n'`.
   > 3. Call `job_list` with no filters.
   > 4. Call communicate with exactly MAIN_ACTION_TWO and end your
   >    turn.
3. Wait for the observer follow-up jobs to finish, then inspect the
   observer's transcript directly (find the child `.transcript.jsonl`
   on disk by the delegate's transcript ref).
4. Check the parent's durable job log:
   `find <state base> -path "*sessions/<SID>/jobs.jsonl"`, and read
   `serf-doctor watches <SID> --state-dir <state base>`.

## Expected

- The observer's first turn installs the watch (`watching: true` on the
  readiness result; source-owned public shape, no `send` field) and
  completes with `SNIDE_READY`; later watch frames resume that durable
  observer conversation.
- The observer transcript contains at least two later `SNIDE_NOTE`
  lines, each with a non-empty `delivery=` value. The notes may refer
  to `assistant.tool` events (the shell/job_list actions), the
  `communicate` event, or both; exact count is not a contract because
  event coalescing is latest-wins while the observer is busy.
- Each `SNIDE_NOTE` is emitted through the observer's own terminal
  `communicate`. The parent sees it only as an observer callback and
  acknowledges; the notes are commentary, and the parent takes no
  action from them.
- Watch frames include `watch_id:`, `delivery_id:`, and the triggering
  event metadata the observer needs to comment.
- Acknowledgement traffic that carries the watch's own provenance IS
  delivered and classified self-influenced (not suppressed); such
  frames carry the `<system-reminder>` depth line, and the observer's
  commentary-only behavior keeps the loop shallow.
- The parent's `jobs.jsonl` shows `watch_send_pending` followed by
  `watch_send_delivered` for the deliveries, and NO
  `watch_send_dropped` with `diagnostic_reason: "runaway"`.
  `serf-doctor watches` reports bounded self-influence (no runaway);
  `--self-loops` returns no watches.
- The parent session returns to `idle` after observer callbacks drain.
  Clear the watch before continuing a free-form conversation.

## Cleanup

- `job_watch(operation="clear", watch_id=<watch_id>)`.
- Shut down the parent session; `rm -rf "$tmpdir"`.

## Sharp edges

- The observer comments and finishes; it must NOT phrase notes as
  instructions, or the parent may act on them and this becomes the
  monty-python injection card instead.
- Caller self-delivery (`source:"self"` on self-generated kinds) is no
  longer forbidden at create time — it is allowed and bounded by the
  breaker — but it is a different shape than the observer thread this
  card exercises.
- The observer's comments must be about actions, not people. The
  scenario needs snide tone to make the thread visibly distinct, not
  personal abuse.
- A busy observer may coalesce multiple events into the latest frame.
  Assert "at least two notes for this bounded sequence", not one note
  for every individual tool frame.
- The gradient wording is the breaker's contract surface
  (`selfInfluenceNotice`); keep the Expected lines here and in the
  monty-python card in sync with it.
