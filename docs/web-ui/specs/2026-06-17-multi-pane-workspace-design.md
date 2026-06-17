# Multi-Pane Workspace — Feasibility + Design Spec

Date: 2026-06-17
Status: Draft for Jesse's review. **No implementation** — this is a design/feasibility doc.
Scope: the Serf web hub (`cmd/serf-hub`).

## What we're trying to do

Today the workspace shows exactly **one** session. Jesse's ask, verbatim:

> "could you have a subagent spec what it would take to be able to open subagents
> or documents as vertical panes next to an agent?"

So: open one or more **vertical panes** beside the agent you're viewing. A pane can be:

1. a **subagent's live session** (the obvious first use: watch a subagent stream while the parent runs), or
2. a **document** — a repo file, a markdown doc, a diff, or an image/artifact.

This doc grounds every architectural claim in the current code (file:line), evaluates the
realistic implementation options, and recommends a phased plan.

---

## TL;DR (executive summary)

- **Recommended MVP: iframe-per-pane.** Each side pane is an `<iframe>` whose `src` is an
  existing route — `/s/<id>` for a subagent session, or a new `/doc/...` route for a document.
  The whole single-instance renderer/appwire stack runs **unchanged** inside each iframe. The
  only real work is the *host* shell: a column layout, a splitter, pane open/close, and a URL
  scheme. Rough effort: **~400–700 LOC** across one CSS layout block, one small host JS module,
  ~2 Go routes, and the "open beside" hook on the subagent row.
- **⚠ One hard prerequisite for the recommended approach:** the current CSP sets
  `frame-ancestors 'none'` (`httpsec.go:35`), which blocks ALL framing — same-origin included. The
  iframe approach is dead until that is relaxed to `frame-ancestors 'self'` (a one-line change +
  test, but a deliberate security relaxation Jesse should approve).
- **The in-page multi-instance path is a trap for the MVP.** The renderer is a hard singleton
  bound to `document.getElementById("conversation")`, with ~18 distinct document-global state
  collisions (enumerated below). Making it N-instance-safe is a large, risky refactor
  (**~1500–2500 LOC** touching renderer.js + renderer-panels.js + renderer-format.js + the
  composer wiring) and should only be attempted later, if iframes prove insufficient.
- **The realtime transport is already multi-session-ready.** One WebSocket per page, multiplexed
  subscriptions keyed by `ref`, notifications tagged with `threadId`/`ref`, additive-vs-replace
  subscribe is a per-request flag. So even the in-page path needs **no protocol changes** —
  the blocker is purely the renderer singleton.

**Top 3 open questions for Jesse** (full list at the end):
1. What does "document" mean concretely, and what are its sources? (repo file / diff / markdown /
   image — and for **remote** sessions, do we need a file-read RPC, or is local-only acceptable?)
2. How many panes max? (1 extra is a clean MVP; N is a layout + lifecycle escalation.)
3. Should a multi-pane layout be **shareable/restorable via URL**, and is the side pane **live**
   or **read-only**?

---

## Key architectural finding: is the renderer a hard singleton?

**Yes — `SerfRenderer` is a hard singleton, not a per-pane factory.** This is the single most
important fact for this design.

- It is a plain object literal, not a class:
  `const SerfRenderer = { init(conversationEl) { ... } }` — `cmd/serf-hub/assets/renderer.js:64`,
  exported once as `window.SerfRenderer = SerfRenderer;` — `renderer.js:3596`.
- Its session state lives directly on that one object: `this.conversation = conversationEl;
  this.sessionId = conversationEl.dataset.sessionId;` — `renderer.js:96-97`. A second `init()`
  call **overwrites** these — there is no second instance.
- Bootstrap hard-binds to a single element by ID: `const conv = document.getElementById("conversation");
  if (conv) SerfRenderer.init(conv);` — `renderer.js:3610-3611`, wired to `DOMContentLoaded` and
  `htmx:afterSwap` — `renderer.js:3613-3614`. It assumes **exactly one** `#conversation`.
- Per-element idempotency exists (`conversationEl.__serfInitialized` — `renderer.js:70-71`), but
  because the live state is shared on the singleton, two `#conversation` elements cannot both be
  driven at once.

