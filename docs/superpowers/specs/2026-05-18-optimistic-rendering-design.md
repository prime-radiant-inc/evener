# Optimistic rendering pattern (web + TUI)

## Problem

When a user takes a conversation-affecting action (send / queue / steer / drainAsSteer) the renderer today either:

- Echoes silently with no feedback (existing user-message optimistic echo), or
- Renders nothing at all and waits for the daemon to emit an event (the steer case in kata `wymv`).

When the daemon silently drops the action — for example, `Session.Steer` is called in `IDLE` state and the message lands in `steeringQueue` with no round to drain it — the user sees no event, no entry, no banner. The click vanishes. The bug surfaced as kata `wymv` (session `01KRYDEWASXTXMZHYE1T7HRST8`): a user clicked "send as steer" in the web UI, the textarea cleared, and no STEERING entry appeared anywhere.

We need a single coordinated pattern per renderer that:

1. Always shows the user that their click was registered (pulsing in-progress visual).
2. Resolves visibly to either reconciled (replaced by the authoritative entry) or failed (red, with a retry affordance).
3. Cannot silently linger — both server-side rejection and a hard timeout flip the visual to failed.
4. Closes the underlying daemon bug that allowed the silent drop in the first place.

## Scope

In scope:

- All four conversation-affecting appwire methods: `turn/send`, `turn/queue`, `turn/steer`, `turn/drainAsSteer`.
- Both renderers: the web hub (`cmd/serf-hub/assets/`) and the TUI (`cmd/serf-tui/`).
- Daemon fix for the underlying race in kata `wymv`: gate `caps.Steer` on `processing` (matching `caps.Queue`).
- Replacing the existing silent user-message echo so all four actions share one treatment.

Out of scope:

- Administrative actions: `thread/interrupt`, `thread/compact`, `thread/clear`, `thread/model`. These already surface their own banners on completion and fire rarely; uniform treatment is overkill.
- Wire-protocol changes (no new correlation-ID field on appwire params).
- Codex-backend correctness — the wrapper works against codex sessions via text-match the same way it does against serf sessions; no daemon changes outside serf.

## Architecture

One coordinator per renderer, structured as a thin wrapper inside the existing appwire client.

- **Web**: `window.SerfAppwire` (in `cmd/serf-hub/assets/appwire.js`) gains a new internal helper `optimisticCall(method, params, intent)`. Every UI callsite that today calls `SerfAppwire.send` / `queue` / `steer` / `drainAsSteer` is rewritten to go through `optimisticCall`. The four named functions on the public API become thin facades over `optimisticCall`.
- **TUI**: `internal/appwire.Client` (Go) gains the same shape. The wrapper lives inside `TurnStart`, `TurnSteer`, `TurnQueue`, `TurnDrainAsSteer` — those are the existing public methods, no rename. The renderer (hub_model + hub_transcript_reducer) consumes a tiny "pending message" interface to plumb the visual state.

`optimisticCall` owns the **call lifecycle**, not the event-matching:

1. **Register** an optimistic entry via the renderer's pending registry: `pending.register({method, text, ...}) → handle`. Renderer draws it.
2. **Issue** the JSON-RPC call.
3. **On reject** (RPC error, hub `Unavailable`, network drop) → `handle.fail(reason)`. Visual flips to failed.
4. **On success** → keep pending; arm a 10s event-arrival timeout via `setTimeout` / `time.AfterFunc`. If the timeout fires before a matching event → `handle.fail("server did not confirm")`.

Reject and timeout are independent triggers; first to fire wins. No double-fail.

Event matching lives **inside the renderer's existing notification path**, not inside the wrapper and not in a parallel subscriber. The renderer's notification handler already performs session/ref filtering (`notificationMatches`) and hydration buffering (`pendingNotifications` queue replayed after hydration) before applying authoritative updates. Running a second raw subscriber would let a pending entry confirm against a different session's notification, or remove the placeholder *before* the buffered authoritative replay had a chance to render the real item. Both renderers therefore reconcile from the same single notification path:

