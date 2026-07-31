# web-steer-in-idle-fails-fast: steering a session with no active turn fails visibly, not silently

**What this covers**: the reject side of the optimistic-mutation pattern
(kata `wymv`), retargeted onto the React composer. Steering only means
something while a turn is in flight, and a steer aimed at a session with no
active turn must be refused **loudly and immediately** at both layers that
can refuse it — the composer, before any request leaves the browser, and the
daemon, if a stale or racy caller gets a request out anyway. The failure this
card exists to catch is a *silent* one: text that disappears, a chip that
spins forever, or a steer quietly accepted against an idle session.

The card used to assert a `.optimistic-failed` chip carrying a Retry link,
driven by calling `window.SerfAppwire.steer(...)` directly. Both are gone —
the vanilla frontend was deleted at `660376f78` and there is no global to
call. The two refusals themselves are alive, and are what this card now
drives.

**Surface**: see `docs/agentic-testing.md`, "Driving the web UI" and "The
REST surface, and what is no longer on it". Note there is no REST route for
steer at all; the only non-browser way to reach it is the AppWire socket,
which Part A uses.

## Pre-state

- Hub running on an isolated `$HOME` and free port (never `9180`,
  Jesse's real one — see the Setup checklist in
  `docs/agentic-testing.md`) with `--serf` resolvable.
- A provider credential good enough to run one trivial turn. Anthropic Haiku
  is enough; no OAuth is required.
- `$HOME/.serf/auth-token` readable (that isolated `$HOME`).
- Part B only: the SPA must be built (`make build-web`) before the hub, or
  the browser gets a placeholder.

## Steps

```bash
tmpdir=$(mktemp -d -t serf-e2e-steer-idle-XXXXX)
TOKEN=$(cat "$HOME/.serf/auth-token")
HUB=http://127.0.0.1:$PORT
```

1. **Spawn a short session and let it reach idle.** Haiku plus a trivial
   prompt finishes in seconds:
   ```bash
   resp=$(curl -s -X POST -H "Content-Type: application/json" \
     -H "Authorization: Bearer $TOKEN" \
     -d "{\"prompt\":\"please run \\\"echo hello\\\" via exec_command then stop\",\"model\":\"anthropic/claude-haiku-4-5-20251001\",\"working_dir\":\"$tmpdir\",\"harness\":\"serf\",\"branch\":\"\",\"access_mode\":\"full\",\"agent\":\"default\",\"launch_overrides\":{}}" \
     $HUB/api/spawn)
   SID=$(echo "$resp" | jq -r '.session_id')
   for i in $(seq 1 60); do
     detail=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID")
     state=$(echo "$detail" | jq -r '.state // ""')
     [ "$state" = "idle" ] && break
     sleep 1
   done
   echo "$detail" | jq '{state, active_turn_id, steer: .capabilities.steer, queue: .capabilities.queue}'
   ```

### Part A — the daemon's refusal (no browser)

2. Dial `ws://127.0.0.1:$PORT/rpc` with `Authorization: Bearer $TOKEN`,
   `initialize`, then send two `turn/steer` requests against the idle
   session and record the JSON-RPC error of each:
   - with `expectedTurnId` omitted entirely, and
   - with `expectedTurnId` set to a plausible-but-stale turn id (the id of
     the finished turn, or any non-empty string).

   Both need `clientMutationId` set to a fresh unique value. The request
   shape is `appwire.TurnSteerParams`; the method constant is
   `MethodTurnSteer = "turn/steer"` (`appwire/types.go:24`). Send frames as
   `{"id":N,"method":…,"params":…}` with **no `jsonrpc` field** — see Sharp
   edges.

   An idle session to aim this at costs nothing: spawn with an empty
   `prompt` and the daemon launches without running a turn (a dormant
   session, which reports `state:"idle"` — `hubapi/types.go:115-119`), so no
   provider credential is needed and no completion request is ever made.

### Part B — the composer's refusal (browser)

3. Navigate to `/auth?token=$TOKEN&next=/s/local:$SID` and wait for
   `[data-testid="composer-input-card"]`.
