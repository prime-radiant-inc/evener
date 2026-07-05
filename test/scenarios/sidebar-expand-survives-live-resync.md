# sidebar-expand-survives-live-resync: expanding a collapsed project survives a resync triggered by live activity elsewhere

**What this covers**: the rebuilt sidebar's client-side expand/collapse bookkeeping
(`cmd/serf-hub/assets/sidebar.js`, `model.expanded`) must survive a `doResync()`
triggered by unrelated live activity. Regression class: a resync re-fetches
`/api/tree` and repaints the whole tree — if the expanded-project bookkeeping
doesn't carry across that repaint, a project the user deliberately opened could
silently collapse back to zero rows the moment something happens anywhere else
on the hub.

## Pre-state

- A freshly built hub + daemon, a scratch `SERF_STATE_DIR`-equivalent (see
  `docs/agentic-testing.md`), a browser authenticated against the hub.
- A project A containing at least one **ended** (non-live) session, so it
  renders collapsed by default. (A project only auto-expands when
  `rollup_live>0` or `rollup_attn>0` — see `hubcore.BuildTree`'s
  `Expanded: rollupLive > 0 || rollupAttn > 0`.)

## Steps

1. Spawn a session in a fresh working dir (project A: `POST /api/spawn`), let
   it finish its first turn, then `POST /api/sessions/<id>/shutdown` so it's
   `ended`.
2. Open `/` in the browser. Confirm project A's header
   (`.project-header[data-project-key="<key>"]`) renders with **zero**
   `.sb-row[data-project-key-of="<key>"]` rows underneath it (collapsed).
3. Click project A's header. Confirm the expected session row(s) now render
   and `window.SerfSidebarModel.expanded.has("<key>")` is `true`.
4. Elsewhere — a different, brand-new working dir — spawn a **live** session
   against a real, credentialed model and let it run at least one real turn.
   This fires a `thread/started` (and later `thread/status/changed`)
   notification over the appwire WebSocket, which the sidebar's
   `onNotification` handler turns into `scheduleResync()` (≥2s coalesced,
   trailing) → `doResync()`.
5. Wait at least 4–5s past the spawn (covers the 2s coalescing window plus the
   fetch round trip). Re-query project A's rows.

## Expected

- After step 2: 0 rows for project A (collapsed).
- After step 3: the session row(s) for project A render with the correct
  title; the expand-state `Set` contains the key.
- After step 5 (post-resync): project A's row(s) are **still present** with
  the same titles. `window.SerfSidebarModel.seq` has increased versus step 3
  (proves a resync actually ran, not just an idle DOM).
- Falsification: if project A's rows are 0, or a title reverts to a stale
  value, after step 5 — the resync dropped the client's expand bookkeeping,
  and the regression this card guards against is back.

## Cleanup

- Shut down every spawned session (`POST /api/sessions/<id>/shutdown`).
- Remove the scratch working directories.

## Sharp edges

- A project's `default_expanded` field is **omitted** from the `/api/tree`
  JSON when `false` (Go `omitempty` on a bool — see `hubapi.TreeProject`).
  The client computes
  `model.expanded.has(p.key) || p.default_expanded` and feeds that straight
  into `String(...)` for the header's `aria-expanded` attribute. When both
  operands are falsy/undefined this renders the literal string
  `aria-expanded="undefined"` instead of `"false"` — a real, reproducible
  accessibility bug (screen readers get an invalid ARIA value). It does
  **not** affect the expand/collapse mechanism itself (driven by the JS
  `Set`, not the DOM attribute), so it won't block this scenario — just
  don't be surprised by it, and consider filing it separately.
- `mktemp -d` on macOS returns a `/var/folders/...` path, but the server
  symlink-resolves working dirs to `/private/var/folders/...` before they
  ever reach `/api/tree`. Match projects by reading the server's own
  `working_dir` field back, not your raw shell variable, or your key lookup
  will silently come up empty.
- If the machine has other concurrent Serf/browser-automation work running,
  use a **dedicated browser profile** (the browsing tool's `set_profile`
  action) for this card. The hub's auth cookie (`serf_hub_auth`) is not
  port-scoped — two hub instances on `127.0.0.1:<different ports>` sharing
  one Chrome profile/cookie-jar will silently clobber each other's session,
  producing a spurious "Unauthorized" page that has nothing to do with the
  sidebar itself. `list_tabs`/`switch_tab` are also unreliable across
  multiple concurrently-running Chrome processes on this tool — re-verify
  the active tab's URL before trusting an eval result.
- A freshly-opened tab can occasionally race its own first `fetchTree()` (the
  sidebar stays on the skeleton placeholder because the fetch's `.then()`
  never ran and its failure path is an empty `.catch()`, so it fails
  silently). Call `window.SerfSidebar.refresh()` once and confirm
  `window.SerfSidebarModel.tree` is truthy before asserting anything.