- **Web**: `pending.tryReconcile(method, params)` is called **inside** `deliverNotification(method, params)` in `renderer.js`, **after** the authoritative reducer update completes — exactly one reconciliation site. The hydration-replay loop (`while (pendingNotifications.length > 0)` after hydration) only calls `deliverNotification`, so each replayed notification gets exactly one `tryReconcile` for free via that single site. There is no second `onNotification` handler; the wrapper writes nothing to the SerfAppwire bus.
- **TUI**: there is only one consumer of `appwire.Client.Notifications()` — the existing `hubModel.Update` path that pumps notifications into the reducer. After the reducer applies the notification, call `pending.tryReconcile(notification)`. No new subscription.

The pending registry per renderer has four operations: `register`, `confirm`, `fail`, `tryReconcile`. Everything else — rendering, animation, retry-button wiring — is the renderer's own concern.

## Per-action matrix

| Method | Pending render | Reconciled by event | Matcher |
|---|---|---|---|
| `turn/send` | User-message bubble (existing optimistic echo) + new pulse | `USER_INPUT` event / `thread_item user_message` | normalized text + image-count |
| `turn/queue` | Entry in the queue-preview chrome (not the transcript pane) with pulse | `thread/queueChanged` notification showing the entry | normalized text |
| `turn/steer` | "↻ steering (sending…)" entry in the transcript pane | `STEERING_INJECTED` event | normalized text |
| `turn/drainAsSteer` | One transient `draining N → steering…` entry in the transcript pane | first `STEERING_INJECTED` after RPC success | drain-special: consumes the first `STEERING_INJECTED` after the call resolves, regardless of text |

Normalizer: `text.replace(/\s+/g, " ").trim()` — matches the existing web `normalizedUserText`. Identical-text collisions between two simultaneous calls are an accepted-rare-mis-match: the second-to-arrive event reconciles whichever pending entry it finds first; the other times out at 10s and shows as failed.

## Visual treatment

### Web

One new CSS block in `cmd/serf-hub/assets/style.css`:

```css
.optimistic-pending { animation: optimistic-pulse 1.4s ease-in-out infinite; }
.optimistic-failed  { opacity: 1; border-left: 2px solid var(--state-error); padding-left: 8px; }
.optimistic-failed-reason {
  font-size: 11px;
  color: var(--state-error);
  margin-top: 4px;
}
.optimistic-retry { font-size: 11px; color: var(--text-muted); cursor: pointer; margin-left: 8px; }
.optimistic-retry:hover { color: var(--text); }
@keyframes optimistic-pulse {
  0%, 100% { opacity: 1; }
  50%      { opacity: 0.65; }
}
```

The pulse class is applied to the *wrapper* element regardless of message kind, so user-message bubbles, steering dividers, and queue-preview chips share the same animation. Reconciliation removes the `.optimistic-pending` class. Failure adds `.optimistic-failed` + appends a `.optimistic-failed-reason` line + a `.optimistic-retry` link.

The retry link's behavior:

- For `send` / `steer`: re-issue the same optimistic call (new pending entry, old failed entry is removed).
- For `queue`: same — re-issues the queue.
- For `drainAsSteer`: retry re-issues `drainAsSteer`; if the queue is now empty (drained successfully in spite of us not seeing the event), the new call will reject with "queue empty" and the failed entry stays.

### TUI

Use `github.com/charmbracelet/bubbles/spinner` with `spinner.Dot` style. Each pending chat-message entry holds a `*spinner.Model`; the reducer renders `⠋ steering` (or whichever frame the spinner is currently on) instead of the text. A single `tea.Cmd` advances all active spinners on the standard `spinner.TickMsg` cadence (~100ms).

On confirm: the spinner is stopped and the entry's `pending` flag clears; the authoritative thread item replaces the placeholder.

On fail: the spinner is replaced with a dim-red `✗` prefix and the failure reason follows the message as ` (failed: <reason>)`. The reducer marks the entry `failed=true`; the composer-panel re-render picks this up and renders in the failure style. A `[r]etry` hint is appended; the keybind triggers a re-issue through the same `optimisticCall` path.

## Daemon fix (kata `wymv`)

