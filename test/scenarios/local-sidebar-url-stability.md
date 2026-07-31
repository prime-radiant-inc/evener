# local-sidebar-url-stability: a rail row opens its session at the one canonical ref URL

**What this covers**: `docs/superpowers/specs/2026-07-13-codex-sidebar-session-navigation-design.md`,
row `local-sidebar-url-stability`, retargeted onto the ref form that commit
`8cea30ca6` ("One ref form") settled on. A local Serf session opened from the
rail must land on `/s/local:<session-id>` — the same qualified form a Codex
row uses, with a different host id — and clicking the same row again must not
open a second copy of the session beside the first.

This card used to assert the opposite (that a local row keeps a *bare*
`/s/<session-id>` route). That was the pre-`8cea30ca6` behaviour and is now
the bug, not the contract: `hubapi.ParseRef` has always required
`<host>:<session>`, the frontend accepted a bare id anyway, and the two forms
compare as different panes — so the same session could occupy two panes at
once. `urlToPane` now matches a qualified ref only (`isRef`,
`cmd/serf-hub/frontend/src/shell/routing.ts:29-32`) and the palette dropped
its bare-id fallback. Jesse's call was no back-compat for the old form.

**Surface**: see `docs/agentic-testing.md`, "Driving the web UI" — rail rows
are `[data-session-ref="local:<SID>"]` (`shell/rail/RailRow.tsx:509`); there
is no `.sb-row` class and no `data-ref` attribute.

## Pre-state

- Freshly built `serf-hub` on an isolated `$HOME` and a kernel-assigned port
  (Setup checklist in `docs/agentic-testing.md`). The frontend must be built
  (`make build-web`) before the hub, or the SPA is a placeholder.
- Browser authenticated to the test hub (`/auth?token=$TOKEN&next=/`).
- At least one local Serf session visible in the rail. Spawning one via
  `POST /api/spawn` is enough; it does not need to be live.

## Steps

1. Find a local session row in the rail and read its ref off the row itself,
   not off its label:
   ```javascript
   Array.from(document.querySelectorAll("[data-session-ref]"), (el) =>
     el.getAttribute("data-session-ref"),
   )
   ```
2. Click that row's title and watch `decodeURIComponent(location.pathname)`
   and the pane that opens.
3. Click the *same* row again and count how many session panes are open.
4. Deep-link check: navigate directly to `/s/<bare-session-id>` — the form
   this card used to require.

## Expected

- **Step 1**: every local row's `data-session-ref` is `local:<session-id>`,
  colon-qualified. Falsify: a bare id in the attribute (the rail would be
  minting refs the router cannot resolve).
- **Step 2**: `decodeURIComponent(location.pathname)` is exactly
  `/s/local:<session-id>` — the one form `paneToURL` emits for a session pane
  (`shell/routing.ts:93-96`) — and the pane that opens is that same session.
  **Decode first**: `paneToURL` runs the ref through `encodeURIComponent`,
  which escapes the colon, so the raw pathname the rail pushes reads
  `/s/local%3A<session-id>`. Comparing the raw value against a literal colon
  fails on correct code. Falsify: the path is a bare `/s/<session-id>`, or it
  is some other qualified form, or the opened workspace is a different
  session than the row named.
- **Step 3**: still exactly one pane for that session. Falsify: a second pane
  for the same session opens beside the first — the precise regression
  `8cea30ca6` closed, which is what "URL stability" is protecting.
- **Step 4**: the bare-id URL renders "Page not found" with the hint "This
  link doesn't match anything in serf." (`shell/NotFound.tsx:16-17`) and no
  session pane. Falsify: the bare id resolves to a session pane — back-compat
  has crept back in, and step 3's double-pane bug is reachable again.
  Note this is a **client-side** 404: `/s/` serves the SPA shell for any id
  (`cmd/serf-hub/web_workspace.go:38-39`), so `curl -o /dev/null -w '%{http_code}'`
  returns 200 here. Assert the rendered text, never the status code.

## Cleanup

- None; this scenario is read-only apart from browser navigation. Shut down
  any session you spawned for it:
  `curl -s -X POST -H "Authorization: Bearer $TOKEN" -d '{}' "$HUB/api/sessions/local:$SID/shutdown"`.

## Sharp edges

- Read the ref from `data-session-ref`, never from the visible label: local
  rows can share a title with rows from another source, and only the ref
  distinguishes them.
- Steps 1-3 need a browser. Step 4's *server* half does not — but the server
  half is not the assertion; the routing decision this card exists for lives
  entirely in the client bundle.
- Subagent rows deep-link into the rail's own placement correction
  (`8cea30ca6`, first half): a subagent ref opened by URL initially lands in
  the main pane and is relocated once the tree arrives. Give the tree a
  moment before judging *where* a subagent pane ended up; this card's
  assertions are about the URL, not the placement.
