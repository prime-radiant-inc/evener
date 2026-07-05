# sidebar-favorite-pinned-across-reload: favoriting a session is server truth and survives a hard reload

**What this covers**: `POST /api/favorite` plus the `/api/tree` `favorites[]`
(tier `"pinned"`) projection, and that the sidebar's Pinned row is driven
entirely by server data — not `localStorage`. The rebuilt sidebar only
persists two things client-side (project expand-state and rail mode); nothing
favorite-related lives in `localStorage`.

## Pre-state

- A hub with at least one session (live or ended; use one that is not also
  attention-worthy — see Sharp edges) to favorite. Note its ref (`local:<id>`).
- Browser authenticated against the hub.

## Steps

1. `POST /api/favorite` with body `{"kind":"session","id":"<id>","favorited":true}`.
2. `GET /api/tree`. Inspect `favorites[]`.
3. In the browser, perform a **hard reload** — a fresh top-level navigation to
   `/` (or `/auth?token=...&next=/`), not a client-side resync.
4. Query `document.querySelector('.sb-row[data-row-id="pinned:local:<id>"]')`.

## Expected

- Step 1: `200 {"ok":true}`.
- Step 2: exactly one entry in `favorites[]` with `"session_id":"<id>"`,
  `"tier":"pinned"`, `"favorite":true`, and `row_id` of the form
  `pinned:local:<id>`.
- Step 4: the Pinned row exists in the DOM with the session's title and a
  `data-favorite` attribute, **immediately after a hard reload** — no click,
  no project-expand needed, since Pinned rows are unconditionally flattened
  into the sidebar (`sidebar.js`'s `flatten()` pushes `tree.favorites`
  unconditionally, ahead of any project section).
- Falsification: the row is missing after reload, or requires expanding its
  parent project to appear (would mean the client derived "pinned" from
  local/optimistic state instead of the server's `favorites[]`).
- Cross-check:
  `Object.keys(localStorage).filter(k => k.indexOf("favorite") >= 0)` is
  empty — there is no local favorite cache to fall back on. If the row
  renders, it's because `/api/tree` said so, full stop.

## Cleanup

- `POST /api/favorite {"kind":"session","id":"<id>","favorited":false}` to
  unfavorite, or just discard the scratch session/project.

## Sharp edges

- `localStorage` **is** used for two unrelated things — expand-state
  (`serf-hub.sidebar.expanded.<projectKey>`) and rail mode
  (`serf-hub.sidebar.rail`). Don't confuse those with favoriting; only
  expand/rail are client-cached, favorite/rename/archive/delete are not.
- The Pinned tier excludes anything already surfaced in NeedsYou (see
  `web_api_tree.go`'s `needsYouIDs` filter in `handleAPITree`). If your test
  session is itself attention-worthy (state `awaiting`/`warning`/`errored`),
  it shows under NeedsYou instead, with a `needsyou:` row-id prefix, not
  `pinned:` — use an `ended` (or plain `active`, non-attention) session so
  the two tiers don't overlap and the assertion stays unambiguous.
- The optimistic-pending path (`sidebar.js`'s `favorite()`/`addPending`)
  updates the DOM instantly on click, before the POST resolves. This card
  deliberately drives the favorite via a raw POST (not a UI click) and checks
  after a hard reload specifically to rule out "still showing the optimistic
  echo" as a false pass.