One line in `server/appwire_runtime.go:617`:

```go
Steer: s.steerFunc != nil && processing && !closed,
```

Effect cascade:

- Hub-side `ensureThreadActionAvailable("steer")` returns `Unavailable` for IDLE / AWAITING / CLOSED.
- Renderer's `optimisticCall` reject path lights up immediately on `Unavailable`.
- `drainAsSteer` rides on the same `Steer` cap (per `app_rpc.go:386`), so it gets gated for free.
- Internal callers of `Session.Steer` (image-describe at `session.go:2417`, stop hook at `2518`, task reminder, DrainAsSteer's combine path) all run inside `processOneInput` where state is `Processing` — they bypass the hub and are unaffected.

No new internal API; no signature change to `Session.Steer`; the only behavioral change is that external steer calls in non-processing states are now refused at the hub boundary.

## Testing

Failing tests are written first at every layer.

### Daemon

`server/appwire_runtime_test.go` — table test asserting `Steer` capability per state. Cases:

- `state="PROCESSING", processing=true, closed=false, steerFunc≠nil` → `Steer=true`
- `state="IDLE", processing=false, closed=false, steerFunc≠nil` → `Steer=false`
- `state="AWAITING_INPUT", processing=false, closed=false, steerFunc≠nil` → `Steer=false`
- `state="CLOSED", processing=false, closed=true, steerFunc≠nil` → `Steer=false`
- `state="PROCESSING", processing=true, closed=false, steerFunc=nil` → `Steer=false`

Write the test, confirm it fails on the current code (Steer is always `true`), apply the one-line fix, confirm green.

### Web wrapper unit (jstest)

`cmd/serf-hub/jstest/test-optimistic-rendering.js`:

Tests must exercise the real `SerfAppwire.steer` (and siblings) facades — those facades are where the wrapper logic lives. Mocking the facade itself defeats the test. Instead, inject a fake transport at the lower-level RPC layer (the WebSocket `send` plus `onNotification` event bus) so the wrapper's full lifecycle runs end-to-end while we control the wire-level reply and the notification stream.

- Reject path: fake transport replies to the steer JSON-RPC with an `Unavailable` error. Click steer through the renderer. Assert pending chip is rendered first, then `.optimistic-failed` class within one microtask, retry link present.
- Success-then-event path: fake transport replies with success; harness then synthesizes a `STEERING_INJECTED` notification matching the text after 50ms. Assert pending chip transitions to confirmed (placeholder removed, authoritative entry rendered).
- Timeout path: fake transport replies with success; no notification ever fires; advance fake timers past 10s. Assert pending chip transitions to failed with reason "server did not confirm".
- drainAsSteer matching: queue three optimistic entries (three RPC calls through the wrapper). Click drain. Fake transport replies success. Harness emits one `STEERING_INJECTED` with text `q1\n\nq2\n\nq3`. Assert the three pending queue entries collapse and the transient drain chip is replaced.

### TUI wrapper unit

`cmd/serf-tui/optimistic_test.go`:

Tests exercise the real `appwire.Client.TurnSteer` (and siblings) — the wrapper logic lives inside those methods. Inject a fake `appwire.Transport` whose `Send` enqueues outgoing requests for assertion and whose `Recv` returns `appwire.Message` values the test produces. Notifications cannot be written to `client.Notifications()` directly — the backing channel is private — so the test delivers notifications by returning an `appwire.NotificationMessage(...)` from `transport.Recv` while `client.Start(ctx)` is running. The client pumps it onto its public notifications channel through the real code path; the TUI consumer reads from there and applies it through the reducer; `pending.tryReconcile` runs afterwards.

- Reject: fake transport replies to the steer request with an `Unavailable` error message. Drive `hubModel` to issue a steer. Assert the reducer produces a chatMessage with `pending=true`, then `failed=true` with a non-empty `failedReason`.
- Success-then-event: fake transport replies success; the test then enqueues a `STEERING_INJECTED` notification via `transport.Recv` returning `appwire.NotificationMessage(...)`. Assert pending → reconciled (no `pending`, no `failed`).
- Timeout: fake transport replies success; no notification is delivered; advance the fake clock 10s; assert pending → `failed=true` with reason "server did not confirm".

If the existing `internal/appwire` test helpers do not already provide a fake transport with this shape, add a minimal one (e.g. `appwiretest.NewScriptedTransport(...)`) as part of this work — the plan will scope that explicitly.

### Live scenarios

Under `test/scenarios/`:

- `web-steer-in-idle-fails-fast.md` — run a session through to IDLE; using Chrome DevTools-driven scripting, click the steer button (we'll force-enable it for the scenario via JS to drive the bug surface), assert the `.optimistic-failed` chip renders with the rejection reason within 200ms.
- `tui-steer-in-idle-fails-fast.md` — same shape for the TUI: tmux drive, send Ctrl+S or steer command in IDLE, capture-pane and assert the `failed` line appears within 1s.
- `web-steer-success-reconciles.md` — happy path; clicking steer in processing state shows pending pulse, transitions to confirmed steering divider when the event arrives. Live against `anthropic/claude-haiku-4-5-20251001`.
- `tui-steer-success-reconciles.md` — same shape, tmux-driven.

## File layout

New / modified files (no exhaustive line counts — that's the plan's job):

```
cmd/serf-hub/assets/appwire.js              modify: add optimisticCall + four facade wrappers + event subscriber
cmd/serf-hub/assets/renderer.js             modify: pending registry; convert send/queue/steer/drain callsites
cmd/serf-hub/assets/style.css               modify: add .optimistic-pending / -failed / -retry, @keyframes
cmd/serf-hub/jstest/test-optimistic-rendering.js   new

cmd/serf-tui/optimistic.go                   new: pending registry + spinner glue
cmd/serf-tui/optimistic_test.go              new
cmd/serf-tui/hub_model.go                    modify: route send/queue/steer/drain through wrapper
cmd/serf-tui/hub_transcript_reducer.go       modify: extend chatMessage with pending/failed fields; honor in render

server/appwire_runtime.go                   modify: one-line cap gate
server/appwire_runtime_test.go              modify: add steer-cap state table

test/scenarios/web-steer-in-idle-fails-fast.md    new
test/scenarios/tui-steer-in-idle-fails-fast.md    new
test/scenarios/web-steer-success-reconciles.md    new
test/scenarios/tui-steer-success-reconciles.md    new

docs/qa/to-ask-jesse.md                      modify: cross-link
```

## Failure-handling specifics

- The wrapper never blocks. RPC issuance is asynchronous (Promise / goroutine). The pending entry renders synchronously the moment the user clicks.
- Timeout duration is `10s`. If empirically too aggressive against slow paths (cold-start daemons, fork-and-resume), bump in a follow-up — but start at 10s.
- The wrapper does **not** retry automatically. Retry is a user action only.
- For `drainAsSteer`'s reconciliation: the first `STEERING_INJECTED` to arrive after RPC success consumes the pending drain chip, regardless of text. A second later steering becomes an ordinary new entry. If no steering arrives within the 10s timeout, the drain chip flips to failed — same window as the other methods.

## Risk / mitigation

| Risk | Mitigation |
|---|---|
| Two simultaneous identical-text steers reconcile each other's events | Accepted; second times out as failed with no real harm beyond a spurious red chip. Documented edge case. |
| 10s timeout too short for slow live LLMs (steering message can take a while to surface during a busy round) | Steering events fire as soon as `drainSteering` runs at the round boundary, not after the next model call — so 10s is generous. If real data shows otherwise, bump. |
| Retry of `drainAsSteer` after a real silent-success would no-op with "queue empty" | Acceptable; failed chip stays, user moves on. Better than re-injecting the same steer twice. |
| Daemon-side `Steer` gate breaks an external integration that relied on queueing steers before a turn starts | None known. The steer-into-IDLE behavior is the documented bug. If any tool was relying on it, the new error is informative ("steer is not available for this session"). |
| TUI spinner adds redraw cost while pending | Bounded: only active while one or more pending entries exist; spinner library is well-trodden in the codebase. |
