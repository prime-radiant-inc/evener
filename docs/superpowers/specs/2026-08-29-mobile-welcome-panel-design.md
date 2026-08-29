# Mobile welcome + sidebar unification

**Status:** design
**Date:** 2026-08-29
**Owner:** evener

## Problem

On mobile, two separate surfaces cover the "nothing is open" and "browse my
sessions" needs: a full-screen `Welcome` pane, and the `TreeDrawer` bottom
`Sheet` that hosts the `<Rail/>` session tree. They share content (the "New
session" button, the resume link, the session list) but live in different
components reached through different entry points. The welcome pane also shows
example task prompts ("Try a task to get started") that should be removed on both
mobile and desktop.

## Goals

1. **Unify** the mobile welcome screen and the bottom-panel sidebar into a
   single expandable surface.
2. The unified panel can **slide up to full screen** and collapse back down,
   via a drag handle plus tap.
3. The panel opens **full screen first** when no session is focused.
4. The key bindings (Mod+K / Mod+I / Mod+J, and "?" inside the palette) move to
   the **bottom of the unified panel** on mobile.
5. Remove the example task prompts from the welcome screen on **both** mobile
   and desktop.
6. Desktop keeps the chord hints on the `Welcome` pane (no bottom panel there).

## Non-goals

- Changing desktop's persistent left rail or dockview layout.
- Changing the ~8 other `Sheet` consumers (TasksPanel, DetailsPanel,
  PinSectionPicker, MobileSettingRows, ActivityPanel,
  TranscriptDetailControl, the dev gallery).
- Persisting the panel's peek/full state across reloads (it is transient, like
  the drawer's open/closed state today).
- A drag gesture on desktop (no bottom panel on desktop).

## Architecture

### The unification model

On mobile, the "welcome screen" and the "sidebar" become **one expandable
bottom panel**. The panel is the existing `Sheet` widget (`side="bottom"`)
extended with an opt-in **expandable** capability. The panel's content has
three regions, top to bottom:

1. **Session tree** — the existing `<Rail/>` content (session list, "New
   session" button, pin sections, archived). Always present.
2. **Welcome actions** — "Jump back in" (resume candidate) + orientation text.
   Present **only when no session is focused** (the panel is then the landing
   screen). Hidden when a session is live.
3. **Key bindings** — the chord hints. Present only when no session is focused,
   at the panel's bottom.

The panel's **content** depends on focus state; the panel's **geometry**
(peek vs. full screen) is orthogonal and is a reusable `Sheet` capability. So
"welcome" and "sidebar" are the same React surface on mobile, distinguished
only by what is focused — not by which component is mounted.

### Platform split

| State | Mobile | Desktop |
|---|---|---|
| No session focused | Unified panel, full-screen-first. Shows tree + welcome actions + key bindings. | `Welcome` pane (example prompts removed, chord hints kept). Rail docked. |
| Session focused | Session pane full screen. Top-bar Sessions button reopens the panel (full). Tree only. | Session pane + rail. |

### Global constraint honored

Panes never ask "am I mobile?" The `Welcome` component stays host-agnostic. The
new mobile panel is a **shell-level** composition in `src/shell/mobile/`, not a
behavior switch inside `Welcome.tsx`. The welcome *content* (actions, hints) is
extracted into a small reusable module both the desktop `Welcome` pane and the
mobile panel render.

## Components

### 1. `Sheet` expandable capability (`src/widgets/sheet/`)

Add an opt-in expandable mode to the shared `Sheet` widget. New props:

```ts
interface SheetProps {
  // ...existing props...
  /** When set, the sheet renders a drag handle and can transition between
   *  `peekHeight` and full screen. Default: undefined (no handle, fixed
   *  geometry — the current behavior every other consumer relies on). */
  expandable?: {
    /** Peek height in px. The collapsed resting height. */
    peekHeight: number;
    /** Open full-screen-first on mount. When true, the sheet starts at full
     *  height; the handle collapses it down to peekHeight. */
    fullScreenFirst?: boolean;
  };
}
```

**Behavior:**
- A drag handle renders at the top of the panel (above the title), visible
  only when `expandable` is set.
- **Drag:** pointer/touch drag on the handle transitions the panel height
  continuously between `peekHeight` and `100vh`. Release snaps to the nearer
  bound (with a small hysteresis band; >50% of the way to the other bound
  commits). Respects `prefers-reduced-motion` (snap, no smooth transition).
- **Tap:** a tap on the handle toggles between peek and full.
- The scrim and Escape-to-close behavior are unchanged (Escape still closes the
  sheet entirely; scrim click still closes).
- The panel's `max-height` is no longer the fixed `min(480px, 100vh)` when
  expandable; it animates between the two bounds. The existing `.bottom` rule
  stays the default for non-expandable sheets.

**Why additive:** `expandable` defaults to `undefined`; every existing
`<Sheet>` call site is byte-for-byte identical in behavior and geometry. No
existing test or consumer changes.

### 2. Welcome content module (`src/panes/welcome/WelcomeContent.tsx`)

Extract the focus-dependent welcome content (the `actions` block) into a
presentational component so both the desktop `Welcome` pane and the mobile
panel render the same markup:

```tsx
interface WelcomeContentProps {
  note?: string;
  /** Hide the chord-hint block. Desktop keeps it; the mobile panel shows it
   *  only when nothing is focused (it controls visibility itself). */
  showHints?: boolean;
}
```

Renders: optional note hint, "Jump back in" (resume candidate, when one
exists), "New session" button, orientation text. **No example prompts** in
either consumer. The chord hints render only when `showHints` is true.

`Welcome.tsx` becomes a thin wrapper: `PaneScaffold` + `EmptyState` +
`<WelcomeContent showHints />`.

### 3. Unified mobile panel (`src/shell/mobile/MobilePanel.tsx`)

New component, replacing the `TreeDrawer`-as-welcome role on mobile. It composes:

```
<Sheet side="bottom" expandable={{ peekHeight, fullScreenFirst: true }} ...>
  <Rail />                         // session tree (always)
  {nothingFocused && <WelcomeContent showHints />}  // welcome actions + key bindings
</Sheet>
```

**State it reads:**
- `useWorkspaceStore.focusedPaneId` — to decide `nothingFocused`
  (`focusedPaneId === null || focusedPane.type === "welcome"`).
- `useNavigationStore` — for the resume candidate (inside `WelcomeContent`).

**Entry points:**
- `StackHost` renders `MobilePanel` (open, full-screen-first) when no session
  is focused — instead of the standalone `Welcome` pane.
- The top-bar Sessions button (today's `TreeDrawer` trigger) opens `MobilePanel`
  when a session is focused.

**Auto-close on navigation:** unchanged from today's `TreeDrawer` — selecting a
session row calls `openPane("session", {ref})`, which changes `focusedPaneId`,
and the panel closes (or collapses, depending on geometry; see State machine).

### 4. `TreeDrawer` becomes the trigger only (`src/shell/mobile/TreeDrawer.tsx`)

`TreeDrawer` keeps its top-bar `IconButton` + needs-you `Badge` trigger but
delegates the panel to `MobilePanel` (passes `children` through, or
`MobilePanel` owns the `Sheet` outright — see Decision: ownership below).
The bottom `Sheet` and its `EmptyState` placeholder are removed from
`TreeDrawer`; `MobilePanel` is the single panel.

### Decision: Sheet ownership

`MobilePanel` owns the `Sheet` and the `expandable` state. `TreeDrawer` keeps
only the trigger button + badge; on open it calls into `MobilePanel`'s open
state. This keeps the one-sheet invariant (never two panels) and lets
`MobilePanel` decide full-screen-first based on focus state, which the trigger
alone cannot know. `StackHost` mounts `MobilePanel` once (always mounted; the
`Sheet`'s `open` prop gates visibility), so the trigger and the focus-driven
opening share one state holder.

## State machine

`MobilePanel` has two orthogonal axes:

### Geometry (peek ↔ full)
```
peek  --[drag up >50% / tap]-->  full
full  --[drag down >50% / tap]-->  peek
```
Initial geometry on open: **full** when `nothingFocused` (full-screen-first),
**full** when opened via the Sessions trigger while a session is focused (per
the user's "full screen first" answer — opening it returns to full).

### Visibility (open/closed)
```
closed --[Sessions button / no-session landing]--> open
open   --[Escape / scrim / session selected]-->   closed
```
Selecting a session from the tree: `openPane("session", {ref})` fires,
`focusedPaneId` changes, `MobilePanel` auto-closes (same effect as today's
`TreeDrawer`). Collapsing to peek does **not** close the panel; it stays open
at peek height. Closing the panel (Escape/scrim) leaves it closed until the
trigger or a return-to-welcome reopens it.

### Focus transitions
- `nothingFocused` → session focused (user tapped a row): panel closes.
- session focused → `nothingFocused` (user went back to welcome, or closed the
  last session): `StackHost` reopens `MobilePanel` full-screen-first. The
  back-button path in `StackHost` that currently calls
  `workspaceStore.getState().openPane("welcome")` is replaced by opening the
  panel (see StackHost changes below).

## Interaction detail

### Drag handle
- A 36px-wide, 4px-tall rounded bar centered at the panel's top, above the
  title row. `cursor: grab` (desktop); no cursor change on touch.
- Pointer events (`pointerdown` on handle → `pointermove` on window →
  `pointerup`), so one code path covers mouse and touch. No separate touch
  handler.
- During drag, the panel height follows the pointer (`100vh - pointerY`),
  clamped to `[peekHeight, 100vh]`. No smooth transition during the drag; the
  snap on release uses a short transition (`--motion-duration-overlay`,
  `--motion-easing-standard`), disabled under `prefers-reduced-motion`.
- A drag that starts on the handle but the user releases without crossing
  the hysteresis midpoint counts as a **tap** (toggles peek/full).

### Tap
- Tap on the handle toggles peek ↔ full.
- Tapping the scrim still closes (unchanged `OverlayPanel` behavior).

### Keyboard
- Escape closes the panel (unchanged). No dedicated "expand/collapse" key —
  the handle is pointer/touch-first; the panel is mobile-only.

## Styling

- New `sheet.module.css` rules for `.handle` and `.expandable` (the
  expandable bottom variant's height behavior). The existing `.bottom` rule
  (`max-height: min(480px, 100vh)`) remains the default for non-expandable
  sheets.
- The expandable bottom sheet uses `height` (not `max-height`) set
  dynamically, with `transition: height var(--motion-duration-overlay)`; the
  `sheetInBottom` keyframe runs on open as today.
- Peek height: **`min(40vh, 360px)`** (chosen so the session tree's first
  ~6 rows are visible without consuming the whole screen; tunable via a CSS
  variable `--sheet-peek-height` so it can be adjusted without code changes).
- Drag handle and welcome content reuse existing design tokens (`--space-*`,
  `--ink-*`, `--surface-*`, `--edge`).

## Desktop changes

`Welcome.tsx` / `WelcomeContent.tsx`:
- Remove `EXAMPLE_PROMPTS` and the `.examples` / `.examplesLabel` markup and
  CSS.
- Keep `CHORD_HINTS` and the `.hints` block (rendered when `showHints`).
- Remove the `goToExample` helper and its test.
- The orientation text ("A session can read and edit the repository…") stays.

`welcome.module.css`:
- Remove `.examples`, `.examplesLabel`, and the `.examples button` rule.
- Keep `.actions`, `.orientation`, `.hints`, `.hintRow`, `.hintFooter`.

No change to desktop rail, dockview, or any non-mobile host.

## Content model (what the panel shows)

| Region | No session focused | Session focused (panel reopened) |
|---|---|---|
| Session tree (`<Rail/>`) | Yes | Yes |
| "Jump back in" / orientation | Yes | No |
| Chord hints | Yes (at bottom) | No |

When a session is focused and the user opens the panel via the Sessions
button, they get the tree only — the welcome content is not the landing
anymore, so it is hidden to avoid redundancy with the live session behind it.

## Testing

### Unit tests (vitest, jsdom)

**`Sheet` expandable:**
- Renders a handle iff `expandable` is set.
- Default (no `expandable`): no handle, geometry unchanged — assert existing
  `.bottom` class still applied, no `.expandable` class.
- Tap on handle toggles peek ↔ full (assert height class/data attribute).
- `fullScreenFirst` starts at full on mount.
- `prefers-reduced-motion` disables the snap transition (assert no transition
  class / inline style).
- Escape and scrim-click still close when expandable.

**`WelcomeContent`:**
- Renders "Jump back in" when a resume candidate exists; omits it otherwise.
- Renders chord hints iff `showHints`.
- Does **not** render any example prompt button (assert absence).
- "New session" navigates to `/new`.

**`Welcome.tsx` (desktop wrapper):**
- Existing welcome tests updated: the example-prompt test removed; the
  chord-hint tests kept; the "orientation" test kept.

**`MobilePanel`:**
- `nothingFocused` → panel open, full-screen-first, shows tree + welcome
  content + hints.
- Session focused → panel reopened shows tree only.
- Selecting a session row closes the panel (focus change effect).
- `StackHost` renders `MobilePanel` instead of the `Welcome` pane when
  `nothingFocused`.

**`TreeDrawer`:**
- Trigger button + badge unchanged; no longer owns a `Sheet`.

### Guard tests
- `layoutguard`: the expandable panel at peek and full must not overflow the
  viewport; the drag handle must not cause horizontal overflow.
- `overflowguard`: welcome content in the panel must not overflow at 390px
  (the existing `#197` cap behavior is preserved by reusing `WelcomeContent`).
- `shellguard` / `spawnguard`: unaffected (no shell-command or spawn changes).

### Test boundaries
- Pane/host seam: `MobilePanel` is tested through the existing
  `StackHost`/`TreeDrawer` test harnesses with scripted navigation stores, not
  a live provider.
- The `Sheet` expandable tests use the existing `sheet.test.tsx` harness.

## Risks & mitigations

1. **Shared `Sheet` regressions.** The `expandable` prop defaults off; add a
   regression test asserting a default `<Sheet side="bottom">` renders
   identically before and after. Run the full `make test-web` suite.
2. **Drag gesture on real touch devices.** jsdom has no real pointer events;
   the drag is unit-tested with dispatched `PointerEvent`s (the existing
   `OverlayPanel` scrim test already dispatches synthetic mouse events the
   same way). A real-device smoke is noted as a follow-up, not a gate.
3. **Two-panel race.** Only one `MobilePanel` is ever mounted (StackHost owns
   it); the `Sheet`'s `open` prop is the single visibility gate. No second
   `Sheet` is instantiated.
4. **Back-stack interaction.** `StackHost`'s back button today calls
   `openPane("welcome")` as the backstop. The change replaces that with
   opening/closing the panel. The back-stack logic (`backStackRef`,
   `wentBackRef`, `popValidBackTarget`) is untouched — the panel is a shell
   overlay, not a pane, so it does not participate in the pane back-stack.
5. **Focus trap.** `OverlayPanel`'s `FocusScope` traps focus inside the panel.
   The session tree and welcome content are focusable; the drag handle is not
   (pointer-only), so tab order stays within content.

## File impact

| File | Change |
|---|---|
| `src/widgets/sheet/index.tsx` | Add `expandable` prop + handle + state. |
| `src/widgets/sheet/sheet.module.css` | Add `.handle`, `.expandable` rules; keep `.bottom` default. |
| `src/widgets/sheet/sheet.test.tsx` | Add expandable tests; keep existing. |
| `src/panes/welcome/WelcomeContent.tsx` | **New:** extracted focus-dependent content. |
| `src/panes/welcome/Welcome.tsx` | Thin wrapper over `WelcomeContent`; remove example prompts. |
| `src/panes/welcome/welcome.module.css` | Remove `.examples*`; keep hints/orientation. |
| `src/panes/welcome/Welcome.test.tsx` | Remove example-prompt test; keep rest. |
| `src/shell/mobile/MobilePanel.tsx` | **New:** unified panel (Sheet + Rail + WelcomeContent). |
| `src/shell/mobile/MobilePanel.module.css` | **New:** panel layout styles. |
| `src/shell/mobile/MobilePanel.test.tsx` | **New:** panel state/content tests. |
| `src/shell/mobile/TreeDrawer.tsx` | Trigger only; delegate panel to `MobilePanel`. |
| `src/shell/mobile/TreeDrawer.test.tsx` | Update: no owned Sheet. |
| `src/shell/mobile/StackHost.tsx` | Render `MobilePanel` when `nothingFocused`; wire trigger. |
| `src/shell/mobile/StackHost.test.tsx` | Update welcome-landing assertions. |
| `src/shell/mobile/StackHost.module.css` | (Likely none — panel is an overlay.) |

## Out of scope / follow-ups

- Persisting peek vs. full preference across reloads.
- A keyboard shortcut to expand/collapse the panel.
- Animating the welcome content's height when it appears/disappears (the panel
  height is driven by geometry, not content; content visibility is an
  immediate toggle).
- Real-device drag calibration (a smoke-test follow-up).
