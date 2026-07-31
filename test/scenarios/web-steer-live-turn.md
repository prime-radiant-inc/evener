# web-steer-live-turn: inject steering into a live turn from the web composer

**What this covers**: kata `a08v` plus the post-kata-`111a`/`0bq1` button
repurposing. The web UI exposes two paths that inject a steering message into
a running model loop — the composer's **Steer** button (and its Shift+Enter
chord), and the command palette's `/steer` command. Both land on the AppWire
`turn/steer` method, both require a live `activeTurnId`, and both must reach
the model: the transcript grows a `STEERING` entry and the model's next
output visibly follows the new instruction.

This is the **classic single-text steer path**: the queue is empty and there
are no staged attachments, so `decideSteerRoute` takes the `"steer"` branch
(`panes/session/composer/submitRouting.ts:33-39`). The drain-the-queue branch
is covered by `web-queue-then-drain-as-steer.md`.

The card previously drove `[data-steer-trigger]` and POSTed `/s/<id>/steer`.
Both died with the vanilla frontend (`660376f78`): there is no steer REST
route at all any more, and the composer is a React component addressed by
`data-testid`.

**Surface**: see `docs/agentic-testing.md`, "Driving the web UI" and "The
REST surface, and what is no longer on it".

## Pre-state

- Hub running on an isolated `$HOME` and free port (never `9180`,
  Jesse's real one — see the Setup checklist in
  `docs/agentic-testing.md`) with `--serf` resolvable.
- A provider credential that can sustain a slow multi-tool turn. Any model
  that honours the pacing trick works; `anthropic/claude-haiku-4-5-20251001`
  is the cheap default the runbook recommends.
- `$HOME/.serf/auth-token` readable (that isolated `$HOME`).
- The SPA built (`make build-web`) **before** the hub binary, or the browser
  gets `dist/PLACEHOLDER` instead of an app.

## Steps

```bash
tmpdir=$(mktemp -d -t serf-e2e-steer-XXXXX)
TOKEN=$(cat "$HOME/.serf/auth-token")
HUB=http://127.0.0.1:$PORT
```

1. **Drop a pacing `AGENTS.md` into the workspace** — see "AGENTS.md pacing
   trick" in `docs/agentic-testing.md`. Without it the model finishes before
   a browser-driving agent can type a steer.

2. **Spawn**, wait for the first turn to settle, then start a second, slow
   turn and wait for it to actually be in flight:
   ```bash
   resp=$(curl -s -X POST -H "Content-Type: application/json" \
     -H "Authorization: Bearer $TOKEN" \
     -d "{\"prompt\":\"Read AGENTS.md if it exists in your cwd. Then write a long, careful 5-paragraph essay about software engineering practices.\",\"model\":\"anthropic/claude-haiku-4-5-20251001\",\"working_dir\":\"$tmpdir\",\"harness\":\"serf\",\"branch\":\"\",\"access_mode\":\"full\",\"agent\":\"default\",\"launch_overrides\":{}}" \
     $HUB/api/spawn)
   SID=$(echo "$resp" | jq -r '.session_id')

   wait_state() {  # $1 = state to wait for
     for i in $(seq 1 90); do
       detail=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID")
       [ "$(echo "$detail" | jq -r '.state // ""')" = "$1" ] && return 0
       sleep 1
     done
     return 1
   }
   wait_state idle

   curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
     -d '{"text":"Re-read AGENTS.md in your cwd (mandatory). Then write a long, careful 5-paragraph essay about software engineering practices. Follow the pacing rules exactly — insert exec_command sleep calls between every paragraph. This must take at least a minute."}' \
     "$HUB/api/sessions/local:$SID/send" &
   wait_state active
   echo "$detail" | jq '{state, active_turn_id, steer: .capabilities.steer}'
   ```

3. **Open the workspace in the browser** at
   `/auth?token=$TOKEN&next=/s/local:$SID` and confirm the live stream has
   caught up — the Steer button renders only while the turn is genuinely in
   flight (`showSteer = busy && capabilities.steer`, `Composer.tsx:382`), so
   its presence *is* the hydration check `data-active-turn-id` used to be:
   ```javascript
   ({
     port: location.port,
     path: location.pathname,
     steer: !!document.querySelector('[data-testid="composer-steer"]'),
     stop: !!document.querySelector('[data-testid="composer-stop"]'),
   })
   ```

4. **Path A — the Steer button.** Type the steer text into the composer and
   click `[data-testid="composer-steer"]`. Take the synchronous snapshot
   before the ack lands (see "Synchronous vs. async assertion shape" in the
   runbook): a `Steering` chip should be in `[data-testid="pending-chips"]`
   immediately, and gone once the daemon acknowledges.
   ```javascript
   (async () => {
     const chips = () => Array.from(
       document.querySelectorAll('[data-testid="pending-chips"] li'), (li) => li.textContent);
     const ta = document.querySelector('[data-testid="composer-input-card"] textarea');
     const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value").set;
     setter.call(ta, "Make it 1 paragraph instead of 5, about Go testing specifically.");
     ta.dispatchEvent(new Event("input", { bubbles: true }));
     document.querySelector('[data-testid="composer-steer"]').click();
     const sync = chips();                                   // optimistic chip, right now
     await new Promise((r) => setTimeout(r, 3000));
     return JSON.stringify({
       sync,
       after: chips(),
       textAfter: ta.value,
       steers: document.querySelectorAll(
         '[data-testid="user-message-item"]:not([data-opens-exchange])').length,
       toast: document.querySelector('[aria-label="Notifications"]')?.textContent,
     }, null, 2);
   })()
   ```

