# Serf Hub Web Routing

Serf Hub uses a full-page app shell plus HTMX-loaded fragments.

- User-facing page routes stay clean and deep-linkable: `/`, `/new`, `/s/:ref`, `/settings`, and `/settings/:section`.
- AppWire and API routes stay under `/rpc` and `/api`.
- Internal fragments live under `/_partials/*` and require `HX-Request: true`.

Fragment routes:

- `/_partials/workspace/empty`
- `/_partials/workspace/spawn`
- `/_partials/s/:ref/workspace`
- `/_partials/s/:ref/state`
- `/_partials/s/:ref/meta`
- `/_partials/s/:ref/details`
- `/_partials/s/:ref/tasks`
- `/_partials/settings/:section`

Legacy fragment-looking paths such as `/sidebar`, `/workspace/spawn`, and
`/s/:ref/state` are not public routes. Direct browser navigation should land on
a page route or fail instead of rendering a fragment without the app shell.

Sidebar routes (JSON, not fragments):

The sidebar is client-rendered: it fetches JSON and keeps its own keyed DOM,
instead of swapping in a server-rendered HTML partial. `/_partials/sidebar`
and `/_partials/sidebar/project` are gone — there is no server-rendered
sidebar left to fragment-route.

- `GET /api/tree` — the sidebar's data source: the full navigation tree
  (live sessions, projects, Needs-you, Pinned, archived projects, test runs).
- `GET /api/tree/project?key=` — one project's node, keyed by its slug; used
  to re-fetch a project the client has expanded.
- `POST /api/favorite` — set or clear a session's or project's favorite
  (Pinned) decision.
- `POST /api/project/delete` — delete every session under a project.
- `POST /api/sessions/{ref}/rename` — rename a session.

## What changed (2026-07-04 sidebar rebuild)

- The sidebar is now client-rendered: it fetches `GET /api/tree` and
  reconciles a keyed DOM against it, instead of htmx-swapping a
  server-rendered partial. The dead `/_partials/sidebar` and
  `/_partials/sidebar/project` routes and their Go templates are removed.
- Project identity is the full working directory, not its basename — two
  same-named projects at different paths get distinct slug-based keys
  instead of colliding into one node.
- Sessions and projects can be favorited (`POST /api/favorite`); favorited
  sessions surface in a Pinned tier.
- A project and every session under it can be deleted in one action
  (`POST /api/project/delete`).
- Test-run sessions are classified into their own tier server-side, in
  `/api/tree` — the client does not yet render them as a distinct sidebar
  section.
- Rename (`POST /api/sessions/{ref}/rename`) is in scope for both live and
  ended sessions.