(Note for the record: an early investigation pass claimed `SerfRenderer` was a
`class { constructor(sessionId, element) }`. That is **fabricated** — no such class exists; the
code above is the truth. The "just `new SerfRenderer()` twice" idea does not work.)

By contrast, the **transport** layer was clearly built with multiplexing in mind (see below), so
the renderer singleton is the *only* hard blocker to running two sessions on one page.

---

## How the app shell works today (ground truth)

### Routing / page model
- `/s/<id>` (no `HX-Request`) serves the **full app shell**, parameterizing the workspace region:
  `ExecuteTemplate(w, "app", {"WorkspaceURL": "/_partials/s/" + id + "/workspace"})` —
  `cmd/serf-hub/web_workspace.go:44` (inside `handleSession`, the `/s/<id>` router at
  `web_workspace.go:23`). So a session URL = app shell + the one workspace partial.
- The app shell has exactly **one** workspace region: `<main id="workspace" hx-get="{{.WorkspaceURL}}"
  hx-trigger="load" hx-swap="innerHTML">` — `cmd/serf-hub/templates/app.html:29-33`.
- The workspace partial renders a single conversation: `<div class="conversation" id="conversation"
  data-session-id="{{.ID}}" data-cwd="{{.WorkingDir}}" data-home="{{.HomeDir}}">` —
  `templates/partials/workspace.html:37-43`.
- Opening a session = an htmx anchor that swaps `#workspace`. Every sidebar row — including
  subagents and forks — is `hx-get="/_partials/s/<id>/workspace" hx-target="#workspace"
  hx-swap="innerHTML" hx-push-url="/s/<id>"` — `templates/partials/sidebar.html:162-211` (the
  subagent variant is at `sidebar.html:188-197`). This is the **single seam** through which all
  sessions are opened today.
- All internal partials are served under `/_partials/` and gated to `HX-Request: true` so they
  can't be navigated to directly — `cmd/serf-hub/web.go:181-210`.

### Layout / CSS
- `body.app` is **flexbox row**: `display: flex` — `cmd/serf-hub/assets/style.css:474`. Two
  children: `#sidebar { width: 260px; flex-shrink: 0 }` — `style.css:477` and
  `#workspace { position: relative; flex: 1; min-width: 0; height: 100vh; display: flex;
  flex-direction: column; overflow: hidden }` — `style.css:478`.
- Inside `#workspace`, three stacked children: `.workspace-header`, `.conversation` (the scroller),
  `.workspace-input`.
- Sidebar collapse is `body.app[data-sidebar-rail] #sidebar { width: 56px }` — `style.css:756`.
- The **details panel is a fixed slide-over overlay**, not a column:
  `.details-panel { position: fixed; top:0; right:0; bottom:0; width: 360px; ... z-index: var(--z-drawer) }`
  — `style.css:2857`. It overlays the workspace rather than reserving column space.
- Mobile (`@media (max-width: 767px)` — `style.css:2926`): `body.app` becomes `display: block`,
  the sidebar becomes an off-canvas fixed drawer, `#workspace` fills the viewport, and the details
  panel becomes full-width.
- **No splitter / resize-handle / drag-divider code exists anywhere** (CSS or JS). A resizable
  divider would be net-new (mousedown/move/up handler, persisted width).

### Realtime transport (already multi-session-capable)
- **One WebSocket per page.** Module-level `let ws = null` — `cmd/serf-hub/assets/appwire.js:26`;
  opened once via `new WebSocket(rpcURL())` — `appwire.js:46`; `rpcURL()` points at `/rpc`
  — `appwire.js:39`. Server route `mux.HandleFunc("/rpc", s.appRPC.ServeWebSocket)` —
  `web.go:115`; one `Connection` per socket — `internal/appserver/websocket.go:31`.
- **Subscriptions are many-to-many per connection.** `Subscriptions{ byConn, byThread }` with
  `Subscribe(connID, threadID)` adding to a *set* — `internal/appserver/subscriptions.go:5-29`.
  One connection can subscribe to N threads.
