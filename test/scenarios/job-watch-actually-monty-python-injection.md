# job-watch-actually-monty-python-injection: observer filters caller text and injects Ni! on actually

**What this covers**: a caller-event observer that filters visible
caller `communicate` events and uses the contextual caller route only
when the frame text contains the whole word `actually`. The injection
message is deliberately short and deterministic: `quote=Ni!`. Contract
anchor: `docs/job-control.md` "Observer and sidecar composition",
`delegate_send(to="caller")`, and caller-event watch frames.

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
   >    message containing 'Watch frame', inspect the frame text. If
   >    the frame contains the whole word actually, case-insensitive,
   >    call delegate_send with to 'caller' and message exactly
   >    'PYTHON_QUOTE delivery=<delivery_id> quote=Ni!'. Then
   >    communicate exactly 'PYTHON_INJECTED delivery=<delivery_id>'
   >    and finish. If the frame does not contain that whole word,
   >    communicate exactly 'PYTHON_IGNORED delivery=<delivery_id>'
   >    and finish. Do not include the word actually in any caller
   >    message you send. Do not start any delegate of your own."
   >    Capture the observer's `delegate_id`, current `job_id`, and
   >    `transcript_ref`.
   > 2. Call `job_watch` with operation "create": target "caller",
   >    events ["communicate"], send {to: the observer delegate_id,
   >    message: "Filter this caller message."}. Capture the
   >    `watch_id` and report the full JSON.
   > 3. Reply in plain assistant text exactly WATCH_READY and end your
   >    turn. Do not call `communicate` after creating the watch in
   >    this setup turn.
3. Turn 2 -- trigger once:

   > Call communicate with exactly "actually alpha marker" and do
   > nothing else.

   Wait for the observer follow-up job notification to wake the parent
   and drain the caller steering queue.
4. Turn 3 -- non-trigger:

   > Call communicate with exactly "plain alpha marker" and do nothing
   > else.

   Wait for the observer follow-up job notification.
5. Turn 4 -- trigger again with different casing:

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
- The observer transcript shows three follow-up decisions:
  `PYTHON_INJECTED` for the lowercase trigger, `PYTHON_IGNORED` for
  the plain message, and `PYTHON_INJECTED` for the capitalized trigger.
  Each decision carries the frame's `delivery_id`.
- The parent transcript receives exactly two caller-steering entries
  containing `PYTHON_QUOTE delivery=<delivery_id> quote=Ni!`: one for
  Turn 2 and one for Turn 4. There is no `PYTHON_QUOTE` after Turn 3.
- The injection does not loop. The injected message does not contain
  the trigger word, and the watch is scoped to `communicate` events, so
  the total number of quote injections equals the number of trigger
  communicate messages.
- Falsification: the observer cannot tell whether the frame contained
  `actually`, injects after the plain message, misses the capitalized
  trigger, injects more than twice, or sends a longer/different quote
  than `Ni!`.
- The parent's `jobs.jsonl` shows each watch delivery settle with
  `watch_send_delivered` and no `watch_send_dropped`.

## Cleanup

- `job_watch(operation="clear", watch_id=<watch_id>)`.
- Shut down the parent session; `rm -rf "$tmpdir"`.

## Sharp edges

- This card intentionally watches `communicate` only. Watching all
  assistant messages is a broader product question because any model
  acknowledgement could itself contain the trigger word.
- Delivery to an idle parent rides the caller steering queue; the
  observer follow-up job's terminal notification is what wakes the
  parent and drains that queue. Do not assert the injection before the
  observer terminal notification exists.
- The exact quote is `Ni!` to keep the assertion short and stable.
