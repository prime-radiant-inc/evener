# ask-cross-session-notify: a pending ask surfaces cross-session (NeedsYou tier + badge + OS notification)

**What this covers**: spec §8 row `ask-cross-session-notify.md` — the roster/needs-you chain
(§1, §5.4) that the whole feature exists to light up: from a *different* session's viewport,
an asking session should surface in the sidebar's NeedsYou tier with a count badge, and the
`active→awaiting` transition should be one of the three transitions that fires the browser's
OS notification (`cmd/serf-hub/assets/notifications.js`).

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
   Wait at least 6 seconds (one full poll cycle, `notifications.js` `POLL_MS=5000`) so the
   poller records Session A's **pre-ask** state as the transition baseline:
   ```
   sleep 6
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
   wait one more poll cycle and read the title/favicon/notification channels:
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
5. The sidebar's NeedsYou tier does **not** live-poll (it only refreshes on page load or an
   explicit `sidebar:refresh` htmx trigger); fetch a fresh copy explicitly:
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

- Step 4: `title` carries the `(1) …` awaiting-count prefix (`applyTitle`); `faviconHasDot`
  is `true` (the amber `awaiting` dot, `STATE_COLORS.awaiting = "#e0af68"`); `captured`
  contains one entry whose `title` includes Session A's id or title (`fireOsNotification`
  fired — the `active→awaiting` transition is one of exactly three alert transitions
  `notifications.js` recognizes).
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

- **Two independent 5s cadences, don't conflate them.** `notifications.js`'s client poll
  (`/api/search`, `POLL_MS=5000`) drives title/favicon/OS-notification and is what step 4
  waits on. The hub's own roster refresh (`cmd/serf-hub/internal/hubcore/roster.go`, also
  5s) is what backs `/api/search`'s data in the first place. Either can add up to ~5s of
  slack; the `sleep 6`/`sleep 7` margins above cover both with room to spare.
- **The OS-notification channel is opt-in and permission-gated in real use** —
  `notifications.js` only calls `fireOsNotification` when the user has toggled the
  `os` preference on (persisted in `localStorage["serf-hub.notifications"]`) **and**
  `Notification.permission === "granted"` **and** the tab lacks focus. A fresh
  CDP-controlled tab has none of those by default, which is why step 2 stubs all three —
  this proves the *call site* fires on the right transition, not that a literal OS toast
  renders (that's outside what a screenshot-driven agent can observe anyway).
- **`prevState` is per-page, in-memory, and reset on load.** `detectTransitions` only fires
  on a transition it can *see* (`before && before !== after`); if Session B's tab is opened
  *after* Session A is already `awaiting`, there is no "before" to compare and the
  transition is silently missed on the first poll. Always open the watching tab and let one
  poll cycle land *before* triggering the ask (step 2's `sleep 6`).
- **The sidebar tier does not live-update.** Unlike the title/favicon/notification channel,
  the sidebar partial (`cmd/serf-hub/templates/partials/sidebar.html`) only re-renders on
  `hx-trigger="load, sidebar:refresh from:body"` — a full page load or an explicit trigger,
  neither of which fires automatically when a *different* session changes state. Step 5
  fetches `/_partials/sidebar` directly rather than assuming the already-open tab's DOM
  updated on its own; a real user would see the same thing only after a reload or their next
  navigation.
- If `Notification` gets stubbed on the wrong `window` (e.g. a stale tab reference after a
  navigation), `window.__asked` stays empty even though the code path fired — re-run the
  arm-and-stub eval (step 2) immediately before the wait if you re-navigate.
