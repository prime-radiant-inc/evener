# Subagent Side-View Chrome — Standalone Thread Document Design

Date: 2026-06-23
Status: Revised after adversarial review; approved design pending implementation planning
Scope: Serf web hub (`cmd/serf-hub`)

## Problem

Subagents can be opened beside a parent session in the web UI. Today, side panes can load the full `/s/<id>` session page, which renders the entire app shell inside the pane: primary sidebar, global search/settings/new-session controls, mobile sidebar affordances, and top-level workspace chrome. That duplicates the host app inside the side pane.

A side-view subagent must still be interactive. It should keep the transcript, live updates, parent breadcrumb, input composer, and the ability to open additional panes to the right.

## Core Design

Separate three surfaces explicitly:

1. **App shell** — global chrome and workspace host.
2. **Thread fragment** — reusable session markup currently represented by `templates/partials/workspace.html`.
3. **Thread document** — a standalone, authenticated, chrome-less HTML document for iframe panes.

Do not iframe raw `/_partials/...` routes. Browser iframe navigation cannot set `HX-Request: true`, and existing partial routes are intentionally HX-gated. The pane target must be a normal GET document route that includes the assets needed to boot a thread view.

## Routes

### Existing full app route

`/s/<id>` remains the public, full-page route for normal navigation.

- It renders `app.html`.
- It includes the global sidebar and app-level controls.
- Its `#workspace` region loads the shared thread fragment.
- It continues to own the browser URL for top-level navigation.

### Existing partial route

`/_partials/s/<id>/workspace` remains the htmx workspace fragment route.

- It stays HX-gated.
- It is used by the full app shell and sidebar htmx swaps.
- It is not used as an iframe `src`.

### New pane/thread document route

Add a standalone route, for example:

- `/thread/<encoded-ref>` or
- `/pane/thread/<encoded-ref>`

The exact path should be chosen during implementation, but it must be a normal authenticated GET route, not HX-gated. It renders a minimal HTML document that:

- omits app-shell chrome;
- includes the shared thread fragment;
- loads the assets needed for renderer, live updates, htmx polling, composer, attachments, markdown, and per-thread controls;
- sets pane/thread context flags so pane-only behavior is explicit.

## Shared Thread Fragment

Keep `templates/partials/workspace.html` as the shared thread fragment, but parameterize app-shell-only controls.

The fragment should include:

- session/subagent title and status;
- parent breadcrumb/banner for subagents;
- conversation transcript container;
- composer/input field;
- input status and queue controls;
- per-thread details/status/actions;
- open-beside affordances for nested panes.

The fragment must not unconditionally render app-shell controls. In particular, the mobile sidebar hamburger currently belongs to the app shell, not the thread document. Add explicit template data such as:

- `ShowSidebarToggle`;
- `ThreadDocumentMode`;
- or equivalent names.

Top-level `/s/<id>` workspace rendering may set `ShowSidebarToggle=true`. The standalone thread document must set it false.

## Thread Document Asset Contract

The standalone thread document must include enough assets to make the thread view executable, not just visually present.

Required capabilities:

- CSS/theme bootstrap.
- htmx for existing polling/swapping attributes used by the thread fragment.
- AppWire/RPC code for live thread reads and updates.
- renderer-format/tools/panels/renderer assets in the order expected by the current app.
- composer/input handling and attachment code.
- markdown rendering (`marked`) and any existing renderer dependencies needed by transcript content.
- toast/focus/pending helpers if required by composer or thread actions.

The implementation may share a template partial for script tags with `app.html`, but the thread document must not load app-shell-only behavior that creates duplicate sidebar/search/settings UI. If an asset initializes global app chrome, split that behavior or guard it behind app-shell presence checks.

## Pane Host and Nested Open-Beside

Side panes should continue using iframe isolation for now because `SerfRenderer` and AppWire client state are document-global singletons. Iframes give each thread its own `window`/`document` and avoid a larger renderer refactor.

Nested open-beside must be supported by an explicit host bridge.

Current code assumes `window.SerfPanes` is absent inside pane iframes and therefore suppresses open-beside controls. That conflicts with the desired behavior. The new contract should be:

