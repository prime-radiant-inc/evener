# ask-cross-session-notify: a pending ask surfaces cross-session (NeedsYou tier + badge + OS notification)

**What this covers**: spec §8 row `ask-cross-session-notify.md` — the roster/needs-you chain
(§1, §5.4) that the whole feature exists to light up: from a *different* session's viewport,
an asking session should surface in the sidebar's NeedsYou tier with a count badge, and its
transition into the `needs_you` attention level should fire the browser's OS notification
(`cmd/serf-hub/assets/notifications.js`, event-driven per
`docs/superpowers/specs/2026-07-03-attention-status-model-design.md`: the hub broadcasts
`serf/attention/changed` on every attention-level transition; ask-produced `awaiting`
normalizes to `needs_you` exactly like any other producer — there is no ask-specific wiring,
per that spec's §11 reconciliation with this one).

## Pre-state

- Hub + credentials as `ask-web-answer.md` (reuse if still running on `127.0.0.1:9280`).
- Two sessions: **Session A** (will ask a question) and **Session B** (an unrelated, already
  running session whose workspace we'll watch from).
- `superpowers-chrome:browsing` available, with tab support (`new_tab`/`switch_tab`).

## Steps

1. Spawn Session B first — trivial, no question — and wait for it to settle `awaiting`
   (your-move — a plain completed turn with nothing pending now rests `awaiting`, not
   `idle`; see `status-vocabulary-roundtrip.md`):
   ```bash
   tmpdir_b=$(mktemp -d -t serf-e2e-ask-notify-b-XXXXX)
   bodyB=$(jq -n --arg wd "$tmpdir_b" '{prompt:"Say hello and stop.", model:"openai/gpt-5.5", working_dir:$wd, harness:"serf", branch:"", access_mode:"full", agent:"default", launch_overrides:{}}')
   SIDB=$(curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" -d "$bodyB" "$HUB/api/spawn" | jq -r '.session_id')
   for i in $(seq 1 60); do
     st=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SIDB" | jq -r '.state // ""')
     [ "$st" = "awaiting" ] && break
     sleep 1
   done
   echo "SIDB=$SIDB"
   ```
2. Open Session B's workspace in a browser tab **now** — before Session A ever asks — and
   arm the notification channel (opt in to OS notifications and stub the `Notification`
   constructor + permission + focus state, since a CDP-controlled tab has no real OS
   notification surface a screenshot can observe, and headless focus/permission semantics
   are not the thing under test here):
   ```
   navigate http://127.0.0.1:9280/auth?token=<TOKEN>&next=/s/<SIDB>
   ```
   ```javascript
   (() => {
     localStorage.setItem('serf-hub.notifications', JSON.stringify({os:true, title:true, favicon:true, sound:false}));
     window.__asked = [];
     window.Notification = function (title, opts) {
       window.__asked.push({ title, body: opts && opts.body });
       return { onclick: null };
     };
     Object.defineProperty(window.Notification, 'permission', { value: 'granted', configurable: true });
     Object.defineProperty(document, 'hasFocus', { value: () => false, configurable: true });
     return 'armed';
   })()
   ```
   Wait a couple seconds for the one-shot `/api/tree` baseline fetch
   (`notifications.js`'s `fetchBaseline`, run on init and on reconnect — there
   is no more poll loop) to resolve. This baseline matters more than it did
   under the old poll: **no baseline → no edge-firing** (the client never
   alerts on a transition it can't attribute a "before" to), so Session B's
   tab must have its baseline landed before Session A's ask broadcasts, not
   merely be open:
   ```
   sleep 2
   ```
3. Now spawn Session A with a question-asking first turn:
   ```bash
   tmpdir_a=$(mktemp -d -t serf-e2e-ask-notify-a-XXXXX)
   bodyA=$(jq -n --arg wd "$tmpdir_a" '{
     prompt: "Before doing any other work, call the ask_user tool once. Ask exactly one question: header \"Rollout\", question \"Should we ship to 10% of traffic first?\", with exactly two options: canary (detail \"10% ramp, safer\") and full (detail \"100% at once, faster\"). Do not do anything else first.",
     model: "openai/gpt-5.5", working_dir: $wd, harness: "serf", branch: "", access_mode: "full", agent: "default", launch_overrides: {}
   }')
   SIDA=$(curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" -d "$bodyA" "$HUB/api/spawn" | jq -r '.session_id')
   for i in $(seq 1 60); do
     st=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SIDA" | jq -r '.state // ""')
     [ "$st" = "awaiting" ] && break
     sleep 1
   done
   echo "SIDA=$SIDA state=$st"
   ```
4. From Session B's tab (never navigated away — this is the "different session's viewport"),
   wait for the hub's attention watcher to broadcast the transition (ticks every ~5s,
   `cmd/serf-hub/main.go`'s `NewAttentionWatcher`; this is the *cross-session* reconcile
   path — Session B's tab was never subscribed to Session A's own `thread/status/changed`,
   so there is no faster own-tab shortcut available here) and read the
   title/favicon/notification channels:
   ```
   sleep 7
   ```
   ```javascript
   (() => {
     const link = document.querySelector("link[rel='icon']");
     return {
       title: document.title,
       faviconHasDot: !!(link && /r='18'/.test(decodeURIComponent(link.href))),
       captured: window.__asked,
     };
   })()
   ```
5. The sidebar now DOES join the broadcast: `serf/attention/changed` is on `sidebar.js`'s
   refresh-trigger allowlist, so an already-open tab's sidebar should self-refresh on the
   same broadcast step 4 waited on. Fetch a fresh copy explicitly anyway, for a
   deterministic assertion independent of that client-side timing — the sidebar is
   client-rendered from `GET /api/tree` JSON post-WS3 (there is no more
   `/_partials/sidebar` route or server-rendered partial to `DOMParser`), and `/api/tree`
   is a plain JSON API route, not an htmx partial, so no `HX-Request` header is needed:
   ```javascript
   fetch('/api/tree', { headers: { Authorization: 'Bearer ' + '<TOKEN>' } }).then(r => r.json()).then(tree => {
     const row = tree.needs_you.find(n => n.session_id === '<SIDA>');
     window.__sidebarCheck = { present: !!row, count: tree.needs_you.length, askPending: row ? row.ask_pending : null };
     return window.__sidebarCheck;
   })
   ```
6. **`loudScope` default: a generic your-move settle stays quiet.** Re-arm the stub from step
   2 (fresh `window.__asked = []`, baseline already landed), then spawn a **third**, throwaway
   session with a trivial no-ask prompt (e.g. "Say hello and stop.") and wait for it to settle
   `awaiting`. This is a generic your-move transition — no `ask_user` call, no error — under
   the default `loudScope: "asks"` preference (`notifications.js`'s default when
   `serf-hub.notifications.loudScope` is unset in `localStorage`):
   ```bash
   tmpdir_c=$(mktemp -d -t serf-e2e-ask-notify-c-XXXXX)
   bodyC=$(jq -n --arg wd "$tmpdir_c" '{prompt:"Say hello and stop.", model:"openai/gpt-5.5", working_dir:$wd, harness:"serf", branch:"", access_mode:"full", agent:"default", launch_overrides:{}}')
   SIDC=$(curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" -d "$bodyC" "$HUB/api/spawn" | jq -r '.session_id')
   for i in $(seq 1 60); do
     st=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SIDC" | jq -r '.state // ""')
     [ "$st" = "awaiting" ] && break
     sleep 1
   done
   echo "SIDC=$SIDC state=$st"
   ```
   Wait for the watcher's next broadcast (as in step 4), then read the stub from Session B's
   tab:
   ```javascript
   window.__asked
   ```

## Expected

- Step 4: `title` carries the `(1) …` needs-you-count prefix (`applyTitle`, summary-driven);
  `faviconHasDot` is `true` (the amber `needs_you` dot, `STATE_COLORS.needs_you = "#e0af68"`);
  `captured` contains one entry whose `title` includes Session A's id or title
  (`fireOsNotification` fired — Session A's entry in the broadcast's `changed[]` transitioned
  `level: "needs_you"` from a `prevLevel` that was neither `needs_you` nor `error`).
- Step 5: `present` is `true`; `askPending` is `true` (Session A asked a question — this is the
  band-ordering wire bit this track adds, Task 21); `count` is at least `1` and the found
  `row` is Session A's (assert on presence/identity, not a hardcoded population count — the
  live NeedsYou tier can otherwise include Session B or C depending on what's still resting
  `awaiting` when this step runs).
- Step 6: `window.__asked` gained **no** new entry for Session C's generic your-move
  transition (still just the one entry from Session A's ask, already asserted in step 4) —
  under the default `loudScope: "asks"`, a plain your-move settle stays quiet while an
  ask-pending transition fires, demonstrating the default scope distinguishes the two.
- Falsification: if a pending ask does not surface the session in the NeedsYou tier and
  raise the OS notification from a *different* session's viewport (the roster path), the
  needs-you chain is broken. Falsification (loudScope): if a generic your-move settle also
  fires an OS notification under the default `"asks"` scope, or if Session A's ask-pending
  entry does NOT fire one, the loud-scope gate is not distinguishing tiers.

## Cleanup

```bash
curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" -d '{}' "$HUB/s/$SIDA/shutdown" >/dev/null
curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" -d '{}' "$HUB/s/$SIDB/shutdown" >/dev/null
curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" -d '{}' "$HUB/s/$SIDC/shutdown" >/dev/null
pkill -f serf-hub-ask
rm -rf "$tmpdir_a" "$tmpdir_b" "$tmpdir_c" /tmp/serf-ask /tmp/serf-hub-ask
```

## Sharp edges

- **One cadence now, not two: the hub's attention watcher.** There is no more client poll.
  `notifications.js` is purely event-driven — a one-shot `/api/tree` baseline on connect,
  then counts and transitions ride each `serf/attention/changed` broadcast. The watcher
  (`cmd/serf-hub/internal/hubcore`, wired up in `cmd/serf-hub/main.go`'s
  `NewAttentionWatcher`) ticks every ~5s; that is the only cadence step 4's `sleep 7` is
  covering margin for.
- **The OS-notification channel is opt-in, permission-gated, and leader-elected in real
  use** — `notifications.js` only calls `fireOsNotification` when the user has toggled the
  `os` preference on (persisted in `localStorage["serf-hub.notifications"]`) **and**
  `Notification.permission === "granted"` **and** the tab lacks focus **and** this tab won
  the Web-Locks leader election (so a multi-tab session doesn't double-alert — moot here
  with one tab on Session B, which is trivially the leader). A fresh CDP-controlled tab has
  none of the first three by default, which is why step 2 stubs them — this proves the
  *call site* fires on the right transition, not that a literal OS toast renders (that's
  outside what a screenshot-driven agent can observe anyway).
- **No baseline → no edge-firing.** `onAttentionChanged` only evaluates transitions once
  the `/api/tree` baseline fetch from init has landed (`hadBaseline`); a broadcast arriving
  before it is applied silently, with no OS/sound fired even if it reports a `needs_you`
  entry. If Session B's tab is opened *after* Session A is already `awaiting`, its baseline
  fetch will already include Session A as `needs_you` (so title/favicon counts are correct
  immediately) but there is no "into-transition" left to observe, so the OS notification for
  *that* ask is missed — not a bug, the documented invariant. Always open the watching tab
  and let its baseline land *before* triggering the ask (step 2).
- **The sidebar tier now CAN live-update, but step 5 doesn't lean on it.** Post-WS3, the
  sidebar is client-rendered by `cmd/serf-hub/assets/sidebar.js` from `GET /api/tree` JSON —
  there is no more server-rendered `templates/partials/sidebar.html` or `/_partials/sidebar`
  route. `sidebar.js` refetches `/api/tree` (`fetchTree`) and reconciles the DOM via keyed
  `RowID`s (not a full re-render) whenever it receives `thread/started`, `thread/closed`,
  `thread/status/changed`, `serf/job/started`, `serf/job/finished`, or `serf/attention/changed`
  (its `QUALIFYING` event map) — so a real user's already-open tab should pick up Session A's
  row without a reload. Step 5 still fetches `/api/tree` directly rather than asserting on the
  already-open tab's DOM, so the check doesn't depend on client-side refresh timing (or on
  Session B's tab having `sidebar.js` wired to a visible sidebar element at all).
- If `Notification` gets stubbed on the wrong `window` (e.g. a stale tab reference after a
  navigation), `window.__asked` stays empty even though the code path fired — re-run the
  arm-and-stub eval (step 2) immediately before the wait if you re-navigate.
- **Step 4's `(1) …` title-count is illustrative, not a hard count to match.** Since the
  unified vocabulary lands (Tasks 1-30 of this track), *every* `awaiting` session — Session
  B's own plain your-move rest included, not just Session A's ask — counts toward the
  NeedsYou tier and its title badge. Live in this run the count read `(3)` (Session A, B, and
  a third throwaway session already in flight for step 6's prep) with Session A still the
  transitioning entry that fired the notification; assert the *transition into needs_you*
  (`captured` gaining Session A's entry) rather than a specific digit in `title`.
