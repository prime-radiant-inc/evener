# web-queue-then-completes: type during a turn, queued message runs after

**What this covers**: kata `111a` (web). While a turn is in flight the
composer's **Send** routes to the queue instead of starting a turn; the queue
strip shows what is waiting; and when the active turn completes the daemon
pops the head of the queue and runs it as a **fresh user turn** — not as a
steer. The user-facing complement to the agent-level
`TestSession_Enqueue_DrainsAfterTurnCompletes` unit (kata `111a` backend) and
to the TUI scenario.

The card previously read `data-capability-send`/`data-capability-queue` off a
server-rendered button and POSTed `/s/<id>/queue`. All three are gone with the
vanilla frontend (`660376f78`): there is no queue REST route, the composer is
a React component, and the send/queue decision is now made client-side by a
pure function.

**Surface**: see `docs/agentic-testing.md`, "Driving the web UI" and "The
REST surface, and what is no longer on it".

## Pre-state

- Hub running on an isolated `$HOME` and free port (never `9180`,
  Jesse's real one — see the Setup checklist in
  `docs/agentic-testing.md`) with `--serf` resolvable.
- A provider credential good enough for one slow multi-tool turn.
- `$HOME/.serf/auth-token` readable (that isolated `$HOME`).
- The SPA built (`make build-web`) **before** the hub binary.

## Steps

```bash
tmpdir=$(mktemp -d -t serf-e2e-queue-XXXXX)
TOKEN=$(cat "$HOME/.serf/auth-token")
HUB=http://127.0.0.1:$PORT
```

1. Drop the pacing `AGENTS.md`, spawn, and get a slow turn in flight — steps
   1-2 of `web-steer-live-turn.md` verbatim.
2. **Server-side gate check (no browser)**: while the turn runs,
   ```bash
   curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
     | jq '{state, active_turn_id, send: .capabilities.send, queue: .capabilities.queue}'
   ```
3. Open `/auth?token=$TOKEN&next=/s/local:$SID` and wait for
   `[data-testid="composer-submit"]`.
4. **Queue a message.** Type `then: write a haiku about Go testing` and press
   the submit chord — `Enter` when the `serf.prefs.enterToSend` preference is
   on, `⌘/Ctrl+Enter` otherwise (`Composer.tsx:389`) — or just click
   `[data-testid="composer-submit"]`. Then read the strip:
   ```javascript
   ({
     port: location.port,
     heading: document.querySelector("h3")?.textContent,   // "Queued messages (1)"
     rows: Array.from(document.querySelectorAll("h3 ~ ul li"), (li) => li.textContent),
     text: document.querySelector('[data-testid="composer-input-card"] textarea').value,
     chips: document.querySelectorAll('[data-testid="pending-chips"] li').length,
     rowActions: Array.from(document.querySelectorAll("h3 ~ ul li button"),
                            (b) => b.getAttribute("aria-label") ?? b.textContent),
   })
   ```
5. **Let the turn finish.** Poll `/api/sessions/local:$SID` until `state` goes
   back to `active` (the queued message running) and then to `idle`, and watch
   `turn_count`.
6. **Read the durable record**:
   ```bash
   go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:30
   ```

## Expected

- **Step 2 (server gate)**: `state=active`, `capabilities.send:false`,
  `capabilities.queue:true` — the hub gates Send on "no turn in flight" and
  Queue on "a turn in flight" (`server/appwire_runtime.go:1046,1055`).
  Falsify: `queue:false` while active — either the harness wired no queue or
  `Capabilities.Queue` stopped being threaded through
  `hubCapabilitiesFromAppwire` (`cmd/serf-hub/web_api_tree.go:792-800`), and
  every browser assertion below is moot.
- **Step 4 (queue submit)**: the strip appears with heading
  `Queued messages (1)` (`composer/queue/QueueStrip.tsx:278`) and one row
  carrying the message text; the composer is cleared; `pending-chips` is
  **empty** — queue mutations are deliberately excluded from that strip
  because `QueueStrip` already chips them
  (`panes/session/pending/PendingChips.tsx:22-28`); and the row offers
  `Steer now`, `Edit message` and `Remove from queue`
  (`QueueStrip.tsx:305-333`). Falsify: the message starts a new turn instead
  of queueing (the routing table regressed —
  `decideSubmitRoute` puts queue ahead of send whenever it is available,
  `submitRouting.ts:19-23`), the strip never appears, or the daemon rejects
  the queue with a conflict (the session was not really active).
- **Step 5 (drain)**: after the original turn settles the daemon pops the
  queue head and runs it as a fresh turn — `state` returns to `active`, then
  `idle`, and `turn_count` advances by exactly one. The strip empties and
  then disappears entirely (it renders only while there is queued work,
  `QueueStrip.tsx:158-162`). Falsify: `turn_count` does not advance (the
  queued message was dropped), the strip keeps its row after the queued
  message has plainly run, or the new turn starts before the original
  finishes (the daemon's outer loop did not wait for the queue drain point).
- **Step 6 (transcript)**: the queued text appears as a `USER` turn, **not**
  as `STEERING`, and the assistant's reply to it answers the queued
  instruction. In the browser the same distinction is visible: a queued
  message that ran is an ordinary
  `[data-testid="user-message-item"][data-opens-exchange="true"]`, whereas a
  steer is the same test id with the attribute **absent**
  (`transcript/messages/UserMessageItem.tsx:98,112`,
  `messages/SteeringItem.tsx:143-146`). Falsify: the queued text lands as
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
  hovering. The strip's depth is what shows the effect.
- **The queue window opens on the status flip alone.** Send/queue
  availability keys on `statusType === "active"` and deliberately does *not*
  wait for `activeTurnId` (`protocol/sendQueueAvailability.ts` header) —
  unlike steer/interrupt, which need both. So a message typed in the gap
  between `thread/status/changed` and `turn/started` queues correctly instead
  of bouncing off the daemon as a conflict. Do not "fix" a card that queues
  in that window.
- **Queued and steered messages render differently on purpose.** Queued text
  becomes a real user turn (attribute present); a steer is mid-turn and
  carries no exchange boundary (attribute absent). Asserting on
  `[data-testid="steering-item"]` finds neither — that element is the
  *daemon*-steering divider.
- **Rows can lose their actions.** `Steer now` / `Edit message` /
  `Remove from queue` need the daemon to report per-entry ids and texts; an
  older or degraded daemon reports neither and the buttons render disabled
  with a reason (`QueueStrip.tsx:296-300`). Disabled row actions are a
  daemon-capability signal, not a UI bug.
- **The strip's absence is a state, not a failure.** No queued work and no
  recovery rows means the component returns `null`.
