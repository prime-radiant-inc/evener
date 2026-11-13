# web-steer-success-reconciles: the optimistic steer row appears instantly and clears on the ack

**What this covers**: the success side of the optimistic-mutation pattern
(kata `wymv`). A steer must be visible **before** the server has agreed to it
— the reader gets feedback at click time, not at round-trip time — and that
placeholder must then disappear exactly once the authoritative record lands,
leaving no duplicate and no orphan.

The end-to-end steer is covered by `web-steer-live-turn.md`; this card adds
the timing check that only a no-await snapshot can make: *the row was there
synchronously*, and *it is gone afterwards*. Neither half is falsifiable
without the other — a card that only looks after the ack cannot tell
"rendered and reconciled" from "never rendered".

The mechanism moved with the frontend rewrite (`660376f78`), and moved in a
way that matters:

- The placeholder is no longer a `details.steering` chip spliced into the
  conversation. It is the pending stack's trailing row —
  `[data-testid="pending-message-row"]`, one row per identity, whose status
  caption carries the state (`Sending`, `Delivers when this step finishes`,
  …) (`pending/PendingMessageStack.tsx:50-69`,
  `pending/pendingMessageCaption.ts:12-40`). The authoritative entry renders
  in the transcript, and the row retires when a transcript item carries the
  same identity (`state/mutation/pendingMessages.ts:346-351,380`), so the old
  "duplicate divider" failure mode cannot reappear.
- Reconciliation is no longer a text match. The row retires when its
  `clientMutationId` comes back — a queue row on `thread/queueChanged`, or a
  transcript item on `evener/steering/injected` or an `item/*` / `turn/*`
  frame (`stores/threads.ts:6619-6642`), collected by
  `reflectedMutationIds` (`state/mutation/pendingEntries.ts:229-238`). So
  the old text-normalize-mismatch failure mode is gone too; an id that never
  comes back is the failure now.

**Surface**: see `docs/developing-evener/agentic-testing.md`, "Driving the web UI" —
specifically "Synchronous vs. async assertion shape", which is this card's
whole method.

## Pre-state

- Hub running on an isolated `$HOME` and free port (never `9180`,
  Jesse's real one — see the Setup checklist in
  `docs/developing-evener/agentic-testing.md`) with `--evener` resolvable.
- Anthropic OAuth or API key configured.
- `$HOME/.evener/auth-token` readable (that isolated `$HOME`).
- The SPA built (`make build-web`) **before** the hub binary.
- A browser. This card is entirely about what the browser renders and when;
  there is no REST-level substitute for it.

## Steps

```bash
tmpdir=$(mktemp -d -t evener-e2e-steer-ok-XXXXX)
TOKEN=$(cat "$HOME/.evener/auth-token")
HUB=http://127.0.0.1:$PORT
```

1. **Drop a pacing `AGENTS.md`** into the workspace so the turn stays in
   flight long enough to click Steer — see "AGENTS.md pacing trick" in
   `docs/developing-evener/agentic-testing.md`, and steps 1-2 of `web-steer-live-turn.md` for
   the spawn/second-turn sequence that reliably produces a slow turn.
2. Wait until `/api/sessions/local:$SID` reports `state=active` with a
   non-empty `active_turn_id`.
3. Open `/auth/$TOKEN?next=/s/local:$SID` and wait for
   `[data-testid="composer-steer"]` — the button renders only while the turn
   is genuinely in flight (`Composer.tsx:382`), so waiting on it is the
   hydration check.
4. **Fire and snapshot without awaiting.** The synchronous read is the
   assertion; everything after it is confirmation:
   ```javascript
   (async () => {
     const rows = () => Array.from(
       document.querySelectorAll('[data-testid="pending-message-row"]'),
       (li) => li.textContent);
     const steers = () => document.querySelectorAll(
       '[data-testid="user-message-item"]:not([data-opens-exchange])').length;
     const before = { rows: rows(), steers: steers() };
     const ta = document.querySelector('[data-testid="composer-input-card"] textarea');
     const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value").set;
     setter.call(ta, "Stop and write a haiku instead.");
     ta.dispatchEvent(new Event("input", { bubbles: true }));
     document.querySelector('[data-testid="composer-steer"]').click();
     const sync = { rows: rows(), steers: steers() };      // no await: right now
     await new Promise((r) => setTimeout(r, 3000));
     const after = {
       rows: rows(),
       steers: steers(),
       toast: document.querySelector('[aria-label="Notifications"]')?.textContent,
     };
     return JSON.stringify({ port: location.port, before, sync, after }, null, 2);
   })()
   ```
5. Let the turn settle and confirm the model actually obeyed:
   ```bash
   go run ./cmd/evener doctor transcript "$SID" --format outline --range last:20
   ```

## Expected

- **`before`**: no pending rows; note the steer count as the baseline.
- **`sync` (the point of the card)**: exactly one pending row, whose text
  contains the steer text and whose caption carries the in-flight state
  (`pending/PendingMessageStack.tsx:50-69`,
  `pending/pendingMessageCaption.ts:12-40`). Falsify: `sync.rows` is empty
  — the optimistic render never happened and the reader stares at an
  unchanged composer until the round trip completes. This is the exact
  regression kata `wymv` was filed for.
- **`after`**: `rows` is empty again — the row retired when its
  `clientMutationId` came back — and `steers` is `before.steers + 1`: the
  authoritative entry is in the transcript. `toast` is empty. Falsify:
  - a row is still present after 3s → the ack never echoed the id;
    reconciliation is broken (check `evener/steering/injected` in the hub log
    before blaming the store);
  - `steers` did not increase → the placeholder cleared without an
    authoritative entry replacing it, which is worse than a stuck row: the
    UI now claims a steer that may not exist;
  - `steers` increased by 2 → something is rendering both the optimistic and
    the authoritative copy into the transcript.
- **Step 5**: exactly one `STEERING` entry for this steer, and the model's
  closing output honours it (a haiku, not the essay it was writing).

## Cleanup

```bash
curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
  -d '{}' "$HUB/api/sessions/local:$SID/shutdown" >/dev/null
rm -rf "$tmpdir"
```

## Sharp edges

- **The row is the transcript's live edge, not a transcript item.**
  `PendingMessageStack` mounts at the conversation's trailing row,
  deliberately (`ConversationBody.tsx:283-303`), rather than injecting an
  optimistic row into the turn list. Looking for the placeholder among the
  transcript items finds nothing and reads as "the optimistic path is
  broken".
- **A human steer renders as `user-message-item` with no
  `data-opens-exchange`**, not as `[data-testid="steering-item"]` — the
  latter is the daemon-steering divider
  (`transcript/messages/SteeringItem.tsx:77`).
- **Do not `await` the click.** `.click()` returns immediately, but an
  `await` of any kind — even `await Promise.resolve()` — yields to the
  microtask queue and can let the ack land before the "synchronous" read.
  Take `sync` on the same tick, as the snippet does.
- **Queue depth and staged attachments change the method.** With either
  present the same button routes to `turn/drainAsSteer` and the row's
  caption reflects a steering row (`submitRouting.ts:33-39`,
  `pending/pendingMessageCaption.ts:12-40`). Start from an empty composer
  state.
- **The `blockedUnknown` case looks different on purpose.** A mutation whose
  fate the client could not determine is a pending row with the
  `Delivery uncertain` caption and a `Retry` action
  (`pending/pendingMessageCaption.ts:23-24`,
  `pending/PendingMessageStack.tsx:103-108`). Seeing that instead of a
  cleared row means the socket dropped mid-flight, not that reconciliation
  is broken.
