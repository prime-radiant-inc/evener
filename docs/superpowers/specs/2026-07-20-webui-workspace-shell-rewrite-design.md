# Web UI Workspace-Shell Rewrite — Design

Status: approved direction (Jesse, 2026-07-20). Hard parallel rewrite; the old web UI is deleted
wholesale at the end. "Nothing about the current ux or style is sacred. clean and modern and well
thought out and responsive for mobile and desktop is more important than matching the old design
exactly. please build reusable widgets. make the whole thing use a consistent style guide."

## 1. Why

The 2026-07-20 architecture study found the push substrate already exists and works: AppWire is a
JSON-RPC-over-WebSocket protocol at `/rpc` with per-thread subscriptions, snapshot-then-subscribe
hydration, and a many-to-many subscription registry (`internal/appserver/subscriptions.go`). One
hub relay per session fans out full duplicate streams to every attached client (`app_relay.go`), so
multiple browsers already receive updates. Cold transcripts project into the same wire shapes as
live sessions.

The view layer is the problem: three coexisting rendering regimes (htmx fragments + Go templates,
~21k lines of hand-rolled no-bundler JS, a keyed-DOM client sidebar), a hard-singleton renderer
that forced panes into iframes, residual polling (tasks 2s, status strip 30s, sidebar 60s), and a
notification reducer written twice (JS and Go/TUI). `renderer.js` went from 2,170 lines on
2026-06-16 back to 6,957 by 2026-07-20.

This rewrite replaces the entire web view layer with one TypeScript client application — the
"workspace shell" — that speaks AppWire over a single socket per window. The protocol, hub, relay,
daemon, and TUI stay as they are, except for the small server-side items in §7.

## 2. Goals

- One rendering regime: a React SPA speaking AppWire. No htmx, no server-rendered fragments, no
  iframes, no polling.
- Panes are the product's shape: a docking workspace (tabs, splits, drag, serialized layouts)
  where sessions, transcripts, docs, spawn, and settings are all pane types.
- Multiple browsers/windows against one hub, all live, all pushed (already true; preserved).
- A reusable widget library under one written style guide; every surface uses it.
- Responsive: full experience on desktop; a deliberate single-pane stack layout on mobile — not a
  squeezed desktop.
- Type safety end to end: TS protocol types generated from `appwire/protocol.go`, drift-tested
  like `docs/appwire-protocol.md`.
- Web tests gate CI (the current 29.7k-line jstest suite never ran in CI).
- Feature parity per §5's inventory, verified by the scenario-card e2e suite.

## 3. Non-goals

- No protocol redesign. AppWire's catalog, framing, and snapshot-recovery model stay. We do not
  add replay cursors; reconnect remains snapshot-rehydrate (cheap, matches the lossy-by-design
  event layer).
- No multi-user identity. The capability-token model stays.
- No offline mode / queued-send-while-disconnected (explicit non-goal today; unchanged).
- No visual-pixel parity with the old UI. Behavior parity, yes; look, no.
- No SSR/Next.js. Static assets from the Go binary + WS.
- No changes to serf-tui or the REST endpoints it uses (§7 keep-list).

## 4. Constraints and invariants

- **Single binary.** Built assets embed via `go:embed`; `make build-hub` produces a self-contained
  `serf-hub`. Node is a build-time dependency only.
- **Remote hub.** Everything works over an SSH tunnel / Tailscale with the cookie or Bearer
  capability token; no absolute-origin assumptions; WS URL derives from page origin.
- **TUI coupling.** `hubapi.Client` consumes: `/api/health`, `/api/tree`, `/api/sessions/{ref}`
  (+ `send|tasks|interrupt|compact|clear|fork|model`), `/api/spawn`, `/api/spawn-schema`,
  `/api/models`. These routes must keep working unchanged.
- **Snapshot recovery.** Clients recover from any doubt (reconnect, stall, gap) by
  `thread/read(subscribe:true, replaceSubscription:false, turnLimit:N)` re-hydration, never by
  assuming delta completeness. Slow consumers get disconnected by the server (32-frame buffer);
  the client must treat that as routine.