- **Subscribe is requested via `thread/read`** with `subscribe` + `replaceSubscription` flags:
  client `readThread(sessionId, includeTurns, subscribe, replaceSubscription)` —
  `appwire.js:313-314`; wire fields `Subscribe` / `ReplaceSubscription` on `ThreadReadParams` —
  `appwire/types.go:381-382`. The server honors **both**: `if subscribeParams.ReplaceSubscription
  { appserver.ReplaceSubscriptions(...) } else { appserver.Subscribe(...) }` —
  `cmd/serf-hub/app_rpc.go:145-148` and `:170-173`.
- **Notifications are tagged by thread and fan out to all handlers.** Server
  `Broadcast(threadID, method, params)` to every subscribed connection —
  `internal/appserver/server.go:62-74`; client dispatches each notification to every registered
  handler — `appwire.js:151-154`; there's already a demux helper `liveThreadKey(params, item)`
  that extracts `ref`/`threadId` — `appwire.js:582-586`. The renderer's own filter
  `notificationMatches()` keys on `params.ref` / `params.threadId` against its `sessionId` —
  `renderer.js:491-500`.

**Implication:** N panes streaming N different sessions over one socket is already
protocol-supported. Each pane just needs its own `thread/read{subscribe:true,
replaceSubscription:false}`. The ONLY thing stopping in-page multi-session is the renderer
singleton + its global UI state.

### Subagents (parent/child)
- Subagents are **full first-class sessions** with their own IDs, flagged `IsSubagent` with a
  `ParentSessionID` — `agent/schema/snapshot.go:43-54`, surfaced on the wire as
  `hubapi.SessionDetail{ ParentSessionID, IsSubagent, ... }` — `hubapi/types.go:82-85`. They are
  routable at `/s/<id>` exactly like any session.
- The workspace already builds a parent breadcrumb for a subagent:
  `fillSubagentLineage()` sets `ParentRouteID` + `ParentTitle` — `web_workspace.go:381-396`,
  rendered as `.subagent-parent-banner` with an `↑ Parent` link — `workspace.html:6-14`.
- **How a subagent is opened today:** inline in the parent transcript, subagents aggregate into a
  "Subagents (N)" module; each row (`.sub-r`, built in `makeSubagentRow()` — `renderer.js:2216`)
  has a dim **"view →"** affordance. Clicking it is a **one-way hard nav**:
  `applyJobRefTarget()` sets `row.onclick = () => this.navigateTo("/s/" + encodeURIComponent(
  data.transcriptRef))` — `renderer.js:2374-2378`, and `navigateTo(href){ window.location.href =
  href }` is the **single centralized navigation seam** — `renderer.js:2380-2386`. Esc-to-parent
  reuses the same seam — `renderer.js:3553-3572`.
- **Cleanest "open beside" hook:** `navigateTo()` / `applyJobRefTarget()` is the perfect
  injection point — the row already carries `data.transcriptRef`. An "open beside" action (a small
  secondary button on the row, or shift-click) calls a new host function instead of `navigateTo`.

### Documents — what's reachable today vs. needs building
| Document type | Status today | Evidence |
|---|---|---|
| **User-submitted image** (from a session transcript) | **Reachable** | `/s/<id>/images/<sha>` serves sha-addressed image bytes by re-scanning the transcript — `cmd/serf-hub/image_serve.go:29-57`, routed in `handleSession` at `web_workspace.go:95-97`. Full-screen lightbox already exists — `renderer-format.js:142-220`. |
| **Diff** (from an edit/write/apply_patch tool call) | **Reachable as tool output** | Client diff renderer `renderDiff()` / `diffRenderer()` with `+N −N` stats and expand — `renderer-tools.js:22-43`, `:457-541`. There is **no** "diff an arbitrary pair of files" path. |
| **Markdown** (assistant text) | **Reachable as message content** | `window.marked.parse(...)` renders assistant text — `renderer.js` (marked usage), engine `assets/marked.min.js`. There is **no** "render an arbitrary `.md` file" path. |
| **Arbitrary repo file** (`.go`, `.md`, etc. from the session cwd) | **Needs building** | `data-cwd`/`data-home` are on `#conversation` — `workspace.html:42-43` — but **no HTTP endpoint serves repo files.** `/assets/` only serves embedded UI assets — `web.go:112`. Needs a new `/doc` route (and, for **remote** sessions, a file-read RPC — see below). |
| **Generated artifact** (e.g. an image a tool wrote to disk) | **Needs building** | Same gap as repo files — no general file-serve path. |

