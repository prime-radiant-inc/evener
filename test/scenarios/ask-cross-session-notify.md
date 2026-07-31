# ask-cross-session-notify: a pending ask surfaces cross-session (tree tier + title/favicon + OS notification)

**What this covers**: spec §8 row `ask-cross-session-notify.md` — the roster/needs-you
chain (§1, §5.4) the whole feature exists to light up. From a *different* session's
viewport, an asking session must (a) enter the hub's `needs_you` tier with `ask_pending`
set, (b) raise that tab's attention count on the title and favicon, and (c) fire the OS
notification channel — driven by the hub's `serf/attention/changed` broadcast. Ask-produced
`awaiting` normalizes to `needs_you` exactly like any other producer; there is no
ask-specific wiring (`docs/superpowers/specs/2026-07-03-attention-status-model-design.md`
§11).

**Surface**: see `docs/agentic-testing.md` — "The REST surface, and what is no longer on
it", and "Driving the web UI with superpowers-chrome:browsing" for the selector map and
**"Seeding preferences before the first load"**, which this card depends on completely.
Four facts that invert what this card used to say:

- **The notification engine is `cmd/serf-hub/frontend/src/notifications/*.ts`**, not
  `assets/notifications.js` (deleted at `660376f78`). It reads `treeStore` +
  `prefsStore` and is started once by `AppShell` (`shell/AppShell.tsx:43,48`).
- **All four notification prefs default OFF** (`stores/prefs.ts:268-273`) and are flat
  `localStorage` keys `serf.prefs.notifications{Title,Favicon,Os,Sound}` holding `"1"`/`"0"`
  (`:224-229`), hydrated once at module load. There is no `serf-hub.notifications` JSON
  blob any more, and a write *after* the page is up does not retroactively rehydrate the
  store. This card must opt in and reload.
- **There is no sidebar "Needs you" section to assert on.** The Rail deliberately does not
  render one (`shell/rail/Rail.tsx:563-573`, kata `vbh8`): attention surfaces inline on the
  session's own row instead. `tree.needs_you` itself is untouched and is still what the
  notification engine snapshots (`notifications/attention.ts:53-67`) — so the tier
  assertion moves to the REST response, and the DOM assertion moves to the row's gloss.
- **The client's edge detection is snapshot-diffing, not `prevLevel`.** A `serf/attention/changed`
  broadcast triggers a debounced `/api/tree` refetch (`stores/tree.ts:443-453`, 250 ms);
  `notifications/index.ts` diffs successive tree snapshots and fires on refs that newly
  appear in `needs_you` (`notifications/attention.ts:70-84`).

Steps 1, 3 and 5 are **browser-free** (REST). Steps 2, 4 and 6 need Chrome.

## Pre-state

