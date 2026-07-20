# Web Rewrite Wave 3 — Workspace Shell (M3) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking. Wave-2 conventions apply (widget pattern, tokens-only
> CSS, wave-local SDD artifacts, parallel streams own disjoint files).

**Goal:** The application shell: dockview workspace on desktop, stack navigator on mobile, the
tree rail, routing, connection/auth chrome — every pane type mountable, layouts persistent.

**Architecture:** A pane registry maps pane-type ids → lazy React components + title/icon
strategy. Desktop hosts them in dockview 7 (`dockview-react`); mobile (<900px) hosts the same
components in a full-screen stack navigator. The tree is a rail/drawer, not a pane. URL ↔
workspace glue lives in one place (`shell/routing.ts`). Layout JSON persists per browser.

**Prereqs:** Wave 1 complete (stores), Wave 2 merged (widgets incl. PaneScaffold, Tree,
Cadence, Dialog, Toast). Executes on a wave worktree off integration after both merges.

## Global Constraints (every task)

- Wave-2 design law binds: tokens-only CSS, no chromatic literals (contract test enforces),
  Plex type, motion budget, sentence case. Cadence is THE liveness affordance everywhere.
- Panes never ask "am I mobile?" — the host decides; panes get `{params, paneApi}` only.
- One `AppwireClient` per window, owned by the shell, injected via context; components consume
  stores, never the client directly (except shell bootstrap).
- Deep-link contract preserved: `/`, `/new`, `/s/{ref}`, `/settings`, `/settings/{section}`,
  `/credentials`, `/thread/{ref}` all resolve; unknown paths render the shell's not-found view
  (SPA fallback semantics pinned in Wave 1 Go tests).
- react-router v7 as a thin URL layer; no data APIs, no loaders.
- dockview CSS: import its stylesheet once in the shell and RESTYLE via its CSS custom
  properties + a `shell/dockview-theme.css` mapping onto our tokens (this file is on the
  token-contract allowlist for referencing --surface/--edge/--ink vars only).
- TDD; RTL with fakeClient (no sockets); pristine output; TS strict.

## Locked interfaces (waves 4-8 import these)

```ts
// shell/paneRegistry.ts
export type PaneTypeId = "session" | "transcript" | "doc" | "spawn" | "settings" | "welcome";
export interface PaneDescriptor<P = unknown> {
  id: PaneTypeId;
  title(params: P, ctx: PaneTitleCtx): string;   // ctx carries thread name lookups
  component: React.LazyExoticComponent<React.ComponentType<PaneProps<P>>>;
  singleton?: boolean;                            // settings/spawn: focus existing instead of second copy
}
export interface PaneProps<P = unknown> { params: P; paneId: string; focused: boolean }
export function registerPane(d: PaneDescriptor): void;   // called from per-pane modules
export function paneFor(id: PaneTypeId): PaneDescriptor;

// shell/workspace.ts (store)
export const useWorkspaceStore: {
  openPane(type: PaneTypeId, params?: unknown, opts?: {beside?: string}): string; // returns paneId; focuses existing singleton/same-params pane
  closePane(paneId: string): void;
  focusPane(paneId: string): void;
  layoutJSON(): unknown;                       // dockview serialization (desktop only)
  restoreLayout(json: unknown): boolean;
};

// shell/routing.ts
export function paneToURL(type: PaneTypeId, params: unknown): string | null;
export function urlToPane(pathname: string): {type: PaneTypeId; params: unknown} | null;
```

Session panes: `params = {ref: string}`. Doc panes: `{ref: string; path: string}`.
Settings: `{section?: string}`. Transcript: `{ref: string}`.

## Tasks

### Task 1 (sequential): shell skeleton — client bootstrap, routing, pane registry, welcome pane
Files: `src/shell/{AppShell.tsx, clientContext.tsx, paneRegistry.ts, routing.ts,
routing.test.ts, paneRegistry.test.ts}`, `src/panes/welcome/{index.tsx,…}`, App.tsx rewired
(dev harness demoted to a route). Steps: registry TDD (register/lookup/singleton semantics);
routing TDD (every deep-link both directions, unknown → null); AppShell mounts client (connect
on mount, banner states from connection store via a `ConnectionBanner` using existing widgets),
renders welcome pane standalone (no dockview yet). Gate: suite green.

### Task 2 (sequential, after 1): dockview desktop host + layout persistence
Files: `src/shell/{DockHost.tsx, DockHost.test.tsx, dockview-theme.css}`,
`src/shell/workspace.ts(.test.ts)`. dockview-react integration: panels render registry panes;
add/close/focus wired to useWorkspaceStore; `api.toJSON()` persisted (debounced) to
localStorage `serf.workspace.layout.v1`, restored on boot with fallback to welcome; titles
live-update via PaneTitleCtx subscribed to thread names. Tests with mocked dockview boundary
kept THIN (the real dockview renders in jsdom acceptably — prefer real; mock only what jsdom
can't do, and say which in the report). Gate: suite green.

### Task 3 ∥ 4 ∥ 5 (parallel streams off the wave branch after Task 2):
- **T3 tree rail + drawer**: `src/shell/rail/**` — Tree widget over the tree store (fetch
  `/api/tree` via a thin `src/stores/tree.ts` you own; refetch on `serf/tree/changed` — NOTE:
  that notification is Go-side Task 6 below; until it lands upstream, refetch on
  thread/started|closed + attention/changed, structured so adding the method is one line);
  attention badges via Cadence/Badge; open-session action → openPane("session",{ref});
  favorite/rename/archive/project-delete actions calling the existing REST endpoints; collapse
  to nothing (desktop) / drawer (mobile, FocusScope). Owns `src/stores/tree.ts` too.
- **T4 mobile stack host**: `src/shell/mobile/**` — <900px breakpoint host: one pane
  full-screen, back-stack, tree drawer trigger, bottom-safe-area padding; same registry; a
  `useIsMobile()` in shell/ (T4 owns it; T2 leaves a TODO seam).
- **T5 auth + connection chrome**: `src/shell/chrome/**` — `/auth` token flow handling
  (redirect catch, cookie success → boot), 503-web-not-built page detection, connection
  banners (reconnecting/closed with manual retry), toasts region, global KeyHint layer (⌘K
  reserved for wave 6). Owns `src/auth.ts` helpers.

### Task 6 (Go, parallel with 3-5, integration worktree via controller or its own stream):
`serf/tree/changed` broadcast per spec §7.3: hub broadcasts an empty notification on roster
refresh deltas, past-index change, archive/favorite/rename/project-delete. Files:
`cmd/serf-hub/web_api_tree.go` + relevant mutation handlers + `appwire/protocol.go` catalog
entry + regenerate (docs + TS types) + Go tests (broadcast asserted via appserver test seams).
Scope: ScopeHub notification, no params. TDD.

### Task 7 (sequential, after merges): wave gate
Full suite + typecheck + lint + build; SERF_HUB_WEB=new manual smoke: hub serves the shell,
deep links open panes, layout survives reload, mobile viewport check via chrome skill
(390px), screenshots archived to the wave worktree sdd dir; wave report; merge to integration.
