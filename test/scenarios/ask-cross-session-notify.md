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

1. Spawn Session B first — trivial, no question — and wait for it to go idle:
   ```bash
   tmpdir_b=$(mktemp -d -t serf-e2e-ask-notify-b-XXXXX)
   bodyB=$(jq -n --arg wd "$tmpdir_b" '{prompt:"Say hello and stop.", model:"openai/gpt-5.5", working_dir:$wd, harness:"serf", branch:"", access_mode:"full", agent:"default", launch_overrides:{}}')
   SIDB=$(curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" -d "$bodyB" "$HUB/api/spawn" | jq -r '.session_id')
   for i in $(seq 1 60); do
     st=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SIDB" | jq -r '.state // ""')
     [ "$st" = "idle" ] && break
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
   deterministic assertion independent of that client-side timing:
   ```javascript
   fetch('/_partials/sidebar').then(r => r.text()).then(html => {
     const doc = new DOMParser().parseFromString(html, 'text/html');
     const tier = doc.querySelector('[data-tier="needs-you"]');
     const rows = tier ? [...tier.querySelectorAll('.needs-you-row')].map(r => r.textContent.trim()) : [];
     window.__sidebarCheck = { present: !!tier, count: tier ? tier.querySelector('.count').textContent : null, rows };
     return window.__sidebarCheck;
   })
   ```

## Expected

- Step 4: `title` carries the `(1) …` needs-you-count prefix (`applyTitle`, summary-driven);
  `faviconHasDot` is `true` (the amber `needs_you` dot, `STATE_COLORS.needs_you = "#e0af68"`);
  `captured` contains one entry whose `title` includes Session A's id or title
  (`fireOsNotification` fired — Session A's entry in the broadcast's `changed[]` transitioned
  `level: "needs_you"` from a `prevLevel` that was neither `needs_you` nor `error`).
- Step 5: `present` is `true`; `rows` includes an entry referencing Session A; `count` is at
  least `"1"`.
- Falsification: if a pending ask does not surface the session in the NeedsYou tier and
  raise the OS notification from a *different* session's viewport (the roster path), the
  needs-you chain is broken.

## Cleanup

```bash
curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" -d '{}' "$HUB/s/$SIDA/shutdown" >/dev/null
curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" -d '{}' "$HUB/s/$SIDB/shutdown" >/dev/null
pkill -f serf-hub-ask
rm -rf "$tmpdir_a" "$tmpdir_b" /tmp/serf-ask /tmp/serf-hub-ask
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
- **The sidebar tier now CAN live-update, but step 5 doesn't lean on it.** The sidebar
  partial (`cmd/serf-hub/templates/partials/sidebar.html`) re-renders on
  `hx-trigger="load, sidebar:refresh from:body"`, and `sidebar.js` now dispatches that
  refresh on `serf/attention/changed` too — so a real user's already-open tab should pick
  up Session A's row without a reload. Step 5 still fetches `/_partials/sidebar` directly
  rather than asserting on the already-open tab's DOM, so the check doesn't depend on
  client-side refresh timing (or on Session B's tab having `sidebar.js` wired to a visible
  sidebar element at all).
- If `Notification` gets stubbed on the wrong `window` (e.g. a stale tab reference after a
  navigation), `window.__asked` stays empty even though the code path fired — re-run the
  arm-and-stub eval (step 2) immediately before the wait if you re-navigate.