4. Type text into the composer, then press **Shift+Enter** — the Steer
   chord. This is deliberate: the Steer *button* does not render at all in an
   idle session (`showSteer = busy && capabilities.steer`,
   `panes/session/composer/Composer.tsx:382`), but the chord reaches
   `handleSteerClick` regardless (`:685-689`), which is exactly the stale
   caller this card cares about.
   ```javascript
   ```javascript
   // A steer the human typed renders as a user-message item WITHOUT
   // data-opens-exchange (SteeringItem.tsx:143-146, UserMessageItem.tsx:98,112) -
   // NOT as [data-testid="steering-item"], which is the daemon-steering divider.
   const steerCount = () => document.querySelectorAll(
     '[data-testid="user-message-item"]:not([data-opens-exchange])').length;
   ```
   ```javascript
   (async () => {
     const before = document.querySelectorAll(
       '[data-testid="user-message-item"]:not([data-opens-exchange])').length;
     const ta = document.querySelector('[data-testid="composer-input-card"] textarea');
     const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value").set;
     setter.call(ta, "this steer should fail visibly");
     ta.dispatchEvent(new Event("input", { bubbles: true }));
     ta.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", shiftKey: true, bubbles: true }));
     await new Promise((r) => setTimeout(r, 1000));
     return JSON.stringify({
       port: location.port,
       steerButton: !!document.querySelector('[data-testid="composer-steer"]'),
       toast: document.querySelector('[aria-label="Notifications"]')?.textContent,
       textAfter: ta.value,
       chips: document.querySelectorAll('[data-testid="pending-chips"] li').length,
       steersBefore: before,
       steersAfter: document.querySelectorAll(
         '[data-testid="user-message-item"]:not([data-opens-exchange])').length,
     }, null, 2);
   })()
   ```

## Expected

- **Step 1**: `state` is `idle`, `active_turn_id` is empty, and
  `capabilities.steer` is **false**. Steer is gated on an in-flight or
  reserved turn — `Steer: steerAvailable && active && !closed`, where
  `active = processing || appReservedTurnID != ""`
  (`server/appwire_runtime.go:1044-1047`); `capabilities.queue` mirrors the
  same gate (`:1055`). Falsify: an idle session reports `steer:true` — the
  capability gate regressed, and the refusals below become the only thing
  standing between a stale click and a bogus steer.
- **Step 2 (daemon, exact)**: the omitted-`expectedTurnId` request is
  rejected with `InvalidParams` (code `-32602`) and the exact message
  `expectedTurnId is required` — refused twice on the way down, once by the
  hub (`cmd/serf-hub/app_rpc.go:381-383`) and once by the daemon
  (`server/appwire_runtime.go:597-599`). The stale-`expectedTurnId` request
  is rejected with `Conflict` (code `-32013`) and a message *containing*
  `turn is not active` (`agent/session_client_mutation_queue.go:325-328`,
  inside the atomic client-mutation step, so it never half-applies).
  Match on the substring: the observed message is prefixed with the method,
  `appwire turn/steer: turn is not active`. Falsify: either request
  succeeds, or hangs, or returns a generic internal error — a steer was
  accepted, or swallowed, against a session with no turn to steer.

  Verified live 2026-07-31 against a hub built from this branch, on an
  isolated `$HOME` and a kernel-assigned port, with `fake429` as the only
  provider: both codes and both messages are exactly as above.
- **Steps 3-4 (composer)**: no `[data-testid="composer-steer"]` button
  exists; the toast region contains `Steer failed: no active turn`
  (`Composer.tsx:641-643`); the composer text is **unchanged** (nothing was
  consumed); `pending-chips` is empty — the composer refuses *before*
  enqueuing anything, so there is no optimistic chip to reconcile; and the
  steer count is identical before and after. Falsify: the text vanishes from
  the composer (silently eaten), a pending chip appears and stays (a request
  went out and never reconciled), a new steer entry appears (the steer was
  accepted), or nothing at all is announced (the silent drop this card exists
  to catch).

## Cleanup

```bash
curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
  -d '{}' "$HUB/api/sessions/local:$SID/shutdown" >/dev/null
rm -rf "$tmpdir"
```

## Sharp edges

- **Part A is the exact half and needs no browser.** Run it alone if Chrome
  is unavailable; it pins both refusal messages verbatim. Part B pins the
  human-visible half, which no unit test can speak for.
- **AppWire frames carry no `jsonrpc` field.** Sending the JSON-RPC 2.0
  envelope every other tool defaults to gets the frame rejected outright
  (`"jsonrpc field is not part of AppWire"`,
  `appwire/jsonrpc.go:164-166`) and the server closes the socket — which
  looks like a connection problem, not a malformed request. Frames are
  `{"id":N,"method":"…","params":{…}}`.
- **Shift+Enter is only the Steer chord while `enterToSend` is off** — with
  the `serf.prefs.enterToSend` preference on, Shift+Enter inserts a literal
  newline instead, to avoid doubling up on that preference's own
  Enter-submits meaning (`Composer.tsx:685-687`). Leave the preference at its
  default, or seed it to `"0"` before the first page load.
- The composer's refusal is a **client-side pre-check**, not a round trip:
  `handleSteerClick` returns before `submitAction`, so nothing reaches the
  network. That is why Part A has to reach the socket directly to exercise
  the daemon's own gate — the two halves cover different code, and neither
  substitutes for the other.
- React's controlled textarea ignores a plain `ta.value = "..."` assignment;
  use the native value setter plus an `input` event, as the snippet does, or
  the composer will still think it is empty and route to `none`.
