# Serf Hub Web Routing

Serf Hub uses a full-page app shell plus HTMX-loaded fragments.

- User-facing page routes stay clean and deep-linkable: `/`, `/new`, `/s/:ref`, `/settings`, and `/settings/:section`.
- AppWire and API routes stay under `/rpc` and `/api`.
- Internal fragments live under `/_partials/*` and require `HX-Request: true`.

Fragment routes:

- `/_partials/sidebar`
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