There is **no syntax highlighting** for files or diffs today (plain `<pre>` + diff CSS classes).

**Important caveat the spec must respect:** the hub talks to sessions through an `appsource`
registry that includes **remote** sources (codex sources, remote daemons) — `app_rpc.go:20-48`.
For those, "read a file from the session's cwd" cannot be a local `os.ReadFile`; it needs to flow
over appwire to the session's host. A local-only file endpoint is simpler but only works for the
`local` source. **This is a real decision, not a detail.**

---

## Scope & UX

### Pane types (MVP set)
1. **Subagent session pane** — a live (or replayed) view of a subagent's session, opened from the
   parent's "Subagents (N)" module via an "open beside" affordance.
2. **Document pane** — initially the cheapest sources: an **image** (already serveable) and a
   **diff** (already in-transcript). Repo-file / markdown panes are a later phase pending the
   file-serve decision.

### Opening a pane
- **From a subagent row:** add a secondary "open beside" control (or shift-click) on `.sub-r`;
  route it to a new host function instead of `navigateTo()` — `renderer.js:2374-2386`.
- **From the sidebar:** optionally, a modifier-click on an `sb-row` opens the session beside the
  current one instead of swapping `#workspace` — `sidebar.html:162-211`. (Deferred; the subagent
  case is the primary ask.)
- **From a diff/image in the transcript:** an "open beside" affordance on the tool card.

### Pane management
- **Add / close / focus:** a thin pane header per side pane (title + close ✕). Closing a pane
  tears down its iframe (which closes that pane's appwire subscription automatically — the relay
  idle-exits once `SubscriberCount == 0`, `app_rpc.go:195-209`).
- **Resize:** a draggable splitter (net-new; none exists). Persist width in `localStorage`.
- **Max panes:** recommend **1 extra pane for the MVP** (primary + one side). Beyond ~2 the
  layout, the per-pane WS subscription count, and the input-focus story all get materially harder.
- **Mobile:** below 767px, collapse to single-pane (the side pane stacks below or becomes a
  swipe-away drawer); we already go single-column there — `style.css:2926`.