1. The top-level app shell owns the real `SerfPanes` manager.
2. The thread document exposes a small pane-client bridge when it is framed.
3. Open-beside actions inside the thread document send a same-origin `postMessage` request to the top-level host, for example `{type: "serf:open-beside", href, title}`.
4. The host validates message origin, source frame, and href before calling `SerfPanes.open`.
5. The new pane opens to the right of the requesting pane when possible; otherwise it follows the existing pane manager ordering rules.

If the thread document is loaded top-level rather than framed, it may either omit open-beside controls or use a local fallback. The primary supported path is host-mediated panes.

## URL Construction and Migration

Centralize pane URL construction.

Create one helper/contract for building thread-document URLs from session refs. Use it everywhere panes are opened:

- subagent rows in `renderer.js`;
- sidebar subagent open-beside controls;
- observer auto-open;
- document/image/file open-beside flows that target sessions or thread-adjacent panes;
- pane restore/normalization code.

The helper must correctly encode:

- local bare IDs;
- `local:<id>` refs;
- remote/source-qualified refs such as `codex:<thread>`;
- refs containing `:` or other path-sensitive characters.

Existing pane state stores raw hrefs in URL `pane=` params and localStorage. Because older entries may contain `/s/<id>`, pane restore must normalize legacy session-pane hrefs to the new thread-document route. This avoids restoring duplicate full app chrome after deploy.

## Breadcrumb and In-Pane Link Policy

Parent breadcrumb behavior must be explicit in pane context.

A parent breadcrumb currently points at `/s/<parent>`. If clicked inside an iframe, that would load the full app shell inside the pane and reintroduce duplicate chrome.

Thread-document behavior should be one of:

- open the parent thread document in the same pane;
- ask the host to focus/open the parent in a pane;
- ask the host to navigate the top-level workspace to the parent.

Implementation should choose one policy and test it. The preferred default is: breadcrumb clicks inside a thread document ask the host to open or focus the parent thread as a pane-safe thread document, not load `/s/<parent>` inside the iframe.

## Security and Isolation

The thread document route must pass through the existing hub auth guard.

CSP and iframe behavior must be specified and tested:

- Preserve protection against third-party framing. Current intended policy is same-origin framing only.
- Ensure the thread document receives the same security headers as other hub pages.
- Decide whether pane iframes are sandboxed.

Sandbox decision:

- If unsandboxed same-origin iframes are used, document the accepted risk: a thread-document XSS could access the host because same-origin iframe DOM access is allowed.
- If sandboxed iframes are used, specify the exact `sandbox` allow-list required for scripts, forms/composer, downloads if any, same-origin storage access, and postMessage.

The host bridge must validate:

- `event.origin` matches the hub origin;
- `event.source` belongs to a known pane iframe;
- requested hrefs are same-origin pane-safe routes or approved document routes;
- titles are treated as text, not HTML.

## Pane Error Handling

The pane manager must handle iframe failures explicitly.

Required behavior:

- show a pane-local loading state while the iframe boots;
- detect iframe load success where possible;
- show a host-rendered pane error for 401/403/404, timeout, or frame refusal where detectable;
- keep the parent workspace stable when a pane fails;
- allow the user to close or retry the failed pane.

Current `panes.js` primarily creates iframes and persists hrefs. It needs an error/loading surface as part of this work.

## Data Flow

### Main session navigation

1. User navigates to `/s/<id>` or clicks a normal sidebar row.
2. Server renders the full app shell.
3. The app shell loads `/_partials/s/<id>/workspace` into `#workspace` via htmx.
4. The workspace fragment renders with app-shell context flags, including any top-level mobile sidebar toggle.
5. Browser URL remains `/s/<id>`.

### Side-pane subagent navigation

1. User clicks open-beside on a subagent row.
2. Code builds the canonical thread-document URL for that subagent ref.
3. Top-level `SerfPanes.open` creates an iframe for the thread document.
4. The thread document loads as a normal authenticated GET.
5. It renders the shared thread fragment with pane/thread-document context flags.
6. It includes required assets and boots renderer/composer/live updates.
7. It does not render global sidebar/search/settings/new-session chrome.
8. Open-beside inside the pane uses the host bridge to open more panes to the right.

