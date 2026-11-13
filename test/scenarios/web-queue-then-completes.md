# web-queue-then-completes: type during a turn, queued message runs after

**What this covers**: kata `111a` (web). While a turn is in flight the
composer's **Send** routes to the queue instead of starting a turn; the
pending stack shows what is waiting; and when the active turn completes the daemon
pops the head of the queue and runs it as a **fresh user turn** — not as a
steer. The user-facing complement to the agent-level
`TestSession_Enqueue_DrainsAfterTurnCompletes` unit (kata `111a` backend).

The card previously read `data-capability-send`/`data-capability-queue` off a
server-rendered button and POSTed `/s/<id>/queue`. All three are gone with the
vanilla frontend (`660376f78`): there is no queue REST route, the composer is
a React component, and the send/queue decision is now made client-side by a
pure function.

**Surface**: see `docs/developing-evener/agentic-testing.md`, "Driving the web UI" and "The
REST surface, and what is no longer on it".

## Pre-state

- Hub running on an isolated `$HOME` and free port (never `9180`,
  Jesse's real one — see the Setup checklist in
  `docs/developing-evener/agentic-testing.md`) with `--evener` resolvable.
- A provider credential good enough for one slow multi-tool turn.
- `$HOME/.evener/auth-token` readable (that isolated `$HOME`).
- The SPA built (`make build-web`) **before** the hub binary.

## Steps

```bash
tmpdir=$(mktemp -d -t evener-e2e-queue-XXXXX)
TOKEN=$(cat "$HOME/.evener/auth-token")
HUB=http://127.0.0.1:$PORT
```

1. Drop the pacing `AGENTS.md`, spawn, and get a slow turn in flight — steps
   1-2 of `web-steer-live-turn.md` verbatim.
2. **Server-side gate check (no browser)**: while the turn runs,
   ```bash
   curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
     | jq '{state, active_turn_id, send: .capabilities.send, queue: .capabilities.queue}'
   ```
3. Open `/auth/$TOKEN?next=/s/local:$SID` and wait for
   `[data-testid="composer-submit"]`.
4. **Queue a message.** Type `then: write a haiku about Go testing` and press
   the submit chord — `Enter` when the `evener.prefs.enterToSend` preference is
   on, `⌘/Ctrl+Enter` otherwise (`Composer.tsx:389`) — or just click
   `[data-testid="composer-submit"]`. Then read the stack:
   ```javascript
   ({
     port: location.port,
     rows: Array.from(document.querySelectorAll('[data-testid="pending-message-row"]'),
                      (li) => li.textContent),
     states: Array.from(document.querySelectorAll('[data-testid="pending-message-row"]'),
                        (li) => li.dataset.state),
     text: document.querySelector('[data-testid="composer-input-card"] textarea').value,
     rowActions: Array.from(document.querySelectorAll('[data-testid="pending-message-row"] button'),
                            (b) => b.getAttribute("aria-label") ?? b.textContent),
   })
   ```
5. **Let the turn finish.** Poll `/api/sessions/local:$SID` until `state` goes
   back to `active` (the queued message running) and then to `idle`, and watch
   `turn_count`.
6. **Read the durable record**:
   ```bash
   go run ./cmd/evener doctor transcript "$SID" --format outline --range last:30
   ```

## Expected

- **Step 2 (server gate)**: `state=active`, `capabilities.send:false`,
  `capabilities.queue:true` — Send is status-computed ("no turn in flight"),
  while Queue advertises harness support and is true whenever the daemon wires a
  queue seam and the thread is not closed (`server/appwire_thread_projection.go`'s
  `appCapabilitiesLocked`).
  Falsify: `queue:false` while active — either the harness wired no queue or
  `Capabilities.Queue` stopped being threaded through the
  appwire thread capabilities, and
  every browser assertion below is moot.
