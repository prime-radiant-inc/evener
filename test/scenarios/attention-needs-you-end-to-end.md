# attention-needs-you-end-to-end: awaiting drives the rail row, tab title, and favicon live

**What this covers**: the attention & status model
(`docs/superpowers/specs/2026-07-03-attention-status-model-design.md`). Before this work,
`agent.SessionState` collapsed "waiting for your reply" into plain `idle`, so the wire
`awaiting` state was unreachable, the needs-you tier never lit up, and notifications keyed
on impossible transitions. This is the single-tab happy path named in the spec's Testing
section, plus the interrupt guard-rail. (The goal-loop variant is intentionally NOT
re-proven here — see Part C.)

**Surface**: see `docs/agentic-testing.md` — "The REST surface, and what is no longer on
it", and "Driving the web UI with superpowers-chrome:browsing" for the selector map and
**"Seeding preferences before the first load"**. Three facts that invert what this card
used to say:

- **There is no `section[data-tier="needs-you"]` and no `a.sb-row`.** The Rail deliberately
  does not render a "Needs you" section at all (`shell/rail/Rail.tsx:563-573`, kata `vbh8`)
  — attention surfaces inline on the session's own row. A row is
  `[data-session-ref="local:<SID>"]` (`shell/rail/RailRow.tsx:509`) and its state word lives
  in `[data-testid="rail-row-activity"]` (`:531`), **lowercased** by `humanizeState`
  (`:138-152`) rather than in `hubapi.StateWord`'s sentence case. So there is exactly one
  row per session now, not two, and no `data-state` attribute to read.
- **Title and favicon default OFF**, not on. `loadNotifications` lands all four
  notification prefs at `false` (`stores/prefs.ts:268-273`), which the engine's own header
  calls the top cross-wave trap (`notifications/index.ts:7-13`: the legacy's title/favicon
  TRUE default is "deliberately NOT ported"). Clearing `localStorage` therefore does not
  give you a badge — it takes one away. This card **opts in** before the first load.
- **The spawn form is not driven here.** `textarea[name=prompt]` /
  `button[data-chip="model"]` / `.chip-picker-search` are gone with the vanilla frontend
  (`660376f78`); the spawn pane's live hooks are `[data-testid="spawn-prompt-card"]` /
  `[data-testid="spawn-submit"]` over an ARIA combobox model picker
  (`panes/spawn/Spawn.tsx:514,558`). Driving it is `spawn-picker-enter-noop.md`'s and
  `spawn-stale-model-cleared.md`'s job; this card spawns over REST so its own assertions
  stay about attention.

Part B (steps 5-7) and Part C are **fully browser-free**. Part A (steps 1-4) needs Chrome.

## Pre-state

- Fresh binaries and an isolated hub, per the Setup checklist in `docs/agentic-testing.md`.
  Everything is named from one `mktemp` run directory, and the hub binds a kernel-assigned
  port — never Jesse's real `9180`, where his own hub runs host-wide flock'd with his real
  token and history:
  ```bash
  run=$(mktemp -d -t serf-e2e-attn-XXXXXX)
  go build -o "$run/serf"     ./cmd/serf
  go build -o "$run/serf-hub" ./cmd/serf-hub
  export HOME="$run/home"
  mkdir -p "$HOME"
  unset XDG_STATE_HOME
  "$run/serf-hub" -addr 127.0.0.1:0 -serf "$run/serf" 2>"$run/hub.log" &
  HUBPID=$!
  for i in $(seq 1 50); do
    PORT=$(grep -oE 'listening on 127\.0\.0\.1:[0-9]+' "$run/hub.log" 2>/dev/null | grep -oE '[0-9]+$') || true
    [ -n "$PORT" ] && break
    kill -0 "$HUBPID" 2>/dev/null || { echo "hub exited before listening:" >&2; cat "$run/hub.log" >&2; exit 1; }
    sleep 0.1
  done
  HUB=http://127.0.0.1:$PORT
  TOKEN=$(cat "$HOME/.serf/auth-token")
  ```
