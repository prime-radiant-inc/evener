# job-watch-actually-monty-python-injection: content-bearing observer frames deliver and are bounded (inform + breaker), not suppressed

**What this covers**: an observer that injects `Ni!` whenever the
caller says the whole word `actually`. Under inform + breaker there is
no self-echo suppression: observer callbacks and the caller's
acknowledgements ARE delivered and re-fire the watch, and each
self-influenced delivered frame carries a `<system-reminder>` depth
line so the observer sees it is reacting to its own influence. The loop
is bounded by the runaway fuse, but the gradient line is designed to
make the participants stand down first — this card's PASS run ended
with the agents disengaging and clearing the watch themselves.
Contract anchor: `docs/job-control.md` "Observer and sidecar
composition", `delegate(watch_parent:true)`, `job_watch(source:"parent")`,
and the observer callback (`communicate(end_turn:true)`).

## Pre-state

- Fresh binaries from the branch under test; hub reachable (own hub via
  `docs/agentic-testing.md`, or an isolated `$HOME` hub on a spare port —
  the PASS run below used the isolated form); credentialed model.
- `tmpdir=$(mktemp -d -t serf-e2e-actually-observer-XXXXX)`.

## Steps

1. Spawn the parent session via `/api/spawn` with
   `working_dir=$tmpdir`. Capture the session `ref`.
2. Turn 1 -- set up the observer (the observer owns the watch):

   > Do these steps in order.
   > 1. Call `delegate` with `watch_parent` true, `max_wait_ms` 120000,
   >    and this exact task: "You are the Actually Watcher observer.
   >    First: call job_watch with operation 'create', source 'parent',
   >    events ['communicate']. Then communicate exactly PYTHON_READY
   >    and finish. When later resumed with a message containing 'Watch
   >    frame', read the event message text in the frame. If it contains
   >    the whole word actually (case-insensitive), call communicate
   >    with end_turn true and message exactly 'PYTHON_QUOTE quote=Ni!'
   >    and finish. Otherwise call communicate with end_turn true and
   >    message exactly 'PYTHON_IGNORED' and finish. Never include the
   >    word actually in your own messages."
   > 2. After the delegate result reports `watching: true`, end your
   >    turn.
3. Turn 2 -- desired trigger (wait for the parent to go idle first):

   > Call communicate with exactly "actually alpha marker" and do
   > nothing else.

   Wait for the observer callback to wake the parent.
4. Turn 3 -- desired non-trigger:

   > Call communicate with exactly "plain alpha marker" and do nothing
   > else.

5. Turn 4 -- desired trigger again with different casing:

   > Call communicate with exactly "Actually beta marker" and do
   > nothing else.

6. Read the parent transcript, the observer transcript, the parent's
   durable `jobs.jsonl`, and `serf-doctor watches <parent SID>
   --state-dir <state base>` (plus `--self-loops`).

## Expected

- Turn 1's delegate result reports the observer ready (`PYTHON_READY`)
  with `watching: true` and the installed watch listed; the public
  watch shape is source-owned (no `send` field — delivery to the
  watch-owning observer is implicit).
- Each delivered frame visible in the observer transcript has
  `watch_id:`, `delivery_id:`, `trigger: event: COMMUNICATE`, a
  `provenance:` line, and an `event:` block with `kind: communicate`
  and the caller's message text.
- The observer answers trigger frames with the `PYTHON_QUOTE quote=Ni!`
  callback and non-trigger frames with `PYTHON_IGNORED`.
- The `PYTHON_QUOTE` callbacks steer the parent; the parent's
  acknowledgements are themselves watched `communicate` events and
  RE-FIRE the watch (delivered + classified, not suppressed): the
  parent's `jobs.jsonl` shows the extra `watch_send_pending` /
  `watch_send_delivered` traffic, and the observer's later frames carry
  the escalating self-influence `<system-reminder>` line
  (`responded to your last message`, then `~N exchanges deep —
  consider disengaging`).
- The loop stays bounded WITHOUT the fuse: no `watch_send_dropped` with
  `diagnostic_reason: "runaway"`. `serf-doctor watches` reports
  `breaker: bounded self-influence, max depth N (no runaway)` with
  per-delivery `depth=` stamps; `--self-loops` returns no watches.
- Exact frame counts are not a contract — coalescing is latest-wins
  while the observer is busy.

## Observed (PASS, 2026-07-02, merged main, anthropic/claude-opus-4-6)

Isolated-`$HOME` hub. The observer received 9 frames (12 pending lines
coalesced to 6 distinct deliveries), injected on both `actually`
triggers, ignored the plain marker, and each acknowledgement re-fired
the watch with the gradient escalating `responded to your last message`
→ `~2` → `~3` → `~4 exchanges deep — consider disengaging`. The
participants then disengaged and CLEARED the watch themselves (watch
`ended: cleared`), before the depth-8 fuse. `serf-doctor watches` read
back `breaker: bounded self-influence, max depth 4 (no runaway)` with
depth stamps 1–4 on the deliveries; `--self-loops` returned
`no watches where the runaway fuse fired`.

## Cleanup

- `job_watch(operation="clear", watch_id=<watch_id>)` if the
  participants have not already cleared it.
- Shut down the parent session; `rm -rf "$tmpdir"`.

## Sharp edges

- This card intentionally watches `communicate` only. Watching all
  assistant messages is a broader product question.
- The observer must create its watch BEFORE communicating readiness, or
  the readiness result cannot report `watching: true`.
- Delivery to an idle parent rides the observer-callback path; wait for
  the parent to return to `idle` between turns instead of asserting on
  wall-clock.
- The exact quote is `Ni!` to keep the assertion short and stable.
- The gradient wording is the breaker's contract surface
  (`selfInfluenceNotice`); if its text changes, update the Expected
  lines here and in the snide card together.