- **Single-writer turns.** The daemon CASes turn starts and 409s losers; the UI surfaces
  Conflict as a normal state (offer steer/queue), not an error dialog.
- **CSP.** The new client ships zero inline scripts; `script-src 'self'` with no `unsafe-inline`.

## 5. Feature-parity inventory

The rewrite is done only when every item below works in the new client. This list came from the
route/asset study plus commits through `4128e762d`. Each maps to milestone tasks in the plan.

**Session pane (transcript):**
user messages; streaming agent messages (markdown); reasoning "think" blocks (collapsible, live);
tool calls with per-tool renderers (read/grep/ls/glob, shell, web fetch/search, delegate, `job_*`
family); subagent module with job rows (spawned→running→done/failed, duration, result preview,
open-transcript action) and watched-child live rows (additive subscriptions — today's
`readThread(ref, false, true, false)`); steering shown as user messages (#24); system/skill
notices; turn separators with timing/usage/cost; tool hover timestamps (#37); per-turn job
notification blocks (#36); inline and output images with lightbox; `ask_user` question cards
(cached at TOOL_CALL_START by call_id, whole pending set resolves at once, `askPending` state);
sandbox-escalation approve/deny cards (M7, blocks the tool until resolved); warnings; scroll
stickiness only-when-at-bottom + "new below" pill; honest liveness (quiet ~Nm → may-be-stalled;
self-heal resubscribe after 180s silence); lazy backward paging of older turns
(`thread/turns/list`, initial window 40).

**Composer:** send vs steer vs queue by session state; queue strip with edit/cancel/promote
(#23); drain-as-steer; fork-any-message-into-composer (#42); aside command (#43); image
attachments (paste, drag-drop, picker); per-session drafts (localStorage); Enter-to-send
preference; interrupt.

**Session chrome:** status row (state dot, model chip + mid-session model switch, reasoning
effort, work-time clock, context gauge, cost); title (auto-generated, rename); actions: fork,
aside, compact, clear, shutdown; tasks panel (push-driven via `serf/task/updated` + fetch on
open — the 2s poll dies); details panel; goal display/set (`goal/set`).

**Sidebar / tree:** live sessions with attention badges (`serf/attention/changed`); needs-you
tier; pinned (favorite); projects keyed by full path; archived sessions behind one disclosure
grouped by project (#44); test-run tier; rename; archive; project delete; collapse to nothing on
desktop (#33); deep links.

**Spawn:** directory picker with recent-projects prepopulation (`serf/projects/recent`, #35);
path validation; model/harness pickers; launch overrides; `?dir=`/`?prompt=` prefill.

**Search / palette:** ⌘K session search plus slash commands (steer, aside, …).

**Settings:** all 16 sections — general, theme, transcript, display, notifications, agents,
serf launch, codex launch, in-repo config, providers→credentials (instance CRUD, OAuth
device-code and browser flows), marketplaces+plugins manager, plugins, skills, MCP servers, hub,
storage, per-project (`?cwd=`).

**Global:** OS notifications + favicon badge + title count with single-tab election (Web Locks +
BroadcastChannel); theme light/dark + density + font size; PWA manifest/install; `/auth?token=`
cookie flow; doc/image viewer (`/doc/file`, `/doc/image`) scoped to a session cwd; standalone
`/thread/{ref}` document view; toasts; focus management/a11y (traps, aria-live) equivalent to
today's.

**New in this rewrite (the workspace value):** dockable multi-pane layout — tabs, splits,
drag-rearrange, resize; any number of session panes limited by space, over one socket; layout
persisted and restored; pane types: session, transcript (read-only thread), doc viewer, spawn,
settings. The tree is not a pane: it is a persistent rail on desktop and a drawer on mobile.
Popout windows (dockview native); mobile stack navigation (tree drawer → full-screen pane,
bottom composer).

## 6. Target architecture

### 6.1 Layout

```
cmd/serf-hub/frontend/
  package.json  package-lock.json  vite.config.ts  tsconfig.json
  index.html
  src/
    protocol/      # generated types + hand-written client
      types.gen.ts       (go:generate from appwire catalog; committed; drift-tested)
      client.ts          (socket, initialize, heartbeat 20s/10s, request/notify, reconnect)
      reducer.ts         (notification → ThreadModel updates; THE one reducer)
    stores/        # zustand
      connection.ts  threads.ts  tree.ts  prefs.ts  layout.ts  toasts.ts
    widgets/       # the reusable library (§6.5)
    panes/         # session/ transcript/ doc/ spawn/ settings/ …
    shell/         # dockview host, pane registry, mobile stack, routing glue
    styles/        # tokens.css, global.css; per-component *.module.css
  dist/            # vite build output; git-ignored; embedded at build
```

Naming: components by domain (`SessionPane`, `QueueStrip`, `ContextGauge`), not by widget kind.

### 6.2 Protocol core

- `types.gen.ts` is emitted by a new `internal/appwirets` generator reflecting over
  `appwire.Methods`/`appwire.Notifications` — same source of truth and drift-test pattern as
  `internal/appwiredoc`. Method names become a typed catalog; params/results become interfaces.
- `client.ts` owns exactly one WebSocket per browser window: connect → `initialize` →
  `initialized` → per-pane `thread/read(subscribe:true, replaceSubscription:false)` subscriptions.
  App-level ping every 20s, 10s timeout, force-close, reconnect with backoff, then re-subscribe
  every open pane's thread and re-hydrate each (§4 snapshot recovery). Request timeouts reject.
- `reducer.ts` applies notifications to an immutable-ish `ThreadModel` (threads → turns → items,
  plus queue, status, model, effort, name, tasks, attention). It is the single successor to both
  `eventsFromNotification`+`renderer.js` bookkeeping and nothing else re-derives wire state.
  Golden tests replay recorded notification fixtures and snapshot the resulting model.

### 6.3 State

Zustand stores, one concern each:
- `connection`: socket state, server info, feature set, reconnect status (drives banners).
- `threads`: `Map<ref, ThreadModel>` maintained by the reducer; per-thread hydration state;
  optimistic pending entries reconciled by server echo (port of today's pending registry).
- `tree`: sidebar model from `GET /api/tree` (kept REST — TUI shares it), refetched on
  tree-relevant notifications (`thread/started|closed`, `serf/attention/changed`, favorites and
  archive mutations) — the 60s idle poll dies; a manual refresh affordance replaces it.
- `prefs`: theme, density, font size, Enter-to-send, notification opt-ins (localStorage-backed).
- `layout`: dockview layout JSON ↔ localStorage; URL deep-link handling.
- `toasts`: transient notices.

Streaming deltas bypass React state: the active item's text accumulates in a ref and flushes to
the DOM directly (see §6.6); the reducer stores deltas only on the in-flight item and emits a
settled item at `item/completed`.

### 6.4 Workspace shell

- **dockview 7** (`dockview-react`): tabs, splits, drag, resize, serialization
  (`api.toJSON()/fromJSON()` → `layout` store), popout windows.
- A pane registry maps pane-type ids → React components + icon + title strategy. Panes receive
  typed params (e.g. `{ref}` for session panes).
- Desktop (≥ 900px): dockview fills the viewport right of the sidebar rail; sidebar collapses to
  nothing (#33 behavior).
- Mobile (< 900px): no dockview. A stack navigator: tree drawer → one full-screen pane at a time,
  swipe/back navigation, bottom-fixed composer in session panes. Same pane components, different
  host. The breakpoint is a shell concern; panes never ask "am I mobile?" beyond CSS.
- Layout persists per browser (localStorage). `/s/{ref}` focuses (or opens) that session's pane.

### 6.5 Widgets and style guide

- CSS Modules + `tokens.css` custom properties. No Tailwind, no CSS-in-JS runtime.
- A fresh token system (spacing, type ramp, radii, semantic colors, elevation, motion) authored in
  the frontend-design pass; dark-first with a light theme; both themes from day one. The old
  design system's *principles* worth keeping (restraint; "color means needs-your-eye"; honest
  liveness) inform it, but no old pixel values are load-bearing.
- Widget library (all keyboard-accessible, aria-correct): Button, IconButton, Chip, Badge,
  StatusDot, Card, PaneScaffold (header/body/footer), Dialog, Sheet/Drawer, Menu, Tabs, Tooltip,
  Toast, Input, Textarea, Select, Switch, Combobox (drives model + directory pickers), Tree,
  VirtualList, Markdown, CodeBlock, DiffBlock, Meter (context gauge), ProgressDots, EmptyState,
  Skeleton, KeyHint, FocusScope.
- A `/dev/widgets` gallery route (dev builds only) renders every widget in every state — the
  living style guide and the review surface for design iteration.
- The style guide is written to `docs/web-ui/design-system.md` (v2, replacing the old content).

### 6.6 Transcript engine

- Turn/item list virtualization with `@tanstack/react-virtual` (transcripts reach thousands of
  items; the 128 MiB read limit exists for a reason).
- Streaming fast path: the in-flight agent message / reasoning / tool output renders a
  `StreamingText` leaf that appends raw text nodes imperatively per delta (surrogate-pair safe),
  with markdown parsed once at item completion — the proven freeze-head/stream-tail approach,
  formalized in one component instead of hand-woven through a 7k-line file.
- Markdown via `marked` (npm) + DOMPurify sanitization (M4 verifies and matches the current
  sanitization posture before enabling any HTML passthrough).
- Scroll: stick-to-bottom only when at bottom before mutation; "↓ N new" pill; anchor
  compensation on prepend (older-page load).

### 6.7 Routing and URLs

react-router v7 (data APIs unused; it's a thin URL layer). Contract preserved: `/`, `/new`,
`/s/{ref}`, `/settings`, `/settings/{section}`, `/credentials`, `/thread/{ref}`, `/doc/file`,
`/doc/image`, `/auth`. New: layout state stays out of the URL except `/s/{ref}` focus semantics;
`/thread/{ref}` renders the app in single-pane mode (also the share-link target).

### 6.8 Notifications, tabs, PWA

Port the single-tab-election pattern (Web Locks + BroadcastChannel) into a `notifications`
module; favicon badge and title count derive from the attention store. PWA manifest kept; assets
re-generated to the new brand tokens.

## 7. Server-side changes (small, enumerated)

1. `internal/appwirets` generator + `go:generate` wiring + drift test (like appwiredoc).
2. Serve the SPA: embed `frontend/dist`; every page route (`/`, `/new`, `/s/…`, `/settings…`,
   `/thread/…`, `/credentials`) returns `index.html`; `/assets/*` serves hashed Vite output with
   immutable cache headers (hashing replaces the `?v=mtime` scheme). Dev mode: the Vite dev
   server proxies `/rpc` (ws), `/api`, `/auth`, `/doc`, and image routes to the hub — cookies are
   port-agnostic on localhost, so the capability-token flow works through the proxy. No Go-side
   dev proxy; `SERF_HUB_ASSETS_DIR` dies with the old assets.
3. Tree-change push: broadcast an empty `serf/tree/changed` notification on roster refresh
   deltas, past-index changes, archive/favorite/rename/project-delete mutations. Client refetches
   `/api/tree` on it (debounced). This kills the sidebar poll without moving the tree off REST.
4. CSP: drop `'unsafe-inline'` for scripts once templates are gone.
5. Final wave only (§10): delete SSR/fragment/form-POST handlers, templates, old assets, jstest.
Everything else — relay, subscriptions, methods, notifications, REST keep-list — unchanged.

## 8. Build, toolchain, CI

- Node ≥ 22 + npm with a committed `package-lock.json`. Runtime dependencies (initial budget,
  additions need a reason in the PR): react, react-dom, dockview, dockview-react, zustand,
  @tanstack/react-virtual, react-router, marked, dompurify. Dev: vite, @vitejs/plugin-react,
  typescript, vitest, @testing-library/react, @testing-library/user-event, jsdom, eslint +
  typescript-eslint.
- `make build-web` → `npm ci && npm run build` (skipped when `dist/` is newer than `src/`);
  `make build-hub` depends on it. `make test-web` → typecheck + vitest.
- CI adds one Node job (typecheck, lint, vitest, build) and the Go build consumes the built
  `dist/` — web tests finally gate merges.

## 9. Testing

- **Unit/component (Vitest + RTL):** reducer golden tests against recorded notification-stream
  fixtures (seeded from `appwire` testdata and captured live sessions); widget behavior tests;
  pane tests with a fake protocol client (no socket).
- **The jstest suite is not ported.** It tested the old DOM. Its *cases* inform the new component
  tests; the directory is deleted with the old UI.
- **e2e:** the 141 scenario cards are the acceptance suite. M9 runs every web-facing card against
  the new UI (per the e2e-scenario-testing skill), revising cards whose steps referenced old DOM
  details while keeping their falsifiable assertions.
- Never test mocks; the fake protocol client replays real recorded frames.

## 10. Deletion wave (the last milestone, one commit series)

Delete: `cmd/serf-hub/templates/` (all), `cmd/serf-hub/assets/*.js` (all, including vendored
htmx/marked), `assets/style.css`, `cmd/serf-hub/jstest/`, the htmx fragment routes
(`/_partials/*`), the `/s/{id}/send|steer|queue|drain-as-steer|promote-queued|cancel-queued`
form-POST handlers (browser uses AppWire; `/api/sessions/{ref}/send` stays for the TUI), the
inline-script CSP exemption, `SERF_HUB_ASSETS_DIR`, and every `web_*.go` block that existed only
to render or serve the above. Update `docs/serf-hub-web-routing.md`, `cmd/serf-hub/README.md`,
`docs/web-ui/*` to describe the new world. `git grep htmx` returns nothing when this lands.

## 11. Risks

- **Streaming perf in React** → the §6.6 imperative fast path is designed in from the start, not
  retrofitted; M4 includes a token-flood benchmark against a recorded 10k-delta session.
- **dockview on mobile** → not used there; the stack navigator is a first-class host (M3).
- **Settings breadth** (16 sections incl. OAuth flows) → biggest grind, isolated to M7, driven by
  per-section parity checklists; the credentials OAuth flows get scenario-card verification.
- **Scenario-card drift** → M9 explicitly audits/revises cards; assertions stay, selectors go.
- **npm supply chain** → lockfile committed, dependency budget in §8, no postinstall scripts.
- **Parity blind spots** → §5 inventory is the checklist; anything discovered mid-build gets
  added there first, then built.

## 12. Size estimate

New: ~15–20k LOC TS/TSX + ~3–5k CSS + ~300 Go (generator, tree broadcast, SPA serving).
Deleted: ~21k JS + 2.8k templates + 5.9k CSS + 29.7k jstest + ~3–4k Go handlers.
Net: the web layer shrinks by roughly a third while gaining panes, types, and CI coverage.

## 13. Milestones (plan input)

- **M0 foundation:** scaffold, codegen + drift test, make/CI wiring, embed + dev proxy, hello-app
  served by hub behind `SERF_HUB_DEV_WEB`.
- **M1 protocol core:** client.ts, reducer.ts, stores, golden fixtures, reconnect/self-heal.
- **M2 style guide + widgets:** tokens, both themes, widget library, `/dev/widgets` gallery,
  design-system.md v2. (frontend-design skill pass.)
- **M3 shell:** dockview host, pane registry, layout persistence, routing, sidebar/tree pane +
  drawer, mobile stack host, auth/health/connection banners.
- **M4 transcript:** virtualized transcript, streaming fast path, markdown/sanitize, all item
  renderers, images/lightbox, paging, scroll/liveness. Benchmark gate.
- **M5 interaction:** composer (send/steer/queue), queue strip edit/cancel/promote, drafts,
  attachments, ask_user, escalations, status row, model switch, session actions, tasks/details,
  goal.
- **M6 surfaces:** spawn, palette/search, notifications/badges, theme/prefs.
- **M7 settings:** all sections + credentials/OAuth + plugins/marketplaces manager.
- **M8 periphery:** doc viewer, `/thread/{ref}` single-pane mode, PWA, popout windows.
- **M9 parity + e2e:** §5 checklist sweep, scenario-card runs (live hub + real sessions), perf.
- **M10 deletion + docs + CSP:** §10, then a full-repo gate (`make lint && make test`,
  Node job green, scenario re-run).
