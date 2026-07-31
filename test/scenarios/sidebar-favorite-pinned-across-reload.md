# sidebar-favorite-pinned-across-reload: favoriting a session is server truth and survives a hard reload

**What this covers**: `POST /api/favorite` (`cmd/serf-hub/web.go:169`,
handler `web_api_favorite.go:16-56`) plus the `/api/tree` `favorites[]`
projection at tier `"pinned"` (`web_api_tree.go:230-245`), and that the rail's
Pinned rows are driven entirely by server data — never by `localStorage`. The
rail persists exactly two things client-side (row expand state and the
sidebar's hidden/width chrome); nothing favorite-related is cached locally, so
a row that survives a cold page load can only have come from the server.

**Surface**: see `docs/agentic-testing.md`, "Driving the web UI" — the selector
map there is the single place these hooks are maintained. A session row is
`[data-session-ref="local:<SID>"]` (`RailRow.tsx:509`), not
`.sb-row[data-row-id=…]`; the star is `[data-testid="favorite-star"]`
(`RailRow.tsx:540-544`). `row_id` is still on the wire and is still
`pinned:local:<SID>` (`web_api_tree.go:1315-1319`) — it is just no longer a DOM
attribute, so assert it over REST and assert the ref in the browser.

## Pre-state

- A freshly built `serf-hub` on an isolated `$HOME` and a kernel-assigned port
  — the Setup checklist in `docs/agentic-testing.md`. Never a real hub. Build
  the frontend (`make build-web`) before the hub for the browser half.
- One top-level session whose state is **not** attention-worthy — an `ended`
  session is the simplest (see Sharp edges on the NeedsYou exclusion). Note its
  ref, `local:<SID>`.
- Browser authenticated to the test hub, on its own Chrome profile.

## Steps

Steps 1-2 and 5 are **browser-free** and carry the exact assertions. Steps 3-4
need a browser and assert only that the rendered row follows the server.

1. `POST /api/favorite` with body
   `{"kind":"session","id":"local:<SID>","favorited":true}`.
2. `GET /api/tree`; inspect `favorites[]`.
3. In the browser, perform a **hard reload** — a fresh top-level navigation to
   `/auth?token=$TOKEN&next=/`, not a client-side refetch. The point is to
   destroy every scrap of in-page state before asserting.
4. Read the Pinned section and the row:
   ```javascript
   (() => {
     const heads = Array.from(document.querySelectorAll("h3"), (h) => h.textContent);
     const row = document.querySelector('[data-session-ref="local:<SID>"]');
     return {
       port: location.port,
       headings: heads,                                  // must contain "Pinned"
       rowPresent: !!row,
       star: !!row?.querySelector('[data-testid="favorite-star"]'),
       localFavoriteKeys: Object.keys(localStorage).filter((k) => /favorit|pin/i.test(k)),
     };
   })()
   ```
5. `POST /api/favorite` with `"favorited":false`, then `GET /api/tree` again.

## Expected

- **Step 1 (exact)**: `200 {"ok":true}` (`web_api_favorite.go:55`). Falsify: a
  `400 {"error":"session id must name a real top-level session"}` — the id
  didn't resolve. `POST /api/favorite` now validates that the id names a real
  top-level session before writing (`topLevelFavoriteSessionID`,
  `web_api_favorite.go:37-44,58-96`), and it accepts the bare `<SID>`, the
  `local:<SID>` ref, or the internal id interchangeably
  (`favoriteSessionIDMatches`, `:87-95`).
- **Step 2 (exact)**: exactly one entry in `favorites[]` with
  `"session_id":"<SID>"`, `"tier":"pinned"`, `"favorite":true`, and
  `"row_id":"pinned:local:<SID>"`. Falsify: any other tier or row-id prefix
  (`needsyou:` means the session was attention-worthy — see Sharp edges), or an
  empty `favorites[]` despite the `200`.
- **Step 4**: `headings` contains `Pinned`; the row exists at
  `[data-session-ref="local:<SID>"]`; it carries `[data-testid="favorite-star"]`;
  and `localFavoriteKeys` is **empty**. All of that holds immediately after the
  reload — no click, no project expand — because the Pinned section is rendered
  unconditionally from `tree.favorites`, ahead of Projects
  (`Rail.tsx:581-587`), and a Pinned row is a depth-0 flat entry
  (`sessionNodes`, `railNodes.ts:178-180`), not something nested under a
  collapsed project. Falsify: the row is missing after a reload, or requires
  expanding its parent project to appear — either would mean the client derived
  "pinned" from local or optimistic state instead of `/api/tree`'s
  `favorites[]`.
- **Step 5 (exact)**: `200 {"ok":true}` and `favorites[]` no longer contains
  the session. Falsify: the decision doesn't clear — the store write is
  one-way.
- **Cross-check**: no localStorage key the frontend writes is favorite-related.
  Every `localStorage.setItem` call site in `cmd/serf-hub/frontend/src` writes
  into one of: `serf.prefs.<name>` (`stores/prefs.ts:110,125`),
  `serf.rail.expanded.v1` (`railExpansion.ts:19`),
  `serf.workspace.layout.v2` (`shell/DockHost.tsx:34,115`),
  `serf.search.recentCommands` (`shell/palette/recentCommands.ts:9,26`),
  `serf-hub.spawn-defaults.*` (`panes/spawn/spawnDefaults.ts:13,54`), or a
  per-session composer draft / seen watermark
  (`panes/session/composer/draft.ts:18,40`,
  `panes/session/transcript/flow/seenWatermark.ts:12,29`). Re-run that grep if
  step 4's `localFavoriteKeys` ever comes back non-empty. If the star renders,
  it is because `/api/tree` said so, full stop.

## Cleanup

- Step 5 already unfavorites. Otherwise discard the scratch session/project
  with the run directory — the favorite decision lives in the isolated state
  root's index DB and goes away with it.
- Kill the hub by the PID you captured (Cleanup recipe in
  `docs/agentic-testing.md`).

## Sharp edges

- **Two localStorage keys exist for *unrelated* rail state and are easy to
  mistake for a favorite cache**: `serf.rail.expanded.v1`, one JSON blob of
  per-row expand overrides (`railExpansion.ts:19`, replacing the old
  key-per-row `serf-hub.sidebar.expanded.<projectKey>` scheme), and the
  chrome prefs `serf.prefs.sidebarHidden` / `serf.prefs.sidebarWidth`
  (`stores/prefs.ts:366-367`, replacing `serf-hub.sidebar.rail`; the old
  `sidebarMode` key is dead and never read, `prefs.ts:56-61`). Expand and
  chrome are client-cached; favorite / rename / archive / delete are not.
- **The Pinned tier excludes anything already surfaced in NeedsYou**
  (`needsYouIDs` filter, `web_api_tree.go:230-241`). A session in `awaiting`,
  `warning`, or `errored` lands in `needs_you[]` with a `needsyou:` row-id
  instead, and step 2's assertion fails for a reason that has nothing to do
  with favoriting. Use an `ended` (or plain `active`) session.
- **The rail does not render a NeedsYou section at all** (`Rail.tsx:563-573`),
  so a session filtered out of Pinned by the rule above does not visibly move —
  it just quietly stops being in the Pinned list. That is why step 2's REST
  assertion is the authoritative one and step 4 is only corroboration.
- **The star is gated on top-level-ness as well as the flag**:
  `session.favorite === true && isTopLevelSession(session)`
  (`RailRow.tsx:540`, `isTopLevelSession` at `:338-342` excludes `subagent`,
  `fork`, `cluster`). Favoriting a nested row writes a decision that never
  surfaces — the row menu correctly doesn't offer it (`:355-361`), and neither
  should this card.
- **This card deliberately drives the favorite over REST, not by clicking the
  star's menu item.** The rail applies an optimistic overlay from before the
  POST until the follow-up refetch settles (`railPending.ts:78-79`,
  `Rail.tsx:318-338`), so a UI-driven check risks reading the optimistic echo
  rather than server truth. A raw POST plus a hard reload rules that out by
  construction.
- Use a dedicated Chrome profile; the auth cookie is not port-scoped. Keep the
  `location.port` assertion inside every `eval`.
