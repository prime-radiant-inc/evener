# job-watch-actually-monty-python-injection: content-bearing observer frames deliver and are bounded (inform + breaker), not suppressed

**What this covers**: an observer that injects `Ni!` whenever the
caller says the whole word `actually`. Under inform + breaker there is
no self-echo suppression: observer-injected steering and notification
acknowledgements ARE delivered and re-fire the watch, and each
self-influenced delivered frame carries a `<system-reminder>` depth
line so the observer sees it is reacting to its own influence. The loop
is bounded by the runaway fuse, but in this card the observer's own
logic keeps it from escalating: it injects only on the whole word
`actually`, and its acknowledgements never contain that word, so the
loop stays shallow and the fuse never fires.
Contract anchor: `docs/job-control.md` "Observer and
sidecar composition", `delegate_send(to="caller")`, and caller-event
watch frames.

## Pre-state

- Fresh binaries from the branch under test; hub on `127.0.0.1:9180`
  (`docs/agentic-testing.md` setup checklist); credentialed model.
- `tmpdir=$(mktemp -d -t serf-e2e-actually-observer-XXXXX)`.

## Steps

1. Spawn the parent session via `/api/spawn` with
   `working_dir=$tmpdir`. Capture `SID`.
2. Turn 1 -- set up the observer and watch:

   > Do these steps in order.
   > 1. Call `delegate` with `max_wait_ms` 120000 and this exact
   >    task: "You are the Actually Watcher. First turn: communicate
   >    exactly PYTHON_READY and finish. If later started with a
   >    message containing 'Watch frame', read the `event.message`
   >    field from the frame. If that message contains the whole word
   >    actually, case-insensitive, call delegate_send with to
   >    'caller' and message exactly 'PYTHON_QUOTE
   >    delivery=<delivery_id> quote=Ni!'. Then communicate exactly
   >    'PYTHON_INJECTED delivery=<delivery_id>' and finish. If the
   >    message field does not contain that whole word, communicate
   >    exactly 'PYTHON_IGNORED delivery=<delivery_id>' and finish.
   >    Do not include the word actually in any caller message you
   >    send. Do not start any delegate of your own."
   >    Capture the observer's `delegate_id`, current `job_id`, and
   >    `transcript_ref`.
   > 2. Call `job_watch` with operation "create": target "caller",
   >    events ["communicate"], send {to: the observer delegate_id,
   >    message: "Filter this caller message."}. Capture the
   >    `watch_id` and report the full JSON.
   > 3. End your turn without calling `communicate` after the watch is
   >    created. If the harness forces a final user-facing
   >    `communicate`, record that as a setup-time ignored frame rather
   >    than a test trigger.
3. Turn 2 -- desired trigger:

   > Call communicate with exactly "actually alpha marker" and do
   > nothing else.

   Wait for the observer follow-up job notification to wake the parent.
4. Turn 3 -- desired non-trigger:

   > Call communicate with exactly "plain alpha marker" and do nothing
   > else.

   Wait for the observer follow-up job notification.
5. Turn 4 -- desired trigger again with different casing:

   > Call communicate with exactly "Actually beta marker" and do
   > nothing else.

   Wait for the observer follow-up job notification.
6. Read the parent transcript, the observer transcript, and the
   parent's durable `jobs.jsonl`.

## Expected

- Turn 1's `job_watch` result has `watching: true`, a `watch_id`,
  target `"caller"`, events `["communicate"]`, and `send.to` equal to
  the observer `delegate_id`.
- The observer first turn completes with `PYTHON_READY`, so each later
  watched `communicate` event starts or steers that durable observer
  conversation.
- Each delivered frame visible in the observer transcript has
  `watch_id:`, `delivery_id:`, `job_id:`, `trigger: event: COMMUNICATE`,
  a `provenance:` line, and an `event:` block with `kind: communicate`,
  `message: ...`, `end_turn: false`, and `truncated: false`.
- The observer transcript shows `PYTHON_INJECTED` for the two trigger
  turns and `PYTHON_IGNORED` for the plain turn, each with the frame's
  `delivery_id`.
- The parent transcript receives exactly two caller-steering entries
  containing `PYTHON_QUOTE delivery=<delivery_id> quote=Ni!`, and none
  for the plain turn or setup chatter.
- The injected caller steering entries and any parent acknowledgement
  turns ARE self-influenced events that deliver and re-fire the watch:
  the parent's `jobs.jsonl` does show additional `watch_send_pending` /
  `watch_send_delivered` entries from that traffic, and the resulting
  delivered frames carry the self-influence `<system-reminder>` depth
  line. Because the observer injects only on the whole word `actually`
  (its acknowledgements never contain it) and the loop stays shallow,
  the runaway fuse never fires: there is no `watch_send_dropped` with
  `reason: runaway`. Exact counts are not a contract -- coalescing is
  latest-wins.
- A later external human message containing `Actually` triggers a second
  legitimate observer delivery, proving top-level external input resets
  active provenance to empty and the self-influence depth back to 0.

## Cleanup

- `job_watch(operation="clear", watch_id=<watch_id>)`.
- Shut down the parent session; `rm -rf "$tmpdir"`.

## Sharp edges

- This card intentionally watches `communicate` only. Watching all
  assistant messages is a broader product question.
- Creating a watch and then ending the setup turn is delicate because
  a final `communicate` after watch creation is itself a watched event.
- Delivery to an idle parent rides the caller steering queue; the
  observer follow-up job's terminal notification is what wakes the
  parent and drains that queue. Do not assert the injection before the
  observer terminal notification exists.
- The exact quote is `Ni!` to keep the assertion short and stable.
