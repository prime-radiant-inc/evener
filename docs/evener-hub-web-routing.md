# Evener Hub Web Routing

Evener Hub serves the SPA shell for page navigation. The browser reads session
data and performs mutations over the authenticated AppWire connection at `/rpc`.
The [HTTP mux](../cmd/evener-hub/web.go) retains document, image, asset, auth,
health, and subscription-debug surfaces.

- Deep-linkable page routes include `/`, `/new`, `/s/:ref`, `/thread/:ref`,
  `/settings`, `/settings/:section`, and `/credentials`.
- `/s/:ref/images/:sha` serves session images through the
  [session route handler](../cmd/evener-hub/web_workspace.go).
- `/doc/file` serves raw session documents; `/doc/image` serves document images.
- `/api/health` and `/api/debug/subscriptions` expose health and subscription
  diagnostics.

The `/_partials/*` fragment endpoints and session form-action endpoints are
unregistered. The session route handler returns 404 for unsupported
`/s/:ref/:subroute` paths, including `state`, `details`, and `tasks`. Other
unmatched paths may serve the SPA shell through the root catch-all route.

Sidebar navigation:

The sidebar is client-rendered: it reads typed navigation resources over the
authenticated AppWire connection and keeps its own keyed DOM, instead of
swapping in a server-rendered HTML partial.

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
