# job-watch-observer-snide-thread: caller events drive a snide observer's own thread without injecting into the caller

**What this covers**: a caller-session event watch delivered to an
observer delegate, where the observer comments in its own transcript
instead of steering the caller. This is the safe "snide commentary in
its own thread" shape: `job_watch(target="caller", events=[...],
send={to:<observer delegate_id>})` starts the observer on each bounded
frame, but the observer uses `communicate`, not `delegate_send(to="caller")`.
Contract anchor: `docs/job-control.md` "Observer and sidecar
composition" plus the caller-event rail.

> **Live validation 2026-07-02 (re-land):** the inform+breaker loop ran
> end-to-end on merged main (isolated hub, anthropic/claude-opus-4-6):
> observer via `delegate(watch_parent:true)` + `job_watch(source:"parent",
> events:["communicate"])` received 9 frames (12 pending lines coalesced to
> 6 distinct deliveries), injected on `actually` triggers, and each
> acknowledgement re-fired the watch with the gradient escalating
> `responded to your last message` → `~2` → `~3` → `~4 exchanges deep —
> consider disengaging`; the participants then disengaged and cleared the
> watch themselves. `serf-doctor watches` read back `breaker: bounded
> self-influence, max depth 4 (no runaway)`; `--self-loops` correctly
> returned none. NOTE: the Steps below still use the June-era public
> `send:{to:...}` watch shape; today's tool is source-owned (delivery
> implicit, `watch_parent` for observers) — modernize the steps before the
> next scripted run.


## Pre-state

- Fresh binaries from the branch under test; hub on `127.0.0.1:9180`
  (`docs/agentic-testing.md` setup checklist); credentialed model.
- `tmpdir=$(mktemp -d -t serf-e2e-snide-observer-XXXXX)`.

## Steps

1. Spawn the parent session via `/api/spawn` with
   `working_dir=$tmpdir`. Capture `SID`.
2. Prompt the parent:

   > Do these steps in order.
   > 1. Call `delegate` with `max_wait_ms` 120000 and this exact
   >    task: "You are a snide observer sidecar. First turn:
   >    communicate exactly SNIDE_READY and finish. If later started
   >    with a message containing 'Watch frame', read the delivery_id
   >    and event/trigger kind from the frame. Write one sardonic
   >    one-line comment about the observed action only; do not insult
   >    any person or group. Prefix it exactly 'SNIDE_NOTE
   >    delivery=<delivery_id> event=<event_kind> '. Use communicate
   >    only. Do not call delegate_send. Do not start any delegate of
   >    your own." Capture the observer's `delegate_id`, current
   >    `job_id`, and `transcript_ref`.
   > 2. Call `job_watch` with operation "create": target "caller",
   >    events ["assistant.tool", "communicate"], send {to: the
   >    observer delegate_id, message: "Comment in your own thread on
   >    this caller event."}. Capture the `watch_id` and report the
   >    full JSON.
   > 3. Run the shell tool in the foreground with command
   >    `printf 'MAIN_ACTION_ONE\n'`.
   > 4. Call `job_list` with no filters.
   > 5. Call communicate with exactly MAIN_ACTION_TWO and end your
   >    turn.
3. Wait for the observer follow-up jobs to finish. Because the
   observer comments only in its own thread, inspect the observer's
   transcript directly using the captured `transcript_ref` (or find the
   child `.transcript.jsonl` on disk by the ref's session id).
4. Check the parent's durable job log:
   `find ~/.local/state/serf/projects -path "*sessions/$SID/jobs.jsonl"`.

## Expected

- The observer's first turn completes with `SNIDE_READY`, so later
  watch frames start fresh follow-up jobs in the same observer
  transcript.
- The `job_watch` result has `watching: true`, a `watch_id`, target
  `"caller"`, and `send.to` equal to the observer `delegate_id`.
- The observer transcript contains at least two later `SNIDE_NOTE`
  lines, each with a non-empty `delivery=` value. The notes may refer
  to `assistant.tool` events (the shell/job_list actions), the
  `communicate` event, or both; exact count is not a contract because
  event coalescing is latest-wins while the observer is busy.
- Each `SNIDE_NOTE` is emitted through the observer's own `communicate`
  call. The observer transcript has no `delegate_send` tool call.
- The observer does not inject into the caller: its transcript has no
  `delegate_send` tool call. The parent transcript may still contain
  `SNIDE_NOTE` inside observer terminal job notifications or in a
  model acknowledgement of those notifications; those are job-output
  echoes, not caller-rail steering.
- Watch frames include `watch_id:`, `delivery_id:`, and the triggering
  event metadata needed for the observer to understand what it is
  commenting on.
- Observer lifecycle and notification traffic that carries the watch's
  own provenance IS delivered and classified self-influenced (bounded
  by the runaway fuse), not suppressed. Such self-influenced deliveries
  may appear, each carrying the `<system-reminder>` depth line.
- The parent's `jobs.jsonl` shows `watch_send_pending` followed by
  `watch_send_delivered` for the deliveries. Self-influenced deliveries
  may appear with the depth line, but because this scenario stays
  shallow the fuse never fires: there is no `watch_send_dropped` with
  `reason: runaway`.
- The parent session returns to `idle` after observer notifications
  drain. If a human or model later answers those notifications with
  `communicate`, that is another watched caller event and may create
  another bounded observer note; clear the watch before continuing a
  free-form conversation.

## Cleanup

- `job_watch(operation="clear", watch_id=<watch_id>)`.
- Shut down the parent session; `rm -rf "$tmpdir"`.

## Sharp edges

- This card configures `send.to` as a real observer delegate, not
  `"caller"`, to demonstrate the observer-in-its-own-thread pattern
  specifically. `send.to="caller"` self-delivery is no longer forbidden
  (the create-time feedback-loop forbid is gone) -- it is now allowed
  and bounded by the breaker -- but it is a different shape than the one
  this card exercises.
- The observer's comments must be about actions, not people. The
  scenario needs snide tone to make the thread visibly distinct, not
  personal abuse.
- A busy observer may coalesce multiple events into the latest frame.
  Assert "at least two notes for this bounded sequence", not one note
  for every individual tool frame.