### URL representation
- **MVP:** the **primary** pane keeps owning the URL (`/s/<id>`, via `hx-push-url`). Side panes are
  ephemeral UI state, not in the URL. This sidesteps the renderer's `history.replaceState("/s/" +
  session_id)` (`renderer.js:807`) fighting over the address bar.
- **Later (if Jesse wants shareable layouts):** encode panes in a query param, e.g.
  `/s/<primary>?pane=s:<subagentId>&pane=doc:<...>`, and have the host reconstruct iframes on load.
  Restorable and shareable, at the cost of a small (de)serializer.

---

## Architecture options

### Option A — iframe-per-pane  ⭐ recommended for MVP

Each pane is an `<iframe>` that loads an existing route:
- subagent pane → `src="/s/<subagentId>"` (the full app shell + workspace, renderer + appwire run
  inside the iframe, untouched);
- document pane → `src="/doc/<...>"` (a new minimal route for the document type).

The **host page** owns only: the column grid, the splitter, the pane chrome (title/close), and the
"open beside" wiring. Each iframe is an independent document, so the singleton renderer and all its
document-global state (below) live in **separate `document` contexts** and never collide.

**Pros**
- **Reuses the entire single-instance stack unchanged.** Zero changes to renderer.js / appwire.js
  semantics. This is the whole point — the renderer's hard-singleton design (the key finding) stops
  being a problem because each pane has its own `window`/`document`.
- Strong isolation: a crash, a runaway scroll, a lightbox, a `document.title` write in one pane can't
  touch another. (Today `document.title` is written globally — `renderer-panels.js:46-60`.)
- Small, well-contained surface area; easy to ship and to back out.
- Each iframe opens its **own** `/rpc` WebSocket. With a 1-extra-pane cap that's 2 sockets/page —
  fine. The relay/subscription model already handles many connections — `subscriptions.go`,
  `server.go:62-74`.

**Cons / risks**
- **WS count scales with panes** (one socket per iframe). Acceptable at 1–2 panes; a reason to cap
  pane count. (In-page multiplexing would use a single socket — but that needs the big refactor.)
- **Cross-pane communication** is limited to `postMessage`. For the MVP we need almost none (the
  host sets `src`; the iframe streams itself). "Esc-to-parent" inside a subagent iframe would just
  navigate that iframe; a host-level "close pane" is cleaner.
- **Styling/theme propagation:** the theme is chosen at load via `localStorage["serf-hub.theme"]`
  applied inline in `<head>` — `app.html:9-16`. Same-origin iframes read the same `localStorage`,
  so theme is consistent. A live theme *toggle* would need a `postMessage` (or each iframe re-reads
  on a storage event) — minor, deferrable.
- **Auth propagation:** the auth guard wraps the mux and uses a cookie/token —
  `hubedge.AuthGuard` at `web.go:146`. Same-origin iframes inherit cookies, so auth "just works"
  for same-origin.
- **⚠ HARD PREREQUISITE — CSP currently forbids ALL framing.** The CSP middleware sets
  `frame-ancestors 'none'` — `cmd/serf-hub/internal/httpsec/httpsec.go:35` (asserted by
  `httpsec_test.go:41`), and there is **no** `frame-src`/`child-src` directive (so framed
  sub-resources fall back to `default-src 'self'`). `frame-ancestors 'none'` means the hub's own
  pages **cannot be embedded in any iframe, including same-origin** — so **Option A does not work
  until this directive is relaxed to `frame-ancestors 'self'`.** This is a one-line change
  (`httpsec.go:35`) plus updating the test, but it is a real, deliberate security relaxation that
  Jesse should sign off on. Without it, every iframe pane renders blank/blocked.
- Slight duplication of chrome (each iframe shows its own header/input). For a *subagent* pane this
  is arguably fine (you may want to message the subagent); for a *document* pane we'd point at a
  stripped `/doc` route with no composer.
- The iframe's renderer will call `history.replaceState` **inside the iframe** — harmless, since it
  only affects the iframe's own history, not the top address bar.

**New work for Option A**
- CSS: turn `#workspace` into a host that can hold a primary region + a side-pane region + splitter
  (nest a grid **inside** `#workspace`; do not touch `body.app`). ~80–150 LOC CSS.
- Host JS module (`panes.js`): open/close/resize, manage iframe elements, splitter drag, optional
  `localStorage` width. ~150–250 LOC.
- Renderer hook: an "open beside" affordance on `.sub-r` rows calling the host instead of
  `navigateTo()` — `renderer.js:2374-2386`. ~30–60 LOC.
- Go: a `/doc` route family for document panes (image already exists; add file/diff later). ~80–200
  LOC depending on document types in scope.
- Verify CSP `frame-ancestors`/`X-Frame-Options` allow same-origin framing.

### Option B — in-page multi-instance (refactor the renderer)

Make `SerfRenderer` a factory that produces N instances, each scoped to its own root element, all
sharing the one WebSocket via ref-demuxed notifications.

**Why it's attractive (long term):** one socket, no chrome duplication, tighter cross-pane
interactions (a single composer that can target either pane, unified scroll/keyboard model),
no iframe boxing.

**Why it's the wrong MVP — the specific global state that must become per-instance.** This is the
cost. Every item below is document-global today and would collide with two renderers on one page:

1. The singleton itself: `const SerfRenderer = {...}`; `window.SerfRenderer = SerfRenderer` —
   `renderer.js:64`, `:3596`. Must become `create(rootEl) -> instance`.
2. Bootstrap binds one element: `document.getElementById("conversation")` — `renderer.js:3610`.
   Must iterate roots / accept a root.
3. Module-level task caches shared across sessions: `const taskDescriptions = new Map();
   const taskDetails = new Map();` — `renderer-format.js:228-229`. Must be per-instance.
4. Lightbox is a hard-coded singleton DOM id `#image-lightbox` — `renderer-format.js:153-156`.
5. Connection banner singleton `#connection-banner` — `renderer.js:676-687`.
6. Browser tab title written globally: `document.title = ...` — `renderer-panels.js:46-60`; plus a
   global `refreshTabTitle` listener — `renderer-panels.js:489-493`.