- Hub, `$HUB`, `$TOKEN`, and the isolated environment exactly as `ask-web-answer.md`'s
  Pre-state (reuse them if that card's hub is still running).
- Two sessions: **Session A** (will ask a question) and **Session B** (an unrelated,
  already-running session whose viewport we watch from).
- `superpowers-chrome:browsing` with tab support, your own Chrome profile claimed
  (`set_profile <worktree-name>`) before the first `use_browser` call, and a **desktop
  viewport ≥ 900 px wide** — below that `AppShell` mounts the mobile stack and no rail at
  all (`shell/useIsMobile.ts:9`, `shell/AppShell.tsx:372`).

## Steps

1. **(browser-free)** Spawn Session B first — trivial, no question — and wait for it to
   settle `awaiting` (a plain completed turn with nothing pending rests `awaiting`, not
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
   echo "SIDB=$SIDB state=$st"
   ```
2. **(browser)** Open Session B's workspace **now** — before Session A ever asks — opt into
   the notification channels, and stub the OS surface. Three separate loads, in this order,
   because the prefs hydrate at module load and the stubs are per-document:
   ```
   navigate $HUB/auth?token=<TOKEN>&next=/s/local:<SIDB>
   ```
   ```javascript
   // values are the strings "1"/"0", never JSON (stores/prefs.ts:224-229)
   localStorage.setItem("serf.prefs.notificationsTitle", "1");
   localStorage.setItem("serf.prefs.notificationsFavicon", "1");
   localStorage.setItem("serf.prefs.notificationsOs", "1");
   localStorage.removeItem("serf.prefs.notificationsLoudScope");  // default "asks"
   "seeded"
   ```
   ```
   navigate $HUB/s/local:<SIDB>
   ```
   ```javascript
   // A CDP-driven tab has no real OS notification surface a screenshot can observe, and
   // headless focus/permission semantics are not what's under test. Stub the exact three
   // things notifications/channels.ts:15-22 reads at fire time.
   (() => {
     window.__asked = [];
     window.Notification = function (title) { window.__asked.push({ title }); };
     Object.defineProperty(window.Notification, "permission", { value: "granted", configurable: true });
     Object.defineProperty(document, "hasFocus", { value: () => false, configurable: true });
     return {
       armed: true,
       port: location.port,
       titleOptIn: localStorage.getItem("serf.prefs.notificationsTitle"),
       osOptIn: localStorage.getItem("serf.prefs.notificationsOs"),
     };
   })()
   ```
   Then wait ~3s for the first `/api/tree` snapshot to land. **That first snapshot IS the
   baseline** (`notifications/index.ts:62-68`), and no baseline means no edge firing — so
   Session B's tab must have its baseline landed *before* Session A asks, not merely be
   open.
3. **(browser-free)** Now spawn Session A with a question-asking first turn:
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
4. **(browser)** From Session B's tab — never navigated away, this is the "different
   session's viewport" — wait out the hub's attention watcher (it ticks every 5 s,
   `cmd/serf-hub/main_background.go:50`) plus the client's 250 ms refetch debounce, then
   read all three channels:
   ```
   sleep 8
   ```
   ```javascript
   (() => {
     const link = document.querySelector("link[rel='icon']");
     const href = link ? decodeURIComponent(link.href) : "";
     const row = document.querySelector('[data-session-ref="local:<SIDA>"]');
     return {
       port: location.port,
       path: location.pathname,                              // still /s/local:<SIDB>
       title: document.title,
       faviconDot: /r='18'/.test(href),
       faviconAmber: /fill='#e0af68'/.test(href),
       captured: window.__asked,
       railRowPresent: !!row,
       railRowActivity: row?.querySelector('[data-testid="rail-row-activity"]')?.textContent,
     };
   })()
   ```
5. **(browser-free)** Read the tier itself from the hub, independent of any client-side
   refresh timing. `/api/tree` accepts the same Bearer token as every other API route
   (`cmd/serf-hub/internal/hubedge/auth_token.go:113-120`):
   ```bash
   curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/tree" | jq --arg sid "$SIDA" '{
     summary: .attentionSummary,
     count: (.needs_you | length),
     rowA: (.needs_you[] | select(.session_id == $sid) | {ref, state, ask_pending, title})
   }'
   ```
6. **(browser + browser-free) `loudScope` default: a generic your-move settle stays quiet.**
   Re-arm the capture in Session B's tab (`window.__asked = []` — the stubs from step 2
   survive, the array does not need to), then spawn a **third** throwaway session with a
   no-ask prompt and wait for it to settle `awaiting`:
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
   Wait another ~8s, then read the capture and the title together:
   ```javascript
   ({ captured: window.__asked, title: document.title })
   ```

## Expected

- **Step 4 (title)**: `title` matches `/^\(\d+\) /` — the attention-count prefix
  `"(" + (needsYou + error) + ") " + base` (`notifications/title.ts:35-38`), applied only
  because step 2 opted in. Falsify: no prefix at all after the opt-in (the title channel is
  dead), or a prefix appearing *without* the opt-in (a default was silently flipped back on
  — `stores/prefs.ts:268-273` says all four are OFF).
- **Step 4 (favicon)**: `faviconDot` and `faviconAmber` are both `true` — the amber
  `needs_you` corner dot (`notifications/favicon.ts:14,33-41`). Falsify: no dot after the
  opt-in.
- **Step 4 (OS channel)**: `captured` contains exactly one entry whose `title` is
  `serf · <Session A's title or its ref>` (`notifications/channels.ts:22`). This is the
  **transition into `needs_you` from outside the tier** that `detectFires` looks for
  (`notifications/attention.ts:70-84`), fired only because the tab is unfocused, is the
  Web-Locks leader, and opted in (`notifications/index.ts:77-82`). Falsify: nothing
  captured — the cross-session edge never fired.
- **Step 4 (rail, qualitative)**: `railRowPresent` is `true` and `railRowActivity` reads
  `question waiting` — the ask band of `awaiting`, lowercased by `humanizeState`
  (`shell/rail/RailRow.tsx:138-147`, rendered at `:531`). Do **not** look for a "needs you"
  section; there isn't one by design. Falsify: the row reads `your move` (the wire's
  `ask_pending` never reached the rail) or the row never appears without a reload (the
  broadcast-driven refetch regressed).
