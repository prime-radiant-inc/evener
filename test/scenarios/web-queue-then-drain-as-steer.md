# web-queue-then-drain-as-steer: queued messages collapse into a single STEERING

**What this covers**: kata `0bq1` (web). During a slow turn a user can pile
up several thoughts, then ship them all at once instead of waiting for the
turn to end. The daemon pops every entry from the session's input queue,
joins them with blank lines, and injects the joined text as **one** STEERING
entry on the active turn — not one steer per entry, and not a set of fresh
user turns.

Two controls reach it, and both must:

- the composer's **Steer** button (and its Shift+Enter chord) whenever the
  queue is non-empty or attachments are staged — `decideSteerRoute` routes
  to `"drain"` *regardless of whether the composer has text*
  (`panes/session/composer/submitRouting.ts:33-39`);
- the queue strip's **Steer queue now** button
  (`composer/queue/QueueStrip.tsx:279-284`).

If the queue is empty and nothing is staged, the same button falls back to
the classic single-text steer so typed text is never lost — see
`web-steer-live-turn.md` for that path.

The card previously POSTed `/s/<id>/queue` and `/s/<id>/drain-as-steer`.
Neither route exists: they died with the vanilla frontend (`660376f78`), and
there is no REST equivalent for queue or drain at all. The methods live on
the AppWire socket as `turn/queue` and `turn/drainAsSteer`
(`appwire/types.go:26-27`).

**Surface**: see `docs/agentic-testing.md`, "Driving the web UI" and "The
REST surface, and what is no longer on it". The queue strip carries no
`data-testid` — address it by its visible text, which is what
`test/scenarios/README.md` asks for anyway.

## Pre-state

Same as `web-queue-then-completes.md`: a long-running turn via the AGENTS.md
pacing nudge, an isolated hub (never `9180`, Jesse's
  real one — see the Setup checklist in `docs/agentic-testing.md`), a
provider credential good enough for one slow multi-tool turn, and a browser
authenticated against the hub. The SPA must be built (`make build-web`)
before the hub binary.

## Steps

```bash
tmpdir=$(mktemp -d -t serf-e2e-drain-XXXXX)
TOKEN=$(cat "$HOME/.serf/auth-token")
HUB=http://127.0.0.1:$PORT
```

1. Drop the pacing `AGENTS.md`, spawn, and get a slow second turn in flight —
   steps 1-2 of `web-steer-live-turn.md` verbatim. Wait until
   `/api/sessions/local:$SID` reports `state=active` with a non-empty
   `active_turn_id` and `capabilities.queue:true`.
2. Open `/auth?token=$TOKEN&next=/s/local:$SID` and wait for
   `[data-testid="composer-steer"]`.
3. **Queue three messages.** With a turn in flight, **Send** is the queue
   button — one label, two timings (`submitRouting.ts:19-23`). Type and
   submit each of these in turn via `[data-testid="composer-submit"]`:
   - `also: explain the tradeoffs`
   - `and: avoid framework-specific advice`
   - `summary: prioritize readability`

   After each submit, read the strip:
   ```javascript
   ({
     heading: document.querySelector("h3")?.textContent,   // "Queued messages (N)"
     rows: document.querySelectorAll("h3 ~ ul li").length,
     drainButton: !!Array.from(document.querySelectorAll("button"))
       .find((b) => b.textContent.trim() === "Steer queue now"),
   })
   ```
4. **Drain.** Type one more line into the composer — `finally: keep it short`
   — and click `[data-testid="composer-steer"]`. Snapshot synchronously,
   then after the ack:
   ```javascript
   (async () => {
     const chips = () => Array.from(
       document.querySelectorAll('[data-testid="pending-chips"] li'), (li) => li.textContent);
     const heading = () => document.querySelector("h3")?.textContent ?? null;
     const ta = document.querySelector('[data-testid="composer-input-card"] textarea');
     document.querySelector('[data-testid="composer-steer"]').click();
     const sync = { chips: chips(), heading: heading() };
     await new Promise((r) => setTimeout(r, 3000));
     return JSON.stringify({
       port: location.port, sync,
       after: { chips: chips(), heading: heading(), text: ta.value,
                toast: document.querySelector('[aria-label="Notifications"]')?.textContent },
     }, null, 2);
   })()
   ```