7. Task badge updater queries **all** `[data-tasks-trigger]` document-wide —
   `renderer-panels.js:378-398`.
8. Mutually-exclusive panels `#tasks-panel` / `#details-panel` — `renderer-panels.js:209`, `:419`.
9. Single composer/form `document.querySelector("form[data-input-form]")` — `renderer.js:3078`
   (and queue list `[data-queue-list]` — `renderer.js:410`; action buttons — `renderer.js:259,
   292, 306, 312`). Two panes can't share one composer without a major rework.
10. Single AppWire pending registry: `setPendingRegistry(this.pending)` overwrites the prior —
    `renderer.js:429-431`.
11. `history.replaceState("/s/"+session_id)` — `renderer.js:807` — two renderers fight the URL.
12. New-content pill singleton `[data-new-content-pill]` — `renderer.js:2941-2948`.
13. Needs-you dock singleton — `renderer.js:3078-3090`.
14. Document-level click/keydown listeners for expand toggles + the body click that does
    `document.getElementById("conversation")` — `renderer-panels.js:69-119`.
15. Esc-to-parent binds `document` once — `renderer.js:3553-3572`.
16. Liveness timer is on the singleton; init clears the prior — `renderer.js:91-94`.
17. Subagent spawn sequence counter on the singleton — `renderer.js:2216` area.
18. Status-dot pulse driven document-wide on every htmx swap — `renderer.js:3620-3624`.

The per-instance notification filter (`notificationMatches`, `renderer.js:491-500`) is already
written as a closure over `sessionId`, so the *event routing* is the one part that's mostly ready —
but everything in the list above must be re-scoped to a root element/namespace.

**Effort/risk:** large. Realistically **~1500–2500 LOC** across renderer.js, renderer-panels.js,
renderer-format.js, plus a layout/composer rework and a big test pass (the jstest suite asserts
much of this singleton behavior — `cmd/serf-hub/jstest/test-renderer*.js`,
`test-appwire-*.js`). High regression risk on the most-used surface. Not an MVP.

### Option C — document-only / read-only reference pane (lightest)

A side pane that is **not** a second live session: just a read-only viewer for a document
(image / diff / file / markdown). No appwire subscription, no renderer, no composer.

- Implemented either as a tiny iframe pointed at `/doc/...`, or — since it's read-only and simple —
  as **in-host DOM** reusing the existing client renderers (`renderDiff` — `renderer-tools.js:22`,
  `marked.parse`, the lightbox) without instantiating a second `SerfRenderer`.
- This is the **cheapest** way to deliver the "documents beside the agent" half of the ask, and it
  composes with Option A (subagent panes are iframes; document panes are read-only viewers).

**Effort:** small for image/diff (machinery exists). Repo-file/markdown still needs the file-serve
endpoint + the remote-vs-local decision.

---

## Recommendation

Ship **Option A (iframe-per-pane) for live subagent panes** + **Option C (read-only viewer) for
document panes**, capped at **one extra pane** for the MVP. Defer Option B (in-page multi-instance)
unless/until iframes prove limiting (e.g. we want one shared composer that can target either pane,
or we want to drive 3+ live panes on one socket).

Rationale: the recommended path treats the **key finding** (renderer is a hard singleton) as a
constraint to route around, not a refactor to fund. iframes give us multi-session "for free" by
giving each session its own document context; the transport already supports it; and the blast
radius is a host shell + a couple of routes, not the core renderer.

---

## Effort & risk summary (LOC / components, not wall-clock)

| Component | Option A + C (recommended MVP) | Option B (in-page) |
|---|---|---|
| CSS layout (nested grid in `#workspace` + splitter) | ~80–150 | ~80–150 (same) |
| Host pane manager JS (`panes.js`) | ~150–250 | n/a (logic folded into renderer) |
| Renderer "open beside" hook | ~30–60 | ~30–60 |
| Renderer N-instance refactor | **0** | **~1500–2500** |
| Composer/input rework for 2 panes | 0 (each iframe has its own) | ~200–400 |
| Go routes (`/doc` family; image exists) | ~80–200 | ~80–200 (same) |
| File-read RPC for remote sessions (if in scope) | ~150–300 | ~150–300 (same) |
| Test updates | moderate (host + routes) | large (renderer/appwire jstests) |
| **Riskiest part** | CSP frame-ancestors; WS-per-iframe count; splitter UX | the 18 globals above + composer model + regressions on the main surface |