- **Step 4 (queue submit)**: the stack appears
  (`pending/PendingMessageStack.tsx:41-49`) with one row per identity
  (`:50-69`) carrying the message text and the `Queued, sends when this turn
  ends` caption (`pending/pendingMessageCaption.ts:17-18`); the composer is
  cleared; and the row's footer offers `Steer now`, `Edit message` and
  `Remove from queue` (`pending/PendingMessageStack.tsx:96-102`). The same
  row set comes from `usePendingMessages`, which merges the queue entry with
  the local record by `clientMutationId`
  (`composer/queue/pendingTurnsStore.ts:487-546`,
  `state/mutation/pendingMessages.ts:426-516`). Falsify: the message starts
  a new turn instead of queueing (the routing table regressed —
  `decideSubmitRoute` puts queue ahead of send whenever it is available,
  `submitRouting.ts:19-23`), the stack never appears, or the daemon rejects
  the queue with a conflict (the session was not really active).
- **Step 5 (drain)**: after the original turn settles the daemon pops the
  queue head and runs it as a fresh turn — `state` returns to `active`, then
  `idle`, and `turn_count` advances by exactly one. The stack empties and
  then disappears entirely (it renders only while a row is shown,
  `pending/PendingMessageStack.tsx:36-39`). Falsify: `turn_count` does not
  advance (the queued message was dropped), the stack keeps its row after
  the queued message has plainly run, or the new turn starts before the
  original finishes (the daemon's outer loop did not wait for the queue
  drain point).
- **Step 6 (transcript)**: the queued text appears as a `USER` turn, **not**
  as `STEERING`, and the assistant's reply to it answers the queued
  instruction. In the browser the same distinction is visible: a queued
  message that ran is an ordinary
  `[data-testid="user-message-item"][data-opens-exchange="true"]`, whereas a
  steer is the same test id with the attribute **absent**
  (`transcript/messages/UserMessageItem.tsx:98,112`,
  `messages/SteeringItem.tsx:124-127`). Falsify: the queued text lands as
  `STEERING` (a drain fired instead of the normal queue drain — see
  `web-queue-then-drain-as-steer.md`), or it never reaches the transcript at
  all.

## Cleanup

```bash
curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
  -d '{}' "$HUB/api/sessions/local:$SID/shutdown" >/dev/null
rm -rf "$tmpdir"
```

## Sharp edges

- **Send keeps one label in every state.** It does not flip to "Queue" — a
  button that means two things depending on when you looked was the thing
  this design rejected (`Composer.tsx:383-388`). Only the tooltip says which
  timing applies, and the tooltip is portalled in on hover/focus only
  (`widgets/tooltip/index.tsx:186-192`), so do not assert on it without
  hovering. The stack's row count is what shows the effect.
- **The queue window opens on the status flip alone.** Send/queue
  availability keys on `statusType === "active"` and deliberately does *not*
  wait for `activeTurnId` (`appwire-client/typescript/sendQueueAvailability.ts` header);
  steer/interrupt key on the same status (`submitRouting.ts`'s `isTurnActive`),
  and `activeTurnId` is transcript-row metadata for all of them. So a message
  typed in the gap between `thread/status/changed` and `turn/started` queues
  correctly instead of bouncing off the daemon as a conflict. Do not "fix" a card that queues
  in that window.
- **Queued and steered messages render differently on purpose.** Queued text
  becomes a real user turn (attribute present); a steer is mid-turn and
  carries no exchange boundary (attribute absent). Asserting on
  `[data-testid="steering-item"]` finds neither — that element is the
  *daemon*-steering divider.
- **Rows can lose their actions.** `Steer now` / `Edit message` /
  `Remove from queue` need the daemon to report per-entry ids and full texts;
  an older or degraded daemon reports neither, so `Steer now` is not offered
  (`pending/PendingMessageStack.tsx:96-102`), `Edit message` is withheld until
  the row carries complete text (`pending/usePendingMessageActions.ts:362-373`),
  and a press without entry ids returns without acting
  (`pending/usePendingMessageActions.ts`, `:137-174`). That is a
  daemon-capability signal, not a UI bug.
- **The stack's absence is a state, not a failure.** No pending rows means
  the component returns `null` (`pending/PendingMessageStack.tsx:39`).
