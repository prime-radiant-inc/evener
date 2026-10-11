# Evener Hub Web Routing

Evener Hub uses a full-page app shell plus HTMX-loaded fragments.

- User-facing page routes stay clean and deep-linkable: `/`, `/new`, `/s/:ref`, `/settings`, and `/settings/:section`.
- AppWire requests use `/rpc`; HTTP routes that have not yet migrated stay
  under `/api`.
- Internal fragments live under `/_partials/*` and require `HX-Request: true`.

Fragment routes:

- `/_partials/workspace/empty`
- `/_partials/workspace/spawn`
- `/_partials/s/:ref/workspace`
- `/_partials/s/:ref/state`
- `/_partials/s/:ref/details`
- `/_partials/s/:ref/tasks`
- `/_partials/settings/:section`

Legacy fragment-looking paths such as `/sidebar`, `/workspace/spawn`, and
`/s/:ref/state` are not public routes. Direct browser navigation should land on
a page route or fail instead of rendering a fragment without the app shell.

Sidebar navigation (AppWire, not fragments):

The sidebar is client-rendered: it reads typed navigation resources over the
authenticated AppWire connection and keeps its own keyed DOM, instead of
swapping in a server-rendered HTML partial. `/_partials/sidebar` and
`/_partials/sidebar/project` are gone — there is no server-rendered sidebar
left to fragment-route.

- `evener/navigation/read` — the typed sidebar read surface. Its `manifest`
  resource provides the bounded descriptor/count index and attention summary;
  `section`, `pin_catalog`, `pin_section`, `catalog`, `project`,
  `project_page`, and `location` resources provide bounded rows and ownership
  details. The canonical request shapes, pagination rules, and response
  envelope are maintained in the [AppWire navigation resource matrix](developing-evener/agentic-testing.md).
  Representation version 3 carries flat session summaries with compact activity
  counts. Session delegate, shell-job and watch collections use the session
  activity methods in the [AppWire catalog](appwire-protocol.md); a navigation
  `location` remains a placement lookup. The separate `evener/archived/list`
  path retains bounded inline fork-original conversations and their disclosure;
  the normalized navigation graph stays flat. See the
  [session activity boundary](product/session-activity.md#navigation-boundary).
- `evener/favorite/set` — set or clear a project's favorite (Pinned) decision;
  the typed method retains the explicit rejection for obsolete session-shaped
  favorite requests.
- `evener/project/delete` — delete every removable session under a
  path-validated local project and return detailed outcomes plus its committed
  navigation receipt.
- `evener/thread/name/set` — rename a live or ended session.

## Navigation identity and mutations

Project identity includes the full working directory; equally named directories
at different paths remain distinct projects. Source identity keeps projects on
different machines distinct. The
[navigation projection](../cmd/evener-hub/navigation_projection.go) owns the
bounded resources and project tiers used by the client.

Project favorites use `evener/favorite/set`. Session pins use
`evener/session-pin/assign` and `evener/session-pin/unpin` with the navigation pin
catalog. A project deletion uses `evener/project/delete`, which validates its
working directory, reports per-session outcomes, and returns a navigation
receipt. Clients reconcile the returned invalidation targets through AppWire.
- Rename uses `evener/thread/name/set` for both live and ended sessions.

## Session document reads

The [document server](../cmd/evener-hub/doc_serve.go) resolves a file against its
owning session's current directory for each read. Live reads obtain the current
daemon descriptor. Archived reads reload session metadata from disk rather than
using the navigation index's cached directory. Remote text reads route through
the owning host with the captured absolute file target.

A missing archived metadata file returns `ResourceNotFound` (HTTP 404). Other
metadata read or decode failures return `SessionUnavailable` (HTTP 503), so the
shared document read owner keeps its paced retry demand instead of treating a
temporary metadata fault as a missing file. Raw HTTP, image HTTP and AppWire
document reads share this classification. Once metadata is readable again, the
next read uses its fresh directory without rebuilding the navigation index.

The browser DocPane and native Reader retain the document's captured target and
owning session during recovery. The shared
[document read demand](../appwire-client/typescript/documentReadDemand.ts)
paces retries while the reader is visible and connected. Hidden or disconnected
readers preserve demand for re-entry. A text HTTP 404 ends the current paced
retry series. Reload, reopen, visibility or connection re-entry, and reader
refresh triggers start a new series. Image load callbacks do not expose HTTP
status, so failed image loads keep paced retries.