5. **Path B — the `/steer` palette command.** Wait for another long turn, then
   open the palette (⌘K / Ctrl-K, or `/` as the first character of an empty
   composer — `shell/AppShell.tsx:266-271`, `Composer.tsx:673-676`), type
   `steer`, select **Steer model** (`shell/palette/commands.ts:431-433`),
   type the steer body and submit.

6. **Wait for the turn to settle and read the durable record.** Do not
   hand-parse the JSONL — use the doctor:
   ```bash
   wait_state idle
   go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:40 | grep STEERING
   ```

## Expected

- **Step 2 (server)**: `state=active` with a non-empty `active_turn_id` and
  `capabilities.steer:true`. Falsify: `steer:false` while a turn is running —
  the capability gate (`server/appwire_runtime.go:1047`) regressed and no UI
  path can steer.
- **Step 3 (hydration)**: `[data-testid="composer-steer"]` is present, as is
  `composer-stop`, and `location.port` is your hub's. Falsify: the Steer
  button never appears while the server reports `state=active` — the AppWire
  socket did not hydrate; check `$run/hub.log` before blaming the composer.
- **Step 4 (Path A)**: `sync` contains one chip reading `Steering` plus the
  steer text (`pending/PendingChips.tsx:38-42,56`); `after` is empty (the
  chip reconciled off the `clientMutationId` echoed back on
  `serf/steering/injected`, `stores/threads.ts:797-801`); the composer text is
  cleared; the steer appears in the transcript as a **user-message item with
  no `data-opens-exchange` attribute** — a steer the human typed reuses
  `UserMessageView` with `opensExchange={false}`
  (`transcript/messages/SteeringItem.tsx:143-146`,
  `UserMessageItem.tsx:98,112`), which is precisely what distinguishes it
  from an ordinary prompt; and the toast region is empty. Falsify: no chip in
  `sync` (the optimistic path never rendered — the bug kata `wymv` was
  about), a chip still present in `after` (the ack never arrived), the
  composer keeps its text, or an error toast appears.
- **Step 5 (Path B)**: the palette closes and the same kind of steer entry
  appears. Falsify: the palette closes with nothing added and nothing said —
  the command's own guard is supposed to be loud
  (`commands.ts:443`, `blocked("steer failed: no active turn")`) when the
  turn ends underneath it.
- **Step 6 (durable)**: the outline contains a `STEERING` entry per steer,
  whose text matches what was typed. The model's next assistant message
  visibly follows it — for the Path A text above, a single short paragraph
  about Go testing, not the original five-paragraph essay. The session ends
  at `state=idle` and stays live; it does **not** end or close. Falsify: no
  `STEERING` entry (the steer never reached the daemon), the model's output
  is unchanged (it reached the transcript but not the running loop), or the
  session reaches `ended`/`closed` — steering must not terminate the turn.

## Cleanup

```bash
curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
  -d '{}' "$HUB/api/sessions/local:$SID/shutdown" >/dev/null
rm -rf "$tmpdir"
```

## Sharp edges

- **A human steer is NOT `[data-testid="steering-item"]`.** That element is
  the collapsible divider for *daemon*-originated steering (labelled
  "System steered: …"); a steer the human typed is indistinguishable from a
  prompt and renders as `user-message-item`. Selecting on `steering-item`
  after a manual steer finds nothing and reads as a regression. Use
  `[data-testid="user-message-item"]:not([data-opens-exchange])`.
- **The first turn usually races to idle before you can steer it.** The model
  does not reliably read `AGENTS.md` on the very first prompt. Send a second
  turn that cites the pacing rules explicitly, as step 2 does.
- **Steer needs the turn id, not just the status.** `isTurnActive` requires
  both `statusType === "active"` and a populated `activeTurnId`
  (`submitRouting.ts:48-50`), which is why the Steer button can lag the
  server's `state=active` by one notification. Wait for the button, not for
  the clock.
- **Shift+Enter is the same action as the button**, but only while the
  `serf.prefs.enterToSend` preference is off (`Composer.tsx:685-687`).
- **An empty queue is part of the premise.** Any queued message, or any
  staged attachment, reroutes the same button to `turn/drainAsSteer`
  (`submitRouting.ts:33-39`) — a different method, a `Draining` chip, and a
  different card.
- **`turn_count` does not move.** It counts committed user→assistant
  exchanges; a steer writes a transcript entry without starting a turn. Don't
  read transcript growth as turn growth.
- **Steering does not terminate the turn.** Unlike interrupt, it injects into
  the running loop — the turn keeps going and the model adapts on its next
  round.