- `ANTHROPIC_API_KEY` in the environment; the cheap model is
  `anthropic/claude-haiku-4-5-20251001` (this repo's standard cheap-model convention).
  A scratch `$HOME` has no `credentials.toml`, so provider auth is env-based here.
- For Part A: the frontend must be built (`make build-web`) **before** the hub — an
  unbuilt checkout ships a one-line `frontend/dist/PLACEHOLDER` and serves no app. Claim
  your own Chrome profile (`set_profile <worktree-name>`) before the first `use_browser`
  call, and use a **viewport ≥ 900 px wide** — below that `AppShell` mounts the mobile
  stack with no rail at all (`shell/useIsMobile.ts:9`, `shell/AppShell.tsx:372`).

## Steps

### A. Happy path — turn settles, thread needs you, thread clears

1. **(browser-free)** Spawn a session that will finish quickly, and note `SID`:
   ```bash
   tmpdir=$(mktemp -d -t serf-e2e-attn-wd-XXXXX)
   SID=$(curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
     -d "{\"prompt\":\"Reply with exactly the word PONG.\",\"model\":\"anthropic/claude-haiku-4-5-20251001\",\"working_dir\":\"$tmpdir\",\"harness\":\"serf\",\"branch\":\"\",\"access_mode\":\"full\",\"agent\":\"default\",\"launch_overrides\":{}}" \
     "$HUB/api/spawn" | jq -r '.session_id')
   echo "SID=$SID"
   ```
2. **(browser)** Open the session **while the turn is still running**, opting into the
   title and favicon channels first. Three loads, in this order — the prefs hydrate once at
   module load (`stores/prefs.ts:344-378`), so a write after the page is up changes
   nothing:
   ```
   navigate $HUB/auth?token=<TOKEN>&next=/s/local:<SID>
   ```
   ```javascript
   // strings "1"/"0", never JSON (stores/prefs.ts:224-229)
   localStorage.setItem("serf.prefs.notificationsTitle", "1");
   localStorage.setItem("serf.prefs.notificationsFavicon", "1");
   "seeded"
   ```
   ```
   navigate $HUB/s/local:<SID>
   ```
   Confirm the tab is live and mid-turn before going on — Steer renders only while a turn
   is genuinely in flight (`composer/Composer.tsx:871`):
   ```javascript
   ({
     port: location.port,
     path: location.pathname,
     steerRendered: !!document.querySelector('[data-testid="composer-steer"]'),
     activity: document.querySelector('[data-session-ref="local:<SID>"] [data-testid="rail-row-activity"]')?.textContent,
     title: document.title,
   })
   ```
3. **(browser + browser-free) Assert all three channels in the still-open tab, with no
   second tab and no refresh.** Bound the wait against the daemon rather than DOM timing:
   ```bash
   for i in $(seq 1 12); do
     state=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" | jq -r '.state // ""')
     [ "$state" = "awaiting" ] && break
     sleep 1
   done
   echo "state=$state"   # expect: awaiting, within ~10s
   ```
   Then, without touching the tab, re-read it (allow up to ~8 s — the 5 s attention-watcher
   tick plus the client's 250 ms refetch debounce):
   ```javascript
   (() => {
     const row = document.querySelector('[data-session-ref="local:<SID>"]');
     const link = document.querySelector("link[rel='icon']");
     const href = link ? decodeURIComponent(link.href) : "";
     return {
       port: location.port,
       rowPresent: !!row,
       activity: row?.querySelector('[data-testid="rail-row-activity"]')?.textContent,
       title: document.title,
       faviconAmber: /r='18'/.test(href) && /fill='#e0af68'/.test(href),
       steerRendered: !!document.querySelector('[data-testid="composer-steer"]'),
     };
   })()
   ```
4. **(browser)** **Reply in the open thread** — composer, not REST; the assertion is about
   the UI's own-tab path. The textarea is React-controlled, so use real key events, not an
   assignment to `.value`:
   ```
   click [data-testid="composer-input-card"] textarea[aria-label="Message"]
   type thanks — nothing else.
   click [data-testid="composer-submit"]
   ```
   Then re-read, retrying for up to **6 s**:
   ```javascript
   (() => {
     const row = document.querySelector('[data-session-ref="local:<SID>"]');
     const link = document.querySelector("link[rel='icon']");
     const href = link ? decodeURIComponent(link.href) : "";
     return {
       activity: row?.querySelector('[data-testid="rail-row-activity"]')?.textContent,
       title: document.title,
       faviconAmber: /r='18'/.test(href) && /fill='#e0af68'/.test(href),
     };
   })()
   ```

### B. Interrupt variant — never shows awaiting (browser-free)

5. Spawn a second session with a long-running prompt, in its own hermetic workdir:
   ```bash
   tmpdir2=$(mktemp -d -t serf-e2e-attn-interrupt-XXXXX)
   SID2=$(curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
     -d "{\"prompt\":\"Run \\\"sleep 60\\\" via the shell tool, then reply DONE.\",\"model\":\"anthropic/claude-haiku-4-5-20251001\",\"working_dir\":\"$tmpdir2\",\"harness\":\"serf\",\"branch\":\"\",\"access_mode\":\"full\",\"agent\":\"default\",\"launch_overrides\":{}}" \
     "$HUB/api/spawn" | jq -r '.session_id')
   # wait for the turn to actually be in flight
   for i in $(seq 1 15); do
     state=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID2" | jq -r '.state // ""')
     [ "$state" = "active" ] && break
     sleep 1
   done
   echo "SID2=$SID2 state=$state"
   ```
6. Interrupt mid-run. Note the namespace: the old `$HUB/s/<id>/interrupt` shim is gone
   (`660376f78`) and 404s silently, which would make step 7 pass for the wrong reason:
   ```bash
   turn=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID2" | jq -r '.active_turn_id // ""')
   curl -s -o /dev/null -w '%{http_code}\n' -X POST -H "Content-Type: application/json" \
     -H "Authorization: Bearer $TOKEN" -d "{\"turn_id\":\"$turn\"}" \
     "$HUB/api/sessions/local:$SID2/interrupt"
   ```
7. Poll for settle, asserting the state is never `awaiting` at any point:
   ```bash
   seen_awaiting=0
   for i in $(seq 1 15); do
     state=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID2" | jq -r '.state // ""')
     [ "$state" = "awaiting" ] && seen_awaiting=1
     [ "$state" = "idle" ] && break
     sleep 1
   done
   echo "final=$state seen_awaiting=$seen_awaiting"   # expect: idle, 0
   ```
   Cross-check the hub's own tree agrees, without a browser:
   ```bash
   curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/tree" | jq --arg sid "$SID2" \
     '[.live[], .needs_you[]] | map(select(.session_id == $sid)) | {states: map(.state), inNeedsYou: (map(.state) | length)}'
   ```

### C. Goal variant — not re-proven here

The spec's Testing section places the goal-loop case in integration, not e2e: a real
goal-loop run adds LLM-turn-count flakiness without adding coverage the unit tests don't
already have. That coverage lives in `agent/session_awaiting_test.go:28`
(`TestSettleTerminalState`) and `agent/session_goal_fix_test.go:37,66`
(`TestSettleGoalOnIdleKicksWindowGoal`, `TestSettleGoalOnIdleNoKickWhenTerminal`) — assert
those pass instead of driving a live goal session here.

## Expected

- **Step 2**: `steerRendered` is `true` and `activity` reads `working` while the turn runs
  (`humanizeState("active")`, `shell/rail/RailRow.tsx:140-141`). If `steerRendered` never
  appears while REST reports `active`, the AppWire socket did not hydrate — check
  `$run/hub.log` and the `location.port` assertion before suspecting attention.
- **Step 3 (all three channels agree, one tab, no refresh)**: within ~10 s of the turn
  settling —
  - `activity` reads `your move` (`awaiting` with no pending ask,
    `shell/rail/RailRow.tsx:143`);
  - `title` matches `/^\(\d+\) /` — the `(needsYou + error)` prefix
    (`notifications/title.ts:35-38`), present only because step 2 opted in;
  - `faviconAmber` is `true` — the amber `needs_you` corner dot
    (`notifications/favicon.ts:14,33-41`);
  - `steerRendered` is `false` (the turn is over).

  Falsify: `awaiting` shows in only one of the three (e.g. the row flipped but the title
  didn't) — a channel is wired to a stale source of truth. Or the prefix appears **without**
  the step-2 opt-in, which means a notification default was flipped back on.
- **Step 4**: both the title prefix and the favicon dot clear, and `activity` leaves
  `your move`. The tab's own reply emits `thread/started`, which is on the tree store's
  refresh-trigger list (`stores/tree.ts:443-451`), so this should land within a second —
  well inside the 6 s ceiling. Falsify: clearing consistently needs the full ~6 s, meaning
  the own-tab trigger regressed to the broadcast-only path (the 5 s attention-watcher tick,
  `cmd/serf-hub/main_background.go:50`); or it never clears at all.
- **Step 6**: the interrupt POST returns **204**. A 404 means the request went to the dead
  `/s/<id>/` shim and nothing was interrupted.
- **Step 7 (exact)**: `seen_awaiting` is `0` and the final state is `idle` — an interrupted
  turn must never pass through `awaiting` on its way to `idle`, and the tree cross-check
  must agree (no `awaiting` for `SID2`, no `needs_you` membership). Falsify:
  `seen_awaiting=1` — the drain-settle upgrade fired on an interrupted turn, exactly the
  regression the design's "boundary function stays untouched" guarantee exists to prevent.

## Cleanup

```bash
for sid in $SID $SID2; do
  curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
    -d '{}' "$HUB/api/sessions/local:$sid/shutdown" >/dev/null 2>&1
done
kill "$HUBPID" 2>/dev/null
rm -rf "$run" "$tmpdir" "$tmpdir2"
```

Kill the hub by the PID you captured — never `pkill -f serf-hub`, which would also kill a
concurrent agent's test hub.

## Sharp edges

- **One row per session, not two.** The old card's "the tier row and the project row both
  carry `data-state`" footgun is gone with the tier; `querySelector` on
  `[data-session-ref="local:<SID>"]` is now unambiguous. A live session lands in the rail's
  flat **Live** section (`shell/rail/Rail.tsx:574-580`), so no project expansion is needed
  to see it.
- **Seeding the prefs is the whole precondition — clearing `localStorage` is the
  opposite.** All four notification prefs read `false` when unset, so a wiped profile
  fails the title assertion by design. Seed, then reload; a write into a running page does
  nothing (`stores/prefs.ts:344-378`).
- **The row's word is lowercase.** `humanizeState` deliberately diverges from
  `hubapi.StateWord`'s sentence case ("Your move" / "Question waiting") to match the gloss
  line's own casing (`shell/rail/RailRow.tsx:127-129`). Compare case-insensitively, or
  compare against the lowercase form.
- **The gloss line can carry more than the state word.** `activityGloss` joins the state
  with the session's branch, and `secondLine` can prepend the project on flat tiers
  (`shell/rail/RailRow.tsx:231-235,246-251`), so assert `includes`, not equality.
- **`.value = "…"` does not reach a React-controlled composer.** Use the browser tool's
  `type` action (real key events) for step 4's reply. Same trap as the ask dock's inputs —
  see `ask-web-answer.md`'s Sharp edges for the `eval`-only workaround.
- **`composer-submit` has one label and two timings**: it routes to `turn/queue` while a
  turn runs and `turn/start` otherwise (`composer/submitRouting.ts:18-23`). At `awaiting`
  it sends, which is what step 4 wants — but if you reply too early the message is queued
  instead and the title will not clear the way this card expects.
- The interrupt variant's daemon-side contract (interrupted turns hard-code `idle`, never
  upgrade to `awaiting`) is also covered by Go tests; Part B is the cross-surface
  confirmation that the same daemon state reaches the hub's REST and tree views, not the
  only proof.