## UI Contract

### Thread document includes

- Session/subagent title and status.
- Parent breadcrumb/banner when applicable.
- Conversation transcript.
- Live update behavior.
- Composer/input field.
- Input status and queue controls owned by the thread.
- Per-thread details/action controls that do not depend on global app chrome.
- Open-beside controls backed by the host bridge.

### Thread document excludes

- `#sidebar` and primary-sidebar markup.
- Global search dialog and trigger.
- Global settings links.
- Global new-session controls.
- Mobile sidebar hamburger/drawer controls.
- App-shell-only keyboard shortcuts or duplicate top-level navigation.

## Testing Plan

### Server/template tests

- `/s/<id>` direct GET renders full app shell with `#sidebar` and workspace loader.
- `/_partials/s/<id>/workspace` remains HX-gated and works with `HX-Request: true`.
- Direct GET of the new thread-document route succeeds without `HX-Request`.
- Thread-document route is authenticated and receives expected security headers.
- Thread document contains conversation container, composer, status/header, and required script/style tags.
- Thread document excludes `#sidebar`, global search/settings/new-session controls, and sidebar/hamburger toggle.
- Subagent thread document includes the parent breadcrumb/banner.
- Breadcrumb href/action in thread-document context is pane-safe and does not target full `/s/<parent>` inside the iframe.
- Route encoding works for bare local IDs, `local:<id>`, and source-qualified refs.

### JS tests

- Renderer subagent open-beside builds the thread-document URL, not `/s/<id>`.
- Sidebar subagent open-beside builds the same thread-document URL.
- Observer auto-open builds pane-safe thread-document URLs.
- Pane restore normalizes legacy `/s/<id>` hrefs to thread-document hrefs.
- Thread-document open-beside uses the host bridge when framed.
- Host bridge validates origin/source/href before opening a pane.
- Open-beside controls remain visible/usable inside a framed thread document.
- Pane manager renders loading and error states for iframe failures/timeouts.

### Integration/browser-style regression

Run a real or JSDOM-backed pane scenario that verifies:

1. Open a real subagent beside the parent.
2. The iframe target is the thread-document route, not `/s/<id>`.
3. The pane has transcript, composer, and parent breadcrumb.
4. The pane has no global sidebar/top-level nav/hamburger.
5. Renderer/composer initialization succeeds in the framed document.
6. Opening another subagent/document from inside the pane sends a validated host request and opens a new pane to the right.
7. A bad pane URL shows pane-local error UI and does not navigate the parent workspace.

## Non-Goals

- Refactoring `SerfRenderer` into a multi-instance in-page renderer.
- Replacing iframe-per-pane with in-page multi-session rendering.
- Redesigning the transcript or composer UI.
- Removing normal full-page `/s/<id>` navigation.
- Building a new global navigation model.

Pane URL persistence is not a non-goal because the current pane manager already persists/restores pane hrefs. This work must preserve or migrate that behavior enough to avoid restoring duplicate app chrome.

## Implementation Notes

- Prefer one shared thread fragment and two document shells: full app shell and minimal thread document shell.
- Do not iframe raw partial routes.
- Do not rely on client-only hiding of full app chrome as the primary solution.
- Keep route/auth/CSP behavior explicit and covered by tests.
- Treat host bridge and URL normalization as part of the feature, not polish.
- Keep changes narrow to route/template boundaries, pane URL construction, bridge wiring, pane error handling, and tests.

## Self-Review

- No placeholders or TBDs remain.
- The design now resolves the partial-vs-iframe contradiction by requiring a standalone thread document route.
- The design explicitly preserves the existing full app route and HX-gated workspace partial.
- The design addresses nested panes with a host bridge instead of assuming `SerfPanes` exists inside iframes.
- The design calls out hamburger/sidebar chrome flags, legacy pane URL migration, breadcrumb behavior, CSP/sandbox decisions, and iframe error handling.
- Scope remains focused on side-view subagent chrome and does not require a renderer singleton refactor.