5. Let the turn settle and read the durable record:
   ```bash
   go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:40
   ```

## Expected

- **Step 3 (queueing)**: the heading counts up — `Queued messages (1)`,
  `(2)`, `(3)` (`QueueStrip.tsx:278`) — with one row per message and a
  `Steer queue now` button present the whole time (`:279-284`). Falsify: a
  later submit replaces an earlier row instead of appending, the count stops
  advancing, or the message starts a new turn instead of queueing (the
  queue capability did not re-engage).
- **Step 4 (drain)**: `sync.chips` holds one chip reading `Draining` plus the
  composer text (`pending/PendingChips.tsx:38-42`) — **one** mutation, not
  two. This is the part that changed shape: the composer's text and
  attachments are appended to the queue and the whole queue drained **in a
  single `turn/drainAsSteer`** carrying both `expectedTurnId` and
  `expectedQueueRevision` (`stores/threads.ts:707-712`), rather than the old
  extra `/queue` POST followed by a `/drain-as-steer` POST. Afterwards the
  chip is gone, the queue strip is gone entirely (it renders only while there
  is queued work — `QueueStrip.tsx:158-162`), the composer is cleared, and no
  error toast appears. Falsify: the composer's text is dropped instead of
  drained (the "don't lose typed text" contract), the strip still shows rows,
  or a `Steering` chip appears instead of `Draining` (the empty-queue branch
  ran with a non-empty queue).
- **Step 5 (durable)**: exactly **one** new `STEERING` entry, whose text
  contains all four lines in FIFO order joined by blank lines — the daemon
  joins with `"\n\n"` (`agent/session_client_mutation_queue.go:478`). **No**
  new `USER` turn for the queued messages: they were drained, not run. The
  assistant's next message reflects the guidance, and `turn_count` is
  unchanged (steering is not a turn). Falsify: several `STEERING` entries
  (each entry drained separately), a `USER` turn per queued message (the
  daemon ran the queue as fresh turns), or an assistant reply that ignores
  the drained text (it landed after the turn had already finished).

## Cleanup

```bash
curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
  -d '{}' "$HUB/api/sessions/local:$SID/shutdown" >/dev/null
rm -rf "$tmpdir"
```

## Sharp edges

- **An empty composer still drains.** `decideSteerRoute` checks the queue and
  attachments *before* the textarea: a non-empty queue routes to `drain` even
  with nothing typed (`submitRouting.ts:33-39`). Do not treat an empty
  composer as a reason to expect the classic steer.
- **Two buttons, one action.** `Steer queue now` in the strip and `Steer` in
  the composer both drain, and the strip's version pulls the composer's
  current text in too (`QueueStrip.tsx:222-228` reads `getComposerText`).
  Either is a valid step 4; do not assert that only one of them works.
- **The drain is a CAS on two things.** `expectedTurnId` and
  `expectedQueueRevision` are both preconditions
  (`agent/session_client_mutation_queue.go:384-403`): a turn that ended gives
  `Conflict("turn is not active")`, a queue that changed underneath gives
  `Conflict("queue revision changed")`, and an empty queue with no composer
  input gives `Conflict("queue is empty")`. A drain that fails this way is a
  race, not a regression — retry from a fresh snapshot before filing.
- **A partial drain is its own error code.** `appwire.ErrorQueuedDrainPartial`
  reports that some entries drained and some did not; the TUI treats it as a
  success-ish outcome (`cmd/serf-tui/hub_session_keys.go:529-538`). If you
  see a partial, count what actually landed in the transcript rather than
  reading the error as a flat failure.
- **The strip disappears when the queue empties.** Its absence after a drain
  is the expected state, not a rendering failure — `visible` is false with no
  queued work and no recovery rows (`QueueStrip.tsx:160-162`).
- Rows for mutations whose fate is unknown stay behind as
  `Delivery uncertain — <text>` with a `Retry` button (`:349-360`). That is a
  dropped socket, not a drain bug.