Riskiest assumptions to validate early:
1. **CSP must be relaxed to allow same-origin framing.** Confirmed blocker: `frame-ancestors 'none'`
   today — `httpsec.go:35`. Change to `'self'` (and update `httpsec_test.go:41`) before building
   anything on iframes. This is a known, deliberate security relaxation — get Jesse's sign-off.
2. **WS-per-iframe count** stays small (drives the 1-extra-pane cap).
3. **Document file access for remote sessions** — if "document" must work for non-local sources, the
   file-read RPC is required and is the largest single backend item.

---

## Phased plan

**Phase 0 — decisions + spikes (no feature code yet)**
- Get Jesse's answers to the open questions (esp. what "document" covers + remote file access).
- **Relax the CSP** from `frame-ancestors 'none'` to `'self'` — `httpsec.go:35` + `httpsec_test.go:41`
  (gating for everything iframe-based; get sign-off first).
- Spike: confirm `/s/<id>` renders correctly inside a same-origin `<iframe>` after the CSP change
  (auth cookie inherited, theme via shared `localStorage`, no layout breakage).

**Phase 1 — MVP: one subagent pane (iframe) + read-only image/diff pane**
- Nest a grid inside `#workspace` (primary region + side region + splitter); leave `body.app`
  alone — `style.css:474-478`. Collapse to single-pane below 767px — `style.css:2926`.
- `panes.js`: open/close/resize one side pane; manage the iframe; splitter drag + persisted width.
- Subagent pane: "open beside" affordance on `.sub-r` rows → host (instead of `navigateTo`) —
  `renderer.js:2374-2386`; iframe `src="/s/<subagentId>"`.
- Document pane (read-only): image (reuse `/s/<id>/images/<sha>` — `image_serve.go`) and diff
  (reuse `renderDiff` — `renderer-tools.js:22`). "Open beside" on the relevant tool cards.
- URL: primary pane keeps the URL; side pane is ephemeral.

**Phase 2 — richer documents**
- `/doc/file` route + path sanitization (mirror the `/api/dirs` sanitizer — `web_api.go`); for
  remote sources, a `serf/file/read`-style RPC over appwire (the missing piece called out above).
- Markdown file pane via `marked.parse`; optional syntax highlighting (net-new; none today).
- Optional: modifier-click an `sb-row` to open any session beside the current — `sidebar.html`.

**Phase 3 — persistence + scale (only if Jesse wants it)**
- Shareable/restorable layouts via `?pane=...` query encoding; host reconstructs panes on load.
- Raise the pane cap; if WS-per-iframe count becomes a problem, **then** consider Option B's
  single-socket in-page multi-instance refactor (budget it as the ~1500–2500 LOC effort above).

---

## Open questions for Jesse

1. **What is a "document," concretely, and what are its sources?** Repo file? Diff of an arbitrary
   pair? Rendered markdown? A generated artifact/image written to disk? Each adds scope.
2. **Local-only, or must documents work for remote sessions too?** If remote, we need a file-read
   RPC over appwire (the largest backend item). If local-only is acceptable for v1, a simple
   hub-side file endpoint suffices.
3. **How many panes max?** 1 extra (clean MVP) vs. N (layout + WS-count + input-focus escalation;
   and the eventual argument for Option B).
4. **Is the second pane LIVE or READ-ONLY?** A live subagent pane (Option A iframe) lets you message
   the subagent; a read-only pane (Option C) is cheaper and simpler. We can support both, but the
   default matters.
5. **Should multi-pane layouts be shareable/restorable via URL?** If yes, we build the `?pane=...`
   encoder in Phase 1/3; if no, side panes stay ephemeral and the URL stays simple.
6. **Subagent pane chrome:** do you want the subagent pane to show its own composer/input (message
   the subagent), or be a read-only stream with just the transcript?
7. **Is iframe isolation acceptable** as the MVP mechanism, given it duplicates per-pane chrome and
   uses one WebSocket per pane — or is a single-socket, single-composer in-page model a hard
   requirement (which means funding Option B up front)?