- **Step 5 (exact)**: `rowA.state` is `"awaiting"`, `rowA.ask_pending` is `true`
  (`hubapi/types.go:114`), `rowA.ref` is `local:<SIDA>`, and
  `summary.needsYou` is ≥ 1 (`hubapi/types.go:50-55`). Assert on presence and identity,
  never on a hardcoded population count — the live tier can also hold Sessions B and C
  depending on what is resting `awaiting` when this runs. Falsify: Session A absent from
  `needs_you`, or present with `ask_pending` unset.
- **Step 6**: `captured` is still **empty** — Session C's generic your-move transition does
  not fire under the default `loudScope: "asks"`, which narrows to `askPending ||
  level === "error"` (`notifications/attention.ts:76-79`; the default is
  `stores/prefs.ts:373`). `title`'s count *does* rise, because counts apply
  unconditionally on every snapshot, before any edge gating
  (`notifications/index.ts:54-58`). Falsify: a plain your-move settle also fires an OS
  notification under `"asks"`, or Session A's ask-pending transition did not.
- Falsification (whole card): if a pending ask does not surface the session in the hub's
  `needs_you` tier *and* raise the OS notification from a different session's viewport, the
  needs-you chain is broken.

## Cleanup

```bash
for sid in $SIDA $SIDB $SIDC; do
  curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
    -d '{}' "$HUB/api/sessions/local:$sid/shutdown" >/dev/null 2>&1
done
rm -rf "$tmpdir_a" "$tmpdir_b" "$tmpdir_c"
```
Leave the hub up if `ask-web-answer.md`'s run still needs it; otherwise kill it by the PID
that card captured and remove its run directory. The old `$HUB/s/$SID/shutdown` shim is
gone and 404s silently, leaving the daemon running to poison the next run's state poll.

## Sharp edges

- **Seed the prefs, then reload — a post-load write does nothing.** `prefsStore` hydrates
  from `localStorage` once at module evaluation (`stores/prefs.ts:344-378`). Writing the
  keys into an already-running page leaves the engine reading the all-OFF defaults, and the
  card fails for a reason that has nothing to do with a regression. Step 2's three-load
  dance is the whole point; do not collapse it.
- **No baseline, no edge firing.** The first tree snapshot after `initNotifications()` is
  the baseline and fires nothing (`notifications/index.ts:62-68`); a reconnect
  re-baselines the same way (`:111-124`). If Session B's tab is opened *after*
  Session A is already `awaiting`, its baseline already contains Session A, so
  title/favicon counts are right immediately but there is no into-transition left to
  observe and the OS notification for that ask is missed. Not a bug — the documented
  invariant. Always open and settle the watching tab first.
- **One extra tab on this origin can silently break step 4.** OS/sound fire only on the
  Web-Locks leader, and the first tab to take the `serf-hub-os-leader` lock holds it for
  its whole lifetime (`notifications/leader.ts:12-45`). A leftover hub tab from an earlier
  run makes Session B's tab a follower, and `captured` stays empty with everything else
  correct. Close other hub tabs, or claim a fresh Chrome profile.
- **The stubs are per-document.** Any `navigate`/reload after step 2 wipes
  `window.__asked`, the `Notification` stub, and the `hasFocus` override. Re-run the
  arming `eval` immediately after any navigation, before the wait.
- **Two cadences, not one.** The hub's attention watcher ticks every 5 s
  (`cmd/serf-hub/main_background.go:50`) and its first tick seeds silently so a hub restart
  never re-notifies (`cmd/serf-hub/internal/hubcore/attention.go:113-133`); the client then
  debounces its refetch by 250 ms (`stores/tree.ts:453`). `sleep 8` covers both with
  margin. There is no client poll loop any more.
- **The title count is not a fixed digit.** *Every* `awaiting` session counts toward
  `attentionSummary.needsYou`, including Session B's own plain your-move rest, so the
  prefix can legitimately read `(2)` or `(3)`. Assert the *transition* (`captured` gaining
  Session A's entry) and the prefix's shape, never a specific number.
