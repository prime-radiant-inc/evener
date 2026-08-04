# Session panels as togglable panes — design

Date: 2026-08-04
Status: approved (design), pending implementation plan

## Goal

The session pane's three auxiliary panels — **Tasks**, **Activity**, and
**Details** — currently open as `Sheet` slide-overs from buttons in
`SessionChrome` (the status row below the composer). On desktop they should
instead open as real panes in the dockview pane manager, and the buttons below
the composer should toggle those panes on and off. On mobile the existing
slide-over behavior stays.

Decisions locked with the user:

- **Mobile:** hybrid — desktop toggles dockview panes, mobile keeps the sheets.
- **Toggle-off:** a true toggle — clicking the button while its pane is open
  closes the pane, even when that pane is focused.
- **Naming:** the activity panel keeps its current "Activity" label.

## Current state

- `panes/session/chrome/SessionChrome.tsx` renders `DetailsPanel`,
  `TasksPanel`, and `ActivityPanel`. Each is a trigger `Button` plus a `Sheet`.
  Below a measured width (640 px) the triggers collapse into the "⋯"
  `SessionActionsMenu`, which opens the same sheets through imperative handles.
- The pane manager is `shell/workspace.ts` (`openPane`/`closePane`, main +
  secondary slots, dedupe by type + deep-equal params) driven through
  `shell/paneActions.ts`. Pane types self-register via `registerPane` with a
  lazy component; `panes/transcript` and `panes/doc` are the precedent for
  contextual, non-routed panes that open in the secondary group.
- `AppShell.tsx` imports every pane registration module eagerly so restored
  dockview layouts can resolve their pane types.
- Mobile uses `shell/mobile/StackHost` (no groups; focused pane full-screen);
  contextual child panes offer `BackToParentAction` as the return path.
- The command palette's `/tasks` command synthesizes a click on
  `data-tasks-trigger` (`shell/palette/commands.ts`).

## Design

### 1. New pane types

Three new `PaneTypeId`s: `sessionTasks`, `sessionActivity`, `sessionDetails`,
each with params `{ ref: string }` (the session/thread ref).

- Registration follows the transcript-pane pattern: a tiny eager module calling
  `registerPane` plus a lazy pane component. `AppShell.tsx` imports the
  registration modules alongside doc/transcript so layout restore resolves
  them.
- `routing.ts`'s `paneToURL` returns `null` for the new types — contextual
  surfaces, not deep-linkable, same as `transcript` and `doc`.
- Tab titles name the session for context: `Tasks · <session-name>`,
  `Activity · <session-name>`, `Details · <session-name>`, using
  `PaneTitleCtx.threadName` with the raw ref as fallback.
- Not singletons; per-session dedupe comes from the store's existing
  same-params rule, so each session has at most one of each panel pane.
- Placement: secondary slot (`openPane(..., { slot: "secondary" })`) — they
  stack as tabs to the right of the main pane and may never displace it.

### 2. Content extraction

Each existing panel component is trigger + `Sheet` + body. The body (all
state, fetching, and rendering) extracts into a host-independent component
with two hosts:

- **Sheet host** (mobile): the existing `Sheet` wrapper, rendering the body.
- **Pane host** (desktop): `PaneScaffold` + the body. The pane claims
  `ensureThread(ref)` on mount and `releaseThread(ref)` on unmount, the same
  claim pattern `Session.tsx` uses, so the thread stays hydrated while the
  pane is open regardless of the session pane.

Fetch behavior is unchanged in substance: Tasks and Activity fetch on mount
(their current fetch-on-open effect becomes fetch-on-mount). Dockview unmounts
a pane's tree while its tab is inactive, so re-activating the tab remounts and
refetches — matching today's close-and-reopen-the-sheet semantics. The
Details pane runs its own `useNowTick` instead of receiving `now` from
`SessionChrome`.

All existing panel states carry over verbatim: loading, unsupported source,
daemon-gone terminal state, stale-list-with-error, retry.

### 3. Toggle mechanics

- New `togglePane(pane: PaneRef)` in `shell/paneActions.ts`: if the workspace
  holds an open pane with the same type and deep-equal params, `closePane` it;
  otherwise `openPane(type, params, { slot: "secondary" })`. Closing works
  even when the pane is focused; focus fallback is the store's existing
  `closePane` behavior.
- New exported `isPaneOpen(state, type, params)` helper in `shell/workspace.ts`
  (sharing the module's `sameParams`) so components can subscribe to open
  state. The desktop buttons render `aria-pressed` from it, so an open panel
  reads as pressed.

### 4. SessionChrome

- The Details / Tasks N/M / Activity buttons branch on `useIsMobile()`:
  mobile opens the Sheet (unchanged, imperative handles stay); desktop calls
  `togglePane`.
- The narrow-width "⋯" overflow items route identically (mobile: sheet via
  handle; desktop: toggle).
- Badge counts on the buttons (`Tasks 2/5`, activity label) are unchanged.
- The palette's `/tasks` command is untouched: it clicks `data-tasks-trigger`,
  which now toggles the pane on desktop and the sheet on mobile.

### 5. What stays

The `Sheet` widget and all three Sheet hosts remain for mobile. No renames:
"Activity" keeps its label everywhere.

### 6. Edge cases

- Session ends while a panel pane is open: the panels' existing daemon-gone /
  ended states render as-is.
- A panel pane restored from a persisted layout works because AppShell
  registers the types eagerly; its own `ensureThread` claim re-hydrates the
  thread.
- Toggling a panel whose session pane was closed still works: the pane claims
  the thread itself.

## Testing

- `workspace.ts` / `paneActions.ts` unit tests: `togglePane` open → close →
  open, dedupe per ref, secondary-slot placement; `isPaneOpen` selector.
- `SessionChrome` tests: desktop toggle vs mobile sheet (stubbed
  `useIsMobile`), `aria-pressed` state, overflow-menu routing in both modes.
- Pane component tests adapted from the existing panel suites: registration,
  tab title, thread claim/release, body states.
- Existing panel tests move with the extracted bodies; coverage is not
  reduced.
- Gates: `npx biome check --write` on touched files, `make test-web`, and
  `make test-web-browser` on a Chrome-capable host.

## Out of scope

- Deep links / routing for the panel panes.
- Any change to what the panels show (content, fields, badges).
- Removing the `Sheet` widget or its other consumers.
- Dockview layout policy beyond secondary-slot placement (no default-open
  panels, no layout presets).
